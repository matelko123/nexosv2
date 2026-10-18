// Package store is the hub's SQLite persistence layer.
package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

var ErrNotFound = errors.New("not found")

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite allows a single writer; one connection avoids SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var current int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&current); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		v, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil {
			return fmt.Errorf("bad migration name %q", e.Name())
		}
		if v <= current {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("%s: %w", e.Name(), err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, v); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// HashToken returns the SHA-256 of a secret token; only hashes are stored.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func timePtr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(v.Int64, 0)
	return &t
}

// ---- users ----

type User struct {
	ID           int64
	Username     string
	PasswordHash string
	TOTPSecret   string // sealed with the hub secret key
	TOTPLastStep int64
	Role         string
	CreatedAt    time.Time
	LastLoginAt  *time.Time
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CreateFirstUser inserts a user only if no user exists yet, atomically.
func (s *Store) CreateFirstUser(ctx context.Context, u *User) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO users (username, password_hash, totp_secret, role, created_at)
		SELECT ?, ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM users)`,
		u.Username, u.PasswordHash, u.TOTPSecret, u.Role, time.Now().Unix())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("setup already completed")
	}
	u.ID, _ = res.LastInsertId()
	return nil
}

const userCols = `id, username, password_hash, totp_secret, totp_last_step, role, created_at, last_login_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var created int64
	var last sql.NullInt64
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.TOTPSecret, &u.TOTPLastStep, &u.Role, &created, &last); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.CreatedAt = time.Unix(created, 0)
	u.LastLoginAt = timePtr(last)
	return &u, nil
}

func (s *Store) UserByUsername(ctx context.Context, username string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username = ?`, username))
}

func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// AdvanceTOTPStep records a used TOTP time step. It fails if step is not
// newer than the last accepted one, which prevents code replay.
func (s *Store) AdvanceTOTPStep(ctx context.Context, userID, step int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET totp_last_step = ?, last_login_at = ? WHERE id = ? AND totp_last_step < ?`,
		step, time.Now().Unix(), userID, step)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("totp code already used")
	}
	return nil
}

// ---- sessions ----

type Session struct {
	UserID     int64
	CSRFToken  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
}

func (s *Store) CreateSession(ctx context.Context, token string, sess Session, ip, ua string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (token_hash, user_id, csrf_token, created_at, expires_at, last_seen_at, ip, user_agent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		HashToken(token), sess.UserID, sess.CSRFToken, sess.CreatedAt.Unix(), sess.ExpiresAt.Unix(), sess.LastSeenAt.Unix(), ip, ua)
	return err
}

func (s *Store) SessionByToken(ctx context.Context, token string) (*Session, error) {
	var sess Session
	var created, expires, seen int64
	err := s.db.QueryRowContext(ctx, `SELECT user_id, csrf_token, created_at, expires_at, last_seen_at FROM sessions WHERE token_hash = ?`,
		HashToken(token)).Scan(&sess.UserID, &sess.CSRFToken, &created, &expires, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sess.CreatedAt, sess.ExpiresAt, sess.LastSeenAt = time.Unix(created, 0), time.Unix(expires, 0), time.Unix(seen, 0)
	return &sess, nil
}

func (s *Store) TouchSession(ctx context.Context, token string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?`, at.Unix(), HashToken(token))
	return err
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, HashToken(token))
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context, idleBefore time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ? OR last_seen_at < ?`, time.Now().Unix(), idleBefore.Unix())
	return err
}

// ---- agents ----

type Agent struct {
	ID         string
	Name       string
	Address    string
	CreatedAt  time.Time
	EnrolledAt *time.Time
	Hostname   string
	InfoJSON   string
	LastSeenAt *time.Time
}

const agentCols = `id, name, address, created_at, enrolled_at, COALESCE(hostname, ''), COALESCE(info_json, ''), last_seen_at`

func scanAgent(row interface{ Scan(...any) error }) (*Agent, error) {
	var a Agent
	var created int64
	var enrolled, seen sql.NullInt64
	if err := row.Scan(&a.ID, &a.Name, &a.Address, &created, &enrolled, &a.Hostname, &a.InfoJSON, &seen); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	a.CreatedAt = time.Unix(created, 0)
	a.EnrolledAt, a.LastSeenAt = timePtr(enrolled), timePtr(seen)
	return &a, nil
}

