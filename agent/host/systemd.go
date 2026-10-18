package host

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	sddbus "github.com/coreos/go-systemd/v22/dbus"

	nexosv1 "nexos/internal/gen/nexos/v1"
)

var (
	unitNamePattern = regexp.MustCompile(`^[a-zA-Z0-9@._:\\-]{1,200}\.(service|socket|timer|mount|target|path)$`)

	// protectedUnits cannot be stopped/restarted from the UI: losing them
	// would cut access to the host or to NexOS itself.
	protectedUnits = []string{
		"docker.service", "docker.socket", "containerd.service", "dbus.service", "dbus.socket",
		"ssh.service", "ssh.socket", "sshd.service", "systemd-*", "wg-quick@*", "networking.service",
		"NetworkManager.service", "user@*", "polkit.service", "tailscaled.service",
	}

	ErrInvalidUnit   = errors.New("invalid unit name")
	ErrProtectedUnit = errors.New("this unit is critical for host access or NexOS and cannot be changed from the UI")
)

func unitProtected(name string) bool {
	for _, p := range protectedUnits {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// dbusConn connects to the host's system bus. The agent container must
// mount /run/dbus/system_bus_socket from the host.
func dbusConn(ctx context.Context) (*sddbus.Conn, error) {
	c, err := sddbus.NewSystemConnectionContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to host systemd (is /run/dbus/system_bus_socket mounted?): %w", err)
	}
	return c, nil
}

func (h *Host) ListUnits(ctx context.Context) ([]*nexosv1.Unit, error) {
	if !h.Enabled(FeatureSystemd) {
		return nil, ErrDisabled
	}
	c, err := dbusConn(ctx)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	patterns := []string{"*.service", "*.timer"}
	loaded, err := c.ListUnitsByPatternsContext(ctx, nil, patterns)
	if err != nil {
		return nil, err
	}
	files, _ := c.ListUnitFilesByPatternsContext(ctx, nil, patterns)
	fileState := map[string]string{}
	for _, f := range files {
		fileState[path.Base(f.Path)] = f.Type
	}
	seen := map[string]bool{}
	var out []*nexosv1.Unit
	for _, u := range loaded {
		seen[u.Name] = true
		out = append(out, &nexosv1.Unit{
			Name: u.Name, Description: u.Description, LoadState: u.LoadState, ActiveState: u.ActiveState,
			SubState: u.SubState, UnitFileState: fileState[u.Name], Protected: unitProtected(u.Name),
		})
	}
	// Installed but not loaded units (e.g. disabled services).
	for name, state := range fileState {
		if !seen[name] && !strings.Contains(name, "@.") {
			out = append(out, &nexosv1.Unit{Name: name, LoadState: "not-loaded", ActiveState: "inactive", SubState: "dead", UnitFileState: state, Protected: unitProtected(name)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (h *Host) UnitAction(ctx context.Context, name string, action nexosv1.UnitActionType) (string, error) {
	if !h.Enabled(FeatureSystemd) {
		return "", ErrDisabled
	}
	if !unitNamePattern.MatchString(name) {
		return "", ErrInvalidUnit
	}
	if action != nexosv1.UnitActionType_UNIT_ACTION_TYPE_START && unitProtected(name) {
		return "", ErrProtectedUnit
	}
	c, err := dbusConn(ctx)
	if err != nil {
		return "", err
	}
	defer c.Close()
	done := make(chan string, 1)
	switch action {
	case nexosv1.UnitActionType_UNIT_ACTION_TYPE_START:
		_, err = c.StartUnitContext(ctx, name, "replace", done)
	case nexosv1.UnitActionType_UNIT_ACTION_TYPE_STOP:
		_, err = c.StopUnitContext(ctx, name, "replace", done)
	case nexosv1.UnitActionType_UNIT_ACTION_TYPE_RESTART:
		_, err = c.RestartUnitContext(ctx, name, "replace", done)
	case nexosv1.UnitActionType_UNIT_ACTION_TYPE_RELOAD:
		_, err = c.ReloadUnitContext(ctx, name, "replace", done)
	default:
		return "", errors.New("unknown action")
	}
	if err != nil {
		return "", err
	}
	select {
	case res := <-done:
		return res, nil
	case <-time.After(90 * time.Second):
		return "timeout", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// StreamJournal reads the host journal with journalctl --root, which works
// on the journal files under the host root mount (no host access needed
// beyond reading /var/log/journal and /run/log/journal).
func (h *Host) StreamJournal(ctx context.Context, unit string, lines uint32, follow bool, send func([]string) error) error {
	if !h.Enabled(FeatureSystemd) {
		return ErrDisabled
	}
	if unit != "" && !unitNamePattern.MatchString(unit) {
		return ErrInvalidUnit
	}
	if lines == 0 || lines > 10000 {
		lines = 300
	}
	args := []string{"--no-pager", "-o", "short-iso", "-n", strconv.Itoa(int(lines))}
	if h.Root != "" {
		args = append(args, "--root", h.Root)
	}
	if unit != "" {
		args = append(args, "-u", unit)
	}
	if follow {
		args = append(args, "-f")
	}
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("journalctl: %w", err)
	}
	lineCh := make(chan string, 256)
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			lineCh <- sc.Text()
		}
		close(lineCh)
	}()
	var batch []string
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := send(batch)
		batch = nil
		return err
	}
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case l, ok := <-lineCh:
			if !ok {
				if err := flush(); err != nil {
					return err
				}
				if err := cmd.Wait(); err != nil && ctx.Err() == nil {
					msg := strings.TrimSpace(stderr.String())
					if msg == "" {
						msg = err.Error()
					}
					return errors.New("journalctl: " + msg)
				}
				return nil
			}
			batch = append(batch, l)
			if len(batch) >= 500 {
				if err := flush(); err != nil {
					return err
				}
			}
		case <-t.C:
			if err := flush(); err != nil {
				return err
			}
		}
	}
}
