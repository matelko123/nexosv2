package docker

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/distribution/reference"
	"github.com/moby/moby/client"

	nexosv1 "nexos/internal/gen/nexos/v1"
)

const updateCheckConcurrency = 4

// CheckImageUpdates compares the digest of each local image tag with the
// digest the registry currently serves for that tag. The daemon performs
// the registry request, so its credentials (docker login) are used.
func (c *Client) CheckImageUpdates(ctx context.Context, refs []string) ([]*nexosv1.ImageUpdate, error) {
	type target struct {
		display    string
		containers []string
	}
	targets := map[string]*target{} // normalized ref -> target
	add := func(display, container string) {
		named, err := reference.ParseNormalizedNamed(display)
		if err != nil {
			return
		}
		if _, pinned := named.(reference.Digested); pinned {
			return // pinned by digest: nothing to update
		}
		key := reference.TagNameOnly(named).String()
		t, ok := targets[key]
		if !ok {
			t = &target{display: display}
			targets[key] = t
		}
		if container != "" {
			t.containers = append(t.containers, container)
		}
	}
	if len(refs) == 0 {
		list, err := c.ListContainers(ctx, true)
		if err != nil {
			return nil, err
		}
		for _, ct := range list {
			// Containers created from an image ID have no tag to follow.
			if strings.HasPrefix(ct.Image, "sha256:") || imageIDPattern.MatchString(ct.Image) {
				continue
			}
			add(ct.Image, ct.Name)
		}
	} else {
		for _, r := range refs {
			add(strings.TrimSpace(r), "")
		}
	}

	out := make([]*nexosv1.ImageUpdate, 0, len(targets))
	var mu sync.Mutex
	sem := make(chan struct{}, updateCheckConcurrency)
	var wg sync.WaitGroup
	for key, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			u := c.checkOne(ctx, key, t.display)
			sort.Strings(t.containers)
			u.Containers = t.containers
			mu.Lock()
			out = append(out, u)
			mu.Unlock()
		}()
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].Display < out[j].Display })
	return out, nil
}

func (c *Client) checkOne(ctx context.Context, ref, display string) *nexosv1.ImageUpdate {
	u := &nexosv1.ImageUpdate{Reference: ref, Display: display, CheckedUnix: time.Now().Unix()}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	local, err := c.api.ImageInspect(ctx, ref)
	if err != nil {
		u.Error = "image not present locally"
		return u
	}
	u.LocalImageId = local.ID
	if len(local.RepoDigests) == 0 {
		u.Error = "locally built image (no registry digest)"
		return u
	}
	remote, err := c.api.DistributionInspect(ctx, ref, client.DistributionInspectOptions{})
	if err != nil {
		u.Error = registryError(err)
		return u
	}
	u.RemoteDigest = remote.Descriptor.Digest.String()
	u.UpdateAvailable = true
	for _, d := range local.RepoDigests {
		if strings.HasSuffix(d, "@"+u.RemoteDigest) {
			u.UpdateAvailable = false
			break
		}
	}
	return u
}

func registryError(err error) string {
	msg := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "registry timeout"
	case strings.Contains(msg, "toomanyrequests"):
		return "registry rate limit"
	case strings.Contains(msg, "unauthorized"), strings.Contains(msg, "denied"), strings.Contains(msg, "not found"), strings.Contains(msg, "manifest unknown"):
		return "not found in registry: locally built or private image (for private registries run docker login on the host)"
	}
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}
