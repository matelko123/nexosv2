package agents

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"time"

	"nexos/hub/auth"
	"nexos/hub/store"
	"nexos/internal/enroll"
	"nexos/internal/pki"
)

// EnrollmentTTL is how long a freshly generated enrollment bundle is valid.
const EnrollmentTTL = 15 * time.Minute

// NewEnrollment creates a single-use enrollment secret for an agent and
// returns the encoded bundle to hand to the agent.
func (m *Manager) NewEnrollment(ctx context.Context, agentID, advertiseAddr string) (string, time.Time, error) {
	secret := auth.RandomToken(32)
	expires := time.Now().Add(EnrollmentTTL)
	if err := m.store.SetEnrollment(ctx, agentID, secret, expires); err != nil {
		return "", time.Time{}, err
	}
	b := enroll.Bundle{HubAddr: advertiseAddr, CAFingerprint: pki.Fingerprint(m.ca.Cert), AgentID: agentID, Secret: secret}
	return b.Encode(), expires, nil
}

// EnrollmentHandler serves the agent enrollment endpoint. It must be served
// over TLS with the hub certificate plus CA chain (see pki.KeyPair.WithChain).
func (m *Manager) EnrollmentHandler() http.Handler {
	limiter := auth.NewLimiter(10*time.Second, 5)
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+enroll.Path, func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !limiter.Allow(ip) {
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		req, err := enroll.DecodeRequest(r.Body)
		if err != nil || !pki.ValidAgentID(req.AgentID) {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		audit := func(ok bool, detail string) {
			_ = m.store.Audit(ctx, store.AuditEntry{IP: ip, Action: "agent.enroll", AgentID: req.AgentID, Target: req.Hostname, Success: ok, Detail: detail})
		}
		agent, err := m.store.Agent(ctx, req.AgentID)
		if err != nil {
			audit(false, "unknown agent")
			http.Error(w, "enrollment refused", http.StatusForbidden)
			return
		}
		if err := m.store.ConsumeEnrollment(ctx, req.AgentID, req.Secret); err != nil {
			audit(false, err.Error())
			slog.Warn("enrollment refused", "agent", req.AgentID, "ip", ip, "err", err)
			http.Error(w, "enrollment refused", http.StatusForbidden)
			return
		}
		leaf, certPEM, err := m.ca.SignAgentCSR([]byte(req.CSRPEM), req.AgentID)
		if err != nil {
			audit(false, "invalid csr: "+err.Error())
			http.Error(w, "invalid csr", http.StatusBadRequest)
			return
		}
		hostname := req.Hostname
		if len(hostname) > 253 {
			hostname = hostname[:253]
		}
		if err := m.store.CompleteEnrollment(ctx, store.AgentCert{
			Fingerprint: pki.Fingerprint(leaf), AgentID: req.AgentID, Serial: leaf.SerialNumber.Text(16), NotAfter: leaf.NotAfter,
		}, hostname); err != nil {
			slog.Error("complete enrollment", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		audit(true, "")
		slog.Info("agent enrolled", "agent", req.AgentID, "hostname", hostname, "ip", ip)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(enroll.Response{CertificatePEM: string(certPEM), CAPEM: string(m.ca.CertPEM)})

		// The agent starts serving right after storing its identity.
		m.Connect(agent.ID, agent.Address)
	})
	return mux
}
