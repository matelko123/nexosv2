package alerts

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"nexos/hub/events"
	"nexos/hub/store"
	nexosv1 "nexos/internal/gen/nexos/v1"
)

func newTestEngine(t *testing.T) (*Engine, *store.Store, *store.Agent) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a := &store.Agent{ID: "abcdefgh12", Name: "srv", Address: "127.0.0.1:1"}
	if err := st.CreateAgent(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	return NewEngine(st, nil, events.NewBus(), nil), st, a
}

func openEvents(t *testing.T, st *store.Store) []store.AlertEvent {
	t.Helper()
	ev, err := st.AlertEvents(context.Background(), true, 100)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func TestDurationAndResolve(t *testing.T) {
	e, st, a := newTestEngine(t)
	ctx := context.Background()
	rules, _ := st.AlertRules(ctx)
	var cpu store.AlertRule
	for _, r := range rules {
		if r.Type == "host_cpu" {
			cpu = r // seeded: 90% for 600 s
		}
	}
	t0 := time.Now()
	hot := map[string]observation{"": {active: true, value: 95, message: "cpu"}}

	e.apply(ctx, cpu, a, hot, t0)
	if n := len(openEvents(t, st)); n != 0 {
		t.Fatalf("fired before duration elapsed: %d", n)
	}
	e.apply(ctx, cpu, a, map[string]observation{"": {active: true, value: 95}}, t0.Add(5*time.Minute))
	if n := len(openEvents(t, st)); n != 0 {
		t.Fatalf("fired after 5 of 10 minutes")
	}
	e.apply(ctx, cpu, a, map[string]observation{"": {active: true, value: 95}}, t0.Add(11*time.Minute))
	if n := len(openEvents(t, st)); n != 1 {
		t.Fatalf("expected 1 firing alert, got %d", n)
	}
	// Still active: no duplicate event.
	e.apply(ctx, cpu, a, map[string]observation{"": {active: true, value: 97}}, t0.Add(12*time.Minute))
	if n := len(openEvents(t, st)); n != 1 {
		t.Fatalf("duplicate alert: %d", n)
	}
	e.apply(ctx, cpu, a, map[string]observation{"": {active: false, value: 20}}, t0.Add(13*time.Minute))
	if n := len(openEvents(t, st)); n != 0 {
		t.Fatalf("alert not resolved")
	}
	// A short spike below the duration never fires.
	e.apply(ctx, cpu, a, hot, t0.Add(20*time.Minute))
	e.apply(ctx, cpu, a, map[string]observation{"": {active: false}}, t0.Add(21*time.Minute))
	all, _ := st.AlertEvents(ctx, false, 100)
	if len(all) != 1 {
		t.Fatalf("spike produced an event: %d events", len(all))
	}
}

func TestDisappearingSubjectResolves(t *testing.T) {
	e, st, a := newTestEngine(t)
	ctx := context.Background()
	r := store.AlertRule{Name: "disk", Type: "host_disk", Threshold: 90, Enabled: true}
	if err := st.SaveAlertRule(ctx, &r); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	e.apply(ctx, r, a, map[string]observation{"/mnt/usb": {active: true, value: 99}, "/": {active: false, value: 40}}, now)
	if n := len(openEvents(t, st)); n != 1 {
		t.Fatalf("expected firing disk alert, got %d", n)
	}
	e.apply(ctx, r, a, map[string]observation{"/": {active: false, value: 40}}, now.Add(time.Minute))
	if n := len(openEvents(t, st)); n != 0 {
		t.Fatalf("alert for unmounted disk not resolved")
	}
}

func TestContainerProblem(t *testing.T) {
	cases := []struct {
		c    *nexosv1.Container
		want bool
	}{
		{&nexosv1.Container{State: "running", Health: "healthy"}, false},
		{&nexosv1.Container{State: "running", Health: "unhealthy"}, true},
		{&nexosv1.Container{State: "restarting"}, true},
		{&nexosv1.Container{State: "exited", Status: "Exited (0) 2 hours ago"}, false},
		{&nexosv1.Container{State: "exited", Status: "Exited (137) 1 minute ago"}, false},
		{&nexosv1.Container{State: "exited", Status: "Exited (143) 1 minute ago"}, false},
		{&nexosv1.Container{State: "exited", Status: "Exited (1) 1 minute ago"}, true},
		{&nexosv1.Container{State: "dead"}, true},
	}
	for _, tc := range cases {
		if got := containerProblem(tc.c) != ""; got != tc.want {
			t.Errorf("%s/%s/%s: got %v want %v", tc.c.State, tc.c.Health, tc.c.Status, got, tc.want)
		}
	}
	if !matchTarget("immich-*", "immich-server") || matchTarget("immich-*", "nextcloud") || !matchTarget("*", "x") {
		t.Error("glob matching")
	}
}

func TestChannelConfigSecrets(t *testing.T) {
	c := ChannelConfig{URL: "https://ntfy.sh/x", Token: "tk", Password: "pw"}
	masked, set := c.Masked()
	if masked.Token != "" || masked.Password != "" || len(set) != 2 || masked.URL == "" {
		t.Fatalf("masking: %+v %v", masked, set)
	}
	upd := ChannelConfig{URL: "https://ntfy.sh/y"}
	upd.MergeSecrets(c)
	if upd.Token != "tk" || upd.URL != "https://ntfy.sh/y" {
		t.Fatalf("merge: %+v", upd)
	}
	if (ChannelConfig{URL: "file:///etc/passwd"}).Validate("ntfy") == nil {
		t.Error("non-http URL accepted")
	}
	if (ChannelConfig{Host: "smtp", From: "a@b\r\nBcc: x@y", To: "c@d"}).Validate("email") == nil {
		t.Error("header injection accepted")
	}
}
