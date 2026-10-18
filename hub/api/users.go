package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/png"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"nexos/hub/auth"
	"nexos/hub/store"
)

const inviteTTL = 48 * time.Hour

var validRoles = map[string]bool{"admin": true, "operator": true, "viewer": true}

// ---- administration ----

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListUsers(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listOrEmpty(list))
}

func (s *Server) newInvite(ctx context.Context, userID, by int64) (string, time.Time, error) {
	token := auth.RandomToken(32)
	exp := time.Now().Add(inviteTTL)
	return token, exp, s.store.CreateInvite(ctx, userID, by, token, exp)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	if !usernamePattern.MatchString(body.Username) {
		writeError(w, http.StatusBadRequest, "username must be 3-32 characters: letters, digits, . _ -")
		return
	}
	if !validRoles[body.Role] {
		writeError(w, http.StatusBadRequest, "invalid role")
		return
	}
	p := principalFrom(r.Context())
	id, err := s.store.CreatePendingUser(r.Context(), body.Username, body.Role)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeError(w, http.StatusConflict, "username already exists")
			return
		}
		internalError(w, err)
		return
	}
	token, exp, err := s.newInvite(r.Context(), id, p.user.ID)
	s.audit(r, "user.create", body.Username, "", err, "role="+body.Role)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "invite_token": token, "expires_at": exp})
}

func userIDParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "uid"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return 0, false
	}
	if id == principalFrom(r.Context()).user.ID {
		writeError(w, http.StatusConflict, "use the account page to manage your own account")
		return 0, false
	}
	return id, true
}

