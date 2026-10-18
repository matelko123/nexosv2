package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"google.golang.org/protobuf/proto"

	nexosv1 "nexos/internal/gen/nexos/v1"
)

const (
	maxLineBytes  = 16 << 10
	logBatchLines = 500
	logFlushEvery = 150 * time.Millisecond
)

// lineSink collects complete lines from one output stream into a shared,
// mutex-protected batch.
type lineSink struct {
	stream string
	mu     *sync.Mutex
	batch  *[]*nexosv1.LogLine
	buf    []byte
}

func (s *lineSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf = append(s.buf, p...)
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			if len(s.buf) > maxLineBytes { // overly long line: emit in chunks
				*s.batch = append(*s.batch, &nexosv1.LogLine{Stream: s.stream, Text: string(s.buf)})
				s.buf = s.buf[:0]
			}
			return len(p), nil
		}
		line := strings.TrimSuffix(string(s.buf[:i]), "\r")
		if len(line) > maxLineBytes {
			line = line[:maxLineBytes]
		}
		*s.batch = append(*s.batch, &nexosv1.LogLine{Stream: s.stream, Text: line})
		s.buf = s.buf[i+1:]
	}
}

func (s *lineSink) flushPartial() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.buf) > 0 {
		*s.batch = append(*s.batch, &nexosv1.LogLine{Stream: s.stream, Text: string(s.buf)})
		s.buf = s.buf[:0]
	}
}

// Logs streams container logs in batches of lines.
func (c *Client) Logs(ctx context.Context, req *nexosv1.StreamLogsRequest, send func([]*nexosv1.LogLine) error) error {
	info, _, err := c.Inspect(ctx, req.GetId())
	if err != nil {
		return err
	}
	opts := client.ContainerLogsOptions{
		ShowStdout: true, ShowStderr: true,
		Follow: req.GetFollow(), Timestamps: req.GetTimestamps(), Tail: "all",
	}
	if req.GetTail() > 0 {
		opts.Tail = strconv.Itoa(int(req.GetTail()))
	}
	if req.GetSinceUnix() > 0 {
		opts.Since = strconv.FormatInt(req.GetSinceUnix(), 10)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	rc, err := c.api.ContainerLogs(ctx, req.GetId(), opts)
	if err != nil {
		return err
	}
	defer rc.Close()

	var mu sync.Mutex
	var batch []*nexosv1.LogLine
	stdout := &lineSink{stream: "stdout", mu: &mu, batch: &batch}
	stderr := &lineSink{stream: "stderr", mu: &mu, batch: &batch}

	copyErr := make(chan error, 1)
	go func() {
		var err error
		if info.Config != nil && info.Config.Tty {
			_, err = io.Copy(stdout, rc)
		} else {
			_, err = stdcopy.StdCopy(stdout, stderr, rc)
		}
		stdout.flushPartial()
		stderr.flushPartial()
		copyErr <- err
	}()

	flush := func() error {
		mu.Lock()
		out := batch
		batch = nil
		mu.Unlock()
		for len(out) > 0 {
			n := min(len(out), logBatchLines)
			if err := send(out[:n]); err != nil {
				return err
			}
			out = out[n:]
		}
		return nil
	}
	t := time.NewTicker(logFlushEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-copyErr:
			if ferr := flush(); ferr != nil {
				return ferr
			}
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
				return err
			}
			return nil
		case <-t.C:
			if err := flush(); err != nil {
				return err
			}
		}
	}
}

// statsWatcher keeps the latest computed sample of one container.
type statsWatcher struct {
	cancel context.CancelFunc
	mu     sync.Mutex
	latest *nexosv1.ContainerStats
}

func memoryUsage(m container.MemoryStats) uint64 {
	// Same as the docker CLI: exclude reclaimable page cache.
	for _, k := range []string{"inactive_file", "total_inactive_file"} {
		if v, ok := m.Stats[k]; ok && v < m.Usage {
			return m.Usage - v
		}
	}
	return m.Usage
}

