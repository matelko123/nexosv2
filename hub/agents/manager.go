// Package agents maintains the hub's connections to its agents.
package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"

	"nexos/hub/events"
	"nexos/hub/store"
	nexosv1 "nexos/internal/gen/nexos/v1"
	"nexos/internal/pki"
)

type Status string

const (
	StatusPending    Status = "pending" // created, not enrolled yet
	StatusConnecting Status = "connecting"
	StatusOnline     Status = "online"
	StatusOffline    Status = "offline"
)

// State is the live view of one agent.
type State struct {
	Status  Status               `json:"status"`
	Error   string               `json:"error,omitempty"`
	Info    *nexosv1.HostInfo    `json:"info,omitempty"`
	Metrics *nexosv1.HostMetrics `json:"metrics,omitempty"`
	Since   time.Time            `json:"since"`
}

type conn struct {
	id     string
	addr   string
	cancel context.CancelFunc
	done   chan struct{}

	mu    sync.RWMutex
	state State
	cc    *grpc.ClientConn
}

type Manager struct {
	store          *store.Store
	ca             *pki.CA
	hubKP          *pki.KeyPair
	bus            *events.Bus
	metricInterval uint32

	mu    sync.Mutex
	conns map[string]*conn
	ctx   context.Context
}

func NewManager(st *store.Store, ca *pki.CA, hubKP *pki.KeyPair, bus *events.Bus, metricInterval time.Duration) *Manager {
	return &Manager{
		store: st, ca: ca, hubKP: hubKP, bus: bus,
		metricInterval: uint32(max(metricInterval/time.Second, 1)),
		conns:          map[string]*conn{},
	}
}

// Start connects to all enrolled agents. It returns immediately.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()
	list, err := m.store.Agents(ctx)
	if err != nil {
		return err
	}
	for _, a := range list {
		if a.EnrolledAt != nil {
			m.Connect(a.ID, a.Address)
		}
	}
	return nil
}

