package docker

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	nexosv1 "nexos/internal/gen/nexos/v1"
)

// ProtectedLabel marks NexOS's own containers. Stopping or removing them
// from the UI would cut the management channel, so such actions are refused.
const ProtectedLabel = "nexos.role"

var (
	ErrProtected = errors.New("this container is part of NexOS and cannot be modified from the UI")
	ErrInvalidID = errors.New("invalid container id")

	idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
)

// ValidID reports whether id looks like a container ID or name.
func ValidID(id string) bool { return idPattern.MatchString(id) }

func toProto(s container.Summary) *nexosv1.Container {
	c := &nexosv1.Container{
		Id:             s.ID,
		Image:          s.Image,
		ImageId:        s.ImageID,
		Command:        s.Command,
		CreatedUnix:    s.Created,
		State:          string(s.State),
		Status:         s.Status,
		Labels:         s.Labels,
		ComposeProject: s.Labels["com.docker.compose.project"],
		ComposeService: s.Labels["com.docker.compose.service"],
	}
	_, c.Protected = s.Labels[ProtectedLabel]
	if len(s.Names) > 0 {
		c.Name = strings.TrimPrefix(s.Names[0], "/")
	}
	if s.Health != nil {
		c.Health = string(s.Health.Status)
	}
	for _, p := range s.Ports {
		pp := &nexosv1.Port{PrivatePort: uint32(p.PrivatePort), PublicPort: uint32(p.PublicPort), Type: p.Type}
		if p.IP.IsValid() {
			pp.Ip = p.IP.String()
		}
		c.Ports = append(c.Ports, pp)
	}
	if s.NetworkSettings != nil {
		for name := range s.NetworkSettings.Networks {
			c.Networks = append(c.Networks, name)
		}
		sort.Strings(c.Networks)
	}
	for _, m := range s.Mounts {
		c.Mounts = append(c.Mounts, &nexosv1.Mount{
			Type: string(m.Type), Source: m.Source, Destination: m.Destination, ReadOnly: !m.RW, Name: m.Name,
		})
	}
	return c
}

func (c *Client) ListContainers(ctx context.Context, all bool) ([]*nexosv1.Container, error) {
	res, err := c.api.ContainerList(ctx, client.ContainerListOptions{All: all})
	if err != nil {
		return nil, err
	}
	out := make([]*nexosv1.Container, 0, len(res.Items))
	for _, s := range res.Items {
		out = append(out, toProto(s))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ComposeProject != out[j].ComposeProject {
			return out[i].ComposeProject < out[j].ComposeProject
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (c *Client) Inspect(ctx context.Context, id string) (container.InspectResponse, []byte, error) {
	if !ValidID(id) {
		return container.InspectResponse{}, nil, ErrInvalidID
	}
	res, err := c.api.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return container.InspectResponse{}, nil, err
	}
	return res.Container, res.Raw, nil
}

func (c *Client) checkMutable(ctx context.Context, id string) error {
	info, _, err := c.Inspect(ctx, id)
	if err != nil {
		return err
	}
	if info.Config != nil {
		if _, ok := info.Config.Labels[ProtectedLabel]; ok {
			return ErrProtected
		}
	}
	return nil
}

func (c *Client) Action(ctx context.Context, id string, action nexosv1.ContainerActionType, timeoutSec int32) error {
	if !ValidID(id) {
		return ErrInvalidID
	}
	// Starting is always harmless; everything else is refused for NexOS itself.
	if action != nexosv1.ContainerActionType_CONTAINER_ACTION_TYPE_START {
		if err := c.checkMutable(ctx, id); err != nil {
			return err
		}
	}
	var timeout *int
	if timeoutSec > 0 {
		t := int(timeoutSec)
		timeout = &t
	}
	var err error
	switch action {
	case nexosv1.ContainerActionType_CONTAINER_ACTION_TYPE_START:
		_, err = c.api.ContainerStart(ctx, id, client.ContainerStartOptions{})
	case nexosv1.ContainerActionType_CONTAINER_ACTION_TYPE_STOP:
		_, err = c.api.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: timeout})
	case nexosv1.ContainerActionType_CONTAINER_ACTION_TYPE_RESTART:
		_, err = c.api.ContainerRestart(ctx, id, client.ContainerRestartOptions{Timeout: timeout})
	case nexosv1.ContainerActionType_CONTAINER_ACTION_TYPE_KILL:
		_, err = c.api.ContainerKill(ctx, id, client.ContainerKillOptions{})
	case nexosv1.ContainerActionType_CONTAINER_ACTION_TYPE_PAUSE:
		_, err = c.api.ContainerPause(ctx, id, client.ContainerPauseOptions{})
	case nexosv1.ContainerActionType_CONTAINER_ACTION_TYPE_UNPAUSE:
		_, err = c.api.ContainerUnpause(ctx, id, client.ContainerUnpauseOptions{})
	default:
		return errors.New("unknown action")
	}
	return err
}

func (c *Client) Remove(ctx context.Context, id string, force, volumes bool) error {
	if !ValidID(id) {
		return ErrInvalidID
	}
	if err := c.checkMutable(ctx, id); err != nil {
		return err
	}
	_, err := c.api.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: force, RemoveVolumes: volumes})
	return err
}

// Events forwards Docker engine events until ctx ends or the stream fails.
func (c *Client) Events(ctx context.Context, fn func(*nexosv1.DockerEvent) error) error {
	res := c.api.Events(ctx, client.EventsListOptions{
		Since: time.Now().Format(time.RFC3339Nano),
	})
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-res.Err:
			return err
		case m := <-res.Messages:
			ev := &nexosv1.DockerEvent{
				Type:         string(m.Type),
				Action:       string(m.Action),
				ActorId:      m.Actor.ID,
				Attributes:   m.Actor.Attributes,
				TimeUnixNano: m.TimeNano,
			}
			if err := fn(ev); err != nil {
				return err
			}
		}
	}
}
