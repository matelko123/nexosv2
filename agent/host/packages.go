package host

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	nexosv1 "nexos/internal/gen/nexos/v1"
)

var aptLine = regexp.MustCompile(`^([^/\s]+)/(\S+)\s+(\S+)\s+\S+\s+\[upgradable from: ([^\]]+)\]`)

// ListPackageUpdates lists pending apt upgrades of the host without touching
// it: the agent's apt reads the host's package state through the read-only
// root mount (RootDir), with locking and cache writes disabled. The result
// reflects the host's last `apt update` (usually daily via unattended-upgrades).
func (h *Host) ListPackageUpdates(ctx context.Context) (*nexosv1.ListPackageUpdatesResponse, error) {
	if !h.Enabled(FeaturePackages) {
		return nil, ErrDisabled
	}
	if _, err := os.Stat(h.path("/var/lib/dpkg/status")); err != nil {
		return nil, errors.New("package updates are supported on Debian/Ubuntu hosts only")
	}
	resp := &nexosv1.ListPackageUpdatesResponse{Distribution: h.osName()}

	args := []string{"list", "--upgradable",
		"-o", "Debug::NoLocking=1",
		"-o", "Dir::Cache::pkgcache=", "-o", "Dir::Cache::srcpkgcache=",
	}
	if h.Root != "" {
		args = append(args, "-o", "RootDir="+h.Root)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "apt", args...)
	cmd.Env = append(os.Environ(), "LANG=C", "LC_ALL=C")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("apt: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		m := aptLine.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		resp.Updates = append(resp.Updates, &nexosv1.PackageUpdate{
			Name: m[1], Source: m[2], NewVersion: m[3], CurrentVersion: m[4],
			Security: strings.Contains(m[2], "-security"),
		})
	}
	sort.Slice(resp.Updates, func(i, j int) bool {
		if resp.Updates[i].Security != resp.Updates[j].Security {
			return resp.Updates[i].Security
		}
		return resp.Updates[i].Name < resp.Updates[j].Name
	})

	// Newest Release file = last successful `apt update`.
	var newest time.Time
	files, _ := filepath.Glob(h.path("/var/lib/apt/lists/*Release"))
	for _, f := range files {
		if st, err := os.Stat(f); err == nil && st.ModTime().After(newest) {
			newest = st.ModTime()
		}
	}
	if !newest.IsZero() {
		resp.ListsUpdatedUnix = newest.Unix()
	}
	if _, err := os.Stat(h.path("/run/reboot-required")); err == nil {
		resp.RebootRequired = true
		if raw, err := os.ReadFile(h.path("/run/reboot-required.pkgs")); err == nil {
			for _, p := range strings.Fields(string(raw)) {
				resp.RebootPackages = append(resp.RebootPackages, p)
			}
		}
	}
	return resp, nil
}

func (h *Host) osName() string {
	raw, err := os.ReadFile(h.path("/etc/os-release"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}
