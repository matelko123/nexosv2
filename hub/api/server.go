// Package api is the hub's HTTP API and static UI server.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"nexos/hub/agents"
	"nexos/hub/alerts"
	"nexos/hub/auth"
	"nexos/hub/events"
	"nexos/hub/selfupdate"
	"nexos/hub/store"
	"nexos/hub/updates"
)

const (
	sessionCookie   = "nexos_session"
	sessionIdle     = 12 * time.Hour
	sessionLifetime = 7 * 24 * time.Hour
	maxBodyBytes    = 1 << 20
)

type Config struct {
	// SetupToken must be presented to create the first admin account.
	SetupToken string
	// EnrollAdvertise is the host:port agents use to reach the enrollment
	// endpoint; empty disables enrollment.
	EnrollAdvertise string
	// SecureCookies sets the Secure attribute on cookies.
	SecureCookies bool
	// HSTS enables Strict-Transport-Security (UI served over TLS).
	HSTS bool
	// TrustedProxies are networks whose X-Forwarded-For is honoured.
	TrustedProxies []*net.IPNet
	// WebAuthnOrigins are extra allowed origins for passkeys (e.g. a dev server).
	WebAuthnOrigins []string
	// Static is the built web UI.
	Static fs.FS
}

type Server struct {
	cfg      Config
	store    *store.Store
	agents   *agents.Manager
	bus      *events.Bus
	sealer   *auth.Sealer
	alerts   *alerts.Engine
	updates  *updates.Checker
	releases *selfupdate.Checker
	hubLoc   hubLocator

	loginByIP   *auth.Limiter
	loginByUser *auth.Limiter

	setupMu sync.Mutex
	setups  map[string]*pendingSetup

	webauthn waSessions
}

