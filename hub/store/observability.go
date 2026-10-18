package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MetricTable selects the resolution of host metrics.
type MetricTable string

const (
	MetricsRaw MetricTable = "host_metrics"
	Metrics1m  MetricTable = "host_metrics_1m"
	Metrics5m  MetricTable = "host_metrics_5m"
)

const metricCols = "cpu, mem_used, mem_total, swap_used, load1, net_rx, net_tx, disk_read, disk_write"

// MetricsFrom reads host metrics from the given table, averaged into
// buckets of bucketMs milliseconds.
func (s *Store) MetricsFrom(ctx context.Context, table MetricTable, agentID string, since time.Time, bucketMs int64) ([]MetricPoint, error) {
	switch table {
	case MetricsRaw, Metrics1m, Metrics5m:
	default:
		return nil, fmt.Errorf("unknown metrics table %q", table)
	}
	if bucketMs < 1 {
		bucketMs = 1
	}
	rows, err := s.db.QueryContext(ctx, `SELECT (ts / ?) * ? AS b, AVG(cpu), AVG(mem_used), MAX(mem_total), AVG(swap_used), AVG(load1),
		AVG(net_rx), AVG(net_tx), AVG(disk_read), AVG(disk_write)
		FROM `+string(table)+` WHERE agent_id = ? AND ts >= ? GROUP BY b ORDER BY b`,
		bucketMs, bucketMs, agentID, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MetricPoint
	for rows.Next() {
		var p MetricPoint
		var memUsed, swapUsed float64
		var memTotal int64
		if err := rows.Scan(&p.TS, &p.CPU, &memUsed, &memTotal, &swapUsed, &p.Load1, &p.NetRx, &p.NetTx, &p.DiskRead, &p.DiskWrite); err != nil {
			return nil, err
		}
		p.MemUsed, p.MemTotal, p.SwapUsed = uint64(memUsed), uint64(memTotal), uint64(swapUsed)
		out = append(out, p)
	}
	return out, rows.Err()
}

// rollup aggregates src into dst buckets of bucketMs for samples in [from, to).
// It is idempotent: buckets are recomputed and replaced.
func (s *Store) rollup(ctx context.Context, src, dst MetricTable, bucketMs int64, from, to time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO `+string(dst)+` (agent_id, ts, `+metricCols+`)
		SELECT agent_id, (ts / ?) * ?, AVG(cpu), AVG(mem_used), MAX(mem_total), AVG(swap_used), AVG(load1),
			AVG(net_rx), AVG(net_tx), AVG(disk_read), AVG(disk_write)
		FROM `+string(src)+` WHERE ts >= ? AND ts < ? GROUP BY agent_id, (ts / ?)`,
		bucketMs, bucketMs, from.UnixMilli(), to.UnixMilli(), bucketMs)
	return err
}

// RollupHostMetrics refreshes the 1m and 5m tables for recent complete buckets.
func (s *Store) RollupHostMetrics(ctx context.Context, now time.Time) error {
	m1 := now.Truncate(time.Minute)
	if err := s.rollup(ctx, MetricsRaw, Metrics1m, time.Minute.Milliseconds(), m1.Add(-5*time.Minute), m1); err != nil {
		return err
	}
	m5 := now.Truncate(5 * time.Minute)
	return s.rollup(ctx, Metrics1m, Metrics5m, (5 * time.Minute).Milliseconds(), m5.Add(-20*time.Minute), m5)
}

// Retention holds how long each resolution is kept.
type Retention struct {
	Raw, OneMinute, FiveMinute, Container, Events time.Duration
}

func (s *Store) PruneHistory(ctx context.Context, now time.Time, r Retention) error {
	stmts := []struct {
		q  string
		at time.Time
	}{
		{`DELETE FROM host_metrics WHERE ts < ?`, now.Add(-r.Raw)},
		{`DELETE FROM host_metrics_1m WHERE ts < ?`, now.Add(-r.OneMinute)},
		{`DELETE FROM host_metrics_5m WHERE ts < ?`, now.Add(-r.FiveMinute)},
		{`DELETE FROM container_metrics WHERE ts < ?`, now.Add(-r.Container)},
	}
	for _, st := range stmts {
		if _, err := s.db.ExecContext(ctx, st.q, st.at.UnixMilli()); err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM alert_events WHERE resolved_at IS NOT NULL AND resolved_at < ?`, now.Add(-r.Events).Unix())
	return err
}

