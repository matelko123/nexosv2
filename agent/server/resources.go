package server

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"nexos/agent/stacks"
	nexosv1 "nexos/internal/gen/nexos/v1"
)

// ---- images ----

type imageService struct {
	nexosv1.UnimplementedImageServiceServer
	s *Server
}

func (x *imageService) ListImages(ctx context.Context, _ *nexosv1.ListImagesRequest) (*nexosv1.ListImagesResponse, error) {
	if err := x.s.requireDocker(); err != nil {
		return nil, err
	}
	list, err := x.s.docker.ListImages(ctx)
	if err != nil {
		return nil, dockerErr(err)
	}
	return &nexosv1.ListImagesResponse{Images: list}, nil
}

func (x *imageService) InspectImage(ctx context.Context, req *nexosv1.InspectImageRequest) (*nexosv1.InspectImageResponse, error) {
	if err := x.s.requireDocker(); err != nil {
		return nil, err
	}
	raw, err := x.s.docker.InspectImage(ctx, req.GetId())
	if err != nil {
		return nil, dockerErr(err)
	}
	return &nexosv1.InspectImageResponse{Json: raw}, nil
}

func (x *imageService) PullImage(req *nexosv1.PullImageRequest, stream grpc.ServerStreamingServer[nexosv1.PullImageResponse]) error {
	if err := x.s.requireDocker(); err != nil {
		return err
	}
	err := x.s.docker.PullImage(stream.Context(), req.GetReference(), stream.Send)
	slog.Info("image pull", "ref", req.GetReference(), "peer", peerName(stream.Context()), "err", err)
	return dockerErr(err)
}

func (x *imageService) RemoveImage(ctx context.Context, req *nexosv1.RemoveImageRequest) (*nexosv1.RemoveImageResponse, error) {
	if err := x.s.requireDocker(); err != nil {
		return nil, err
	}
	resp, err := x.s.docker.RemoveImage(ctx, req.GetId(), req.GetForce())
	slog.Info("image remove", "id", req.GetId(), "force", req.GetForce(), "peer", peerName(ctx), "err", err)
	return resp, dockerErr(err)
}

func (x *imageService) PruneImages(ctx context.Context, req *nexosv1.PruneImagesRequest) (*nexosv1.PruneImagesResponse, error) {
	if err := x.s.requireDocker(); err != nil {
		return nil, err
	}
	res, err := x.s.docker.PruneImages(ctx, req.GetAll())
	slog.Info("image prune", "all", req.GetAll(), "peer", peerName(ctx), "err", err)
	if err != nil {
		return nil, dockerErr(err)
	}
	return &nexosv1.PruneImagesResponse{Result: res}, nil
}

// ---- networks ----

type networkService struct {
	nexosv1.UnimplementedNetworkServiceServer
	s *Server
}

func (x *networkService) ListNetworks(ctx context.Context, _ *nexosv1.ListNetworksRequest) (*nexosv1.ListNetworksResponse, error) {
	if err := x.s.requireDocker(); err != nil {
		return nil, err
	}
	list, err := x.s.docker.ListNetworks(ctx)
	if err != nil {
		return nil, dockerErr(err)
	}
	return &nexosv1.ListNetworksResponse{Networks: list}, nil
}

func (x *networkService) InspectNetwork(ctx context.Context, req *nexosv1.InspectNetworkRequest) (*nexosv1.InspectNetworkResponse, error) {
	if err := x.s.requireDocker(); err != nil {
		return nil, err
	}
	raw, err := x.s.docker.InspectNetwork(ctx, req.GetId())
	if err != nil {
		return nil, dockerErr(err)
	}
	return &nexosv1.InspectNetworkResponse{Json: raw}, nil
}

func (x *networkService) CreateNetwork(ctx context.Context, req *nexosv1.CreateNetworkRequest) (*nexosv1.CreateNetworkResponse, error) {
	if err := x.s.requireDocker(); err != nil {
		return nil, err
	}
	resp, err := x.s.docker.CreateNetwork(ctx, req)
	slog.Info("network create", "name", req.GetName(), "driver", req.GetDriver(), "peer", peerName(ctx), "err", err)
	return resp, dockerErr(err)
}