func New(cfg Config, st *store.Store, mgr *agents.Manager, bus *events.Bus, sealer *auth.Sealer, eng *alerts.Engine, chk *updates.Checker, rel *selfupdate.Checker) *Server {
	return &Server{
		cfg: cfg, store: st, agents: mgr, bus: bus, sealer: sealer, alerts: eng, updates: chk, releases: rel,
		loginByIP:   auth.NewLimiter(12*time.Second, 10), // 10 burst, then 5/min
		loginByUser: auth.NewLimiter(time.Minute, 5),
		setups:      map[string]*pendingSetup{},
	}
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(s.securityHeaders)

	r.Route("/api", func(r chi.Router) {
		r.Use(noStore)
		r.Get("/setup/status", s.handleSetupStatus)
		r.Post("/setup/begin", s.handleSetupBegin)
		r.Post("/setup/complete", s.handleSetupComplete)
		r.Post("/auth/login", s.handleLogin)
		r.Post("/auth/passkey", s.handlePasskeyLogin)
		r.Get("/invite/{token}", s.handleInviteInfo)
		r.Post("/invite/begin", s.handleInviteBegin)
		r.Post("/invite/complete", s.handleInviteComplete)

		r.Group(func(r chi.Router) {
			r.Use(s.requireSession)
			r.Get("/auth/me", s.handleMe)
			r.Post("/auth/logout", s.handleLogout)
			r.Get("/ws", s.handleWS)
			r.Get("/system/version", s.handleVersion)
			r.Get("/account", s.handleAccount)
			r.Post("/account/password", s.handleChangePassword)
			r.Delete("/account/sessions/{sid}", s.handleRevokeSession)
			r.Post("/account/sessions/revoke-others", s.handleRevokeOtherSessions)
			r.Post("/account/passkeys/begin", s.handlePasskeyBegin)
			r.Post("/account/passkeys/finish", s.handlePasskeyFinish)
			r.Delete("/account/passkeys/{kid}", s.handlePasskeyDelete)

			r.Get("/hosts", s.handleListHosts)
			r.Get("/hosts/{id}", s.handleGetHost)
			r.Get("/hosts/{id}/metrics", s.handleHostMetrics)
			r.Get("/hosts/{id}/containers", s.handleListContainers)
			r.Get("/hosts/{id}/containers/stats", s.handleContainerStats)
			r.Get("/hosts/{id}/containers/{cid}/logs", s.handleContainerLogs)

			r.Get("/hosts/{id}/images", s.handleListImages)
			r.Get("/hosts/{id}/images/{iid}/inspect", s.handleInspectImage)
			r.Get("/hosts/{id}/networks", s.handleListNetworks)
			r.Get("/hosts/{id}/networks/{nid}/inspect", s.handleInspectNetwork)
			r.Get("/hosts/{id}/volumes", s.handleListVolumes)
			r.Get("/hosts/{id}/volumes/{vname}/inspect", s.handleInspectVolume)
			r.Get("/hosts/{id}/stacks", s.handleListStacks)
			r.Get("/hosts/{id}/container-history/{name}", s.handleContainerHistory)
			r.Get("/hosts/{id}/container-history", s.handleContainersHistory)
			r.Get("/hosts/{id}/disk-usage", s.handleDiskUsage)
			r.Get("/hosts/{id}/updates", s.handleListUpdates)
			r.Get("/updates", s.handleAllUpdates)
			r.Get("/alerts", s.handleAlertEvents)
			r.Get("/alerts/rules", s.handleAlertRules)
			r.Get("/hosts/{id}/units", s.handleListUnits)
			r.Get("/hosts/{id}/packages", s.handlePackageUpdates)

			r.Group(func(r chi.Router) {
				r.Use(requireRole("operator"))
				r.Get("/hosts/{id}/containers/{cid}/inspect", s.handleInspectContainer)
				r.Post("/hosts/{id}/containers/{cid}/actions/{action}", s.handleContainerAction)
				r.Delete("/hosts/{id}/containers/{cid}", s.handleRemoveContainer)
				r.Post("/hosts/{id}/images/pull", s.handlePullImage)
				r.Post("/hosts/{id}/networks/{nid}/connect", s.handleNetworkMembership(true))
				r.Post("/hosts/{id}/networks/{nid}/disconnect", s.handleNetworkMembership(false))
				r.Get("/hosts/{id}/stacks/{sname}", s.handleGetStack)
				r.Post("/hosts/{id}/stacks/{sname}/run", s.handleRunStack)
				r.Post("/hosts/{id}/updates/check", s.handleCheckUpdates)
				r.Get("/hosts/{id}/processes", s.handleListProcesses)
				r.Get("/hosts/{id}/journal", s.handleJournal)
			})

			// Anything that deletes data or can grant host access (a compose
			// file can mount / or run privileged containers) is admin only.
			r.Group(func(r chi.Router) {
				r.Use(requireRole("admin"))
				r.Get("/hosts/{id}/containers/{cid}/exec", s.handleContainerExec)
				r.Delete("/hosts/{id}/images/{iid}", s.handleRemoveImage)
				r.Post("/hosts/{id}/images/prune", s.handlePruneImages)
				r.Post("/hosts/{id}/networks", s.handleCreateNetwork)
				r.Delete("/hosts/{id}/networks/{nid}", s.handleRemoveNetwork)
				r.Post("/hosts/{id}/networks/prune", s.handlePruneNetworks)
				r.Post("/hosts/{id}/volumes", s.handleCreateVolume)
				r.Delete("/hosts/{id}/volumes/{vname}", s.handleRemoveVolume)
				r.Post("/hosts/{id}/volumes/prune", s.handlePruneVolumes)
				r.Post("/hosts/{id}/stacks", s.handleCreateStack)
				r.Put("/hosts/{id}/stacks/{sname}", s.handleUpdateStack)
				r.Delete("/hosts/{id}/stacks/{sname}", s.handleDeleteStack)
				r.Post("/hosts/{id}/processes/{pid}/signal", s.handleSignalProcess)
				r.Post("/hosts/{id}/units/{unit}/{action}", s.handleUnitAction)
				r.Post("/alerts/rules", s.handleCreateRule)
				r.Put("/alerts/rules/{rid}", s.handleUpdateRule)
				r.Delete("/alerts/rules/{rid}", s.handleDeleteRule)
				// Channel settings hold tokens and passwords (masked on read).
				r.Get("/alerts/channels", s.handleAlertChannels)
				r.Post("/alerts/channels", s.handleCreateChannel)
				r.Put("/alerts/channels/{cid}", s.handleUpdateChannel)
				r.Delete("/alerts/channels/{cid}", s.handleDeleteChannel)
				r.Post("/alerts/channels/{cid}/test", s.handleTestChannel)
			})

			r.Group(func(r chi.Router) {
				r.Use(requireRole("admin"))
				r.Post("/hosts", s.handleCreateHost)
				r.Patch("/hosts/{id}", s.handleUpdateHost)
				r.Delete("/hosts/{id}", s.handleDeleteHost)
				r.Post("/hosts/{id}/enrollment", s.handleNewEnrollment)
				r.Post("/hosts/{id}/rotate-certificate", s.handleRotateCert)
				r.Get("/audit", s.handleAudit)
				r.Post("/system/version/check", s.handleCheckVersion)
				r.Post("/system/update/hub", s.handleUpdateHub)
				r.Post("/hosts/{id}/update-agent", s.handleUpdateAgent)
				r.Get("/users", s.handleListUsers)
				r.Post("/users", s.handleCreateUser)
				r.Patch("/users/{uid}", s.handleUpdateUser)
				r.Delete("/users/{uid}", s.handleDeleteUser)
				r.Post("/users/{uid}/reset", s.handleResetUser)
				r.Get("/users/{uid}/sessions", s.handleUserSessions)
				r.Delete("/users/{uid}/sessions", s.handleRevokeUserSessions)
			})
		})
		r.NotFound(func(w http.ResponseWriter, r *http.Request) { writeError(w, http.StatusNotFound, "not found") })
	})
	r.Handle("/*", s.staticHandler())
	return r
}

