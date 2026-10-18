package docker

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/client"

	nexosv1 "nexos/internal/gen/nexos/v1"
)

const (
	// A result older than this triggers a new pass on the next request.
	duStale = 10 * time.Minute
	// Explicit refreshes are ignored if the last pass started more recently.
	duMinInterval = time.Minute
	// Upper bound for one pass, Docker's own volume walk included.
	duPassTimeout = 5 * time.Minute
	// Time spent walking bind mounts; large trees end up marked partial.
	duWalkBudget = 3 * time.Minute
	// How long the very first request waits before answering "computing".
	duFirstWait = 15 * time.Second
)

// Host paths that are never walked: virtual filesystems, sockets and Docker's
// own storage (volumes are measured through the Docker API instead).
var duSkipPrefixes = []string{"/proc", "/sys", "/dev", "/run", "/var/run", "/var/lib/docker"}

// DiskUsage measures space taken by volumes, bind mounts and writable layers.
// Measuring walks directory trees, so passes run in the background and the
// latest result is served from cache.
type DiskUsage struct {
	c        *Client
	hostRoot string

	mu      sync.Mutex
	last    *nexosv1.GetDiskUsageResponse
	running bool
	started time.Time
	done    chan struct{}
}

// NewDiskUsage measures paths under hostRoot, where the agent mounts the host
// filesystem; with an empty hostRoot, paths are read as-is.
func NewDiskUsage(c *Client, hostRoot string) *DiskUsage {
	return &DiskUsage{c: c, hostRoot: hostRoot}
}

// Get returns the cached result, starting a pass when it is stale or when
// refresh is set. Only the first request ever waits for a pass to finish.
func (d *DiskUsage) Get(ctx context.Context, refresh bool) *nexosv1.GetDiskUsageResponse {
	d.mu.Lock()
	stale := d.last == nil || time.Since(time.Unix(d.last.ComputedUnix, 0)) > duStale
	if !d.running && (stale || (refresh && time.Since(d.started) > duMinInterval)) {
		d.running, d.started, d.done = true, time.Now(), make(chan struct{})
		go d.pass(d.done)
	}
	wait := d.last == nil && d.running
	done := d.done
	d.mu.Unlock()

	if wait {
		select {
		case <-done:
		case <-time.After(duFirstWait):
		case <-ctx.Done():
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	out := &nexosv1.GetDiskUsageResponse{}
	if d.last != nil {
		out = &nexosv1.GetDiskUsageResponse{
			Volumes: d.last.Volumes, Binds: d.last.Binds, Containers: d.last.Containers,
			ComputedUnix: d.last.ComputedUnix, Error: d.last.Error,
		}
	}
	out.Computing = d.running
	return out
}

func (d *DiskUsage) pass(done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), duPassTimeout)
	defer cancel()
	start := time.Now()
	res := d.compute(ctx)
	slog.Info("disk usage computed", "volumes", len(res.Volumes), "binds", len(res.Binds), "duration", time.Since(start))

	d.mu.Lock()
	d.last, d.running = res, false
	d.mu.Unlock()
	close(done)
}

func (d *DiskUsage) compute(ctx context.Context) *nexosv1.GetDiskUsageResponse {
	out := &nexosv1.GetDiskUsageResponse{}
	df, err := d.c.api.DiskUsage(ctx, client.DiskUsageOptions{Containers: true, Volumes: true, Verbose: true})
	if err != nil {
		out.Error = err.Error()
	} else {
		for _, v := range df.Volumes.Items {
			size := int64(-1)
			if v.UsageData != nil {
				size = v.UsageData.Size
			}
			out.Volumes = append(out.Volumes, &nexosv1.VolumeUsage{Name: v.Name, SizeBytes: size})
		}
		for _, ct := range df.Containers.Items {
			out.Containers = append(out.Containers, &nexosv1.ContainerDiskUsage{Id: ct.ID, SizeRwBytes: ct.SizeRw, SizeRootFsBytes: ct.SizeRootFs})
		}
	}

	// Bind mounts are not covered by Docker's accounting; walk them here.
	sources := map[string]bool{}
	if cl, err := d.c.api.ContainerList(ctx, client.ContainerListOptions{All: true}); err == nil {
		for _, ct := range cl.Items {
			for _, m := range ct.Mounts {
				if m.Type == "bind" && m.Source != "" {
					sources[path.Clean(m.Source)] = true
				}
			}
		}
	} else if out.Error == "" {
		out.Error = err.Error()
	}
	paths := make([]string, 0, len(sources))
	for p := range sources {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	deadline := time.Now().Add(duWalkBudget)
	for _, p := range paths {
		out.Binds = append(out.Binds, d.measure(ctx, p, deadline))
	}
	out.ComputedUnix = time.Now().Unix()
	return out
}

func skippedPath(p string) bool {
	if p == "/" {
		return true
	}
	for _, pre := range duSkipPrefixes {
		if p == pre || strings.HasPrefix(p, pre+"/") {
			return true
		}
	}
	return false
}

// measure sums allocated space under a host path, counting hard links once.
func (d *DiskUsage) measure(ctx context.Context, source string, deadline time.Time) *nexosv1.BindUsage {
	u := &nexosv1.BindUsage{Source: source}
	if skippedPath(source) {
		u.Error = "system path, not measured"
		return u
	}
	if time.Now().After(deadline) {
		u.Error = "time budget exhausted, not measured"
		return u
	}
	root := source
	if d.hostRoot != "" {
		root = filepath.Join(d.hostRoot, source)
	}
	if _, err := os.Lstat(root); err != nil {
		u.Error = err.Error()
		return u
	}
	seen := map[fileID]bool{}
	n := 0
	err := filepath.WalkDir(root, func(_ string, e fs.DirEntry, err error) error {
		if err != nil {
			// Unreadable entries are skipped; the rest still counts.
			if e != nil && e.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if n++; n%1024 == 0 && (time.Now().After(deadline) || ctx.Err() != nil) {
			u.Partial = true
			return fs.SkipAll
		}
		info, err := e.Info()
		if err != nil {
			return nil
		}
		u.SizeBytes += allocated(info, seen)
		return nil
	})
	if err != nil {
		u.Error = err.Error()
	}
	return u
}
