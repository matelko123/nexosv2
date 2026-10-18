package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"nexos/hub/agents"
	"nexos/hub/auth"
	"nexos/hub/events"
	"nexos/hub/store"
	nexosv1 "nexos/internal/gen/nexos/v1"
)

// RuleTypes lists supported rule types with their threshold unit.
var RuleTypes = map[string]string{
	"host_offline":        "",
	"host_cpu":            "%",
	"host_memory":         "%",
	"host_disk":           "%",
	"host_temperature":    "°C",
	"container_unhealthy": "",
	"image_update":        "",
}

type key struct {
	rule    int64
	agent   string
	subject string
}

type state struct {
	since   time.Time
	firing  bool
	eventID int64
}

// observation is the result of evaluating a rule for one subject.
type observation struct {
	active  bool
	value   float64
	message string
}

type Engine struct {
	store    *store.Store
	agents   *agents.Manager
	bus      *events.Bus
	sealer   *auth.Sealer
	interval time.Duration

	mu     sync.Mutex
	states map[key]*state
}

func NewEngine(st *store.Store, mgr *agents.Manager, bus *events.Bus, sealer *auth.Sealer) *Engine {
	return &Engine{store: st, agents: mgr, bus: bus, sealer: sealer, interval: 15 * time.Second, states: map[key]*state{}}
}