func (x *networkService) RemoveNetwork(ctx context.Context, req *nexosv1.RemoveNetworkRequest) (*nexosv1.RemoveNetworkResponse, error) {
	if err := x.s.requireDocker(); err != nil {
		return nil, err
	}
	err := x.s.docker.RemoveNetwork(ctx, req.GetId())
	slog.Info("network remove", "id", req.GetId(), "peer", peerName(ctx), "err", err)
	return &nexosv1.RemoveNetworkResponse{}, dockerErr(err)
}

func (x *networkService) ConnectNetwork(ctx context.Context, req *nexosv1.ConnectNetworkRequest) (*nexosv1.ConnectNetworkResponse, error) {
	if err := x.s.requireDocker(); err != nil {
		return nil, err
	}
	err := x.s.docker.ConnectNetwork(ctx, req)
	slog.Info("network connect", "network", req.GetNetworkId(), "container", req.GetContainerId(), "peer", peerName(ctx), "err", err)
	return &nexosv1.ConnectNetworkResponse{}, dockerErr(err)
}

func (x *networkService) DisconnectNetwork(ctx context.Context, req *nexosv1.DisconnectNetworkRequest) (*nexosv1.DisconnectNetworkResponse, error) {
	if err := x.s.requireDocker(); err != nil {
		return nil, err
	}
	err := x.s.docker.DisconnectNetwork(ctx, req)
	slog.Info("network disconnect", "network", req.GetNetworkId(), "container", req.GetContainerId(), "peer", peerName(ctx), "err", err)
	return &nexosv1.DisconnectNetworkResponse{}, dockerErr(err)
}

func (x *networkService) PruneNetworks(ctx context.Context, _ *nexosv1.PruneNetworksRequest) (*nexosv1.PruneNetworksResponse, error) {
	if err := x.s.requireDocker(); err != nil {
		return nil, err
	}
	res, err := x.s.docker.PruneNetworks(ctx)
	slog.Info("network prune", "peer", peerName(ctx), "err", err)
	if err != nil {
		return nil, dockerErr(err)
	}
	return &nexosv1.PruneNetworksResponse{Result: res}, nil
}

// ---- volumes ----

type volumeService struct {
	nexosv1.UnimplementedVolumeServiceServer
	s *Server
}

func (x *volumeService) ListVolumes(ctx context.Context, _ *nexosv1.ListVolumesRequest) (*nexosv1.ListVolumesResponse, error) {
	if err := x.s.requireDocker(); err != nil {
		return nil, err
	}
	list, err := x.s.docker.ListVolumes(ctx)
	if err != nil {
		return nil, dockerErr(err)
	}
	return &nexosv1.ListVolumesResponse{Volumes: list}, nil
}

func (x *volumeService) InspectVolume(ctx context.Context, req *nexosv1.InspectVolumeRequest) (*nexosv1.InspectVolumeResponse, error) {
	if err := x.s.requireDocker(); err != nil {
		return nil, err
	}
	raw, err := x.s.docker.InspectVolume(ctx, req.GetName())
	if err != nil {
		return nil, dockerErr(err)
	}
	return &nexosv1.InspectVolumeResponse{Json: raw}, nil
}

func (x *volumeService) CreateVolume(ctx context.Context, req *nexosv1.CreateVolumeRequest) (*nexosv1.CreateVolumeResponse, error) {
	if err := x.s.requireDocker(); err != nil {
		return nil, err
	}
	v, err := x.s.docker.CreateVolume(ctx, req)
	slog.Info("volume create", "name", req.GetName(), "peer", peerName(ctx), "err", err)
	if err != nil {
		return nil, dockerErr(err)
	}
	return &nexosv1.CreateVolumeResponse{Volume: v}, nil
}

func (x *volumeService) RemoveVolume(ctx context.Context, req *nexosv1.RemoveVolumeRequest) (*nexosv1.RemoveVolumeResponse, error) {
	if err := x.s.requireDocker(); err != nil {
		return nil, err
	}
	err := x.s.docker.RemoveVolume(ctx, req.GetName(), req.GetForce())
	slog.Info("volume remove", "name", req.GetName(), "peer", peerName(ctx), "err", err)
	return &nexosv1.RemoveVolumeResponse{}, dockerErr(err)
}