// ---- container metrics ----

type ContainerPoint struct {
	Name     string  `json:"-"`
	TS       int64   `json:"ts"`
	CPU      float64 `json:"cpu"`
	Mem      uint64  `json:"mem"`
	MemLimit uint64  `json:"mem_limit"`
	NetRx    float64 `json:"net_rx"`
	NetTx    float64 `json:"net_tx"`
}

func (s *Store) InsertContainerMetrics(ctx context.Context, agentID string, pts []ContainerPoint) error {
	if len(pts) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT OR REPLACE INTO container_metrics (agent_id, name, ts, cpu, mem, mem_limit, net_rx, net_tx) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, p := range pts {
		if _, err := stmt.ExecContext(ctx, agentID, p.Name, p.TS, p.CPU, int64(p.Mem), int64(p.MemLimit), p.NetRx, p.NetTx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ContainerMetrics(ctx context.Context, agentID, name string, since time.Time, bucketMs int64) ([]ContainerPoint, error) {
	if bucketMs < 1 {
		bucketMs = 1
	}
	rows, err := s.db.QueryContext(ctx, `SELECT (ts / ?) * ? AS b, AVG(cpu), AVG(mem), MAX(mem_limit), AVG(net_rx), AVG(net_tx)
		FROM container_metrics WHERE agent_id = ? AND name = ? AND ts >= ? GROUP BY b ORDER BY b`,
		bucketMs, bucketMs, agentID, name, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ContainerPoint
	for rows.Next() {
		var p ContainerPoint
		var mem float64
		var limit int64
		if err := rows.Scan(&p.TS, &p.CPU, &mem, &limit, &p.NetRx, &p.NetTx); err != nil {
			return nil, err
		}
		p.Mem, p.MemLimit = uint64(mem), uint64(limit)
		out = append(out, p)
	}
	return out, rows.Err()
}

// ContainerTotals is the summed history of several containers, e.g. the
// services of one compose project.
type ContainerTotals struct {
	Points []ContainerPoint `json:"points"`
	// Bytes transferred in the window: rows are per-minute average rates.
	RxBytes float64 `json:"rx_bytes"`
	TxBytes float64 `json:"tx_bytes"`
}

// ContainerMetricsSum adds up the per-bucket averages of the named containers.
func (s *Store) ContainerMetricsSum(ctx context.Context, agentID string, names []string, since time.Time, bucketMs int64) (*ContainerTotals, error) {
	out := &ContainerTotals{Points: []ContainerPoint{}}
	if len(names) == 0 {
		return out, nil
	}
	if bucketMs < 1 {
		bucketMs = 1
	}
	in := strings.TrimSuffix(strings.Repeat("?,", len(names)), ",")
	args := []any{agentID}
	for _, n := range names {
		args = append(args, n)
	}
	args = append(args, since.UnixMilli())
	where := `agent_id = ? AND name IN (` + in + `) AND ts >= ?`

	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(net_rx), 0) * 60, COALESCE(SUM(net_tx), 0) * 60 FROM container_metrics WHERE `+where, args...).
		Scan(&out.RxBytes, &out.TxBytes); err != nil {
		return nil, err
	}
	// Average each container within a bucket first, then sum across containers.
	rows, err := s.db.QueryContext(ctx, `SELECT b, SUM(cpu), SUM(mem), SUM(mem_limit), SUM(net_rx), SUM(net_tx) FROM (
			SELECT name, (ts / ?) * ? AS b, AVG(cpu) AS cpu, AVG(mem) AS mem, MAX(mem_limit) AS mem_limit, AVG(net_rx) AS net_rx, AVG(net_tx) AS net_tx
			FROM container_metrics WHERE `+where+` GROUP BY name, b
		) GROUP BY b ORDER BY b`, append([]any{bucketMs, bucketMs}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p ContainerPoint
		var mem, limit float64
		if err := rows.Scan(&p.TS, &p.CPU, &mem, &limit, &p.NetRx, &p.NetTx); err != nil {
			return nil, err
		}
		p.Mem, p.MemLimit = uint64(mem), uint64(limit)
		out.Points = append(out.Points, p)
	}
	return out, rows.Err()
}

// ---- image updates ----

type ImageUpdate struct {
	AgentID         string    `json:"agent_id"`
	Reference       string    `json:"reference"`
	Display         string    `json:"display"`
	LocalImageID    string    `json:"local_image_id,omitempty"`
	RemoteDigest    string    `json:"remote_digest,omitempty"`
	UpdateAvailable bool      `json:"update_available"`
	Error           string    `json:"error,omitempty"`
	Containers      []string  `json:"containers"`
	CheckedAt       time.Time `json:"checked_at"`
}

// ReplaceImageUpdates stores the result of a full check for an agent.
func (s *Store) ReplaceImageUpdates(ctx context.Context, agentID string, list []ImageUpdate) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM image_updates WHERE agent_id = ?`, agentID); err != nil {
		return err
	}
	for _, u := range list {
		cj, _ := json.Marshal(u.Containers)
		if _, err := tx.ExecContext(ctx, `INSERT INTO image_updates (agent_id, reference, display, local_image_id, remote_digest, update_available, error, containers_json, checked_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			agentID, u.Reference, u.Display, u.LocalImageID, u.RemoteDigest, u.UpdateAvailable, u.Error, string(cj), u.CheckedAt.Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ImageUpdates returns stored results; agentID "" returns all agents.
func (s *Store) ImageUpdates(ctx context.Context, agentID string) ([]ImageUpdate, error) {
	q := `SELECT agent_id, reference, display, COALESCE(local_image_id, ''), COALESCE(remote_digest, ''), update_available,
		COALESCE(error, ''), COALESCE(containers_json, '[]'), checked_at FROM image_updates`
	args := []any{}
	if agentID != "" {
		q += ` WHERE agent_id = ?`
		args = append(args, agentID)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY display`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImageUpdate
	for rows.Next() {
		var u ImageUpdate
		var cj string
		var checked int64
		if err := rows.Scan(&u.AgentID, &u.Reference, &u.Display, &u.LocalImageID, &u.RemoteDigest, &u.UpdateAvailable, &u.Error, &cj, &checked); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(cj), &u.Containers)
		u.CheckedAt = time.Unix(checked, 0)
		out = append(out, u)
	}
	return out, rows.Err()
}

// ---- alert channels ----

type AlertChannel struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Config    string    `json:"-"` // sealed
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) AlertChannels(ctx context.Context) ([]AlertChannel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, type, config, enabled, created_at FROM alert_channels ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertChannel
	for rows.Next() {
		var c AlertChannel
		var created int64
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.Config, &c.Enabled, &created); err != nil {
			return nil, err
		}
		c.CreatedAt = time.Unix(created, 0)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) AlertChannel(ctx context.Context, id int64) (*AlertChannel, error) {
	var c AlertChannel
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT id, name, type, config, enabled, created_at FROM alert_channels WHERE id = ?`, id).
		Scan(&c.ID, &c.Name, &c.Type, &c.Config, &c.Enabled, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	c.CreatedAt = time.Unix(created, 0)
	return &c, err
}

func (s *Store) SaveAlertChannel(ctx context.Context, c *AlertChannel) error {
	if c.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO alert_channels (name, type, config, enabled, created_at) VALUES (?, ?, ?, ?, ?)`,
			c.Name, c.Type, c.Config, c.Enabled, time.Now().Unix())
		if err != nil {
			return err
		}
		c.ID, _ = res.LastInsertId()
		return nil
	}
	return affected(s.db.ExecContext(ctx, `UPDATE alert_channels SET name = ?, type = ?, config = ?, enabled = ? WHERE id = ?`,
		c.Name, c.Type, c.Config, c.Enabled, c.ID))
}

