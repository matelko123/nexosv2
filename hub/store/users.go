package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

var ErrLastAdmin = errors.New("at least one admin must remain")

// UserSummary is a user as listed in the administration UI.
type UserSummary struct {
	ID          int64      `json:"id"`
	Username    string     `json:"username"`
	Role        string     `json:"role"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	// Pending: invited, password and second factor not set yet.
	Pending  bool `json:"pending"`
	Sessions int  `json:"sessions"`
	Passkeys int  `json:"passkeys"`
}

func (s *Store) ListUsers(ctx context.Context) ([]UserSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT u.id, u.username, u.role, u.created_at, u.last_login_at, u.password_hash = '',
		(SELECT COUNT(*) FROM sessions s WHERE s.user_id = u.id AND s.expires_at > ?),
		(SELECT COUNT(*) FROM webauthn_credentials w WHERE w.user_id = u.id)
		FROM users u ORDER BY u.username COLLATE NOCASE`, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserSummary
	for rows.Next() {
		var u UserSummary
		var created int64
		var last sql.NullInt64
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &created, &last, &u.Pending, &u.Sessions, &u.Passkeys); err != nil {
			return nil, err
		}
		u.CreatedAt, u.LastLoginAt = time.Unix(created, 0), timePtr(last)
		out = append(out, u)
	}
	return out, rows.Err()
}

// CreatePendingUser adds a user without credentials (to be set via invite).
func (s *Store) CreatePendingUser(ctx context.Context, username, role string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO users (username, password_hash, totp_secret, role, created_at) VALUES (?, '', '', ?, ?)`,
		username, role, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) countAdmins(ctx context.Context, tx *sql.Tx) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = 'admin' AND password_hash <> ''`).Scan(&n)
	return n, err
}

// SetUserRole changes a role, refusing to demote the last active admin.
func (s *Store) SetUserRole(ctx context.Context, id int64, role string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := affected(tx.ExecContext(ctx, `UPDATE users SET role = ? WHERE id = ?`, role, id)); err != nil {
		return err
	}
	if n, err := s.countAdmins(ctx, tx); err != nil {
		return err
	} else if n == 0 {
		return ErrLastAdmin
	}
	return tx.Commit()
}

// DeleteUser removes a user (cascading to sessions, passkeys, invites),
// refusing to delete the last active admin.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := affected(tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)); err != nil {
		return err
	}
	if n, err := s.countAdmins(ctx, tx); err != nil {
		return err
	} else if n == 0 {
		return ErrLastAdmin
	}
	return tx.Commit()
}

// ResetUserCredentials clears password, TOTP and passkeys and ends all
// sessions; the user can only regain access through a new invitation.
func (s *Store) ResetUserCredentials(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := affected(tx.ExecContext(ctx, `UPDATE users SET password_hash = '', totp_secret = '', totp_last_step = 0 WHERE id = ?`, id)); err != nil {
		return err
	}
	for _, q := range []string{`DELETE FROM sessions WHERE user_id = ?`, `DELETE FROM webauthn_credentials WHERE user_id = ?`, `DELETE FROM user_invites WHERE user_id = ?`} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	if n, err := s.countAdmins(ctx, tx); err != nil {
		return err
	} else if n == 0 {
		return ErrLastAdmin
	}
	return tx.Commit()
}

func (s *Store) SetUserCredentials(ctx context.Context, id int64, passwordHash, sealedTOTP string, step int64) error {
	return affected(s.db.ExecContext(ctx, `UPDATE users SET password_hash = ?, totp_secret = ?, totp_last_step = ? WHERE id = ?`,
		passwordHash, sealedTOTP, step, id))
}

func (s *Store) SetUserPassword(ctx context.Context, id int64, passwordHash string) error {
	return affected(s.db.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, passwordHash, id))
}

// ---- invites ----

func (s *Store) CreateInvite(ctx context.Context, userID, createdBy int64, token string, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO user_invites (token_hash, user_id, expires_at, created_by) VALUES (?, ?, ?, ?)`,
		HashToken(token), userID, expires.Unix(), createdBy)
	return err
}

// InviteUser returns the user an unexpired invite token belongs to.
func (s *Store) InviteUser(ctx context.Context, token string) (*User, error) {
	var uid int64
	err := s.db.QueryRowContext(ctx, `SELECT user_id FROM user_invites WHERE token_hash = ? AND expires_at > ?`, HashToken(token), time.Now().Unix()).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.UserByID(ctx, uid)
}

// ConsumeInvite deletes the invite (and any other invites of that user).
func (s *Store) ConsumeInvite(ctx context.Context, token string) error {
	u, err := s.InviteUser(ctx, token)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM user_invites WHERE user_id = ?`, u.ID)
	return err
}