func (s *Store) CreateAgent(ctx context.Context, a *Agent) error {
	a.CreatedAt = time.Now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO agents (id, name, address, created_at) VALUES (?, ?, ?, ?)`,
		a.ID, a.Name, a.Address, a.CreatedAt.Unix())
	return err
}

func (s *Store) UpdateAgent(ctx context.Context, id, name, address string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE agents SET name = ?, address = ? WHERE id = ?`, name, address, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteAgent(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM agents WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Agent(ctx context.Context, id string) (*Agent, error) {
	return scanAgent(s.db.QueryRowContext(ctx, `SELECT `+agentCols+` FROM agents WHERE id = ?`, id))
}

func (s *Store) Agents(ctx context.Context) ([]*Agent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+agentCols+` FROM agents ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) SetAgentSeen(ctx context.Context, id, infoJSON string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET info_json = ?, last_seen_at = ? WHERE id = ?`, infoJSON, at.Unix(), id)
	return err
}

// ---- enrollment ----

func (s *Store) SetEnrollment(ctx context.Context, agentID, secret string, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_enrollments (agent_id, secret_hash, expires_at) VALUES (?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET secret_hash = excluded.secret_hash, expires_at = excluded.expires_at`,
		agentID, HashToken(secret), expires.Unix())
	return err
}

// ConsumeEnrollment validates and deletes the agent's enrollment secret in a
// single transaction, so a secret can be used at most once.
func (s *Store) ConsumeEnrollment(ctx context.Context, agentID, secret string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var hash []byte
	var expires int64
	err = tx.QueryRowContext(ctx, `SELECT secret_hash, expires_at FROM agent_enrollments WHERE agent_id = ?`, agentID).Scan(&hash, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("no pending enrollment")
	}
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(hash, HashToken(secret)) != 1 {
		return errors.New("invalid enrollment secret")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_enrollments WHERE agent_id = ?`, agentID); err != nil {
		return err
	}
	if time.Now().Unix() > expires {
		// Commit the deletion of the expired secret, then refuse.
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("enrollment secret expired")
	}
	return tx.Commit()
}

func (s *Store) EnrollmentPending(ctx context.Context, agentID string) (bool, time.Time, error) {
	var expires int64
	err := s.db.QueryRowContext(ctx, `SELECT expires_at FROM agent_enrollments WHERE agent_id = ?`, agentID).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) {
		return false, time.Time{}, nil
	}
	return err == nil, time.Unix(expires, 0), err
}

// ---- agent certificates ----

type AgentCert struct {
	Fingerprint string
	AgentID     string
	Serial      string
	NotAfter    time.Time
	IssuedAt    time.Time
	RevokedAt   *time.Time
}

func (s *Store) AddAgentCert(ctx context.Context, c AgentCert) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_certs (fingerprint, agent_id, serial, not_after, issued_at) VALUES (?, ?, ?, ?, ?)`,
		c.Fingerprint, c.AgentID, c.Serial, c.NotAfter.Unix(), time.Now().Unix())
	return err
}

// CompleteEnrollment records the first certificate, revokes any earlier ones
// (re-enrollment) and marks the agent enrolled.
func (s *Store) CompleteEnrollment(ctx context.Context, c AgentCert, hostname string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `UPDATE agent_certs SET revoked_at = ? WHERE agent_id = ? AND revoked_at IS NULL`, now, c.AgentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_certs (fingerprint, agent_id, serial, not_after, issued_at) VALUES (?, ?, ?, ?, ?)`,
		c.Fingerprint, c.AgentID, c.Serial, c.NotAfter.Unix(), now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET enrolled_at = ?, hostname = ? WHERE id = ?`, now, hostname, c.AgentID); err != nil {
		return err
	}
	return tx.Commit()
}

// RevokeOtherAgentCerts revokes every certificate of the agent except keep.
func (s *Store) RevokeOtherAgentCerts(ctx context.Context, agentID, keep string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agent_certs SET revoked_at = ? WHERE agent_id = ? AND fingerprint <> ? AND revoked_at IS NULL`,
		time.Now().Unix(), agentID, keep)
	return err
}

