package host

import (
	"context"
	"errors"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"

	nexosv1 "nexos/internal/gen/nexos/v1"
)

var containerCgroup = regexp.MustCompile(`(?:docker[-/]|/docker/)([a-f0-9]{64})`)

type cpuSample struct {
	created int64
	total   float64
}

// processSampler remembers CPU times between calls so CPU usage can be
// computed like top does (delta of CPU time over wall time).
type processSampler struct {
	mu   sync.Mutex
	at   time.Time
	prev map[int32]cpuSample
}

var sampler = &processSampler{}

func snapshotTimes(ctx context.Context, procs []*process.Process) map[int32]cpuSample {
	out := make(map[int32]cpuSample, len(procs))
	for _, p := range procs {
		t, err := p.TimesWithContext(ctx)
		if err != nil {
			continue
		}
		created, _ := p.CreateTimeWithContext(ctx)
		out[p.Pid] = cpuSample{created: created, total: t.User + t.System}
	}
	return out
}

// ListProcesses returns all host processes. It needs the host /proc
// (HOST_PROC) and is only meaningful with the processes feature.
func (h *Host) ListProcesses(ctx context.Context) (*nexosv1.ListProcessesResponse, error) {
	if !h.Enabled(FeatureProcesses) {
		return nil, ErrDisabled
	}
	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, err
	}
	sampler.mu.Lock()
	prev, prevAt := sampler.prev, sampler.at
	sampler.mu.Unlock()
	if prev == nil || time.Since(prevAt) > time.Minute {
		// No recent baseline: take one and wait briefly.
		prev, prevAt = snapshotTimes(ctx, procs), time.Now()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(700 * time.Millisecond):
		}
	}
	now := time.Now()
	cur := snapshotTimes(ctx, procs)
	sampler.mu.Lock()
	sampler.prev, sampler.at = cur, now
	sampler.mu.Unlock()
	elapsed := now.Sub(prevAt).Seconds()

	vm, _ := mem.VirtualMemoryWithContext(ctx)
	var memTotal uint64
	if vm != nil {
		memTotal = vm.Total
	}
	users := h.users()
	resp := &nexosv1.ListProcessesResponse{MemoryTotalBytes: memTotal}
	for _, p := range procs {
		out := &nexosv1.Process{Pid: p.Pid}
		out.Ppid, _ = p.PpidWithContext(ctx)
		out.Name, _ = p.NameWithContext(ctx)
		if cmd, err := p.CmdlineWithContext(ctx); err == nil {
			if len(cmd) > 1000 {
				cmd = cmd[:1000]
			}
			out.Command = cmd
		}
		if uids, err := p.UidsWithContext(ctx); err == nil && len(uids) > 0 {
			uid := strconv.Itoa(int(uids[0]))
			out.User = users[uid]
			if out.User == "" {
				out.User = uid
			}
		}
		if mi, err := p.MemoryInfoWithContext(ctx); err == nil {
			out.MemoryRssBytes = mi.RSS
			if memTotal > 0 {
				out.MemoryPercent = float64(mi.RSS) / float64(memTotal) * 100
			}
		}
		if st, err := p.StatusWithContext(ctx); err == nil && len(st) > 0 {
			out.Status = st[0]
		}
		if n, err := p.NumThreadsWithContext(ctx); err == nil {
			out.Threads = n
		}
		if c, ok := cur[p.Pid]; ok {
			out.StartedUnix = c.created / 1000
			if b, ok := prev[p.Pid]; ok && b.created == c.created && elapsed > 0 {
				out.CpuPercent = max(0, (c.total-b.total)/elapsed*100)
			}
		}
		out.ContainerId = containerOf(h, p.Pid)
		resp.Processes = append(resp.Processes, out)
	}
	sort.Slice(resp.Processes, func(i, j int) bool { return resp.Processes[i].CpuPercent > resp.Processes[j].CpuPercent })
	if n, err := cpu.CountsWithContext(ctx, true); err == nil {
		resp.CpuCores = uint32(n)
	}
	return resp, nil
}

// containerOf returns the Docker container id of a process from its cgroup.
func containerOf(h *Host, pid int32) string {
	procRoot := os.Getenv("HOST_PROC")
	if procRoot == "" {
		procRoot = "/proc"
	}
	raw, err := os.ReadFile(procRoot + "/" + strconv.Itoa(int(pid)) + "/cgroup")
	if err != nil {
		return ""
	}
	if m := containerCgroup.FindStringSubmatch(string(raw)); m != nil {
		return m[1]
	}
	return ""
}

var (
	ErrProtectedProcess = errors.New("refusing to signal this process")
	ErrProcessChanged   = errors.New("process no longer exists or its PID was reused")
)

// Signal sends SIGTERM or SIGKILL to a host process. The agent must share
// the host PID namespace (pid: host) and have CAP_KILL.
func (h *Host) Signal(ctx context.Context, pid int32, sig nexosv1.ProcessSignal, started int64) error {
	if !h.Enabled(FeatureProcesses) {
		return ErrDisabled
	}
	if pid <= 1 || int(pid) == os.Getpid() || int(pid) == os.Getppid() {
		return ErrProtectedProcess
	}
	p, err := process.NewProcessWithContext(ctx, pid)
	if err != nil {
		return ErrProcessChanged
	}
	created, err := p.CreateTimeWithContext(ctx)
	if err != nil || created/1000 != started {
		return ErrProcessChanged
	}
	// Never kill the processes that run Docker itself or the kernel threads.
	if name, _ := p.NameWithContext(ctx); strings.HasPrefix(name, "kthread") || name == "dockerd" || name == "containerd" || name == "systemd" {
		return ErrProtectedProcess
	}
	return killPID(int(pid), sig == nexosv1.ProcessSignal_PROCESS_SIGNAL_KILL)
}
