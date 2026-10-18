// Package stacks manages Docker Compose projects through the compose CLI.
//
// Managed stacks live in Dir/<name>/compose.yaml (+ .env). Dir must be
// mounted into the agent container at the same path as on the host, so that
// relative bind mounts in compose files resolve to the same host paths.
package stacks

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"nexos/agent/docker"
	nexosv1 "nexos/internal/gen/nexos/v1"
	"nexos/internal/pki"
)

const (
	composeFile  = "compose.yaml"
	envFile      = ".env"
	maxFileBytes = 1 << 20
)

var (
	ErrInvalidName = errors.New("invalid stack name: use lowercase letters, digits, '-' and '_' (max 63)")
	ErrNotFound    = errors.New("stack not found")
	ErrExists      = errors.New("stack already exists")
	ErrNotManaged  = errors.New("this compose project is managed outside NexOS: only start, stop, restart and down are available")
	ErrProtected   = errors.New("this stack contains NexOS's own containers and cannot be changed from the UI")
	ErrBusy        = errors.New("another operation is running on this stack")
	// ErrInvalid wraps compose validation failures.
	ErrInvalid = errors.New("invalid compose file")

	namePattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	composeVariants = []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"}
)

// ValidName reports whether name is an acceptable compose project name.
func ValidName(name string) bool { return namePattern.MatchString(name) }

type Manager struct {
	Dir      string
	HostRoot string // where the host filesystem is mounted (read-only), "" if not containerised
	Docker   *docker.Client

	mu   sync.Mutex
	busy map[string]bool
}

