package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"nexos/hub/alerts"
	"nexos/hub/store"
)

// metricRanges maps a UI range to the table resolution and bucket size.
var metricRanges = map[string]struct {
	window time.Duration
	table  store.MetricTable
	bucket time.Duration
}{
	"15m": {15 * time.Minute, store.MetricsRaw, 0},
	"1h":  {time.Hour, store.MetricsRaw, 15 * time.Second},
	"6h":  {6 * time.Hour, store.Metrics1m, time.Minute},
	"24h": {24 * time.Hour, store.Metrics1m, 5 * time.Minute},
	"7d":  {7 * 24 * time.Hour, store.Metrics5m, 30 * time.Minute},
	"30d": {30 * 24 * time.Hour, store.Metrics5m, 2 * time.Hour},
	"90d": {90 * 24 * time.Hour, store.Metrics5m, 6 * time.Hour},
}

func (s *Server) handleHostMetrics(w http.ResponseWriter, r *http.Request) {
	rg, ok := metricRanges[r.URL.Query().Get("range")]
	if !ok {
		rg = metricRanges["1h"]
	}
	pts, err := s.store.MetricsFrom(r.Context(), rg.table, chi.URLParam(r, "id"), time.Now().Add(-rg.window), rg.bucket.Milliseconds())
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listOrEmpty(pts))
}

var containerRanges = map[string]struct {
	window, bucket time.Duration
}{
	"1h":  {time.Hour, time.Minute},
	"6h":  {6 * time.Hour, time.Minute},
	"24h": {24 * time.Hour, 5 * time.Minute},
	"7d":  {7 * 24 * time.Hour, 30 * time.Minute},
}

func (s *Server) handleContainerHistory(w http.ResponseWriter, r *http.Request) {
	name, ok := param(w, r, "name", containerIDPattern)
	if !ok {
		return
	}
	rg, ok := containerRanges[r.URL.Query().Get("range")]
	if !ok {
		rg = containerRanges["6h"]
	}
	pts, err := s.store.ContainerMetrics(r.Context(), chi.URLParam(r, "id"), name, time.Now().Add(-rg.window), rg.bucket.Milliseconds())
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listOrEmpty(pts))
}

// ---- image updates ----

func (s *Server) handleListUpdates(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ImageUpdates(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listOrEmpty(list))
}

func (s *Server) handleAllUpdates(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ImageUpdates(r.Context(), "")
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listOrEmpty(list))
}

func (s *Server) handleCheckUpdates(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	list, err := s.updates.Check(r.Context(), id)
	s.audit(r, "image.updates.check", "", id, grpcErr(err), "")
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listOrEmpty(list))
}

// ---- alert events ----

func (s *Server) handleAlertEvents(w http.ResponseWriter, r *http.Request) {
	open := r.URL.Query().Get("state") != "all"
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	list, err := s.store.AlertEvents(r.Context(), open, limit)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listOrEmpty(list))
}

// ---- alert rules ----

func (s *Server) handleAlertRules(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.AlertRules(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": listOrEmpty(list), "types": alerts.RuleTypes})
}

func (s *Server) validateRule(ctx context.Context, rule *store.AlertRule) error {
	rule.Name = strings.TrimSpace(rule.Name)
	if l := len([]rune(rule.Name)); l < 1 || l > 80 {
		return errors.New("name must be 1-80 characters")
	}
	if _, ok := alerts.RuleTypes[rule.Type]; !ok {
		return errors.New("unknown rule type")
	}
	if rule.DurationS < 0 || rule.DurationS > 7*24*3600 {
		return errors.New("duration out of range")
	}
	if rule.Threshold < 0 || rule.Threshold > 1000 {
		return errors.New("threshold out of range")
	}
	if len(rule.Target) > 200 {
		return errors.New("target too long")
	}
	if rule.AgentID != "" {
		if _, err := s.store.Agent(ctx, rule.AgentID); err != nil {
			return errors.New("unknown host")
		}
	}
	if rule.Channels == nil {
		rule.Channels = []int64{}
	}
	for _, id := range rule.Channels {
		if _, err := s.store.AlertChannel(ctx, id); err != nil {
			return errors.New("unknown notification channel")
		}
	}
	return nil
}

func (s *Server) saveRule(w http.ResponseWriter, r *http.Request, id int64) {
	var rule store.AlertRule
	if !decodeJSON(w, r, &rule) {
		return
	}
	rule.ID = id
	if err := s.validateRule(r.Context(), &rule); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	err := s.store.SaveAlertRule(r.Context(), &rule)
	action := "alert.rule.create"
	if id != 0 {
		action = "alert.rule.update"
	}
	s.audit(r, action, rule.Name, rule.AgentID, err, rule.Type)
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "rule not found")
		return
	} else if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rule)
}

