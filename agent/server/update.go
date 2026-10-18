package server

import (
	"errors"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"nexos/agent/docker"
	nexosv1 "nexos/internal/gen/nexos/v1"
)

type updateService struct {
	nexosv1.UnimplementedUpdateServiceServer
	s *Server
}

func (x *updateService) UpdateComponent(req *nexosv1.UpdateComponentRequest, stream grpc.ServerStreamingServer[nexosv1.UpdateComponentResponse]) error {
	if err := x.s.requireDocker(); err != nil {
		return err
	}
	ctx := stream.Context()
	err := x.s.docker.UpdateComponent(ctx, req.GetRole(), req.GetImage(), req.GetRegistryAuth(), func(line string) error {
		return stream.Send(&nexosv1.UpdateComponentResponse{Line: line})
	})
	slog.Info("component update", "role", req.GetRole(), "image", req.GetImage(), "peer", peerName(ctx), "err", err)
	if errors.Is(err, docker.ErrUpdate) {
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	if err != nil {
		return dockerErr(err)
	}
	return stream.Send(&nexosv1.UpdateComponentResponse{Done: true})
}
