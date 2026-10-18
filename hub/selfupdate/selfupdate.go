// Package selfupdate checks GitHub for new NexOS releases. Installing an
// update is always an explicit admin action; nothing updates on its own.
package selfupdate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"nexos/hub/events"
	"nexos/internal/version"
)

type Config struct {
	// APIURL is the GitHub API base URL (GitHub Enterprise or tests).
	APIURL string
	// Repo is "owner/name" on GitHub.
	Repo string
	// Token is a GitHub token; needed for private repositories (scopes:
	// repo read for releases, read:packages for pulling images).
	Token string
	// ImagePrefix + role ("hub" / "agent") gives the image repository.
	ImagePrefix string
	Interval    time.Duration
}

type Release struct {
	Version     string    `json:"version"`
	Name        string    `json:"name"`
	Notes       string    `json:"notes"`
	URL         string    `json:"url"`
	PublishedAt time.Time `json:"published_at"`
}

type Status struct {
	HubVersion      string    `json:"hub_version"`
	Latest          *Release  `json:"latest,omitempty"`
	UpdateAvailable bool      `json:"update_available"`
	CheckedAt       time.Time `json:"checked_at,omitzero"`
	Error           string    `json:"error,omitempty"`
	Repo            string    `json:"repo"`
}

type Checker struct {
	cfg  Config
	bus  *events.Bus
	http *http.Client

	mu        sync.Mutex
	latest    *Release
	checkedAt time.Time
	err       string
}

func New(cfg Config, bus *events.Bus) *Checker {
	return &Checker{cfg: cfg, bus: bus, http: &http.Client{Timeout: 20 * time.Second}}
}

// Run checks shortly after start and then every interval (0 disables).
func (c *Checker) Run(ctx context.Context) {
	if c.cfg.Interval <= 0 || c.cfg.Repo == "" {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Minute):
	}
	for {
		if err := c.Check(ctx); err != nil {
			slog.Warn("release check failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.cfg.Interval):
		}
	}
}

// Check fetches the latest published (non-draft, non-prerelease) release.
func (c *Checker) Check(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.cfg.APIURL, "/")+"/repos/"+c.cfg.Repo+"/releases/latest", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "NexOS/"+version.Version)
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return c.fail(err.Error())
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusNotFound:
		// No release yet, or a private repository without a token.
		msg := "no published release found"
		if c.cfg.Token == "" {
			msg += " (private repository? set NEXOS_UPDATE_GITHUB_TOKEN)"
		}
		return c.fail(msg)
	case resp.StatusCode != http.StatusOK:
		return c.fail(fmt.Sprintf("GitHub API: %s", resp.Status))
	}
	var gh struct {
		TagName     string    `json:"tag_name"`
		Name        string    `json:"name"`
		Body        string    `json:"body"`
		HTMLURL     string    `json:"html_url"`
		PublishedAt time.Time `json:"published_at"`
	}
	if err := json.Unmarshal(body, &gh); err != nil {
		return c.fail("invalid GitHub response")
	}
	if !version.Valid(gh.TagName) {
		return c.fail("latest release tag is not a version: " + gh.TagName)
	}
	rel := &Release{Version: gh.TagName, Name: gh.Name, Notes: gh.Body, URL: gh.HTMLURL, PublishedAt: gh.PublishedAt}

	c.mu.Lock()
	prev := c.latest
	c.latest, c.checkedAt, c.err = rel, time.Now(), ""
	c.mu.Unlock()
	if (prev == nil || prev.Version != rel.Version) && version.Newer(rel.Version, version.Version) {
		slog.Info("new NexOS release available", "version", rel.Version, "current", version.Version)
		c.bus.Publish(events.Event{Type: "release.available", Data: rel})
	}
	return nil
}

func (c *Checker) fail(msg string) error {
	c.mu.Lock()
	c.checkedAt, c.err = time.Now(), msg
	c.mu.Unlock()
	return fmt.Errorf("%s", msg)
}

func (c *Checker) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Status{HubVersion: version.Version, Latest: c.latest, CheckedAt: c.checkedAt, Error: c.err, Repo: c.cfg.Repo}
	if c.latest != nil {
		s.UpdateAvailable = version.Newer(c.latest.Version, version.Version)
	}
	return s
}

// Image returns the image reference of a component at a version.
func (c *Checker) Image(role, ver string) string { return c.cfg.ImagePrefix + role + ":" + ver }

// RegistryAuth returns encoded credentials for pulling private images from
// ghcr.io with the configured token, or "" for public images.
func (c *Checker) RegistryAuth() string {
	if c.cfg.Token == "" || !strings.HasPrefix(c.cfg.ImagePrefix, "ghcr.io/") {
		return ""
	}
	raw, _ := json.Marshal(map[string]string{"username": "nexos", "password": c.cfg.Token, "serveraddress": "ghcr.io"})
	return base64.URLEncoding.EncodeToString(raw)
}
