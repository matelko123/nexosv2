// Command nexos-agent runs on every managed server and exposes host and
// Docker management to the NexOS hub over mutually authenticated gRPC.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"nexos/agent/docker"
	"nexos/agent/host"
	"nexos/agent/identity"
	"nexos/agent/server"
	"nexos/agent/stacks"
	"nexos/internal/enroll"
	"nexos/internal/pki"
	"nexos/internal/version"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	cmd := "run"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "run":
		err = run()
	case "enroll":
		if len(os.Args) < 3 {
			err = errors.New("usage: nexos-agent enroll <bundle>")
			break
		}
		err = doEnroll(os.Args[2])
	case "update-helper":
		err = runUpdateHelper(os.Args[2:])
	case "version":
		fmt.Println(version.Version)
	default:
		err = fmt.Errorf("unknown command %q (run, enroll, version)", cmd)
	}
	if err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func dataDir() string {
	if d := os.Getenv("NEXOS_DATA_DIR"); d != "" {
		return d
	}
	return "/data"
}

func doEnroll(bundle string) error {
	b, err := enroll.ParseBundle(bundle)
	if err != nil {
		return err
	}
	dir := dataDir()
	if identity.Exists(dir) {
		return errors.New("agent is already enrolled; remove the data directory to re-enroll")
	}
	key, err := pki.GenerateKey()
	if err != nil {
		return err
	}
	csr, err := pki.CreateCSR(key, pki.AgentName(b.AgentID))
	if err != nil {
		return err
	}
	hostname, _ := os.Hostname()
	if h := os.Getenv("NEXOS_HOSTNAME"); h != "" {
		hostname = h
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := enroll.Do(ctx, b, csr, hostname)
	if err != nil {
		return err
	}
	if err := identity.Save(dir, b.AgentID, key, res.CertPEM, res.CAPEM); err != nil {
		return err
	}
	slog.Info("enrolled", "agent_id", b.AgentID, "hub", b.HubAddr)
	return nil
}

// checkListen refuses wildcard addresses unless explicitly allowed: the agent
// is meant to listen on the WireGuard address only.
func checkListen(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("NEXOS_LISTEN: %w", err)
	}
	ip := net.ParseIP(host)
	if (host == "" || (ip != nil && ip.IsUnspecified())) && os.Getenv("NEXOS_ALLOW_ANY_LISTEN") != "true" {
		return errors.New("NEXOS_LISTEN must be a specific address (e.g. the WireGuard IP 10.66.0.2:7443); set NEXOS_ALLOW_ANY_LISTEN=true to override")
	}
	return nil
}

func run() error {
	// Validate configuration before enrolling, so a bad setting does not
	// burn the single-use enrollment bundle.
	addr := os.Getenv("NEXOS_LISTEN")
	if addr == "" {
		return errors.New("NEXOS_LISTEN is required (e.g. 10.66.0.2:7443)")
	}
	if err := checkListen(addr); err != nil {
		return err
	}
	dir := dataDir()
	if !identity.Exists(dir) {
		bundle := os.Getenv("NEXOS_ENROLL")
		if bundle == "" {
			return errors.New("agent is not enrolled: set NEXOS_ENROLL to the bundle shown by the hub, or run `nexos-agent enroll <bundle>`")
		}
		if err := doEnroll(bundle); err != nil {
			return fmt.Errorf("enroll: %w", err)
		}
	}
	id, err := identity.Load(dir)
	if err != nil {
		return err
	}

	dc, err := docker.New()
	if err != nil {
		slog.Warn("docker client unavailable", "err", err)
		dc = nil
	}
	var sm *stacks.Manager
	if dc != nil {
		stacksDir := os.Getenv("NEXOS_STACKS_DIR")
		if stacksDir == "" {
			stacksDir = "/opt/nexos/stacks"
		}
		if sm, err = stacks.New(stacksDir, os.Getenv("NEXOS_HOST_ROOT"), dc); err != nil {
			slog.Warn("stack management disabled", "err", err)
			sm = nil
		}
	}
	hf := host.New(os.Getenv("NEXOS_HOST_FEATURES"), os.Getenv("NEXOS_HOST_ROOT"))
	if fs := hf.Features(); len(fs) > 0 {
		slog.Info("host features enabled", "features", fs)
	}
	var du *docker.DiskUsage
	if dc != nil {
		du = docker.NewDiskUsage(dc, os.Getenv("NEXOS_HOST_ROOT"))
	}
	gs := server.New(id, dc, sm, hf, du).GRPCServer()

	var ln net.Listener
	// The WireGuard interface may come up after the container; retry binding.
	for attempt := 0; ; attempt++ {
		ln, err = net.Listen("tcp", addr)
		if err == nil {
			break
		}
		if attempt >= 30 || !strings.Contains(err.Error(), "assign requested address") {
			return err
		}
		slog.Warn("listen address not available yet, retrying", "addr", addr, "err", err)
		time.Sleep(2 * time.Second)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		gs.GracefulStop()
	}()
	slog.Info("agent listening", "addr", addr, "agent_id", id.AgentID, "cert_not_after", id.KeyPair.Leaf().NotAfter, "version", version.Version)
	return gs.Serve(ln)
}