// CheckAgentCert reports whether fingerprint is a live certificate the hub
// issued to agentID.
func (s *Store) CheckAgentCert(ctx context.Context, agentID, fingerprint string) error {
	var notAfter int64
	var revoked sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT not_after, revoked_at FROM agent_certs WHERE fingerprint = ? AND agent_id = ?`,
		fingerprint, agentID).Scan(&notAfter, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("certificate not issued to this agent")
	}
	if err != nil {
		return err
	}
	if revoked.Valid {
		return errors.New("certificate revoked")
	}
	if time.Now().Unix() > notAfter {
		return errors.New("certificate expired")
	}
	return nil
}

// LatestAgentCert returns the newest non-revoked certificate of an agent.
func (s *Store) LatestAgentCert(ctx context.Context, agentID string) (*AgentCert, error) {
	var c AgentCert
	var notAfter, issued int64
	err := s.db.QueryRowContext(ctx, `SELECT fingerprint, agent_id, serial, not_after, issued_at FROM agent_certs
		WHERE agent_id = ? AND revoked_at IS NULL ORDER BY not_after DESC LIMIT 1`, agentID).
		Scan(&c.Fingerprint, &c.AgentID, &c.Serial, &notAfter, &issued)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.NotAfter, c.IssuedAt = time.Unix(notAfter, 0), time.Unix(issued, 0)
	return &c, nil
}

// ---- audit ----

type AuditEntry struct {
	ID       int64     `json:"id"`
	Time     time.Time `json:"time"`
	UserID   *int64    `json:"user_id,omitempty"`
	Username string    `json:"username,omitempty"`
	IP       string    `json:"ip,omitempty"`
	Action   string    `json:"action"`
	Target   string    `json:"target,omitempty"`
	AgentID  string    `json:"agent_id,omitempty"`
	Success  bool      `json:"success"`
	Detail   string    `json:"detail,omitempty"`
}

func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_log (ts, user_id, username, ip, action, target, agent_id, success, detail)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Time.UnixMilli(), e.UserID, e.Username, e.IP, e.Action, e.Target, e.AgentID, e.Success, e.Detail)
	return err
}

func (s *Store) AuditLog(ctx context.Context, limit int) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, ts, user_id, COALESCE(username, ''), COALESCE(ip, ''), action,
		COALESCE(target, ''), COALESCE(agent_id, ''), success, COALESCE(detail, '') FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var ts int64
		var uid sql.NullInt64
		if err := rows.Scan(&e.ID, &ts, &uid, &e.Username, &e.IP, &e.Action, &e.Target, &e.AgentID, &e.Success, &e.Detail); err != nil {
			return nil, err
		}
		e.Time = time.UnixMilli(ts)
		if uid.Valid {
			e.UserID = &uid.Int64
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- metrics ----

type MetricPoint struct {
	TS        int64   `json:"ts"`
	CPU       float64 `json:"cpu"`
	MemUsed   uint64  `json:"mem_used"`
	MemTotal  uint64  `json:"mem_total"`
	SwapUsed  uint64  `json:"swap_used"`
	Load1     float64 `json:"load1"`
	NetRx     float64 `json:"net_rx"`
	NetTx     float64 `json:"net_tx"`
	DiskRead  float64 `json:"disk_read"`
	DiskWrite float64 `json:"disk_write"`
}

func (s *Store) InsertMetric(ctx context.Context, agentID string, p MetricPoint) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO host_metrics
		(agent_id, ts, cpu, mem_used, mem_total, swap_used, load1, net_rx, net_tx, disk_read, disk_write)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		agentID, p.TS, p.CPU, int64(p.MemUsed), int64(p.MemTotal), int64(p.SwapUsed), p.Load1, p.NetRx, p.NetTx, p.DiskRead, p.DiskWrite)
	return err
}

// Metrics returns samples of an agent since the given time, averaged into
// buckets of bucketMs milliseconds (0 = raw).
func (s *Store) Metrics(ctx context.Context, agentID string, since time.Time, bucketMs int64) ([]MetricPoint, error) {
	if bucketMs < 1 {
		bucketMs = 1
	}
	rows, err := s.db.QueryContext(ctx, `SELECT (ts / ?) * ? AS b, AVG(cpu), AVG(mem_used), MAX(mem_total), AVG(swap_used), AVG(load1),
		AVG(net_rx), AVG(net_tx), AVG(disk_read), AVG(disk_write)
		FROM host_metrics WHERE agent_id = ? AND ts >= ? GROUP BY b ORDER BY b`,
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

func (s *Store) PruneMetrics(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM host_metrics WHERE ts < ?`, before.UnixMilli())
	return err
}
