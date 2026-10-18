package api

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"nexos/hub/agents"
	"nexos/hub/store"
	nexosv1 "nexos/internal/gen/nexos/v1"
	"nexos/internal/pki"
)

type hostDTO struct {
	ID                  string               `json:"id"`
	Name                string               `json:"name"`
	Address             string               `json:"address"`
	Hostname            string               `json:"hostname,omitempty"`
	CreatedAt           time.Time            `json:"created_at"`
	EnrolledAt          *time.Time           `json:"enrolled_at,omitempty"`
	LastSeenAt          *time.Time           `json:"last_seen_at,omitempty"`
	Status              agents.Status        `json:"status"`
	StatusSince         *time.Time           `json:"status_since,omitempty"`
	Error               string               `json:"error,omitempty"`
	Info                *nexosv1.HostInfo    `json:"info,omitempty"`
	Metrics             *nexosv1.HostMetrics `json:"metrics,omitempty"`
	EnrollmentExpiresAt *time.Time           `json:"enrollment_expires_at,omitempty"`
	CertNotAfter        *time.Time           `json:"cert_not_after,omitempty"`
}

func (s *Server) hostDTO(r *http.Request, a *store.Agent) hostDTO {
	d := hostDTO{ID: a.ID, Name: a.Name, Address: a.Address, Hostname: a.Hostname, CreatedAt: a.CreatedAt, EnrolledAt: a.EnrolledAt, LastSeenAt: a.LastSeenAt}
	if a.EnrolledAt == nil {
		d.Status = agents.StatusPending
		if ok, exp, err := s.store.EnrollmentPending(r.Context(), a.ID); err == nil && ok {
			d.EnrollmentExpiresAt = &exp
		}
		return d
	}
	st := s.agents.State(a.ID)
	d.Status, d.Error, d.Info, d.Metrics = st.Status, st.Error, st.Info, st.Metrics
	if !st.Since.IsZero() {
		d.StatusSince = &st.Since
	}
	if d.Info == nil && a.InfoJSON != "" {
		var info nexosv1.HostInfo
		if json.Unmarshal([]byte(a.InfoJSON), &info) == nil {
			d.Info = &info
		}
	}
	if c, err := s.store.LatestAgentCert(r.Context(), a.ID); err == nil {
		d.CertNotAfter = &c.NotAfter
	}
	return d
}

func (s *Server) handleListHosts(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.Agents(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	out := make([]hostDTO, 0, len(list))
	for _, a := range list {
		out = append(out, s.hostDTO(r, a))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetHost(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.Agent(r.Context(), chi.URLParam(r, "id"))
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "host not found")
		return
	} else if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.hostDTO(r, a))
}

type hostInput struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

func (in *hostInput) validate() error {
	in.Name = strings.TrimSpace(in.Name)
	in.Address = strings.TrimSpace(in.Address)
	if l := len([]rune(in.Name)); l < 1 || l > 64 {
		return errors.New("name must be 1-64 characters")
	}
	host, port, err := net.SplitHostPort(in.Address)
	if err != nil || host == "" {
		return errors.New("address must be host:port, e.g. 10.66.0.2:7443")
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return errors.New("invalid port")
	}
	return nil
}

func (s *Server) handleCreateHost(w http.ResponseWriter, r *http.Request) {
	if s.cfg.EnrollAdvertise == "" {
		writeError(w, http.StatusConflict, "enrollment is disabled: set NEXOS_ENROLL_LISTEN on the hub")
		return
	}
	var in hostInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := in.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a := &store.Agent{ID: pki.NewAgentID(), Name: in.Name, Address: in.Address}
	if err := s.store.CreateAgent(r.Context(), a); err != nil {
		internalError(w, err)
		return
	}
	bundle, exp, err := s.agents.NewEnrollment(r.Context(), a.ID, s.cfg.EnrollAdvertise)
	s.audit(r, "host.create", a.Name, a.ID, err, a.Address)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"host": s.hostDTO(r, a), "bundle": bundle, "expires_at": exp})
}

func (s *Server) handleUpdateHost(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in hostInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := in.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	prev, err := s.store.Agent(r.Context(), id)
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "host not found")
		return
	} else if err != nil {
		internalError(w, err)
		return
	}
	err = s.store.UpdateAgent(r.Context(), id, in.Name, in.Address)
	s.audit(r, "host.update", in.Name, id, err, in.Address)
	if err != nil {
		internalError(w, err)
		return
	}
	if prev.EnrolledAt != nil && prev.Address != in.Address {
		s.agents.Connect(id, in.Address)
	}
	a, _ := s.store.Agent(r.Context(), id)
	writeJSON(w, http.StatusOK, s.hostDTO(r, a))
}

func (s *Server) handleDeleteHost(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.agents.Disconnect(id)
	// Deleting the agent cascades to its certificates, so its pin is gone and
	// it can never connect again without a new enrollment.
	err := s.store.DeleteAgent(r.Context(), id)
	s.audit(r, "host.delete", "", id, err, "")
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "host not found")
		return
	} else if err != nil {
		internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleNewEnrollment issues a new single-use bundle, e.g. when an agent lost
// its data directory. Existing certificates are revoked once it enrolls.
func (s *Server) handleNewEnrollment(w http.ResponseWriter, r *http.Request) {
	if s.cfg.EnrollAdvertise == "" {
		writeError(w, http.StatusConflict, "enrollment is disabled: set NEXOS_ENROLL_LISTEN on the hub")
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := s.store.Agent(r.Context(), id); isNotFound(err) {
		writeError(w, http.StatusNotFound, "host not found")
		return
	} else if err != nil {
		internalError(w, err)
		return
	}
	bundle, exp, err := s.agents.NewEnrollment(r.Context(), id, s.cfg.EnrollAdvertise)
	s.audit(r, "host.enrollment", "", id, err, "")
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bundle": bundle, "expires_at": exp})
}

func (s *Server) handleRotateCert(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	cc, err := s.agents.Client(id)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	err = s.agents.Renew(r.Context(), id, cc)
	s.audit(r, "host.cert.rotate", "", id, err, "")
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	entries, err := s.store.AuditLog(r.Context(), limit)
	if err != nil {
		internalError(w, err)
		return
	}
	if entries == nil {
		entries = []store.AuditEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}