// Run restores open alerts and evaluates rules until ctx ends.
func (e *Engine) Run(ctx context.Context) {
	if open, err := e.store.AlertEvents(ctx, true, 10000); err == nil {
		e.mu.Lock()
		for _, ev := range open {
			e.states[key{ev.RuleID, ev.AgentID, ev.Subject}] = &state{since: ev.StartedAt, firing: true, eventID: ev.ID}
		}
		e.mu.Unlock()
	}
	// Let agents reconnect after a restart before judging them offline.
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
	}
	t := time.NewTicker(e.interval)
	defer t.Stop()
	for {
		if err := e.evaluate(ctx, time.Now()); err != nil && ctx.Err() == nil {
			slog.Warn("alert evaluation failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (e *Engine) evaluate(ctx context.Context, now time.Time) error {
	rules, err := e.store.AlertRules(ctx)
	if err != nil {
		return err
	}
	hosts, err := e.store.Agents(ctx)
	if err != nil {
		return err
	}
	var updates map[string][]store.ImageUpdate
	containers := map[string][]*nexosv1.Container{}
	known := map[int64]bool{}

	for _, r := range rules {
		known[r.ID] = true
		if !r.Enabled {
			e.resolveRule(ctx, r, now, "")
			continue
		}
		for _, h := range hosts {
			if h.EnrolledAt == nil || (r.AgentID != "" && r.AgentID != h.ID) {
				continue
			}
			var obs map[string]observation
			var ok bool
			switch r.Type {
			case "container_unhealthy":
				list, have := containers[h.ID]
				if !have {
					list = e.listContainers(ctx, h.ID)
					containers[h.ID] = list
				}
				obs, ok = evalContainers(r, list)
			case "image_update":
				if updates == nil {
					updates = map[string][]store.ImageUpdate{}
					all, err := e.store.ImageUpdates(ctx, "")
					if err != nil {
						return err
					}
					for _, u := range all {
						updates[u.AgentID] = append(updates[u.AgentID], u)
					}
				}
				obs, ok = evalUpdates(r, updates[h.ID]), true
			default:
				obs, ok = evalHost(r, e.agents.State(h.ID))
			}
			if !ok {
				continue // no data (e.g. host offline): keep current states
			}
			e.apply(ctx, r, h, obs, now)
		}
	}
	// Forget states of deleted rules (their events were deleted with them).
	e.mu.Lock()
	for k := range e.states {
		if !known[k.rule] {
			delete(e.states, k)
		}
	}
	e.mu.Unlock()
	return nil
}

func (e *Engine) listContainers(ctx context.Context, agentID string) []*nexosv1.Container {
	cc, err := e.agents.Client(agentID)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := nexosv1.NewContainerServiceClient(cc).ListContainers(ctx, &nexosv1.ListContainersRequest{All: true})
	if err != nil {
		return nil
	}
	return resp.GetContainers()
}

func evalHost(r store.AlertRule, st agents.State) (map[string]observation, bool) {
	if r.Type == "host_offline" {
		if st.Status == agents.StatusPending {
			return nil, false
		}
		down := st.Status != agents.StatusOnline
		msg := "Host nie odpowiada"
		if st.Error != "" {
			msg += ": " + st.Error
		}
		return map[string]observation{"": {active: down, message: msg}}, true
	}
	m := st.Metrics
	if st.Status != agents.StatusOnline || m == nil {
		return nil, false
	}
	switch r.Type {
	case "host_cpu":
		v := m.GetCpuPercent()
		return map[string]observation{"": {active: v >= r.Threshold, value: v, message: fmt.Sprintf("Użycie CPU %.0f%% (próg %.0f%%)", v, r.Threshold)}}, true
	case "host_memory":
		if m.GetMemoryTotalBytes() == 0 {
			return nil, false
		}
		v := float64(m.GetMemoryUsedBytes()) / float64(m.GetMemoryTotalBytes()) * 100
		return map[string]observation{"": {active: v >= r.Threshold, value: v, message: fmt.Sprintf("Użycie pamięci %.0f%% (próg %.0f%%)", v, r.Threshold)}}, true
	case "host_disk":
		out := map[string]observation{}
		for _, d := range m.GetDisks() {
			if r.Target != "" && r.Target != d.GetMountpoint() {
				continue
			}
			v := d.GetUsedPercent()
			out[d.GetMountpoint()] = observation{active: v >= r.Threshold, value: v,
				message: fmt.Sprintf("Dysk %s zajęty w %.0f%% (próg %.0f%%), wolne %s", d.GetMountpoint(), v, r.Threshold, humanBytes(d.GetFreeBytes()))}
		}
		return out, true
	case "host_temperature":
		maxT, sensor := 0.0, ""
		for _, t := range m.GetTemperatures() {
			if t.GetCelsius() > maxT {
				maxT, sensor = t.GetCelsius(), t.GetSensor()
			}
		}
		if sensor == "" {
			return nil, false
		}
		return map[string]observation{"": {active: maxT >= r.Threshold, value: maxT, message: fmt.Sprintf("Temperatura %s: %.0f°C (próg %.0f°C)", sensor, maxT, r.Threshold)}}, true
	}
	return nil, false
}

var exitCodeRe = regexp.MustCompile(`^Exited \((\d+)\)`)

// containerProblem describes why a container needs attention, or "".
// Exit codes 0, 137 and 143 follow a deliberate stop and are not problems.
func containerProblem(c *nexosv1.Container) string {
	switch {
	case c.GetHealth() == "unhealthy":
		return "healthcheck: unhealthy"
	case c.GetState() == "restarting":
		return "restartuje się w pętli"
	case c.GetState() == "dead":
		return "stan: dead"
	case c.GetState() == "exited":
		if m := exitCodeRe.FindStringSubmatch(c.GetStatus()); m != nil {
			if code, _ := strconv.Atoi(m[1]); code != 0 && code != 137 && code != 143 {
				return "zakończony z kodem " + m[1]
			}
		}
	}
	return ""
}

func matchTarget(glob, name string) bool {
	if glob == "" || glob == "*" {
		return true
	}
	ok, err := path.Match(glob, name)
	return err == nil && ok
}

func evalContainers(r store.AlertRule, list []*nexosv1.Container) (map[string]observation, bool) {
	if list == nil {
		return nil, false
	}
	out := map[string]observation{}
	for _, c := range list {
		if !matchTarget(r.Target, c.GetName()) {
			continue
		}
		p := containerProblem(c)
		out[c.GetName()] = observation{active: p != "", message: fmt.Sprintf("Kontener %s: %s (%s)", c.GetName(), p, c.GetStatus())}
	}
	return out, true
}

func evalUpdates(r store.AlertRule, list []store.ImageUpdate) map[string]observation {
	out := map[string]observation{}
	for _, u := range list {
		match := false
		for _, c := range u.Containers {
			if matchTarget(r.Target, c) {
				match = true
				break
			}
		}
		if !match {
			continue
		}
		out[u.Display] = observation{active: u.UpdateAvailable, message: fmt.Sprintf("Nowa wersja obrazu %s (używają: %s)", u.Display, joinMax(u.Containers, 5))}
	}
	return out
}

// apply advances the state machine for every subject of a rule on a host.
func (e *Engine) apply(ctx context.Context, r store.AlertRule, h *store.Agent, obs map[string]observation, now time.Time) {
	e.mu.Lock()
	// Subjects that disappeared (container removed, disk unmounted) resolve.
	for k := range e.states {
		if k.rule == r.ID && k.agent == h.ID {
			if _, ok := obs[k.subject]; !ok {
				obs[k.subject] = observation{active: false, message: "obiekt zniknął"}
			}
		}
	}
	type change struct {
		fire bool
		k    key
		o    observation
		st   *state
	}
	var changes []change
	for subject, o := range obs {
		k := key{r.ID, h.ID, subject}
		st := e.states[k]
		switch {
		case o.active && st == nil:
			st = &state{since: now}
			e.states[k] = st
			if r.DurationS == 0 {
				changes = append(changes, change{true, k, o, st})
			}
		case o.active && !st.firing && now.Sub(st.since) >= time.Duration(r.DurationS)*time.Second:
			changes = append(changes, change{true, k, o, st})
		case !o.active && st != nil:
			delete(e.states, k)
			if st.firing {
				changes = append(changes, change{false, k, o, st})
			}
		}
	}
	for _, ch := range changes {
		if ch.fire {
			ch.st.firing = true
		}
	}
	e.mu.Unlock()

	for _, ch := range changes {
		n := Notification{Resolved: !ch.fire, Rule: r.Name, RuleType: r.Type, Host: h.Name, Subject: ch.k.subject, Message: ch.o.message, Value: round1(ch.o.value), Time: now}
		if ch.fire {
			ev := &store.AlertEvent{RuleID: r.ID, AgentID: h.ID, Subject: ch.k.subject, Value: n.Value, Message: n.Message, StartedAt: ch.st.since}
			if err := e.store.OpenAlertEvent(ctx, ev); err != nil {
				slog.Error("open alert event", "err", err)
				continue
			}
			e.mu.Lock()
			ch.st.eventID = ev.ID
			e.mu.Unlock()
			slog.Info("alert firing", "rule", r.Name, "host", h.Name, "subject", ch.k.subject, "message", n.Message)
			e.bus.Publish(events.Event{Type: "alert.fired", HostID: h.ID, Data: ev})
		} else {
			if err := e.store.ResolveAlertEvent(ctx, ch.st.eventID, now); err != nil {
				slog.Error("resolve alert event", "err", err)
			}
			slog.Info("alert resolved", "rule", r.Name, "host", h.Name, "subject", ch.k.subject)
			e.bus.Publish(events.Event{Type: "alert.resolved", HostID: h.ID, Data: map[string]any{"id": ch.st.eventID}})
			if r.Type == "image_update" {
				continue // "image is up to date again" is not worth a message
			}
		}
		e.notify(ctx, r.Channels, n)
	}
}

// resolveRule resolves all firing alerts of a (now disabled) rule.
func (e *Engine) resolveRule(ctx context.Context, r store.AlertRule, now time.Time, _ string) {
	e.mu.Lock()
	var ids []int64
	for k, st := range e.states {
		if k.rule == r.ID {
			if st.firing {
				ids = append(ids, st.eventID)
			}
			delete(e.states, k)
		}
	}
	e.mu.Unlock()
	for _, id := range ids {
		_ = e.store.ResolveAlertEvent(ctx, id, now)
		e.bus.Publish(events.Event{Type: "alert.resolved", Data: map[string]any{"id": id}})
	}
}

// notify sends n to the given channels in the background.
func (e *Engine) notify(ctx context.Context, channelIDs []int64, n Notification) {
	for _, id := range channelIDs {
		ch, err := e.store.AlertChannel(ctx, id)
		if err != nil || !ch.Enabled {
			continue
		}
		cfg, err := e.channelConfig(ch)
		if err != nil {
			slog.Error("alert channel config", "channel", ch.Name, "err", err)
			continue
		}
		go func() {
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if err := Send(sctx, ch.Type, cfg, n); err != nil {
				slog.Warn("alert notification failed", "channel", ch.Name, "type", ch.Type, "err", err)
				_ = e.store.Audit(sctx, store.AuditEntry{Action: "alert.notify", Target: ch.Name, Success: false, Detail: err.Error()})
			}
		}()
	}
}

func (e *Engine) channelConfig(ch *store.AlertChannel) (ChannelConfig, error) {
	var cfg ChannelConfig
	raw, err := e.sealer.Open(ch.Config)
	if err != nil {
		return cfg, err
	}
	err = json.Unmarshal([]byte(raw), &cfg)
	return cfg, err
}

// SealConfig encrypts a channel configuration for storage.
func (e *Engine) SealConfig(cfg ChannelConfig) string {
	raw, _ := json.Marshal(cfg)
	return e.sealer.Seal(string(raw))
}

// ChannelConfig decrypts a stored channel configuration.
func (e *Engine) ChannelConfig(ch *store.AlertChannel) (ChannelConfig, error) {
	return e.channelConfig(ch)
}

// Test sends a test notification synchronously.
func (e *Engine) Test(ctx context.Context, ch *store.AlertChannel) error {
	cfg, err := e.channelConfig(ch)
	if err != nil {
		return err
	}
	return Send(ctx, ch.Type, cfg, Notification{
		Rule: "Test powiadomienia", RuleType: "test", Host: "NexOS",
		Message: "Jeśli to widzisz, kanał „" + ch.Name + "” działa.", Time: time.Now(),
	})
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func joinMax(list []string, n int) string {
	if len(list) <= n {
		return strings.Join(list, ", ")
	}
	return strings.Join(list[:n], ", ") + fmt.Sprintf(" +%d", len(list)-n)
}
