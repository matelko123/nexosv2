package server

import (
	"context"
	"errors"
	"log/slog"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"nexos/agent/docker"
	nexosv1 "nexos/internal/gen/nexos/v1"
)

// dockerErr maps Docker client errors to gRPC status codes.
func dockerErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, docker.ErrInvalidID):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, docker.ErrProtected):
		return status.Error(codes.FailedPrecondition, err.Error())
	case cerrdefs.IsNotFound(err):
		return status.Error(codes.NotFound, err.Error())
	case cerrdefs.IsConflict(err), cerrdefs.IsNotModified(err):
		return status.Error(codes.FailedPrecondition, err.Error())
	case cerrdefs.IsInvalidArgument(err):
		return status.Error(codes.InvalidArgument, err.Error())
	}
	return status.Error(codes.Internal, err.Error())
}

func (s *Server) requireDocker() error {
	if s.docker == nil {
		return status.Error(codes.Unavailable, "docker is not available on this host")
	}
	return nil
}

type containerService struct {
	nexosv1.UnimplementedContainerServiceServer
	s *Server
}

func (x *containerService) ListContainers(ctx context.Context, req *nexosv1.ListContainersRequest) (*nexosv1.ListContainersResponse, error) {
	if err := x.s.requireDocker(); err != nil {
		return nil, err
	}
	list, err := x.s.docker.ListContainers(ctx, req.GetAll())
	if err != nil {
		return nil, dockerErr(err)
	}
	return &nexosv1.ListContainersResponse{Containers: list}, nil
}

func (x *containerService) InspectContainer(ctx context.Context, req *nexosv1.InspectContainerRequest) (*nexosv1.InspectContainerResponse, error) {
	if err := x.s.requireDocker(); err != nil {
		return nil, err
	}
	_, raw, err := x.s.docker.Inspect(ctx, req.GetId())
	if err != nil {
		return nil, dockerErr(err)
	}
	return &nexosv1.InspectContainerResponse{Json: raw}, nil
}

func (x *containerService) ContainerAction(ctx context.Context, req *nexosv1.ContainerActionRequest) (*nexosv1.ContainerActionResponse, error) {
	if err := x.s.requireDocker(); err != nil {
		return nil, err
	}
	err := x.s.docker.Action(ctx, req.GetId(), req.GetAction(), req.GetTimeoutSeconds())
	slog.Info("container action", "id", req.GetId(), "action", req.GetAction().String(), "peer", peerName(ctx), "err", err)
	if err != nil {
		return nil, dockerErr(err)
	}
	return &nexosv1.ContainerActionResponse{}, nil
}

func (x *containerService) RemoveContainer(ctx context.Context, req *nexosv1.RemoveContainerRequest) (*nexosv1.RemoveContainerResponse, error) {
	if err := x.s.requireDocker(); err != nil {
		return nil, err
	}
	err := x.s.docker.Remove(ctx, req.GetId(), req.GetForce(), req.GetRemoveVolumes())
	slog.Info("container remove", "id", req.GetId(), "force", req.GetForce(), "volumes", req.GetRemoveVolumes(), "peer", peerName(ctx), "err", err)
	if err != nil {
		return nil, dockerErr(err)
	}
	return &nexosv1.RemoveContainerResponse{}, nil
}

func (x *containerService) StreamLogs(req *nexosv1.StreamLogsRequest, stream grpc.ServerStreamingServer[nexosv1.StreamLogsResponse]) error {
	if err := x.s.requireDocker(); err != nil {
		return err
	}
	err := x.s.docker.Logs(stream.Context(), req, func(lines []*nexosv1.LogLine) error {
		return stream.Send(&nexosv1.StreamLogsResponse{Lines: lines})
	})
	return dockerErr(err)
}

func (x *containerService) StreamStats(req *nexosv1.StreamStatsRequest, stream grpc.ServerStreamingServer[nexosv1.StreamStatsResponse]) error {
	if err := x.s.requireDocker(); err != nil {
		return err
	}
	interval := time.Duration(req.GetIntervalSeconds()) * time.Second
	interval = min(max(interval, time.Second), time.Minute)
	err := x.s.docker.Stats(stream.Context(), req.GetContainerIds(), interval, stream.Send)
	return dockerErr(err)
}

// streamWriter turns exec output into ExecResponse messages.
type streamWriter struct {
	stream grpc.BidiStreamingServer[nexosv1.ExecRequest, nexosv1.ExecResponse]
}

func (w streamWriter) Write(p []byte) (int, error) {
	// gRPC may retain the slice after Send returns; copy it.
	buf := append([]byte(nil), p...)
	if err := w.stream.Send(&nexosv1.ExecResponse{Msg: &nexosv1.ExecResponse_Output{Output: buf}}); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (x *containerService) Exec(stream grpc.BidiStreamingServer[nexosv1.ExecRequest, nexosv1.ExecResponse]) error {
	if err := x.s.requireDocker(); err != nil {
		return err
	}
	ctx := stream.Context()
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	start := first.GetStart()
	if start == nil {
		return status.Error(codes.InvalidArgument, "first message must be start")
	}
	sess, err := x.s.docker.StartExec(ctx, start)
	if err != nil {
		return dockerErr(err)
	}
	defer sess.Close()
	slog.Info("exec started", "container", start.GetContainerId(), "command", start.GetCommand(), "user", start.GetUser(), "peer", peerName(ctx))

	// Client input runs in its own goroutine; only this goroutine sends.
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				sess.Close() // client went away: end the process I/O
				return
			}
			switch m := msg.GetMsg().(type) {
			case *nexosv1.ExecRequest_Stdin:
				if _, err := sess.Write(m.Stdin); err != nil {
					return
				}
			case *nexosv1.ExecRequest_Resize:
				_ = sess.Resize(ctx, m.Resize.GetCols(), m.Resize.GetRows())
			}
		}
	}()

	copyErr := sess.CopyOutput(streamWriter{stream: stream})
	code := sess.ExitCode(context.WithoutCancel(ctx))
	slog.Info("exec ended", "container", start.GetContainerId(), "exit_code", code, "err", copyErr)
	if ctx.Err() == nil {
		_ = stream.Send(&nexosv1.ExecResponse{Msg: &nexosv1.ExecResponse_ExitCode{ExitCode: int32(code)}})
	}
	return nil
}

type eventService struct {
	nexosv1.UnimplementedEventServiceServer
	s *Server
}

func (x *eventService) StreamEvents(_ *nexosv1.StreamEventsRequest, stream grpc.ServerStreamingServer[nexosv1.StreamEventsResponse]) error {
	if err := x.s.requireDocker(); err != nil {
		return err
	}
	err := x.s.docker.Events(stream.Context(), func(ev *nexosv1.DockerEvent) error {
		return stream.Send(&nexosv1.StreamEventsResponse{Event: ev})
	})
	return dockerErr(err)
}