// ---- middleware ----

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; worker-src 'self' blob:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		if s.cfg.HSTS {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

type ctxKey int

const principalKey ctxKey = iota

type principal struct {
	user    *store.User
	session *store.Session
	token   string
}

func principalFrom(ctx context.Context) *principal {
	p, _ := ctx.Value(principalKey).(*principal)
	return p
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// sameOrigin rejects cross-site requests carrying an Origin header that
// does not match the request host.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" {
			writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		ctx := r.Context()
		sess, err := s.store.SessionByToken(ctx, c.Value)
		now := time.Now()
		if err != nil || now.After(sess.ExpiresAt) || now.Sub(sess.LastSeenAt) > sessionIdle {
			if err == nil {
				_ = s.store.DeleteSession(ctx, c.Value)
			}
			s.clearSessionCookie(w)
			writeError(w, http.StatusUnauthorized, "session expired")
			return
		}
		user, err := s.store.UserByID(ctx, sess.UserID)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		if !isSafeMethod(r.Method) {
			token := r.Header.Get("X-CSRF-Token")
			if !sameOrigin(r) || subtle.ConstantTimeCompare([]byte(token), []byte(sess.CSRFToken)) != 1 {
				writeError(w, http.StatusForbidden, "invalid CSRF token")
				return
			}
		}
		if now.Sub(sess.LastSeenAt) > time.Minute {
			_ = s.store.TouchSession(ctx, c.Value, now)
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, principalKey, &principal{user: user, session: sess, token: c.Value})))
	})
}

var roleRank = map[string]int{"viewer": 1, "operator": 2, "admin": 3}

func requireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := principalFrom(r.Context())
			if p == nil || roleRank[p.user.Role] < roleRank[role] {
				writeError(w, http.StatusForbidden, "insufficient permissions")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ---- helpers ----

func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || len(s.cfg.TrustedProxies) == 0 {
		return host
	}
	trusted := func(ip net.IP) bool {
		for _, n := range s.cfg.TrustedProxies {
			if n.Contains(ip) {
				return true
			}
		}
		return false
	}
	if !trusted(ip) {
		return host
	}
	// Walk X-Forwarded-For from the right, skipping trusted proxies.
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		cand := net.ParseIP(strings.TrimSpace(parts[i]))
		if cand == nil {
			break
		}
		if !trusted(cand) {
			return cand.String()
		}
	}
	return host
}

func (s *Server) audit(r *http.Request, action, target, agentID string, err error, detail string) {
	e := store.AuditEntry{IP: s.clientIP(r), Action: action, Target: target, AgentID: agentID, Success: err == nil, Detail: detail}
	if p := principalFrom(r.Context()); p != nil {
		e.UserID, e.Username = &p.user.ID, p.user.Username
	}
	if err != nil {
		if e.Detail != "" {
			e.Detail += ": "
		}
		e.Detail += err.Error()
	}
	if aerr := s.store.Audit(r.Context(), e); aerr != nil {
		slog.Error("audit write failed", "err", aerr, "action", action)
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func internalError(w http.ResponseWriter, err error) {
	slog.Error("internal error", "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", Expires: expires,
		HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteStrictMode,
	})
}

func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }

// Maintenance prunes expired sessions and pending setups periodically.
func (s *Server) Maintenance(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = s.store.DeleteExpiredSessions(ctx, time.Now().Add(-sessionIdle))
			s.setupMu.Lock()
			for id, p := range s.setups {
				if time.Now().After(p.expires) {
					delete(s.setups, id)
				}
			}
			s.setupMu.Unlock()
		}
	}
}
