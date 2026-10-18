package docker

import (
	"context"
	"errors"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/distribution/reference"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	nexosv1 "nexos/internal/gen/nexos/v1"
)

var (
	imageIDPattern     = regexp.MustCompile(`^(sha256:)?[a-f0-9]{12,64}$`)
	resourceNamePatten = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	anonVolumePattern  = regexp.MustCompile(`^[a-f0-9]{64}$`)
	builtinNetworks    = map[string]bool{"bridge": true, "host": true, "none": true}
)

// ---- images ----

func (c *Client) ListImages(ctx context.Context) ([]*nexosv1.Image, error) {
	res, err := c.api.ImageList(ctx, client.ImageListOptions{})
	if err != nil {
		return nil, err
	}
	// The daemon reports -1 for "containers" unless asked for expensive
	// disk usage data, so count users from the container list instead.
	users := map[string]int64{}
	if cl, err := c.api.ContainerList(ctx, client.ContainerListOptions{All: true}); err == nil {
		for _, ct := range cl.Items {
			users[ct.ImageID]++
		}
	}
	out := make([]*nexosv1.Image, 0, len(res.Items))
	for _, im := range res.Items {
		out = append(out, &nexosv1.Image{
			Id: im.ID, RepoTags: im.RepoTags, RepoDigests: im.RepoDigests,
			CreatedUnix: im.Created, SizeBytes: im.Size, Containers: users[im.ID], Labels: im.Labels,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedUnix > out[j].CreatedUnix })
	return out, nil
}

func (c *Client) InspectImage(ctx context.Context, id string) ([]byte, error) {
	if !imageIDPattern.MatchString(id) {
		return nil, ErrInvalidID
	}
	res, err := c.api.ImageInspect(ctx, id)
	if err != nil {
		return nil, err
	}
	return encodeJSON(res.InspectResponse)
}

// PullImage pulls ref and reports progress. Progress updates of a single
// layer are throttled; status changes are always delivered.
func (c *Client) PullImage(ctx context.Context, ref string, send func(*nexosv1.PullImageResponse) error) error {
	named, err := reference.ParseNormalizedNamed(strings.TrimSpace(ref))
	if err != nil {
		return errors.Join(ErrInvalidID, err)
	}
	named = reference.TagNameOnly(named)
	resp, err := c.api.ImagePull(ctx, named.String(), client.ImagePullOptions{})
	if err != nil {
		return err
	}
	defer resp.Close()
	type last struct {
		status string
		at     time.Time
	}
	seen := map[string]last{}
	for msg, err := range resp.JSONMessages(ctx) {
		if err != nil {
			return err
		}
		out := &nexosv1.PullImageResponse{Id: msg.ID, Status: msg.Status}
		if msg.Progress != nil {
			out.Current, out.Total = msg.Progress.Current, msg.Progress.Total
		}
		if msg.Error != nil {
			out.Error = msg.Error.Message
		}
		if prev, ok := seen[msg.ID]; ok && msg.ID != "" && prev.status == msg.Status && out.Error == "" && time.Since(prev.at) < 250*time.Millisecond {
			continue
		}
		seen[msg.ID] = last{status: msg.Status, at: time.Now()}
		if err := send(out); err != nil {
			return err
		}
		if out.Error != "" {
			return errors.New(out.Error)
		}
	}
	return nil
}

func (c *Client) RemoveImage(ctx context.Context, id string, force bool) (*nexosv1.RemoveImageResponse, error) {
	if !imageIDPattern.MatchString(id) {
		return nil, ErrInvalidID
	}
	res, err := c.api.ImageRemove(ctx, id, client.ImageRemoveOptions{Force: force, PruneChildren: true})
	if err != nil {
		return nil, err
	}
	out := &nexosv1.RemoveImageResponse{}
	for _, it := range res.Items {
		if it.Deleted != "" {
			out.Deleted = append(out.Deleted, it.Deleted)
		}
		if it.Untagged != "" {
			out.Untagged = append(out.Untagged, it.Untagged)
		}
	}
	return out, nil
}

func (c *Client) PruneImages(ctx context.Context, all bool) (*nexosv1.PruneResult, error) {
	f := client.Filters{}
	if all {
		f = f.Add("dangling", "false")
	} else {
		f = f.Add("dangling", "true")
	}
	res, err := c.api.ImagePrune(ctx, client.ImagePruneOptions{Filters: f})
	if err != nil {
		return nil, err
	}
	out := &nexosv1.PruneResult{SpaceReclaimedBytes: res.Report.SpaceReclaimed}
	for _, d := range res.Report.ImagesDeleted {
		if d.Deleted != "" {
			out.Deleted = append(out.Deleted, d.Deleted)
		}
	}
	return out, nil
}

// ---- networks ----

func (c *Client) ListNetworks(ctx context.Context) ([]*nexosv1.Network, error) {
	res, err := c.api.NetworkList(ctx, client.NetworkListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]*nexosv1.Network, 0, len(res.Items))
	for _, s := range res.Items {
		n := &nexosv1.Network{
			Id: s.ID, Name: s.Name, Driver: s.Driver, Scope: s.Scope, Internal: s.Internal, Attachable: s.Attachable,
			Ipv6: s.EnableIPv6, CreatedUnix: s.Created.Unix(), Labels: s.Labels,
			Builtin: builtinNetworks[s.Name], ComposeProject: s.Labels["com.docker.compose.project"],
		}
		for _, cfg := range s.IPAM.Config {
			if cfg.Subnet.IsValid() {
				n.Subnets = append(n.Subnets, cfg.Subnet.String())
			}
			if cfg.Gateway.IsValid() {
				n.Gateways = append(n.Gateways, cfg.Gateway.String())
			}
		}
		// The list endpoint omits endpoints; inspect each network for them.
		if ins, err := c.api.NetworkInspect(ctx, s.ID, client.NetworkInspectOptions{}); err == nil {
			for id, ep := range ins.Network.Containers {
				e := &nexosv1.NetworkEndpoint{ContainerId: id, ContainerName: ep.Name, Mac: ep.MacAddress.String()}
				if ep.IPv4Address.IsValid() {
					e.Ipv4 = ep.IPv4Address.String()
				}
				if ep.IPv6Address.IsValid() {
					e.Ipv6 = ep.IPv6Address.String()
				}
				n.Endpoints = append(n.Endpoints, e)
			}
			sort.Slice(n.Endpoints, func(i, j int) bool { return n.Endpoints[i].ContainerName < n.Endpoints[j].ContainerName })
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (c *Client) InspectNetwork(ctx context.Context, id string) ([]byte, error) {
	if !resourceNamePatten.MatchString(id) {
		return nil, ErrInvalidID
	}
	res, err := c.api.NetworkInspect(ctx, id, client.NetworkInspectOptions{})
	if err != nil {
		return nil, err
	}
	return res.Raw, nil
}

func (c *Client) CreateNetwork(ctx context.Context, req *nexosv1.CreateNetworkRequest) (*nexosv1.CreateNetworkResponse, error) {
	if !resourceNamePatten.MatchString(req.GetName()) {
		return nil, errors.Join(ErrInvalidID, errors.New("invalid network name"))
	}
	opts := client.NetworkCreateOptions{
		Driver: req.GetDriver(), Internal: req.GetInternal(), Attachable: req.GetAttachable(), Options: req.GetOptions(),
		Labels: map[string]string{"nexos.created": "true"},
	}
	if opts.Driver == "" {
		opts.Driver = "bridge"
	}
	if req.GetIpv6() {
		v := true
		opts.EnableIPv6 = &v
	}
	if req.GetSubnet() != "" {
		sub, err := netip.ParsePrefix(req.GetSubnet())
		if err != nil {
			return nil, errors.Join(ErrInvalidID, errors.New("invalid subnet"))
		}
		cfg := network.IPAMConfig{Subnet: sub}
		if req.GetGateway() != "" {
			gw, err := netip.ParseAddr(req.GetGateway())
			if err != nil || !sub.Contains(gw) {
				return nil, errors.Join(ErrInvalidID, errors.New("gateway must be an address inside the subnet"))
			}
			cfg.Gateway = gw
		}
		opts.IPAM = &network.IPAM{Config: []network.IPAMConfig{cfg}}
	}
	res, err := c.api.NetworkCreate(ctx, req.GetName(), opts)
	if err != nil {
		return nil, err
	}
	return &nexosv1.CreateNetworkResponse{Id: res.ID, Warnings: res.Warning}, nil
}

func (c *Client) RemoveNetwork(ctx context.Context, id string) error {
	if !resourceNamePatten.MatchString(id) || builtinNetworks[id] {
		return ErrInvalidID
	}
	_, err := c.api.NetworkRemove(ctx, id, client.NetworkRemoveOptions{})
	return err
}

func (c *Client) ConnectNetwork(ctx context.Context, req *nexosv1.ConnectNetworkRequest) error {
	if !resourceNamePatten.MatchString(req.GetNetworkId()) || !ValidID(req.GetContainerId()) {
		return ErrInvalidID
	}
	ep := &network.EndpointSettings{Aliases: req.GetAliases()}
	if req.GetIpv4() != "" {
		ip, err := netip.ParseAddr(req.GetIpv4())
		if err != nil || !ip.Is4() {
			return errors.Join(ErrInvalidID, errors.New("invalid IPv4 address"))
		}
		ep.IPAMConfig = &network.EndpointIPAMConfig{IPv4Address: ip}
	}
	_, err := c.api.NetworkConnect(ctx, req.GetNetworkId(), client.NetworkConnectOptions{Container: req.GetContainerId(), EndpointConfig: ep})
	return err
}

func (c *Client) DisconnectNetwork(ctx context.Context, req *nexosv1.DisconnectNetworkRequest) error {
	if !resourceNamePatten.MatchString(req.GetNetworkId()) || !ValidID(req.GetContainerId()) {
		return ErrInvalidID
	}
	if err := c.checkMutable(ctx, req.GetContainerId()); err != nil {
		return err
	}
	_, err := c.api.NetworkDisconnect(ctx, req.GetNetworkId(), client.NetworkDisconnectOptions{Container: req.GetContainerId(), Force: req.GetForce()})
	return err
}

func (c *Client) PruneNetworks(ctx context.Context) (*nexosv1.PruneResult, error) {
	res, err := c.api.NetworkPrune(ctx, client.NetworkPruneOptions{})
	if err != nil {
		return nil, err
	}
	return &nexosv1.PruneResult{Deleted: res.Report.NetworksDeleted}, nil
}

// ---- volumes ----

func (c *Client) ListVolumes(ctx context.Context) ([]*nexosv1.Volume, error) {
	res, err := c.api.VolumeList(ctx, client.VolumeListOptions{})
	if err != nil {
		return nil, err
	}
	users := map[string][]string{}
	if cl, err := c.api.ContainerList(ctx, client.ContainerListOptions{All: true}); err == nil {
		for _, ct := range cl.Items {
			name := strings.TrimPrefix(firstOr(ct.Names, ct.ID), "/")
			for _, m := range ct.Mounts {
				if m.Type == "volume" && m.Name != "" {
					users[m.Name] = append(users[m.Name], name)
				}
			}
		}
	}
	out := make([]*nexosv1.Volume, 0, len(res.Items))
	for _, v := range res.Items {
		vol := &nexosv1.Volume{
			Name: v.Name, Driver: v.Driver, Mountpoint: v.Mountpoint, Scope: v.Scope, Labels: v.Labels, Options: v.Options,
			UsedBy: users[v.Name], ComposeProject: v.Labels["com.docker.compose.project"],
			Anonymous: anonVolumePattern.MatchString(v.Name) || v.Labels["com.docker.volume.anonymous"] != "",
		}
		if t, err := time.Parse(time.RFC3339, v.CreatedAt); err == nil {
			vol.CreatedUnix = t.Unix()
		}
		sort.Strings(vol.UsedBy)
		out = append(out, vol)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Anonymous != out[j].Anonymous {
			return !out[i].Anonymous
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func firstOr(s []string, def string) string {
	if len(s) > 0 {
		return s[0]
	}
	return def
}

func (c *Client) InspectVolume(ctx context.Context, name string) ([]byte, error) {
	if !resourceNamePatten.MatchString(name) {
		return nil, ErrInvalidID
	}
	res, err := c.api.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if err != nil {
		return nil, err
	}
	return res.Raw, nil
}

func (c *Client) CreateVolume(ctx context.Context, req *nexosv1.CreateVolumeRequest) (*nexosv1.Volume, error) {
	if req.GetName() != "" && !resourceNamePatten.MatchString(req.GetName()) {
		return nil, errors.Join(ErrInvalidID, errors.New("invalid volume name"))
	}
	labels := map[string]string{"nexos.created": "true"}
	for k, v := range req.GetLabels() {
		labels[k] = v
	}
	res, err := c.api.VolumeCreate(ctx, client.VolumeCreateOptions{
		Name: req.GetName(), Driver: req.GetDriver(), DriverOpts: req.GetDriverOptions(), Labels: labels,
	})
	if err != nil {
		return nil, err
	}
	v := res.Volume
	return &nexosv1.Volume{Name: v.Name, Driver: v.Driver, Mountpoint: v.Mountpoint, Scope: v.Scope, Labels: v.Labels}, nil
}

func (c *Client) RemoveVolume(ctx context.Context, name string, force bool) error {
	if !resourceNamePatten.MatchString(name) {
		return ErrInvalidID
	}
	_, err := c.api.VolumeRemove(ctx, name, client.VolumeRemoveOptions{Force: force})
	return err
}

func (c *Client) PruneVolumes(ctx context.Context, all bool) (*nexosv1.PruneResult, error) {
	res, err := c.api.VolumePrune(ctx, client.VolumePruneOptions{All: all})
	if err != nil {
		return nil, err
	}
	return &nexosv1.PruneResult{Deleted: res.Report.VolumesDeleted, SpaceReclaimedBytes: res.Report.SpaceReclaimed}, nil
}