// Connect starts (or restarts) the connection loop for an agent.
func (m *Manager) Connect(id, addr string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.conns[id]; ok {
		old.cancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	c := &conn{id: id, addr: addr, cancel: cancel, done: make(chan struct{}), state: State{Status: StatusConnecting, Since: time.Now()}}
	m.conns[id] = c
	go m.run(ctx, c)
}

// Disconnect stops the connection loop of an agent and waits for it.
func (m *Manager) Disconnect(id string) {
	m.mu.Lock()
	c, ok := m.conns[id]
	delete(m.conns, id)
	m.mu.Unlock()
	if ok {
		c.cancel()
		<-c.done
	}
}

// State returns the live state of an agent; unknown agents are pending.
func (m *Manager) State(id string) State {
	m.mu.Lock()
	c, ok := m.conns[id]
	m.mu.Unlock()
	if !ok {
		return State{Status: StatusPending}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

// Client returns the gRPC connection to an online agent.
func (m *Manager) Client(id string) (*grpc.ClientConn, error) {
	m.mu.Lock()
	c, ok := m.conns[id]
	m.mu.Unlock()
	if !ok {
		return nil, errors.New("agent not connected")
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.state.Status != StatusOnline || c.cc == nil {
		return nil, fmt.Errorf("agent is %s", c.state.Status)
	}
	return c.cc, nil
}

func (m *Manager) setState(c *conn, update func(*State)) {
	c.mu.Lock()
	prev := c.state.Status
	update(&c.state)
	if c.state.Status != prev {
		c.state.Since = time.Now()
	}
	st := c.state
	c.mu.Unlock()
	if st.Status != prev {
		m.bus.Publish(events.Event{Type: "host.status", HostID: c.id, Data: map[string]any{"status": st.Status, "error": st.Error, "since": st.Since}})
	}
}

func (m *Manager) dial(c *conn) (*grpc.ClientConn, error) {
	pin := func(fp string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return m.store.CheckAgentCert(ctx, c.id, fp)
	}
	tlsCfg := pki.HubClientConfig(m.hubKP, m.ca.Pool(), c.id, pin)
	return grpc.NewClient("passthrough:///"+c.addr,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 20 * time.Second, Timeout: 10 * time.Second, PermitWithoutStream: true}),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(16<<20)),
	)
}

func (m *Manager) run(ctx context.Context, c *conn) {
	defer close(c.done)
	backoff := time.Second
	for {
		err := m.session(ctx, c)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("agent connection lost", "agent", c.id, "addr", c.addr, "err", err)
		m.setState(c, func(s *State) {
			s.Status = StatusOffline
			s.Error = errString(err)
			s.Metrics = nil
		})
		// A session that lasted is a reason to retry quickly again.
		if errors.Is(err, errSessionWasHealthy) {
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

var errSessionWasHealthy = errors.New("stream ended")

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// session runs one connected lifetime: info, certificate renewal, metrics.
func (m *Manager) session(ctx context.Context, c *conn) error {
	cc, err := m.dial(c)
	if err != nil {
		return err
	}
	defer cc.Close()
	sys := nexosv1.NewSystemServiceClient(cc)

	ictx, cancel := context.WithTimeout(ctx, 10*time.Second)
	infoResp, err := sys.GetInfo(ictx, &nexosv1.GetInfoRequest{})
	cancel()
	if err != nil {
		return err
	}
	info := infoResp.GetInfo()
	if raw, err := json.Marshal(info); err == nil {
		_ = m.store.SetAgentSeen(ctx, c.id, string(raw), time.Now())
	}
	c.mu.Lock()
	c.cc = cc
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.cc = nil
		c.mu.Unlock()
	}()
	m.setState(c, func(s *State) { s.Status, s.Error, s.Info = StatusOnline, "", info })
	m.bus.Publish(events.Event{Type: "host.info", HostID: c.id, Data: info})
	slog.Info("agent online", "agent", c.id, "hostname", info.GetHostname())

	sctx, stop := context.WithCancel(ctx)
	defer stop()
	go m.renewLoop(sctx, c, cc)
	go m.infoLoop(sctx, c, sys)
	go m.eventLoop(sctx, c, cc)
	go m.containerStatsLoop(sctx, c, cc)

	stream, err := sys.StreamMetrics(sctx, &nexosv1.StreamMetricsRequest{IntervalSeconds: m.metricInterval})
	if err != nil {
		return err
	}
	started := time.Now()
	for {
		resp, err := stream.Recv()
		if err != nil {
			if time.Since(started) > time.Minute {
				return fmt.Errorf("%w: %v", errSessionWasHealthy, err)
			}
			return err
		}
		mt := resp.GetMetrics()
		m.setState(c, func(s *State) { s.Metrics = mt })
		m.bus.Publish(events.Event{Type: "host.metrics", HostID: c.id, Data: mt})
		if err := m.store.InsertMetric(ctx, c.id, store.MetricPoint{
			TS: mt.GetTimestampUnixMs(), CPU: mt.GetCpuPercent(),
			MemUsed: mt.GetMemoryUsedBytes(), MemTotal: mt.GetMemoryTotalBytes(), SwapUsed: mt.GetSwapUsedBytes(),
			Load1: mt.GetLoad1(), NetRx: mt.GetNetworkRxBytesPerSecond(), NetTx: mt.GetNetworkTxBytesPerSecond(),
			DiskRead: mt.GetDiskReadBytesPerSecond(), DiskWrite: mt.GetDiskWriteBytesPerSecond(),
		}); err != nil {
			slog.Warn("store metric", "agent", c.id, "err", err)
		}
	}
}

// infoLoop refreshes host and Docker info periodically.
func (m *Manager) infoLoop(ctx context.Context, c *conn, sys nexosv1.SystemServiceClient) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			ictx, cancel := context.WithTimeout(ctx, 10*time.Second)
			resp, err := sys.GetInfo(ictx, &nexosv1.GetInfoRequest{})
			cancel()
			if err != nil {
				continue
			}
			info := resp.GetInfo()
			m.setState(c, func(s *State) { s.Info = info })
			m.bus.Publish(events.Event{Type: "host.info", HostID: c.id, Data: info})
			if raw, err := json.Marshal(info); err == nil {
				_ = m.store.SetAgentSeen(ctx, c.id, string(raw), time.Now())
			}
		}
	}
}

// eventLoop relays Docker engine events of an agent to the browsers, which
// use them to refresh container/image/network views immediately.
func (m *Manager) eventLoop(ctx context.Context, c *conn, cc *grpc.ClientConn) {
	client := nexosv1.NewEventServiceClient(cc)
	for ctx.Err() == nil {
		stream, err := client.StreamEvents(ctx, &nexosv1.StreamEventsRequest{})
		if err == nil {
			for {
				resp, rerr := stream.Recv()
				if rerr != nil {
					break
				}
				m.bus.Publish(events.Event{Type: "docker.event", HostID: c.id, Data: resp.GetEvent()})
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second): // e.g. Docker restarting on the host
		}
	}
}

