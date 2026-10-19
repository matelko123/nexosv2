package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nexos/hub/store"
	"nexos/internal/enroll"
)

func TestAddHost(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NEXOS_DATA_DIR", dir)
	t.Setenv("NEXOS_ENROLL_LISTEN", "10.66.0.1:7444")

	r, w, _ := os.Pipe()
	stdout := os.Stdout
	os.Stdout = w
	err := addHost([]string{"serwer-b", "10.66.0.2:7443"})
	os.Stdout = stdout
	w.Close()
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(r)
	b, err := enroll.ParseBundle(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("stdout is not a bundle: %q: %v", out, err)
	}
	if b.HubAddr != "10.66.0.1:7444" {
		t.Fatalf("hub address %q", b.HubAddr)
	}

	st, err := store.Open(filepath.Join(dir, "nexos.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, err := st.Agent(context.Background(), b.AgentID)
	if err != nil || a.Name != "serwer-b" || a.Address != "10.66.0.2:7443" {
		t.Fatalf("agent %+v, %v", a, err)
	}
	if ok, _, _ := st.EnrollmentPending(context.Background(), a.ID); !ok {
		t.Fatal("enrollment not pending")
	}

	if err := addHost([]string{"x", "10.66.0.3"}); err == nil {
		t.Fatal("address without port accepted")
	}
	t.Setenv("NEXOS_ENROLL_LISTEN", "")
	if err := addHost([]string{"x", "10.66.0.3:7443"}); err == nil {
		t.Fatal("accepted with enrollment disabled")
	}
}