func (s *Server) userErr(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case isNotFound(err):
		writeError(w, http.StatusNotFound, "user not found")
	case errors.Is(err, store.ErrLastAdmin):
		writeError(w, http.StatusConflict, "at least one admin must remain")
	default:
		internalError(w, err)
	}
	return true
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := userIDParam(w, r)
	if !ok {
		return
	}
	var body struct {
		Role string `json:"role"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !validRoles[body.Role] {
		writeError(w, http.StatusBadRequest, "invalid role")
		return
	}
	err := s.store.SetUserRole(r.Context(), id, body.Role)
	s.audit(r, "user.role", strconv.FormatInt(id, 10), "", err, "role="+body.Role)
	if !s.userErr(w, err) {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := userIDParam(w, r)
	if !ok {
		return
	}
	err := s.store.DeleteUser(r.Context(), id)
	s.audit(r, "user.delete", strconv.FormatInt(id, 10), "", err, "")
	if !s.userErr(w, err) {
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleResetUser clears credentials and ends sessions; the user regains
// access only through the returned invitation.
func (s *Server) handleResetUser(w http.ResponseWriter, r *http.Request) {
	id, ok := userIDParam(w, r)
	if !ok {
		return
	}
	err := s.store.ResetUserCredentials(r.Context(), id)
	if s.userErr(w, err) {
		s.audit(r, "user.reset", strconv.FormatInt(id, 10), "", err, "")
		return
	}
	token, exp, err := s.newInvite(r.Context(), id, principalFrom(r.Context()).user.ID)
	s.audit(r, "user.reset", strconv.FormatInt(id, 10), "", err, "")
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invite_token": token, "expires_at": exp})
}

func (s *Server) handleUserSessions(w http.ResponseWriter, r *http.Request) {
	id, ok := userIDParam(w, r)
	if !ok {
		return
	}
	list, err := s.store.UserSessions(r.Context(), id)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listOrEmpty(list))
}

func (s *Server) handleRevokeUserSessions(w http.ResponseWriter, r *http.Request) {
	id, ok := userIDParam(w, r)
	if !ok {
		return
	}
	err := s.store.DeleteUserSessions(r.Context(), id, "")
	s.audit(r, "user.sessions.revoke", strconv.FormatInt(id, 10), "", err, "")
	if err != nil {
		internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- invitations (unauthenticated, token protected) ----

func (s *Server) handleInviteInfo(w http.ResponseWriter, r *http.Request) {
	if !s.loginByIP.Allow(s.clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	u, err := s.store.InviteUser(r.Context(), chi.URLParam(r, "token"))
	if err != nil {
		writeError(w, http.StatusNotFound, "invitation is invalid or expired")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"username": u.Username, "role": u.Role})
}

func (s *Server) handleInviteBegin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.loginByIP.Allow(s.clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	u, err := s.store.InviteUser(r.Context(), req.Token)
	if err != nil {
		writeError(w, http.StatusNotFound, "invitation is invalid or expired")
		return
	}
	if err := auth.ValidatePassword(req.Password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp, err := s.beginTOTPSetup(&pendingSetup{username: u.Username, userID: u.ID, inviteToken: req.Token}, req.Password)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// beginTOTPSetup hashes the password, creates a TOTP secret and stores the
// pending setup; shared by first-run setup and invitations.
func (s *Server) beginTOTPSetup(p *pendingSetup, password string) (map[string]string, error) {
	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, err
	}
	key, err := auth.NewTOTP(p.username)
	if err != nil {
		return nil, err
	}
	img, err := key.Image(240, 240)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	p.passwordHash, p.totpSecret, p.expires = hash, key.Secret(), time.Now().Add(10*time.Minute)
	id := auth.RandomToken(24)
	s.setupMu.Lock()
	s.setups[id] = p
	s.setupMu.Unlock()
	return map[string]string{
		"setup_id": id, "totp_secret": key.Secret(), "totp_uri": key.URL(),
		"qr_png": "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()),
	}, nil
}

func (s *Server) handleInviteComplete(w http.ResponseWriter, r *http.Request) {
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
	if !ok || p.userID == 0 || time.Now().After(p.expires) {
		writeError(w, http.StatusBadRequest, "setup session expired, start again")
		return
	}
	step, ok := auth.CheckTOTP(p.totpSecret, req.Code, time.Now())
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid authenticator code")
		return
	}
	// Re-check and consume the invite atomically with setting credentials.
	if err := s.store.ConsumeInvite(r.Context(), p.inviteToken); err != nil {
		writeError(w, http.StatusNotFound, "invitation is invalid or expired")
		return
	}
	if err := s.store.SetUserCredentials(r.Context(), p.userID, p.passwordHash, s.sealer.Seal(p.totpSecret), step); err != nil {
		internalError(w, err)
		return
	}
	s.setupMu.Lock()
	delete(s.setups, req.SetupID)
	s.setupMu.Unlock()
	u, err := s.store.UserByID(r.Context(), p.userID)
	if err != nil {
		internalError(w, err)
		return
	}
	s.auditAs(r, u, "user.invite.accept", u.Username, nil)
	s.startSession(w, r, u)
}

// ---- own account ----

func (s *Server) handleAccount(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	sessions, err := s.store.UserSessions(r.Context(), p.user.ID)
	if err != nil {
		internalError(w, err)
		return
	}
	current := store.SessionID(p.token)
	for i := range sessions {
		sessions[i].Current = sessions[i].ID == current
	}
	keys, err := s.store.Passkeys(r.Context(), p.user.ID)
	if err != nil {
		internalError(w, err)
		return
	}
	_, rpErr := s.relyingParty(r)
	resp := map[string]any{
		"user":     userDTO{ID: p.user.ID, Username: p.user.Username, Role: p.user.Role},
		"sessions": listOrEmpty(sessions), "passkeys": listOrEmpty(keys),
		"passkeys_available": rpErr == nil,
	}
	if rpErr != nil {
		resp["passkeys_unavailable_reason"] = rpErr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	var req struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
		Code    string `json:"code"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.loginByUser.Allow(strings.ToLower(p.user.Username)) {
		writeError(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	secret, err := s.sealer.Open(p.user.TOTPSecret)
	if err != nil {
		internalError(w, err)
		return
	}
	step, codeOK := auth.CheckTOTP(secret, req.Code, time.Now())
	if !auth.VerifyPassword(p.user.PasswordHash, req.Current) || !codeOK || s.store.AdvanceTOTPStep(r.Context(), p.user.ID, step) != nil {
		s.audit(r, "account.password", p.user.Username, "", errors.New("verification failed"), "")
		writeError(w, http.StatusForbidden, "current password or code is incorrect")
		return
	}
	if err := auth.ValidatePassword(req.New); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := auth.HashPassword(req.New)
	if err == nil {
		err = s.store.SetUserPassword(r.Context(), p.user.ID, hash)
	}
	if err == nil {
		// Other devices must sign in again with the new password.
		err = s.store.DeleteUserSessions(r.Context(), p.user.ID, store.SessionID(p.token))
	}
	s.audit(r, "account.password", p.user.Username, "", err, "")
	if err != nil {
		internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	err := s.store.DeleteSessionByID(r.Context(), p.user.ID, chi.URLParam(r, "sid"))
	s.audit(r, "account.session.revoke", "", "", err, "")
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	} else if err != nil {
		internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	err := s.store.DeleteUserSessions(r.Context(), p.user.ID, store.SessionID(p.token))
	s.audit(r, "account.session.revoke_others", "", "", err, "")
	if err != nil {
		internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- passkeys (WebAuthn) ----

type waUser struct {
	handle []byte
	name   string
	creds  []webauthn.Credential
}

func (u waUser) WebAuthnID() []byte                         { return u.handle }
func (u waUser) WebAuthnName() string                       { return u.name }
func (u waUser) WebAuthnDisplayName() string                { return u.name }
func (u waUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

type waPending struct {
	data    webauthn.SessionData
	userID  int64
	expires time.Time
}

// waSessions holds in-flight WebAuthn ceremonies (challenge state).
type waSessions struct {
	mu sync.Mutex
	m  map[string]waPending
}

func (ws *waSessions) put(key string, p waPending) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.m == nil {
		ws.m = map[string]waPending{}
	}
	for k, v := range ws.m {
		if time.Now().After(v.expires) {
			delete(ws.m, k)
		}
	}
	ws.m[key] = p
}

func (ws *waSessions) take(key string) (waPending, bool) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	p, ok := ws.m[key]
	delete(ws.m, key)
	return p, ok && time.Now().Before(p.expires)
}

// relyingParty builds the WebAuthn configuration for the host name the UI
// is reached at. Passkeys are bound to a domain, so IP addresses cannot be
// used (browsers refuse them).
func (s *Server) relyingParty(r *http.Request) (*webauthn.WebAuthn, error) {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if net.ParseIP(host) != nil {
		return nil, errors.New("passkeys need a host name (e.g. nexos.lan), not an IP address")
	}
	origins := append([]string{"https://" + r.Host}, s.cfg.WebAuthnOrigins...)
	return webauthn.New(&webauthn.Config{RPID: host, RPDisplayName: "NexOS", RPOrigins: origins})
}

func (s *Server) webauthnUser(ctx context.Context, u *store.User) (waUser, []store.Passkey, error) {
	handle, err := s.store.WebAuthnHandle(ctx, u.ID)
	if err != nil {
		return waUser{}, nil, err
	}
	keys, err := s.store.Passkeys(ctx, u.ID)
	if err != nil {
		return waUser{}, nil, err
	}
	wu := waUser{handle: handle, name: u.Username}
	for _, k := range keys {
		var c webauthn.Credential
		if json.Unmarshal([]byte(k.Credential), &c) == nil {
			wu.creds = append(wu.creds, c)
		}
	}
	return wu, keys, nil
}

func (s *Server) handlePasskeyBegin(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	rp, err := s.relyingParty(r)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	wu, _, err := s.webauthnUser(r.Context(), p.user)
	if err != nil {
		internalError(w, err)
		return
	}
	exclude := make([]protocol.CredentialDescriptor, 0, len(wu.creds))
	for _, c := range wu.creds {
		exclude = append(exclude, c.Descriptor())
	}
	options, data, err := rp.BeginRegistration(wu,
		webauthn.WithExclusions(exclude),
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementPreferred),
	)
	if err != nil {
		internalError(w, err)
		return
	}
	s.webauthn.put("reg:"+strconv.FormatInt(p.user.ID, 10), waPending{data: *data, userID: p.user.ID, expires: time.Now().Add(5 * time.Minute)})
	writeJSON(w, http.StatusOK, options)
}

func (s *Server) handlePasskeyFinish(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" || len([]rune(name)) > 64 {
		writeError(w, http.StatusBadRequest, "passkey name must be 1-64 characters")
		return
	}
	pending, ok := s.webauthn.take("reg:" + strconv.FormatInt(p.user.ID, 10))
	if !ok {
		writeError(w, http.StatusBadRequest, "registration expired, start again")
		return
	}
	rp, err := s.relyingParty(r)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid passkey response")
		return
	}
	wu, _, err := s.webauthnUser(r.Context(), p.user)
	if err != nil {
		internalError(w, err)
		return
	}
	cred, err := rp.CreateCredential(wu, pending.data, parsed)
	if err != nil {
		s.audit(r, "account.passkey.add", name, "", err, "")
		writeError(w, http.StatusBadRequest, "passkey verification failed")
		return
	}
	raw, _ := json.Marshal(cred)
	err = s.store.AddPasskey(r.Context(), store.Passkey{ID: cred.ID, UserID: p.user.ID, Name: name, Credential: string(raw)})
	s.audit(r, "account.passkey.add", name, "", err, "")
	if err != nil {
		internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePasskeyDelete(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	err := s.store.DeletePasskey(r.Context(), p.user.ID, chi.URLParam(r, "kid"))
	s.audit(r, "account.passkey.delete", "", "", err, "")
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "passkey not found")
		return
	} else if err != nil {
		internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// beginPasskeyLogin is called after a correct password when no TOTP code
// was given and the user has passkeys: it returns the assertion options.
func (s *Server) beginPasskeyLogin(w http.ResponseWriter, r *http.Request, u *store.User) bool {
	rp, err := s.relyingParty(r)
	if err != nil {
		return false
	}
	wu, _, err := s.webauthnUser(r.Context(), u)
	if err != nil || len(wu.creds) == 0 {
		return false
	}
	options, data, err := rp.BeginLogin(wu, webauthn.WithUserVerification(protocol.VerificationPreferred))
	if err != nil {
		return false
	}
	id := auth.RandomToken(24)
	s.webauthn.put("login:"+id, waPending{data: *data, userID: u.ID, expires: time.Now().Add(3 * time.Minute)})
	writeJSON(w, http.StatusOK, map[string]any{"passkey_required": true, "login_id": id, "options": options})
	return true
}

func (s *Server) handlePasskeyLogin(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin request refused")
		return
	}
	if !s.loginByIP.Allow(s.clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	loginID := r.URL.Query().Get("login_id")
	pending, ok := s.webauthn.take("login:" + loginID)
	if !ok {
		writeError(w, http.StatusUnauthorized, "login expired, start again")
		return
	}
	rp, err := s.relyingParty(r)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid passkey response")
		return
	}
	u, err := s.store.UserByID(r.Context(), pending.userID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid passkey")
		return
	}
	wu, _, err := s.webauthnUser(r.Context(), u)
	if err != nil {
		internalError(w, err)
		return
	}
	cred, err := rp.ValidateLogin(wu, pending.data, parsed)
	if err != nil {
		s.auditAs(r, u, "auth.login", u.Username, errors.New("passkey verification failed"))
		writeError(w, http.StatusUnauthorized, "invalid passkey")
		return
	}
	raw, _ := json.Marshal(cred)
	_ = s.store.UpdatePasskeyUse(r.Context(), cred.ID, string(raw))
	s.auditAs(r, u, "auth.login", u.Username, nil)
	s.startSession(w, r, u)
}
