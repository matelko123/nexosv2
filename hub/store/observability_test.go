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

func TestContainerMetricsSum(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.CreateAgent(ctx, &Agent{ID: "abcdefgh12", Name: "a", Address: "x:1"}); err != nil {
		t.Fatal(err)
	}
	m := time.Now().Truncate(time.Hour).Add(-30 * time.Minute)
	// Two services of a stack over two minutes, plus a container outside it.
	if err := st.InsertContainerMetrics(ctx, "abcdefgh12", []ContainerPoint{
		{Name: "app", TS: m.UnixMilli(), CPU: 10, Mem: 100, NetRx: 1000, NetTx: 10},
		{Name: "app", TS: m.Add(time.Minute).UnixMilli(), CPU: 30, Mem: 300, NetRx: 3000, NetTx: 30},
		{Name: "db", TS: m.UnixMilli(), CPU: 5, Mem: 50, NetRx: 100, NetTx: 1},
		{Name: "other", TS: m.UnixMilli(), CPU: 99, Mem: 999, NetRx: 9999, NetTx: 99},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := st.ContainerMetricsSum(ctx, "abcdefgh12", []string{"app", "db"}, m.Add(-time.Hour), (5 * time.Minute).Milliseconds())
	if err != nil {
		t.Fatal(err)
	}
	// One 5-minute bucket: app averages to cpu 20 / mem 200 / rx 2000, plus db.
	if len(res.Points) != 1 || res.Points[0].CPU != 25 || res.Points[0].Mem != 250 || res.Points[0].NetRx != 2100 {
		t.Fatalf("points: %+v", res.Points)
	}
	// Per-minute rates × 60 s: (1000 + 3000 + 100) × 60.
	if res.RxBytes != 4100*60 || res.TxBytes != 41*60 {
		t.Fatalf("totals: rx %v tx %v", res.RxBytes, res.TxBytes)
	}
	empty, err := st.ContainerMetricsSum(ctx, "abcdefgh12", nil, m, 1)
	if err != nil || len(empty.Points) != 0 {
		t.Fatalf("empty: %+v %v", empty, err)
	}
}
