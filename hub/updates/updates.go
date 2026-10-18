// Package updates periodically asks agents whether the images their
// containers use have newer versions in the registry.
package updates

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"nexos/hub/agents"
	"nexos/hub/events"
	"nexos/hub/store"
	nexosv1 "nexos/internal/gen/nexos/v1"
)

type Checker struct {
	store    *store.Store
	agents   *agents.Manager
	bus      *events.Bus
	interval time.Duration

	mu      sync.Mutex
	running map[string]bool
}

func NewChecker(st *store.Store, mgr *agents.Manager, bus *events.Bus, interval time.Duration) *Checker {
	return &Checker{store: st, agents: mgr, bus: bus, interval: interval, running: map[string]bool{}}
}

// Run checks all online agents every interval. A zero interval disables
// scheduled checks (manual checks still work).
func (c *Checker) Run(ctx context.Context) {
	if c.interval <= 0 {
		return
	}
	// Give agents time to connect after a hub restart.
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Minute):
	}
	for {
		list, err := c.store.Agents(ctx)
		if err == nil {
			for _, a := range list {
				if c.agents.State(a.ID).Status != agents.StatusOnline {
					continue
				}
				if _, err := c.Check(ctx, a.ID); err != nil {
					slog.Warn("image update check failed", "agent", a.ID, "err", err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.interval):
		}
	}
}

// Check runs a full update check for one agent and stores the result.
func (c *Checker) Check(ctx context.Context, agentID string) ([]store.ImageUpdate, error) {
	c.mu.Lock()
	if c.running[agentID] {
		c.mu.Unlock()
		return c.store.ImageUpdates(ctx, agentID)
	}
	c.running[agentID] = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.running, agentID)
		c.mu.Unlock()
	}()

	cc, err := c.agents.Client(agentID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	resp, err := nexosv1.NewImageServiceClient(cc).CheckImageUpdates(ctx, &nexosv1.CheckImageUpdatesRequest{})
	if err != nil {
		return nil, err
	}
	out := make([]store.ImageUpdate, 0, len(resp.GetUpdates()))
	for _, u := range resp.GetUpdates() {
		out = append(out, store.ImageUpdate{
			AgentID: agentID, Reference: u.GetReference(), Display: u.GetDisplay(), LocalImageID: u.GetLocalImageId(),
			RemoteDigest: u.GetRemoteDigest(), UpdateAvailable: u.GetUpdateAvailable(), Error: u.GetError(),
			Containers: u.GetContainers(), CheckedAt: time.Unix(u.GetCheckedUnix(), 0),
		})
	}
	if err := c.store.ReplaceImageUpdates(ctx, agentID, out); err != nil {
		return nil, err
	}
	n := 0
	for _, u := range out {
		if u.UpdateAvailable {
			n++
		}
	}
	slog.Info("image updates checked", "agent", agentID, "images", len(out), "updates", n)
	c.bus.Publish(events.Event{Type: "updates.checked", HostID: agentID, Data: map[string]int{"images": len(out), "updates": n}})
	return out, nil
}