// ---- sessions ----

type SessionInfo struct {
	ID         string    `json:"id"` // hex of the token hash, never the token
	UserID     int64     `json:"user_id"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	IP         string    `json:"ip"`
	UserAgent  string    `json:"user_agent"`
	Current    bool      `json:"current"`
}

func SessionID(token string) string { return hex.EncodeToString(HashToken(token)) }

func (s *Store) UserSessions(ctx context.Context, userID int64) ([]SessionInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT token_hash, user_id, created_at, last_seen_at, expires_at, COALESCE(ip, ''), COALESCE(user_agent, '')
		FROM sessions WHERE user_id = ? AND expires_at > ? ORDER BY last_seen_at DESC`, userID, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionInfo
	for rows.Next() {
		var si SessionInfo
		var hash []byte
		var created, seen, expires int64
		if err := rows.Scan(&hash, &si.UserID, &created, &seen, &expires, &si.IP, &si.UserAgent); err != nil {
			return nil, err
		}
		si.ID = hex.EncodeToString(hash)
		si.CreatedAt, si.LastSeenAt, si.ExpiresAt = time.Unix(created, 0), time.Unix(seen, 0), time.Unix(expires, 0)
		out = append(out, si)
	}
	return out, rows.Err()
}

// DeleteSessionByID ends a session of the given user.
func (s *Store) DeleteSessionByID(ctx context.Context, userID int64, id string) error {
	hash, err := hex.DecodeString(id)
	if err != nil {
		return ErrNotFound
	}
	return affected(s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ? AND user_id = ?`, hash, userID))
}

// DeleteUserSessions ends all sessions of a user except keepID.
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64, keepID string) error {
	keep, _ := hex.DecodeString(keepID)
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?`, userID, keep)
	return err
}

// ---- WebAuthn ----

// WebAuthnHandle returns the user's opaque handle, creating it if needed.
func (s *Store) WebAuthnHandle(ctx context.Context, userID int64) ([]byte, error) {
	var h []byte
	if err := s.db.QueryRowContext(ctx, `SELECT webauthn_handle FROM users WHERE id = ?`, userID).Scan(&h); err != nil {
		return nil, err
	}
	if len(h) > 0 {
		return h, nil
	}
	h = make([]byte, 32)
	if _, err := rand.Read(h); err != nil {
		return nil, err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE users SET webauthn_handle = ? WHERE id = ? AND webauthn_handle IS NULL`, h, userID)
	if err != nil {
		return nil, err
	}
	return s.WebAuthnHandle(ctx, userID)
}

type Passkey struct {
	ID         []byte     `json:"-"`
	IDHex      string     `json:"id"`
	UserID     int64      `json:"-"`
	Name       string     `json:"name"`
	Credential string     `json:"-"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

func (s *Store) Passkeys(ctx context.Context, userID int64) ([]Passkey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, name, credential, created_at, last_used_at FROM webauthn_credentials WHERE user_id = ? ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Passkey
	for rows.Next() {
		var p Passkey
		var created int64
		var used sql.NullInt64
		if err := rows.Scan(&p.ID, &p.UserID, &p.Name, &p.Credential, &created, &used); err != nil {
			return nil, err
		}
		p.IDHex = hex.EncodeToString(p.ID)
		p.CreatedAt, p.LastUsedAt = time.Unix(created, 0), timePtr(used)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) AddPasskey(ctx context.Context, p Passkey) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO webauthn_credentials (id, user_id, name, credential, created_at) VALUES (?, ?, ?, ?, ?)`,
		p.ID, p.UserID, p.Name, p.Credential, time.Now().Unix())
	return err
}

// UpdatePasskeyUse stores the new signature counter state after a login.
func (s *Store) UpdatePasskeyUse(ctx context.Context, id []byte, credential string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE webauthn_credentials SET credential = ?, last_used_at = ? WHERE id = ?`, credential, time.Now().Unix(), id)
	return err
}

func (s *Store) DeletePasskey(ctx context.Context, userID int64, idHex string) error {
	id, err := hex.DecodeString(idHex)
	if err != nil {
		return ErrNotFound
	}
	return affected(s.db.ExecContext(ctx, `DELETE FROM webauthn_credentials WHERE id = ? AND user_id = ?`, id, userID))
}
