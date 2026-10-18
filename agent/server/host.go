package server

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"nexos/agent/host"
	nexosv1 "nexos/internal/gen/nexos/v1"
)

func hostErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, host.ErrDisabled), errors.Is(err, host.ErrProtectedUnit), errors.Is(err, host.ErrProtectedProcess):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, host.ErrInvalidUnit):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, host.ErrProcessChanged):
		return status.Error(codes.NotFound, err.Error())
	}
	return status.Error(codes.Internal, err.Error())
}

type hostService struct {
	nexosv1.UnimplementedHostServiceServer
	s *Server
}

func (x *hostService) ListProcesses(ctx context.Context, _ *nexosv1.ListProcessesRequest) (*nexosv1.ListProcessesResponse, error) {
	resp, err := x.s.host.ListProcesses(ctx)
	return resp, hostErr(err)
}

func (x *hostService) SignalProcess(ctx context.Context, req *nexosv1.SignalProcessRequest) (*nexosv1.SignalProcessResponse, error) {
	err := x.s.host.Signal(ctx, req.GetPid(), req.GetSignal(), req.GetStartedUnix())
	slog.Info("process signal", "pid", req.GetPid(), "signal", req.GetSignal().String(), "peer", peerName(ctx), "err", err)
	return &nexosv1.SignalProcessResponse{}, hostErr(err)
}

func (x *hostService) ListUnits(ctx context.Context, _ *nexosv1.ListUnitsRequest) (*nexosv1.ListUnitsResponse, error) {
	units, err := x.s.host.ListUnits(ctx)
	if err != nil {
		return nil, hostErr(err)
	}
	return &nexosv1.ListUnitsResponse{Units: units}, nil
}

func (x *hostService) UnitAction(ctx context.Context, req *nexosv1.UnitActionRequest) (*nexosv1.UnitActionResponse, error) {
	res, err := x.s.host.UnitAction(ctx, req.GetName(), req.GetAction())
	slog.Info("unit action", "unit", req.GetName(), "action", req.GetAction().String(), "result", res, "peer", peerName(ctx), "err", err)
	if err != nil {
		return nil, hostErr(err)
	}
	return &nexosv1.UnitActionResponse{Result: res}, nil
}

func (x *hostService) StreamJournal(req *nexosv1.StreamJournalRequest, stream grpc.ServerStreamingServer[nexosv1.StreamJournalResponse]) error {
	err := x.s.host.StreamJournal(stream.Context(), req.GetUnit(), req.GetLines(), req.GetFollow(), func(lines []string) error {
		return stream.Send(&nexosv1.StreamJournalResponse{Lines: lines})
	})
	return hostErr(err)
}

func (x *hostService) ListPackageUpdates(ctx context.Context, _ *nexosv1.ListPackageUpdatesRequest) (*nexosv1.ListPackageUpdatesResponse, error) {
	resp, err := x.s.host.ListPackageUpdates(ctx)
	return resp, hostErr(err)
}