func (s *Store) DeleteAlertChannel(ctx context.Context, id int64) error {
	return affected(s.db.ExecContext(ctx, `DELETE FROM alert_channels WHERE id = ?`, id))
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- alert rules ----

type AlertRule struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	AgentID   string    `json:"agent_id,omitempty"` // "" = all hosts
	Threshold float64   `json:"threshold"`
	DurationS int64     `json:"duration_s"`
	Target    string    `json:"target"`
	Channels  []int64   `json:"channels"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) AlertRules(ctx context.Context) ([]AlertRule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, type, COALESCE(agent_id, ''), threshold, duration_s, target, channels, enabled, created_at
		FROM alert_rules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertRule
	for rows.Next() {
		var r AlertRule
		var ch string
		var created int64
		if err := rows.Scan(&r.ID, &r.Name, &r.Type, &r.AgentID, &r.Threshold, &r.DurationS, &r.Target, &ch, &r.Enabled, &created); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(ch), &r.Channels)
		if r.Channels == nil {
			r.Channels = []int64{}
		}
		r.CreatedAt = time.Unix(created, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) SaveAlertRule(ctx context.Context, r *AlertRule) error {
	ch, _ := json.Marshal(r.Channels)
	var agent any
	if r.AgentID != "" {
		agent = r.AgentID
	}
	if r.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO alert_rules (name, type, agent_id, threshold, duration_s, target, channels, enabled, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.Name, r.Type, agent, r.Threshold, r.DurationS, r.Target, string(ch), r.Enabled, time.Now().Unix())
		if err != nil {
			return err
		}
		r.ID, _ = res.LastInsertId()
		return nil
	}
	return affected(s.db.ExecContext(ctx, `UPDATE alert_rules SET name = ?, type = ?, agent_id = ?, threshold = ?, duration_s = ?, target = ?, channels = ?, enabled = ?
		WHERE id = ?`, r.Name, r.Type, agent, r.Threshold, r.DurationS, r.Target, string(ch), r.Enabled, r.ID))
}

func (s *Store) DeleteAlertRule(ctx context.Context, id int64) error {
	return affected(s.db.ExecContext(ctx, `DELETE FROM alert_rules WHERE id = ?`, id))
}

// ---- alert events ----

type AlertEvent struct {
	ID         int64      `json:"id"`
	RuleID     int64      `json:"rule_id"`
	RuleName   string     `json:"rule_name"`
	RuleType   string     `json:"rule_type"`
	AgentID    string     `json:"agent_id"`
	HostName   string     `json:"host_name"`
	Subject    string     `json:"subject,omitempty"`
	Value      float64    `json:"value"`
	Message    string     `json:"message"`
	StartedAt  time.Time  `json:"started_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

func (s *Store) OpenAlertEvent(ctx context.Context, e *AlertEvent) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO alert_events (rule_id, agent_id, subject, value, message, started_at) VALUES (?, ?, ?, ?, ?, ?)`,
		e.RuleID, e.AgentID, e.Subject, e.Value, e.Message, e.StartedAt.Unix())
	if err != nil {
		return err
	}
	e.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) ResolveAlertEvent(ctx context.Context, id int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE alert_events SET resolved_at = ? WHERE id = ? AND resolved_at IS NULL`, at.Unix(), id)
	return err
}

// AlertEvents lists events; open=true returns only unresolved ones.
func (s *Store) AlertEvents(ctx context.Context, open bool, limit int) ([]AlertEvent, error) {
	where := ""
	if open {
		where = "WHERE e.resolved_at IS NULL"
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.id, e.rule_id, r.name, r.type, e.agent_id, COALESCE(a.name, ''), e.subject, COALESCE(e.value, 0), e.message,
		e.started_at, e.resolved_at FROM alert_events e JOIN alert_rules r ON r.id = e.rule_id LEFT JOIN agents a ON a.id = e.agent_id
		`+where+` ORDER BY e.started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertEvent
	for rows.Next() {
		var e AlertEvent
		var started int64
		var resolved sql.NullInt64
		if err := rows.Scan(&e.ID, &e.RuleID, &e.RuleName, &e.RuleType, &e.AgentID, &e.HostName, &e.Subject, &e.Value, &e.Message, &started, &resolved); err != nil {
			return nil, err
		}
		e.StartedAt = time.Unix(started, 0)
		e.ResolvedAt = timePtr(resolved)
		out = append(out, e)
	}
	return out, rows.Err()
}