func idParam(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

func (s *Server) handleCreateRule(w http.ResponseWriter, r *http.Request) { s.saveRule(w, r, 0) }

func (s *Server) handleUpdateRule(w http.ResponseWriter, r *http.Request) {
	if id, ok := idParam(w, r, "rid"); ok {
		s.saveRule(w, r, id)
	}
}

func (s *Server) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "rid")
	if !ok {
		return
	}
	err := s.store.DeleteAlertRule(r.Context(), id)
	s.audit(r, "alert.rule.delete", strconv.FormatInt(id, 10), "", err, "")
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "rule not found")
		return
	} else if err != nil {
		internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- alert channels ----

type channelDTO struct {
	store.AlertChannel
	Config     alerts.ChannelConfig `json:"config"`
	SecretsSet []string             `json:"secrets_set"`
}

func (s *Server) channelDTO(ch store.AlertChannel) channelDTO {
	d := channelDTO{AlertChannel: ch, SecretsSet: []string{}}
	if cfg, err := s.alerts.ChannelConfig(&ch); err == nil {
		d.Config, d.SecretsSet = cfg.Masked()
		if d.SecretsSet == nil {
			d.SecretsSet = []string{}
		}
	}
	return d
}

func (s *Server) handleAlertChannels(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.AlertChannels(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	out := make([]channelDTO, 0, len(list))
	for _, ch := range list {
		out = append(out, s.channelDTO(ch))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) saveChannel(w http.ResponseWriter, r *http.Request, id int64) {
	var body struct {
		Name    string               `json:"name"`
		Type    string               `json:"type"`
		Enabled bool                 `json:"enabled"`
		Config  alerts.ChannelConfig `json:"config"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if l := len([]rune(body.Name)); l < 1 || l > 80 {
		writeError(w, http.StatusBadRequest, "name must be 1-80 characters")
		return
	}
	ch := &store.AlertChannel{ID: id, Name: body.Name, Type: body.Type, Enabled: body.Enabled}
	if id != 0 {
		prev, err := s.store.AlertChannel(r.Context(), id)
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "channel not found")
			return
		} else if err != nil {
			internalError(w, err)
			return
		}
		if prev.Type == body.Type {
			if old, err := s.alerts.ChannelConfig(prev); err == nil {
				body.Config.MergeSecrets(old)
			}
		}
	}
	if err := body.Config.Validate(body.Type); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ch.Config = s.alerts.SealConfig(body.Config)
	err := s.store.SaveAlertChannel(r.Context(), ch)
	action := "alert.channel.create"
	if id != 0 {
		action = "alert.channel.update"
	}
	s.audit(r, action, ch.Name, "", err, ch.Type)
	if err != nil {
		internalError(w, err)
		return
	}
	saved, _ := s.store.AlertChannel(r.Context(), ch.ID)
	writeJSON(w, http.StatusOK, s.channelDTO(*saved))
}

func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request) { s.saveChannel(w, r, 0) }

func (s *Server) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	if id, ok := idParam(w, r, "cid"); ok {
		s.saveChannel(w, r, id)
	}
}

func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "cid")
	if !ok {
		return
	}
	err := s.store.DeleteAlertChannel(r.Context(), id)
	s.audit(r, "alert.channel.delete", strconv.FormatInt(id, 10), "", err, "")
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	} else if err != nil {
		internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r, "cid")
	if !ok {
		return
	}
	ch, err := s.store.AlertChannel(r.Context(), id)
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	} else if err != nil {
		internalError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	err = s.alerts.Test(ctx, ch)
	s.audit(r, "alert.channel.test", ch.Name, "", err, ch.Type)
	if err != nil {
		writeError(w, http.StatusBadGateway, "sending failed: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
