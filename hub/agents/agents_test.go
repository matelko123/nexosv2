package agents_test

import (
	"context"
	"crypto/tls"
	"net"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"nexos/agent/identity"
	"nexos/agent/server"
	"nexos/hub/agents"
	"nexos/hub/certs"
	"nexos/hub/events"
	"nexos/hub/store"
	"nexos/internal/enroll"
	"nexos/internal/pki"
)

type hubFixture struct {
	st     *store.Store
	ca     *pki.CA
	mgr    *agents.Manager
	bus    *events.Bus
	enroll *httptest.Server
}

func newHub(t *testing.T, ctx context.Context) *hubFixture {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "nexos.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ca, err := pki.LoadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	hubCert, err := certs.LoadLeaf(dir, "hub", ca, certs.HubOptions())
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	mgr := agents.NewManager(st, ca, hubCert.KP, bus, time.Second)
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	// Wrap the listener ourselves: httptest.StartTLS would inject its own
	// certificate, which wins over GetCertificate for SNI-less clients.
	srv := httptest.NewUnstartedServer(mgr.EnrollmentHandler())
	tlsCfg := pki.ServerConfig(hubCert.KP)
	tlsCfg.GetCertificate = hubCert.KP.WithChain(ca.Cert)
	srv.Listener = tls.NewListener(srv.Listener, tlsCfg)
	srv.Start()
	t.Cleanup(srv.Close)
	return &hubFixture{st: st, ca: ca, mgr: mgr, bus: bus, enroll: srv}
}

// startAgent enrolls an agent with bundle and serves it on a free port.
func startAgent(t *testing.T, bundle string, ln net.Listener) *identity.Identity {
	t.Helper()
	b, err := enroll.ParseBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := pki.GenerateKey()
	csr, _ := pki.CreateCSR(key, "x")
	res, err := enroll.Do(context.Background(), b, csr, "test-host")
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	dir := t.TempDir()
	if err := identity.Save(dir, b.AgentID, key, res.CertPEM, res.CAPEM); err != nil {
		t.Fatal(err)
	}
	id, err := identity.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	gs := server.New(id, nil, nil, nil).GRPCServer()
	go gs.Serve(ln)
	t.Cleanup(gs.Stop)
	return id
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestEnrollConnectMetricsRotate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHub(t, ctx)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := &store.Agent{ID: pki.NewAgentID(), Name: "test", Address: ln.Addr().String()}
	if err := h.st.CreateAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	enrollAddr := h.enroll.Listener.Addr().String()
	bundle, _, err := h.mgr.NewEnrollment(ctx, a.ID, enrollAddr)
	if err != nil {
		t.Fatal(err)
	}

	id := startAgent(t, bundle, ln)

	// Bundle is single use.
	b, _ := enroll.ParseBundle(bundle)
	key, _ := pki.GenerateKey()
	csr, _ := pki.CreateCSR(key, "x")
	if _, err := enroll.Do(ctx, b, csr, "again"); err == nil {
		t.Fatal("enrollment bundle accepted twice")
	}

	waitFor(t, "agent online with metrics", func() bool {
		st := h.mgr.State(a.ID)
		return st.Status == agents.StatusOnline && st.Metrics != nil
	})
	if pts, err := h.st.Metrics(ctx, a.ID, time.Now().Add(-time.Minute), 0); err != nil || len(pts) == 0 {
		t.Fatalf("metrics not stored: %v (%d)", err, len(pts))
	}

	// Rotate the certificate and check the agent now serves the new one.
	before := pki.Fingerprint(id.KeyPair.Leaf())
	cc, err := h.mgr.Client(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.mgr.Renew(ctx, a.ID, cc); err != nil {
		t.Fatalf("renew: %v", err)
	}
	after := pki.Fingerprint(id.KeyPair.Leaf())
	if before == after {
		t.Fatal("certificate not rotated")
	}
	if err := h.st.CheckAgentCert(ctx, a.ID, before); err == nil {
		t.Fatal("old certificate still accepted after rotation")
	}
	if err := h.st.CheckAgentCert(ctx, a.ID, after); err != nil {
		t.Fatalf("new certificate not accepted: %v", err)
	}

	// Reconnect with the rotated cert works.
	h.mgr.Connect(a.ID, a.Address)
	waitFor(t, "reconnect after rotation", func() bool {
		st := h.mgr.State(a.ID)
		return st.Status == agents.StatusOnline && st.Metrics != nil
	})

	// After deletion the pin is gone: a reconnect attempt must fail.
	h.mgr.Disconnect(a.ID)
	if err := h.st.DeleteAgent(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.st.CreateAgent(ctx, &store.Agent{ID: a.ID, Name: "ghost", Address: a.Address}); err != nil {
		t.Fatal(err)
	}
	h.mgr.Connect(a.ID, a.Address)
	waitFor(t, "rejected connection", func() bool {
		return h.mgr.State(a.ID).Status == agents.StatusOffline
	})
	if st := h.mgr.State(a.ID); st.Error == "" {
		t.Fatal("expected pin error")
	}
}

func TestEnrollmentRejectsBadSecret(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHub(t, ctx)
	a := &store.Agent{ID: pki.NewAgentID(), Name: "x", Address: "127.0.0.1:1"}
	if err := h.st.CreateAgent(ctx, a); err != nil {
		t.Fatal(err)
	}
	bundle, _, err := h.mgr.NewEnrollment(ctx, a.ID, h.enroll.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := enroll.ParseBundle(bundle)
	good := b.Secret
	b.Secret = "wrong"
	key, _ := pki.GenerateKey()
	csr, _ := pki.CreateCSR(key, "x")
	if _, err := enroll.Do(ctx, b, csr, "h"); err == nil {
		t.Fatal("wrong secret accepted")
	}
	// A wrong attempt does not burn the real secret.
	b.Secret = good
	if _, err := enroll.Do(ctx, b, csr, "h"); err != nil {
		t.Fatalf("valid secret rejected: %v", err)
	}
	// Wrong CA pin is rejected client-side before any secret is sent.
	other, _ := pki.NewCA("o")
	b.CAFingerprint = pki.Fingerprint(other.Cert)
	if _, err := enroll.Do(ctx, b, csr, "h"); err == nil {
		t.Fatal("wrong CA fingerprint accepted")
	}
}