func (x *volumeService) PruneVolumes(ctx context.Context, req *nexosv1.PruneVolumesRequest) (*nexosv1.PruneVolumesResponse, error) {
	if err := x.s.requireDocker(); err != nil {
		return nil, err
	}
	res, err := x.s.docker.PruneVolumes(ctx, req.GetAll())
	slog.Info("volume prune", "all", req.GetAll(), "peer", peerName(ctx), "err", err)
	if err != nil {
		return nil, dockerErr(err)
	}
	return &nexosv1.PruneVolumesResponse{Result: res}, nil
}

// ---- stacks ----

func stackErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, stacks.ErrInvalidName), errors.Is(err, stacks.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, stacks.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, stacks.ErrExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, stacks.ErrNotManaged), errors.Is(err, stacks.ErrProtected), errors.Is(err, stacks.ErrBusy):
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	return dockerErr(err)
}

type stackService struct {
	nexosv1.UnimplementedStackServiceServer
	s *Server
}

func (x *stackService) ready() error {
	if x.s.stacks == nil {
		return status.Error(codes.Unavailable, "stack management is not available on this host")
	}
	return nil
}

func (x *stackService) ListStacks(ctx context.Context, _ *nexosv1.ListStacksRequest) (*nexosv1.ListStacksResponse, error) {
	if err := x.ready(); err != nil {
		return nil, err
	}
	list, err := x.s.stacks.List(ctx)
	if err != nil {
		return nil, stackErr(err)
	}
	return &nexosv1.ListStacksResponse{Stacks: list}, nil
}

func (x *stackService) GetStack(ctx context.Context, req *nexosv1.GetStackRequest) (*nexosv1.GetStackResponse, error) {
	if err := x.ready(); err != nil {
		return nil, err
	}
	resp, err := x.s.stacks.Get(ctx, req.GetName(), req.GetIncludeEnv())
	return resp, stackErr(err)
}

func (x *stackService) SaveStack(ctx context.Context, req *nexosv1.SaveStackRequest) (*nexosv1.SaveStackResponse, error) {
	if err := x.ready(); err != nil {
		return nil, err
	}
	st, err := x.s.stacks.Save(ctx, req.GetName(), req.GetCompose(), req.GetEnv(), req.GetCreate())
	slog.Info("stack save", "name", req.GetName(), "create", req.GetCreate(), "peer", peerName(ctx), "err", err)
	if err != nil {
		return nil, stackErr(err)
	}
	return &nexosv1.SaveStackResponse{Stack: st}, nil
}

func (x *stackService) RunStack(req *nexosv1.RunStackRequest, stream grpc.ServerStreamingServer[nexosv1.RunStackResponse]) error {
	if err := x.ready(); err != nil {
		return err
	}
	send := func(o *nexosv1.StackOutput) error { return stream.Send(&nexosv1.RunStackResponse{Output: o}) }
	code, err := x.s.stacks.Run(stream.Context(), req, func(l string) error {
		return send(&nexosv1.StackOutput{Msg: &nexosv1.StackOutput_Line{Line: l}})
	})
	slog.Info("stack run", "name", req.GetName(), "op", req.GetOperation().String(), "exit", code, "peer", peerName(stream.Context()), "err", err)
	if err != nil {
		return stackErr(err)
	}
	return send(&nexosv1.StackOutput{Msg: &nexosv1.StackOutput_ExitCode{ExitCode: int32(code)}})
}

func (x *stackService) DeleteStack(req *nexosv1.DeleteStackRequest, stream grpc.ServerStreamingServer[nexosv1.DeleteStackResponse]) error {
	if err := x.ready(); err != nil {
		return err
	}
	send := func(o *nexosv1.StackOutput) error { return stream.Send(&nexosv1.DeleteStackResponse{Output: o}) }
	code, err := x.s.stacks.Delete(stream.Context(), req, func(l string) error {
		return send(&nexosv1.StackOutput{Msg: &nexosv1.StackOutput_Line{Line: l}})
	})
	slog.Info("stack delete", "name", req.GetName(), "down", req.GetDown(), "exit", code, "peer", peerName(stream.Context()), "err", err)
	if err != nil {
		return stackErr(err)
	}
	return send(&nexosv1.StackOutput{Msg: &nexosv1.StackOutput_ExitCode{ExitCode: int32(code)}})
}
