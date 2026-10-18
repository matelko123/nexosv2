// Package host implements the opt-in host management features: processes,
// systemd units and journal, and package updates.
//
// Each feature needs extra privileges in the agent container (see
// deploy/agent/compose.host-features.yaml) and is enabled explicitly with
// NEXOS_HOST_FEATURES, e.g. "processes,systemd,packages".
package host

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	FeatureProcesses = "processes"
	FeatureSystemd   = "systemd"
	FeaturePackages  = "packages"
)

var ErrDisabled = errors.New("this host feature is not enabled on the agent (NEXOS_HOST_FEATURES)")

// Host gives access to the host through the read-only root mount.
type Host struct {
	Root     string // host root as seen by the agent ("" when not containerised)
	features map[string]bool
}

// New parses the comma-separated feature list.
func New(featureList, root string) *Host {
	h := &Host{Root: root, features: map[string]bool{}}
	for _, f := range strings.Split(featureList, ",") {
		switch f = strings.TrimSpace(strings.ToLower(f)); f {
		case FeatureProcesses, FeatureSystemd, FeaturePackages:
			h.features[f] = true
		}
	}
	return h
}

func (h *Host) Enabled(f string) bool { return h != nil && h.features[f] }

// Features lists enabled features, sorted.
func (h *Host) Features() []string {
	if h == nil {
		return nil
	}
	out := make([]string, 0, len(h.features))
	for f := range h.features {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// path maps an absolute host path to the agent's view of it.
func (h *Host) path(p string) string {
	if h.Root == "" {
		return p
	}
	return filepath.Join(h.Root, p)
}

// users maps uid to user name from the host's /etc/passwd.
func (h *Host) users() map[string]string {
	out := map[string]string{}
	raw, err := os.ReadFile(h.path("/etc/passwd"))
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Split(line, ":")
		if len(f) >= 3 {
			out[f[2]] = f[0]
		}
	}
	return out
}