func New(dir, hostRoot string, dc *docker.Client) (*Manager, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("stacks dir: %w", err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	return &Manager{Dir: abs, HostRoot: hostRoot, Docker: dc, busy: map[string]bool{}}, nil
}

func (m *Manager) stackDir(name string) string { return filepath.Join(m.Dir, name) }

// composePath returns the compose file of a managed stack, or "".
func (m *Manager) composePath(name string) string {
	for _, f := range composeVariants {
		p := filepath.Join(m.stackDir(name), f)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

func (m *Manager) lock(name string) (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy[name] {
		return nil, ErrBusy
	}
	m.busy[name] = true
	return func() {
		m.mu.Lock()
		delete(m.busy, name)
		m.mu.Unlock()
	}, nil
}

// List returns managed stacks plus compose projects discovered from labels.
func (m *Manager) List(ctx context.Context) ([]*nexosv1.Stack, error) {
	byName := map[string]*nexosv1.Stack{}
	entries, err := os.ReadDir(m.Dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || !ValidName(e.Name()) {
			continue
		}
		p := m.composePath(e.Name())
		if p == "" {
			continue
		}
		st := &nexosv1.Stack{Name: e.Name(), Managed: true, WorkingDir: m.stackDir(e.Name()), ConfigFiles: []string{p}, Status: "empty"}
		if fi, err := os.Stat(p); err == nil {
			st.UpdatedUnix = fi.ModTime().Unix()
		}
		byName[e.Name()] = st
	}
	if m.Docker != nil {
		containers, err := m.Docker.ListContainers(ctx, true)
		if err != nil {
			return nil, err
		}
		for _, c := range containers {
			proj := c.ComposeProject
			if proj == "" {
				continue
			}
			st, ok := byName[proj]
			if !ok {
				st = &nexosv1.Stack{
					Name:       proj,
					WorkingDir: c.Labels["com.docker.compose.project.working_dir"],
				}
				if f := c.Labels["com.docker.compose.project.config_files"]; f != "" {
					st.ConfigFiles = strings.Split(f, ",")
				}
				byName[proj] = st
			}
			st.ContainersTotal++
			if c.State == "running" {
				st.ContainersRunning++
			}
			if c.Protected {
				st.Protected = true
			}
		}
	}
	out := make([]*nexosv1.Stack, 0, len(byName))
	for _, st := range byName {
		switch {
		case st.ContainersTotal == 0:
			st.Status = "empty"
		case st.ContainersRunning == st.ContainersTotal:
			st.Status = "running"
		case st.ContainersRunning == 0:
			st.Status = "stopped"
		default:
			st.Status = "partial"
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Managed != out[j].Managed {
			return out[i].Managed
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (m *Manager) find(ctx context.Context, name string) (*nexosv1.Stack, error) {
	if !ValidName(name) {
		return nil, ErrInvalidName
	}
	list, err := m.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, st := range list {
		if st.Name == name {
			return st, nil
		}
	}
	return nil, ErrNotFound
}

func readLimited(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxFileBytes {
		return "", errors.New("file too large")
	}
	return string(b), nil
}

// hostPath maps a host path to where it is visible inside the agent.
func (m *Manager) hostPath(p string) string {
	if m.HostRoot == "" || strings.HasPrefix(p, m.Dir+string(filepath.Separator)) {
		return p
	}
	return filepath.Join(m.HostRoot, p)
}

func (m *Manager) Get(ctx context.Context, name string, includeEnv bool) (*nexosv1.GetStackResponse, error) {
	st, err := m.find(ctx, name)
	if err != nil {
		return nil, err
	}
	resp := &nexosv1.GetStackResponse{Stack: st}
	if st.Managed {
		if resp.Compose, err = readLimited(st.ConfigFiles[0]); err != nil {
			return nil, err
		}
		if includeEnv {
			env, err := readLimited(filepath.Join(st.WorkingDir, envFile))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			resp.Env = env
		}
		return resp, nil
	}
	// External project: show its files read-only through the host mount.
	files := st.ConfigFiles
	if includeEnv && st.WorkingDir != "" {
		files = append(append([]string{}, files...), filepath.Join(st.WorkingDir, envFile))
	}
	for _, p := range files {
		f := &nexosv1.StackFile{Path: p}
		if !filepath.IsAbs(p) {
			f.Error = "relative path"
		} else if content, err := readLimited(m.hostPath(filepath.Clean(p))); err != nil {
			f.Error = err.Error()
		} else {
			f.Content = content
		}
		resp.Files = append(resp.Files, f)
	}
	return resp, nil
}

// composeArgs builds the base command for a stack.
func (m *Manager) composeArgs(st *nexosv1.Stack) []string {
	args := []string{"compose", "--ansi", "never", "--progress", "plain", "-p", st.Name}
	if st.Managed {
		args = append(args, "--project-directory", st.WorkingDir, "-f", st.ConfigFiles[0])
	}
	return args
}

// Save validates and writes a managed stack.
func (m *Manager) Save(ctx context.Context, name, compose, env string, create bool) (*nexosv1.Stack, error) {
	if !ValidName(name) {
		return nil, ErrInvalidName
	}
	if len(compose) > maxFileBytes || len(env) > maxFileBytes {
		return nil, errors.Join(ErrInvalid, errors.New("file too large"))
	}
	unlock, err := m.lock(name)
	if err != nil {
		return nil, err
	}
	defer unlock()

	dir := m.stackDir(name)
	exists := m.composePath(name) != ""
	if create && exists {
		return nil, ErrExists
	}
	if !create && !exists {
		return nil, ErrNotFound
	}
	if create {
		// Refuse to shadow a compose project that already runs elsewhere.
		if st, err := m.find(ctx, name); err == nil && !st.Managed {
			return nil, fmt.Errorf("%w: a compose project named %q already exists on this host", ErrExists, name)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	// Validate the candidate files before replacing anything.
	tmpCompose := filepath.Join(dir, ".nexos-compose.yaml")
	tmpEnv := filepath.Join(dir, ".nexos.env")
	defer os.Remove(tmpCompose)
	defer os.Remove(tmpEnv)
	if err := os.WriteFile(tmpCompose, []byte(compose), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(tmpEnv, []byte(env), 0o600); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "docker", "compose", "--ansi", "never", "-p", name, "--project-directory", dir,
		"-f", tmpCompose, "--env-file", tmpEnv, "config", "--quiet")
	if out, err := cmd.CombinedOutput(); err != nil {
		if create && !exists {
			os.Remove(dir)
		}
		msg := strings.TrimSpace(strings.ReplaceAll(string(out), tmpCompose, composeFile))
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%w: %s", ErrInvalid, msg)
	}

	// Older variants (docker-compose.yml...) are replaced by compose.yaml.
	for _, f := range composeVariants[1:] {
		os.Remove(filepath.Join(dir, f))
	}
	if err := pki.WriteFileAtomic(filepath.Join(dir, composeFile), []byte(compose), 0o644); err != nil {
		return nil, err
	}
	if env == "" {
		if err := os.Remove(filepath.Join(dir, envFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	} else if err := pki.WriteFileAtomic(filepath.Join(dir, envFile), []byte(env), 0o600); err != nil {
		return nil, err
	}
	return m.find(ctx, name)
}

var opArgs = map[nexosv1.StackOperation][]string{
	nexosv1.StackOperation_STACK_OPERATION_UP:      {"up", "--detach", "--remove-orphans"},
	nexosv1.StackOperation_STACK_OPERATION_DOWN:    {"down", "--remove-orphans"},
	nexosv1.StackOperation_STACK_OPERATION_PULL:    {"pull"},
	nexosv1.StackOperation_STACK_OPERATION_RESTART: {"restart"},
	nexosv1.StackOperation_STACK_OPERATION_STOP:    {"stop"},
	nexosv1.StackOperation_STACK_OPERATION_START:   {"start"},
}

// Run executes a compose operation, streaming combined output lines.
func (m *Manager) Run(ctx context.Context, req *nexosv1.RunStackRequest, line func(string) error) (int, error) {
	st, err := m.find(ctx, req.GetName())
	if err != nil {
		return -1, err
	}
	op := req.GetOperation()
	base, ok := opArgs[op]
	if !ok {
		return -1, errors.New("unknown operation")
	}
	if st.Protected {
		return -1, ErrProtected
	}
	if !st.Managed && (op == nexosv1.StackOperation_STACK_OPERATION_UP || op == nexosv1.StackOperation_STACK_OPERATION_PULL) {
		return -1, ErrNotManaged
	}
	unlock, err := m.lock(st.Name)
	if err != nil {
		return -1, err
	}
	defer unlock()

	args := append(m.composeArgs(st), base...)
	if op == nexosv1.StackOperation_STACK_OPERATION_UP && req.GetPull() {
		args = append(args, "--pull", "always")
	}
	if op == nexosv1.StackOperation_STACK_OPERATION_DOWN && req.GetRemoveVolumes() {
		args = append(args, "--volumes")
	}
	return runStreaming(ctx, args, line)
}

// Delete removes a managed stack, optionally taking it down first.
func (m *Manager) Delete(ctx context.Context, req *nexosv1.DeleteStackRequest, line func(string) error) (int, error) {
	st, err := m.find(ctx, req.GetName())
	if err != nil {
		return -1, err
	}
	if !st.Managed {
		return -1, ErrNotManaged
	}
	if st.Protected {
		return -1, ErrProtected
	}
	if req.GetDown() && st.ContainersTotal > 0 {
		code, err := m.Run(ctx, &nexosv1.RunStackRequest{Name: st.Name, Operation: nexosv1.StackOperation_STACK_OPERATION_DOWN, RemoveVolumes: req.GetRemoveVolumes()}, line)
		if err != nil || code != 0 {
			return code, err
		}
	}
	unlock, err := m.lock(st.Name)
	if err != nil {
		return -1, err
	}
	defer unlock()
	dir := m.stackDir(st.Name)
	// Paranoia: dir must be a direct child of the stacks directory.
	if filepath.Dir(dir) != m.Dir {
		return -1, ErrInvalidName
	}
	if err := os.RemoveAll(dir); err != nil {
		return -1, err
	}
	_ = line("removed " + dir)
	return 0, nil
}

func runStreaming(ctx context.Context, args []string, line func(string) error) (int, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	_ = line("$ docker " + strings.Join(args, " "))
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		pw.Close()
		done <- err
	}()
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var sendErr error
	for sc.Scan() {
		if sendErr == nil {
			sendErr = line(sc.Text())
		}
	}
	err := <-done
	if sendErr != nil {
		return -1, sendErr
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}
