package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"nexos/hub/agents"
	"nexos/hub/auth"
	"nexos/hub/store"
	"nexos/internal/enroll"
	"nexos/internal/pki"
)

// addHost registers a host and prints its single-use enrollment bundle, the
// CLI counterpart of "Dodaj host" in the UI. The installer uses it to enroll
// agents without a logged-in admin (docker exec nexos-hub /nexos-hub
// add-host <name> <address>). Only the bundle goes to stdout, so it can be
// captured by a script.
func addHost(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: nexos-hub add-host <name> <address:port>")
	}
	name, address := strings.TrimSpace(args[0]), strings.TrimSpace(args[1])
	if l := len([]rune(name)); l < 1 || l > 64 {
		return errors.New("name must be 1-64 characters")
	}
	if host, port, err := net.SplitHostPort(address); err != nil || host == "" {
		return errors.New("address must be host:port, e.g. 10.66.0.2:7443")
	} else if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return errors.New("invalid port")
	}
	advertise := env("NEXOS_ENROLL_ADVERTISE", os.Getenv("NEXOS_ENROLL_LISTEN"))
	if advertise == "" {
		return errors.New("enrollment is disabled: set NEXOS_ENROLL_LISTEN on the hub")
	}

	dataDir := env("NEXOS_DATA_DIR", "/data")
	ca, err := pki.LoadOrCreateCA(filepath.Join(dataDir, "pki"))
	if err != nil {
		return fmt.Errorf("agent CA: %w", err)
	}
	st, err := store.Open(filepath.Join(dataDir, "nexos.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()

	a := &store.Agent{ID: pki.NewAgentID(), Name: name, Address: address}
	if err := st.CreateAgent(ctx, a); err != nil {
		return err
	}
	secret := auth.RandomToken(32)
	if err := st.SetEnrollment(ctx, a.ID, secret, time.Now().Add(agents.EnrollmentTTL)); err != nil {
		return err
	}
	_ = st.Audit(ctx, store.AuditEntry{Username: "cli", Action: "host.create", Target: a.Name, AgentID: a.ID, Success: true, Detail: a.Address})
	fmt.Fprintf(os.Stderr, "Host %q (%s) added; the bundle is valid for %s.\n", a.Name, a.Address, agents.EnrollmentTTL)
	fmt.Println(enroll.Bundle{HubAddr: advertise, CAFingerprint: pki.Fingerprint(ca.Cert), AgentID: a.ID, Secret: secret}.Encode())
	return nil
}
