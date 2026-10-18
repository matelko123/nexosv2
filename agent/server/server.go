// Package server implements the agent's gRPC services.
package server

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"nexos/agent/docker"
	"nexos/agent/host"
	"nexos/agent/identity"
	"nexos/agent/metrics"
	"nexos/agent/stacks"
	nexosv1 "nexos/internal/gen/nexos/v1"
	"nexos/internal/pki"
	"nexos/internal/version"
)

type Server struct {
	id     *identity.Identity
	docker *docker.Client
	stacks *stacks.Manager
	host   *host.Host
}

func New(id *identity.Identity, dc *docker.Client, sm *stacks.Manager, h *host.Host) *Server {
	return &Server{id: id, docker: dc, stacks: sm, host: h}
}

// GRPCServer builds the mTLS gRPC server with all services registered.
func (s *Server) GRPCServer() *grpc.Server {
	creds := credentials.NewTLS(pki.AgentServerConfig(s.id.KeyPair, s.id.CAPool))
	gs := grpc.NewServer(
		grpc.Creds(creds),
		grpc.MaxRecvMsgSize(4<<20),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 10 * time.Second, PermitWithoutStream: true}),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: 30 * time.Second, Timeout: 10 * time.Second}),
		grpc.ChainUnaryInterceptor(auditUnary),
		grpc.ChainStreamInterceptor(auditStream),
	)
	nexosv1.RegisterSystemServiceServer(gs, &systemService{s: s})
	nexosv1.RegisterCertificateServiceServer(gs, &certificateService{s: s})
	nexosv1.RegisterContainerServiceServer(gs, &containerService{s: s})
	nexosv1.RegisterEventServiceServer(gs, &eventService{s: s})
	nexosv1.RegisterImageServiceServer(gs, &imageService{s: s})
	nexosv1.RegisterNetworkServiceServer(gs, &networkService{s: s})
	nexosv1.RegisterVolumeServiceServer(gs, &volumeService{s: s})
	nexosv1.RegisterStackServiceServer(gs, &stackService{s: s})
	nexosv1.RegisterHostServiceServer(gs, &hostService{s: s})
	nexosv1.RegisterUpdateServiceServer(gs, &updateService{s: s})
	return gs
}

func peerName(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "unknown"
	}
	if ti, ok := p.AuthInfo.(credentials.TLSInfo); ok && len(ti.State.PeerCertificates) > 0 {
		return ti.State.PeerCertificates[0].Subject.CommonName + "@" + p.Addr.String()
	}
	return p.Addr.String()
}

// auditUnary logs every call locally. Reads are logged at debug level so the
// log stays focused on state changes.
func auditUnary(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	start := time.Now()
	resp, err := h(ctx, req)
	slog.Info("rpc", "method", info.FullMethod, "peer", peerName(ctx), "code", status.Code(err).String(), "duration", time.Since(start))
	return resp, err
}

func auditStream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
	start := time.Now()
	slog.Info("stream open", "method", info.FullMethod, "peer", peerName(ss.Context()))
	err := h(srv, ss)
	slog.Info("stream closed", "method", info.FullMethod, "peer", peerName(ss.Context()), "code", status.Code(err).String(), "duration", time.Since(start))
	return err
}

type systemService struct {
	nexosv1.UnimplementedSystemServiceServer
	s *Server
}

func (x *systemService) GetInfo(ctx context.Context, _ *nexosv1.GetInfoRequest) (*nexosv1.GetInfoResponse, error) {
	info := metrics.HostInfo(ctx)
	info.AgentVersion = version.Version
	info.HostFeatures = x.s.host.Features()
	if x.s.docker != nil {
		info.Docker = x.s.docker.Info(ctx)
	} else {
		info.Docker = &nexosv1.DockerInfo{Error: "docker client not configured"}
	}
	return &nexosv1.GetInfoResponse{Info: info}, nil
}

func (x *systemService) StreamMetrics(req *nexosv1.StreamMetricsRequest, stream grpc.ServerStreamingServer[nexosv1.StreamMetricsResponse]) error {
	interval := time.Duration(req.GetIntervalSeconds()) * time.Second
	interval = min(max(interval, time.Second), time.Minute)
	ctx := stream.Context()
	sampler := metrics.NewSampler()
	// Prime counters so the first message already carries rates.
	sampler.Sample(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := stream.Send(&nexosv1.StreamMetricsResponse{Metrics: sampler.Sample(ctx)}); err != nil {
				return err
			}
		}
	}
}

type certificateService struct {
	nexosv1.UnimplementedCertificateServiceServer
	s *Server
}

func (x *certificateService) CreateCSR(context.Context, *nexosv1.CreateCSRRequest) (*nexosv1.CreateCSRResponse, error) {
	csr, err := x.s.id.NewCSR()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create csr: %v", err)
	}
	return &nexosv1.CreateCSRResponse{CsrPem: csr}, nil
}

func (x *certificateService) InstallCertificate(_ context.Context, req *nexosv1.InstallCertificateRequest) (*nexosv1.InstallCertificateResponse, error) {
	notAfter, err := x.s.id.Install(req.GetCertificatePem())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "install certificate: %v", err)
	}
	slog.Info("certificate rotated", "not_after", notAfter)
	return &nexosv1.InstallCertificateResponse{NotAfterUnix: notAfter.Unix()}, nil
}
