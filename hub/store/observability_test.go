package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestRollupAndPrune(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.CreateAgent(ctx, &Agent{ID: "abcdefgh12", Name: "a", Address: "x:1"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(5 * time.Minute).Add(5 * time.Minute)
	m1 := now.Add(-3 * time.Minute)
	// Two samples in one minute (cpu 10 and 30), one in the next (cpu 50).
	for _, p := range []MetricPoint{
		{TS: m1.UnixMilli(), CPU: 10, MemTotal: 100},
		{TS: m1.Add(30 * time.Second).UnixMilli(), CPU: 30, MemTotal: 100},
		{TS: m1.Add(time.Minute).UnixMilli(), CPU: 50, MemTotal: 100},
	} {
		if err := st.InsertMetric(ctx, "abcdefgh12", p); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.RollupHostMetrics(ctx, now); err != nil {
		t.Fatal(err)
	}
	// Idempotent.
	if err := st.RollupHostMetrics(ctx, now); err != nil {
		t.Fatal(err)
	}
	pts, err := st.MetricsFrom(ctx, Metrics1m, "abcdefgh12", now.Add(-time.Hour), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 2 || pts[0].CPU != 20 || pts[1].CPU != 50 {
		t.Fatalf("1m rollup: %+v", pts)
	}
	five, _ := st.MetricsFrom(ctx, Metrics5m, "abcdefgh12", now.Add(-time.Hour), 0)
	if len(five) != 1 || five[0].CPU != 35 {
		t.Fatalf("5m rollup: %+v", five)
	}

	if err := st.InsertContainerMetrics(ctx, "abcdefgh12", []ContainerPoint{{Name: "web", TS: m1.UnixMilli(), CPU: 5, Mem: 10}}); err != nil {
		t.Fatal(err)
	}
	// Prune everything older than "now + 1h": all rows go.
	r := Retention{Raw: -time.Hour, OneMinute: -time.Hour, FiveMinute: -time.Hour, Container: -time.Hour, Events: time.Hour}
	if err := st.PruneHistory(ctx, now, r); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []MetricTable{MetricsRaw, Metrics1m, Metrics5m} {
		if p, _ := st.MetricsFrom(ctx, tbl, "abcdefgh12", time.Time{}, 0); len(p) != 0 {
			t.Fatalf("%s not pruned", tbl)
		}
	}
	if c, _ := st.ContainerMetrics(ctx, "abcdefgh12", "web", time.Time{}, 0); len(c) != 0 {
		t.Fatal("container metrics not pruned")
	}
}

func TestDefaultAlertRulesSeeded(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rules, err := st.AlertRules(context.Background())
	if err != nil || len(rules) != 7 {
		t.Fatalf("seeded rules: %d %v", len(rules), err)
	}
}