func cpuPercent(s *container.StatsResponse) float64 {
	cpuDelta := float64(s.CPUStats.CPUUsage.TotalUsage) - float64(s.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(s.CPUStats.SystemUsage) - float64(s.PreCPUStats.SystemUsage)
	cpus := float64(s.CPUStats.OnlineCPUs)
	if cpus == 0 {
		cpus = float64(len(s.CPUStats.CPUUsage.PercpuUsage))
	}
	if cpuDelta <= 0 || sysDelta <= 0 || cpus == 0 {
		return 0
	}
	return cpuDelta / sysDelta * cpus * 100
}

func (c *Client) watchStats(ctx context.Context, id string, w *statsWatcher) {
	res, err := c.api.ContainerStats(ctx, id, client.ContainerStatsOptions{Stream: true})
	if err != nil {
		return
	}
	defer res.Body.Close()
	dec := json.NewDecoder(bufio.NewReader(res.Body))
	for {
		var s container.StatsResponse
		if err := dec.Decode(&s); err != nil {
			return
		}
		st := &nexosv1.ContainerStats{
			Id:               id,
			Name:             strings.TrimPrefix(s.Name, "/"),
			CpuPercent:       cpuPercent(&s),
			MemoryUsageBytes: memoryUsage(s.MemoryStats),
			MemoryLimitBytes: s.MemoryStats.Limit,
			Pids:             s.PidsStats.Current,
		}
		if st.MemoryLimitBytes > 0 {
			st.MemoryPercent = float64(st.MemoryUsageBytes) / float64(st.MemoryLimitBytes) * 100
		}
		for _, n := range s.Networks {
			st.NetworkRxBytes += n.RxBytes
			st.NetworkTxBytes += n.TxBytes
		}
		for _, e := range s.BlkioStats.IoServiceBytesRecursive {
			switch strings.ToLower(e.Op) {
			case "read":
				st.BlockReadBytes += e.Value
			case "write":
				st.BlockWriteBytes += e.Value
			}
		}
		w.mu.Lock()
		w.latest = st
		w.mu.Unlock()
	}
}

// Stats streams stats of the given containers (all running ones when ids
// is empty) every interval. Each container is watched by one long-lived
// Docker stats stream; the set is refreshed periodically in "all" mode.
func (c *Client) Stats(ctx context.Context, ids []string, interval time.Duration, send func(*nexosv1.StreamStatsResponse) error) error {
	for _, id := range ids {
		if !ValidID(id) {
			return ErrInvalidID
		}
	}
	watchers := map[string]*statsWatcher{}
	defer func() {
		for _, w := range watchers {
			w.cancel()
		}
	}()
	refresh := func() error {
		want := ids
		if len(want) == 0 {
			list, err := c.ListContainers(ctx, false)
			if err != nil {
				return err
			}
			want = nil
			for _, ct := range list {
				want = append(want, ct.Id)
			}
		}
		keep := map[string]bool{}
		for _, id := range want {
			keep[id] = true
			if _, ok := watchers[id]; !ok {
				wctx, cancel := context.WithCancel(ctx)
				w := &statsWatcher{cancel: cancel}
				watchers[id] = w
				go c.watchStats(wctx, id, w)
			}
		}
		for id, w := range watchers {
			if !keep[id] {
				w.cancel()
				delete(watchers, id)
			}
		}
		return nil
	}
	if err := refresh(); err != nil {
		return err
	}

	type prevNet struct {
		rx, tx uint64
		at     time.Time
	}
	prev := map[string]prevNet{}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	resync := time.NewTicker(10 * time.Second)
	defer resync.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-resync.C:
			if len(ids) == 0 {
				_ = refresh()
			}
		case now := <-tick.C:
			msg := &nexosv1.StreamStatsResponse{TimestampUnixMs: now.UnixMilli()}
			for id, w := range watchers {
				w.mu.Lock()
				st := w.latest
				w.mu.Unlock()
				if st == nil {
					continue
				}
				out := proto.Clone(st).(*nexosv1.ContainerStats)
				if p, ok := prev[id]; ok {
					dt := now.Sub(p.at).Seconds()
					if dt > 0 && out.NetworkRxBytes >= p.rx && out.NetworkTxBytes >= p.tx {
						out.NetworkRxBytesPerSecond = float64(out.NetworkRxBytes-p.rx) / dt
						out.NetworkTxBytesPerSecond = float64(out.NetworkTxBytes-p.tx) / dt
					}
				}
				prev[id] = prevNet{rx: out.NetworkRxBytes, tx: out.NetworkTxBytes, at: now}
				msg.Stats = append(msg.Stats, out)
			}
			if err := send(msg); err != nil {
				return err
			}
		}
	}
}

// ExecSession is an interactive process attached to a container.
type ExecSession struct {
	c    *Client
	id   string
	tty  bool
	resp client.HijackedResponse
}

// DefaultShell prefers bash and falls back to sh.
var DefaultShell = []string{"/bin/sh", "-c", "if command -v bash >/dev/null 2>&1; then exec bash; else exec sh; fi"}

func (c *Client) StartExec(ctx context.Context, start *nexosv1.ExecStart) (*ExecSession, error) {
	if !ValidID(start.GetContainerId()) {
		return nil, ErrInvalidID
	}
	cmd := start.GetCommand()
	if len(cmd) == 0 {
		cmd = DefaultShell
	}
	size := client.ConsoleSize{Height: uint(max(start.GetRows(), 1)), Width: uint(max(start.GetCols(), 1))}
	created, err := c.api.ExecCreate(ctx, start.GetContainerId(), client.ExecCreateOptions{
		User: start.GetUser(), TTY: start.GetTty(), ConsoleSize: size,
		AttachStdin: true, AttachStdout: true, AttachStderr: true,
		Cmd: cmd, Env: []string{"TERM=xterm-256color"},
	})
	if err != nil {
		return nil, err
	}
	att, err := c.api.ExecAttach(ctx, created.ID, client.ExecAttachOptions{TTY: start.GetTty(), ConsoleSize: size})
	if err != nil {
		return nil, err
	}
	return &ExecSession{c: c, id: created.ID, tty: start.GetTty(), resp: att.HijackedResponse}, nil
}

func (e *ExecSession) Write(p []byte) (int, error) { return e.resp.Conn.Write(p) }

func (e *ExecSession) Resize(ctx context.Context, cols, rows uint32) error {
	_, err := e.c.api.ExecResize(ctx, e.id, client.ExecResizeOptions{Height: uint(rows), Width: uint(cols)})
	return err
}

// CopyOutput copies process output to w until the process ends.
func (e *ExecSession) CopyOutput(w io.Writer) error {
	var err error
	if e.tty {
		_, err = io.Copy(w, e.resp.Reader)
	} else {
		_, err = stdcopy.StdCopy(w, w, e.resp.Reader)
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// ExitCode returns the process exit code (-1 if unknown).
func (e *ExecSession) ExitCode(ctx context.Context) int {
	for range 20 {
		res, err := e.c.api.ExecInspect(ctx, e.id, client.ExecInspectOptions{})
		if err != nil {
			return -1
		}
		if !res.Running {
			return res.ExitCode
		}
		time.Sleep(50 * time.Millisecond)
	}
	return -1
}

func (e *ExecSession) Close() { e.resp.Close() }