// containerStatsLoop records per-container resource usage history: samples
// every 10 s are averaged into one row per container and minute.
func (m *Manager) containerStatsLoop(ctx context.Context, c *conn, cc *grpc.ClientConn) {
	client := nexosv1.NewContainerServiceClient(cc)
	type acc struct {
		n                       int
		cpu, mem, rx, tx, limit float64
	}
	for ctx.Err() == nil {
		stream, err := client.StreamStats(ctx, &nexosv1.StreamStatsRequest{IntervalSeconds: 10})
		if err == nil {
			bucket := time.Now().Truncate(time.Minute)
			sums := map[string]*acc{}
			flush := func() {
				pts := make([]store.ContainerPoint, 0, len(sums))
				for name, a := range sums {
					n := float64(a.n)
					pts = append(pts, store.ContainerPoint{
						Name: name, TS: bucket.UnixMilli(), CPU: a.cpu / n, Mem: uint64(a.mem / n),
						MemLimit: uint64(a.limit), NetRx: a.rx / n, NetTx: a.tx / n,
					})
				}
				if err := m.store.InsertContainerMetrics(context.WithoutCancel(ctx), c.id, pts); err != nil {
					slog.Warn("store container metrics", "agent", c.id, "err", err)
				}
				sums = map[string]*acc{}
			}
			for {
				resp, rerr := stream.Recv()
				if rerr != nil {
					break
				}
				now := time.UnixMilli(resp.GetTimestampUnixMs())
				if b := now.Truncate(time.Minute); b.After(bucket) {
					flush()
					bucket = b
				}
				for _, st := range resp.GetStats() {
					name := st.GetName()
					if name == "" {
						continue
					}
					a := sums[name]
					if a == nil {
						a = &acc{}
						sums[name] = a
					}
					a.n++
					a.cpu += st.GetCpuPercent()
					a.mem += float64(st.GetMemoryUsageBytes())
					a.limit = float64(st.GetMemoryLimitBytes())
					a.rx += st.GetNetworkRxBytesPerSecond()
					a.tx += st.GetNetworkTxBytesPerSecond()
				}
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
		}
	}
}

func (m *Manager) renewLoop(ctx context.Context, c *conn, cc *grpc.ClientConn) {
	for {
		if err := m.renewIfNeeded(ctx, c.id, cc); err != nil && ctx.Err() == nil {
			slog.Error("agent certificate renewal failed", "agent", c.id, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(6 * time.Hour):
		}
	}
}

// renewIfNeeded rotates the agent certificate when it nears expiry. The agent
// generates the new key; the hub only signs the CSR.
func (m *Manager) renewIfNeeded(ctx context.Context, id string, cc *grpc.ClientConn) error {
	cur, err := m.store.LatestAgentCert(ctx, id)
	if err != nil {
		return err
	}
	if time.Until(cur.NotAfter) > pki.RenewBefore {
		return nil
	}
	return m.Renew(ctx, id, cc)
}

// Renew unconditionally rotates an agent's certificate.
func (m *Manager) Renew(ctx context.Context, id string, cc *grpc.ClientConn) error {
	certs := nexosv1.NewCertificateServiceClient(cc)
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	csr, err := certs.CreateCSR(rctx, &nexosv1.CreateCSRRequest{})
	if err != nil {
		return fmt.Errorf("create csr: %w", err)
	}
	leaf, certPEM, err := m.ca.SignAgentCSR(csr.GetCsrPem(), id)
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	fp := pki.Fingerprint(leaf)
	// Record before installing so the agent's next handshake is accepted.
	if err := m.store.AddAgentCert(rctx, store.AgentCert{Fingerprint: fp, AgentID: id, Serial: leaf.SerialNumber.Text(16), NotAfter: leaf.NotAfter}); err != nil {
		return err
	}
	if _, err := certs.InstallCertificate(rctx, &nexosv1.InstallCertificateRequest{CertificatePem: certPEM}); err != nil {
		return fmt.Errorf("install: %w", err)
	}
	if err := m.store.RevokeOtherAgentCerts(rctx, id, fp); err != nil {
		return err
	}
	_ = m.store.Audit(rctx, store.AuditEntry{Action: "agent.cert.renew", AgentID: id, Success: true, Detail: "not_after=" + leaf.NotAfter.Format(time.RFC3339)})
	slog.Info("agent certificate renewed", "agent", id, "not_after", leaf.NotAfter)
	return nil
}
