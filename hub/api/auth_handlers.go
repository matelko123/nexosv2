package api

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"image/png"
	"net/http"
	"regexp"
	"strings"
	"time"

	"nexos/hub/auth"
	"nexos/hub/store"
)

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9._-]{3,32}$`)

type userDTO struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

type meResponse struct {
	User      userDTO `json:"user"`
	CSRFToken string  `json:"csrf_token"`
}

type pendingSetup struct {
	username     string
	passwordHash string
	totpSecret   string
	expires      time.Time
	// Set for invitations (existing pending user); zero for first-run setup.
	userID      int64
	inviteToken string
}

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.CountUsers(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"needs_setup": n == 0})
}

// handleSetupBegin validates the setup token and credentials and returns a
// fresh TOTP secret. Nothing is persisted until the TOTP code is confirmed.
func (s *Server) handleSetupBegin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SetupToken string `json:"setup_token"`
		Username   string `json:"username"`
		Password   string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.loginByIP.Allow(s.clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	if n, err := s.store.CountUsers(r.Context()); err != nil {
		internalError(w, err)
		return
	} else if n > 0 {
		writeError(w, http.StatusConflict, "setup already completed")
		return
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(req.SetupToken)), []byte(s.cfg.SetupToken)) != 1 {
		s.audit(r, "setup.begin", req.Username, "", errors.New("invalid setup token"), "")
		writeError(w, http.StatusForbidden, "invalid setup token (see the hub log)")
		return
	}
	if !usernamePattern.MatchString(req.Username) {
		writeError(w, http.StatusBadRequest, "username must be 3-32 characters: letters, digits, . _ -")
		return
	}
	if err := auth.ValidatePassword(req.Password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		internalError(w, err)
		return
	}
	key, err := auth.NewTOTP(req.Username)
	if err != nil {
		internalError(w, err)
		return
	}
	img, err := key.Image(240, 240)
	if err != nil {
		internalError(w, err)
		return
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		internalError(w, err)
		return
	}
	id := auth.RandomToken(24)
	s.setupMu.Lock()
	s.setups[id] = &pendingSetup{username: req.Username, passwordHash: hash, totpSecret: key.Secret(), expires: time.Now().Add(10 * time.Minute)}
	s.setupMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{
		"setup_id":    id,
		"totp_secret": key.Secret(),
		"totp_uri":    key.URL(),
		"qr_png":      "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()),
	})
}

func (s *Server) handleSetupComplete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SetupID string `json:"setup_id"`
		Code    string `json:"code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	s.setupMu.Lock()
	p, ok := s.setups[req.SetupID]
	s.setupMu.Unlock()
	if !ok || p.userID != 0 || time.Now().After(p.expires) {
		writeError(w, http.StatusBadRequest, "setup session expired, start again")
		return
	}
	step, ok := auth.CheckTOTP(p.totpSecret, req.Code, time.Now())
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid authenticator code")
		return
	}
	u := &store.User{Username: p.username, PasswordHash: p.passwordHash, TOTPSecret: s.sealer.Seal(p.totpSecret), Role: "admin"}
	if err := s.store.CreateFirstUser(r.Context(), u); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	s.setupMu.Lock()
	delete(s.setups, req.SetupID)
	s.setupMu.Unlock()
	_ = s.store.AdvanceTOTPStep(r.Context(), u.ID, step)
	s.auditAs(r, u, "setup.complete", u.Username, nil)
	s.startSession(w, r, u)
}

func (s *Server) auditAs(r *http.Request, u *store.User, action, target string, err error) {
	e := store.AuditEntry{IP: s.clientIP(r), Action: action, Target: target, Success: err == nil}
	if u != nil {
		e.UserID, e.Username = &u.ID, u.Username
	}
	if err != nil {
		e.Detail = err.Error()
	}
	_ = s.store.Audit(r.Context(), e)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin request refused")
		return
	}
	ip := s.clientIP(r)
	uname := strings.ToLower(strings.TrimSpace(req.Username))
	if !s.loginByIP.Allow(ip) || !s.loginByUser.Allow(uname) {
		s.audit(r, "auth.login", uname, "", errors.New("rate limited"), "")
		writeError(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	fail := func(reason string, u *store.User) {
		s.auditAs(r, u, "auth.login", uname, errors.New(reason))
		// One generic message: do not reveal which factor failed.
		writeError(w, http.StatusUnauthorized, "invalid username, password or code")
	}
	u, err := s.store.UserByUsername(r.Context(), uname)
	if err != nil {
		if !isNotFound(err) {
			internalError(w, err)
			return
		}
		auth.VerifyDummy(req.Password)
		fail("unknown user", nil)
		return
	}
	if !auth.VerifyPassword(u.PasswordHash, req.Password) {
		fail("bad password", u)
		return
	}
	// No code: a passkey can serve as the second factor instead of TOTP.
	if strings.TrimSpace(req.Code) == "" {
		if s.beginPasskeyLogin(w, r, u) {
			return
		}
		fail("missing second factor", u)
		return
	}
	secret, err := s.sealer.Open(u.TOTPSecret)
	if err != nil {
		internalError(w, err)
		return
	}
	step, ok := auth.CheckTOTP(secret, req.Code, time.Now())
	if !ok {
		fail("bad totp code", u)
		return
	}
	if err := s.store.AdvanceTOTPStep(r.Context(), u.ID, step); err != nil {
		fail("totp code reused", u)
		return
	}
	s.auditAs(r, u, "auth.login", uname, nil)
	s.startSession(w, r, u)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *store.User) {
	token := auth.RandomToken(32)
	now := time.Now()
	sess := store.Session{UserID: u.ID, CSRFToken: auth.RandomToken(32), CreatedAt: now, ExpiresAt: now.Add(sessionLifetime), LastSeenAt: now}
	ua := r.UserAgent()
	if len(ua) > 256 {
		ua = ua[:256]
	}
	if err := s.store.CreateSession(r.Context(), token, sess, s.clientIP(r), ua); err != nil {
		internalError(w, err)
		return
	}
	s.setSessionCookie(w, token, sess.ExpiresAt)
	writeJSON(w, http.StatusOK, meResponse{User: userDTO{ID: u.ID, Username: u.Username, Role: u.Role}, CSRFToken: sess.CSRFToken})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	writeJSON(w, http.StatusOK, meResponse{User: userDTO{ID: p.user.ID, Username: p.user.Username, Role: p.user.Role}, CSRFToken: p.session.CSRFToken})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	_ = s.store.DeleteSession(r.Context(), p.token)
	s.clearSessionCookie(w)
	s.audit(r, "auth.logout", p.user.Username, "", nil, "")
	w.WriteHeader(http.StatusNoContent)
}
