package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/distribution/reference"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// DefaultUpdateImagePrefix is the official image repository; updates are
// only accepted from it unless NEXOS_UPDATE_IMAGE_PREFIX says otherwise.
const DefaultUpdateImagePrefix = "ghcr.io/matelko123/nexosv2-"

var ErrUpdate = errors.New("update refused")

func updateImagePrefix() string {
	if p := os.Getenv("NEXOS_UPDATE_IMAGE_PREFIX"); p != "" {
		return p
	}
	return DefaultUpdateImagePrefix
}

// ComposeTarget identifies the compose service a container belongs to.
type ComposeTarget struct {
	ContainerID string
	Image       string // image reference in the container config (what compose uses)
	Project     string
	WorkingDir  string
	ConfigFiles []string
	Service     string
}

// FindRole locates the single container labelled nexos.role=role and the
// compose service that manages it.
func (c *Client) FindRole(ctx context.Context, role string) (*ComposeTarget, error) {
	res, err := c.api.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: client.Filters{}.Add("label", ProtectedLabel+"="+role)})
	if err != nil {
		return nil, err
	}
	if len(res.Items) != 1 {
		return nil, fmt.Errorf("%w: expected one container with label %s=%s, found %d", ErrUpdate, ProtectedLabel, role, len(res.Items))
	}
	info, _, err := c.Inspect(ctx, res.Items[0].ID)
	if err != nil {
		return nil, err
	}
	l := info.Config.Labels
	t := &ComposeTarget{
		ContainerID: info.ID, Image: info.Config.Image,
		Project: l["com.docker.compose.project"], WorkingDir: l["com.docker.compose.project.working_dir"],
		Service: l["com.docker.compose.service"],
	}
	if f := l["com.docker.compose.project.config_files"]; f != "" {
		t.ConfigFiles = strings.Split(f, ",")
	}
	if t.Project == "" || t.Service == "" || t.WorkingDir == "" || len(t.ConfigFiles) == 0 {
		return nil, fmt.Errorf("%w: the %s container is not managed by docker compose", ErrUpdate, role)
	}
	return t, nil
}

// validateUpdateImage accepts only tagged images of the official repository
// for the given role, e.g. ghcr.io/matelko123/nexosv2-agent:v1.2.0.
func validateUpdateImage(image, role string) (reference.NamedTagged, error) {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid image reference", ErrUpdate)
	}
	tagged, ok := named.(reference.NamedTagged)
	if !ok {
		return nil, fmt.Errorf("%w: image must be tagged with a version", ErrUpdate)
	}
	if want := updateImagePrefix() + role; reference.FamiliarName(named) != want && named.Name() != want {
		return nil, fmt.Errorf("%w: image must come from %s", ErrUpdate, want)
	}
	return tagged, nil
}

// UpdateComponent deploys a new image for the hub or the agent itself.
func (c *Client) UpdateComponent(ctx context.Context, role, image, registryAuth string, progress func(string) error) error {
	if role != "hub" && role != "agent" {
		return fmt.Errorf("%w: unknown component", ErrUpdate)
	}
	ref, err := validateUpdateImage(image, role)
	if err != nil {
		return err
	}
	target, err := c.FindRole(ctx, role)
	if err != nil {
		return err
	}
	// The helper runs the agent image (it has the docker and compose CLIs).
	helperImage := ref.String()
	if role == "hub" {
		self, err := c.FindRole(ctx, "agent")
		if err != nil {
			return err
		}
		helperImage = self.Image
	}

	_ = progress("pobieranie " + ref.String())
	pull, err := c.api.ImagePull(ctx, ref.String(), client.ImagePullOptions{RegistryAuth: registryAuth})
	if err != nil {
		return err
	}
	last := ""
	for msg, err := range pull.JSONMessages(ctx) {
		if err != nil {
			pull.Close()
			return err
		}
		if msg.Error != nil {
			pull.Close()
			return errors.New(msg.Error.Message)
		}
		// Only whole-image status lines; per-layer progress is noise here.
		if msg.ID == "" && msg.Status != "" && msg.Status != last {
			last = msg.Status
			_ = progress(msg.Status)
		}
	}
	pull.Close()

	// Compose deploys the image named in the compose file; point that name
	// at the new version so `compose up` picks it up without pulling.
	if target.Image != ref.String() {
		if _, err := c.api.ImageTag(ctx, client.ImageTagOptions{Source: ref.String(), Target: target.Image}); err != nil {
			return fmt.Errorf("tag %s: %w", target.Image, err)
		}
		_ = progress("oznaczono " + ref.String() + " jako " + target.Image)
	}

	// Mount the compose directory (and directories of compose files outside
	// it) at the same paths, so relative paths resolve as on the host.
	dirs := map[string]bool{target.WorkingDir: true}
	for _, f := range target.ConfigFiles {
		if d := filepath.Dir(f); !strings.HasPrefix(d, target.WorkingDir) {
			dirs[d] = true
		}
	}
	mounts := []mount.Mount{{Type: mount.TypeBind, Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"}}
	for d := range dirs {
		mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: d, Target: d, ReadOnly: true})
	}
	args := []string{"update-helper", "--project", target.Project, "--workdir", target.WorkingDir, "--service", target.Service}
	for _, f := range target.ConfigFiles {
		args = append(args, "--file", f)
	}
	created, err := c.api.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: fmt.Sprintf("nexos-updater-%s-%d", role, time.Now().Unix()),
		Config: &container.Config{
			Image:  helperImage,
			Cmd:    args,
			Labels: map[string]string{ProtectedLabel: "updater"},
			Env:    []string{"DOCKER_CONFIG=/tmp/docker-config"},
		},
		HostConfig: &container.HostConfig{
			AutoRemove:     true,
			Mounts:         mounts,
			NetworkMode:    "none",
			ReadonlyRootfs: true,
			Tmpfs:          map[string]string{"/tmp": ""},
			SecurityOpt:    []string{"no-new-privileges:true"},
		},
	})
	if err != nil {
		return fmt.Errorf("create update helper: %w", err)
	}
	if _, err := c.api.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("start update helper: %w", err)
	}
	_ = progress(fmt.Sprintf("kontener pomocniczy uruchomiony, %s zostanie odtworzony za chwilę", target.Service))
	return nil
}
