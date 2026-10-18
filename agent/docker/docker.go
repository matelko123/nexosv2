// Package docker wraps the Docker Engine API client used by the agent.
package docker

import (
	"context"
	"time"

	"github.com/moby/moby/client"

	nexosv1 "nexos/internal/gen/nexos/v1"
)

// Client is a thin wrapper around the Docker Engine API client. The
// connection is configured from DOCKER_HOST (default: local socket).
type Client struct {
	api *client.Client
}

func New() (*Client, error) {
	api, err := client.New(client.FromEnv)
	if err != nil {
		return nil, err
	}
	return &Client{api: api}, nil
}

func (c *Client) Close() error { return c.api.Close() }

// Info summarises the Docker daemon. Errors are reported in the result, not
// returned, so the agent stays useful on hosts where Docker is down.
func (c *Client) Info(ctx context.Context) *nexosv1.DockerInfo {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res, err := c.api.Info(ctx, client.InfoOptions{})
	if err != nil {
		return &nexosv1.DockerInfo{Error: err.Error()}
	}
	out := &nexosv1.DockerInfo{
		Available:         true,
		ServerVersion:     res.Info.ServerVersion,
		ContainersTotal:   uint32(res.Info.Containers),
		ContainersRunning: uint32(res.Info.ContainersRunning),
		ContainersPaused:  uint32(res.Info.ContainersPaused),
		ContainersStopped: uint32(res.Info.ContainersStopped),
		Images:            uint32(res.Info.Images),
		StorageDriver:     res.Info.Driver,
	}
	if v, err := c.api.ServerVersion(ctx, client.ServerVersionOptions{}); err == nil {
		out.ApiVersion = v.APIVersion
	}
	return out
}
