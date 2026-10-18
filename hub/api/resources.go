package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc"

	nexosv1 "nexos/internal/gen/nexos/v1"
)

var (
	imageIDParam  = regexp.MustCompile(`^(sha256:)?[a-f0-9]{12,64}$`)
	stackNameRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	resourceParam = containerIDPattern
)

func param(w http.ResponseWriter, r *http.Request, name string, re *regexp.Regexp) (string, bool) {
	v := chi.URLParam(r, name)
	if !re.MatchString(v) {
		writeError(w, http.StatusBadRequest, "invalid "+name)
		return "", false
	}
	return v, true
}

func rpcCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 2*time.Minute)
}

// streamWriter writes newline-delimited JSON and flushes after each object,
// so the browser can render progress while an operation runs.
type ndjson struct {
	w http.ResponseWriter
	f http.Flusher
}

func newNDJSON(w http.ResponseWriter) *ndjson {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	f, _ := w.(http.Flusher)
	return &ndjson{w: w, f: f}
}

func (n *ndjson) send(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := n.w.Write(append(raw, '\n')); err != nil {
		return err
	}
	if n.f != nil {
		n.f.Flush()
	}
	return nil
}

// relay forwards a server stream as NDJSON. It returns the final error
// (nil on clean EOF) after reporting it to the client.
func relay[T any](n *ndjson, recv func() (T, error), convert func(T) any) error {
	for {
		msg, err := recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			_ = n.send(map[string]string{"error": grpcErr(err).Error()})
			return grpcErr(err)
		}
		if err := n.send(convert(msg)); err != nil {
			return err
		}
	}
}

func listOrEmpty[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

// ---- images ----

func (s *Server) handleListImages(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	ctx, cancel := rpcCtx(r)
	defer cancel()
	resp, err := nexosv1.NewImageServiceClient(cc).ListImages(ctx, &nexosv1.ListImagesRequest{})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listOrEmpty(resp.GetImages()))
}

func (s *Server) handleInspectImage(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	iid, ok := param(w, r, "iid", imageIDParam)
	if !ok {
		return
	}
	ctx, cancel := rpcCtx(r)
	defer cancel()
	resp, err := nexosv1.NewImageServiceClient(cc).InspectImage(ctx, &nexosv1.InspectImageRequest{Id: iid})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(resp.GetJson())
}

func (s *Server) handlePullImage(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	var body struct {
		Reference string `json:"reference"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Reference == "" || len(body.Reference) > 512 {
		writeError(w, http.StatusBadRequest, "invalid image reference")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	stream, err := nexosv1.NewImageServiceClient(cc).PullImage(ctx, &nexosv1.PullImageRequest{Reference: body.Reference})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	n := newNDJSON(w)
	err = relay(n, stream.Recv, func(m *nexosv1.PullImageResponse) any { return m })
	if err == nil {
		_ = n.send(map[string]bool{"done": true})
	}
	s.audit(r, "image.pull", body.Reference, chi.URLParam(r, "id"), err, "")
}

func (s *Server) handleRemoveImage(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	iid, ok := param(w, r, "iid", imageIDParam)
	if !ok {
		return
	}
	force := r.URL.Query().Get("force") == "true"
	ctx, cancel := rpcCtx(r)
	defer cancel()
	resp, err := nexosv1.NewImageServiceClient(cc).RemoveImage(ctx, &nexosv1.RemoveImageRequest{Id: iid, Force: force})
	s.audit(r, "image.remove", auditTarget(r.URL.Query().Get("name"), iid), chi.URLParam(r, "id"), grpcErr(err), "force="+strconv.FormatBool(force))
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

type pruneBody struct {
	All bool `json:"all"`
}

func (s *Server) handlePruneImages(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	var body pruneBody
	if !decodeJSON(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	resp, err := nexosv1.NewImageServiceClient(cc).PruneImages(ctx, &nexosv1.PruneImagesRequest{All: body.All})
	s.audit(r, "image.prune", "", chi.URLParam(r, "id"), grpcErr(err), pruneDetail(body.All, resp.GetResult()))
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp.GetResult())
}

func pruneDetail(all bool, res *nexosv1.PruneResult) string {
	return "all=" + strconv.FormatBool(all) + " deleted=" + strconv.Itoa(len(res.GetDeleted())) +
		" reclaimed=" + strconv.FormatUint(res.GetSpaceReclaimedBytes(), 10)
}

// ---- networks ----

func (s *Server) handleListNetworks(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	ctx, cancel := rpcCtx(r)
	defer cancel()
	resp, err := nexosv1.NewNetworkServiceClient(cc).ListNetworks(ctx, &nexosv1.ListNetworksRequest{})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listOrEmpty(resp.GetNetworks()))
}

func (s *Server) handleInspectNetwork(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	nid, ok := param(w, r, "nid", resourceParam)
	if !ok {
		return
	}
	ctx, cancel := rpcCtx(r)
	defer cancel()
	resp, err := nexosv1.NewNetworkServiceClient(cc).InspectNetwork(ctx, &nexosv1.InspectNetworkRequest{Id: nid})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(resp.GetJson())
}

func (s *Server) handleCreateNetwork(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	var body struct {
		Name       string `json:"name"`
		Driver     string `json:"driver"`
		Internal   bool   `json:"internal"`
		Attachable bool   `json:"attachable"`
		IPv6       bool   `json:"ipv6"`
		Subnet     string `json:"subnet"`
		Gateway    string `json:"gateway"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	switch body.Driver {
	case "", "bridge", "macvlan", "ipvlan", "overlay":
	default:
		writeError(w, http.StatusBadRequest, "unsupported driver")
		return
	}
	ctx, cancel := rpcCtx(r)
	defer cancel()
	resp, err := nexosv1.NewNetworkServiceClient(cc).CreateNetwork(ctx, &nexosv1.CreateNetworkRequest{
		Name: body.Name, Driver: body.Driver, Internal: body.Internal, Attachable: body.Attachable,
		Ipv6: body.IPv6, Subnet: body.Subnet, Gateway: body.Gateway,
	})
	s.audit(r, "network.create", body.Name, chi.URLParam(r, "id"), grpcErr(err), "driver="+body.Driver+" subnet="+body.Subnet)
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleRemoveNetwork(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	nid, ok := param(w, r, "nid", resourceParam)
	if !ok {
		return
	}
	ctx, cancel := rpcCtx(r)
	defer cancel()
	_, err := nexosv1.NewNetworkServiceClient(cc).RemoveNetwork(ctx, &nexosv1.RemoveNetworkRequest{Id: nid})
	s.audit(r, "network.remove", auditTarget(r.URL.Query().Get("name"), nid), chi.URLParam(r, "id"), grpcErr(err), "")
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleNetworkMembership(connect bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cc, ok := s.agentConn(w, r)
		if !ok {
			return
		}
		nid, ok := param(w, r, "nid", resourceParam)
		if !ok {
			return
		}
		var body struct {
			Container string   `json:"container"`
			Aliases   []string `json:"aliases"`
			IPv4      string   `json:"ipv4"`
			Force     bool     `json:"force"`
		}
		if !decodeJSON(w, r, &body) {
			return
		}
		if !containerIDPattern.MatchString(body.Container) {
			writeError(w, http.StatusBadRequest, "invalid container")
			return
		}
		ctx, cancel := rpcCtx(r)
		defer cancel()
		client := nexosv1.NewNetworkServiceClient(cc)
		var err error
		action := "network.disconnect"
		if connect {
			action = "network.connect"
			_, err = client.ConnectNetwork(ctx, &nexosv1.ConnectNetworkRequest{NetworkId: nid, ContainerId: body.Container, Aliases: body.Aliases, Ipv4: body.IPv4})
		} else {
			_, err = client.DisconnectNetwork(ctx, &nexosv1.DisconnectNetworkRequest{NetworkId: nid, ContainerId: body.Container, Force: body.Force})
		}
		s.audit(r, action, auditTarget(r.URL.Query().Get("name"), nid), chi.URLParam(r, "id"), grpcErr(err), "container="+body.Container)
		if err != nil {
			writeGRPCError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handlePruneNetworks(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	ctx, cancel := rpcCtx(r)
	defer cancel()
	resp, err := nexosv1.NewNetworkServiceClient(cc).PruneNetworks(ctx, &nexosv1.PruneNetworksRequest{})
	s.audit(r, "network.prune", "", chi.URLParam(r, "id"), grpcErr(err), pruneDetail(false, resp.GetResult()))
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp.GetResult())
}

// ---- volumes ----

func (s *Server) handleListVolumes(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	ctx, cancel := rpcCtx(r)
	defer cancel()
	resp, err := nexosv1.NewVolumeServiceClient(cc).ListVolumes(ctx, &nexosv1.ListVolumesRequest{})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listOrEmpty(resp.GetVolumes()))
}

func (s *Server) handleInspectVolume(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	name, ok := param(w, r, "vname", resourceParam)
	if !ok {
		return
	}
	ctx, cancel := rpcCtx(r)
	defer cancel()
	resp, err := nexosv1.NewVolumeServiceClient(cc).InspectVolume(ctx, &nexosv1.InspectVolumeRequest{Name: name})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(resp.GetJson())
}

func (s *Server) handleCreateVolume(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	ctx, cancel := rpcCtx(r)
	defer cancel()
	resp, err := nexosv1.NewVolumeServiceClient(cc).CreateVolume(ctx, &nexosv1.CreateVolumeRequest{Name: body.Name})
	s.audit(r, "volume.create", body.Name, chi.URLParam(r, "id"), grpcErr(err), "")
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, resp.GetVolume())
}

func (s *Server) handleRemoveVolume(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	name, ok := param(w, r, "vname", resourceParam)
	if !ok {
		return
	}
	ctx, cancel := rpcCtx(r)
	defer cancel()
	_, err := nexosv1.NewVolumeServiceClient(cc).RemoveVolume(ctx, &nexosv1.RemoveVolumeRequest{Name: name})
	s.audit(r, "volume.remove", name, chi.URLParam(r, "id"), grpcErr(err), "")
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePruneVolumes(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	var body pruneBody
	if !decodeJSON(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	resp, err := nexosv1.NewVolumeServiceClient(cc).PruneVolumes(ctx, &nexosv1.PruneVolumesRequest{All: body.All})
	s.audit(r, "volume.prune", "", chi.URLParam(r, "id"), grpcErr(err), pruneDetail(body.All, resp.GetResult()))
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp.GetResult())
}

// ---- stacks ----

func (s *Server) handleListStacks(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	ctx, cancel := rpcCtx(r)
	defer cancel()
	resp, err := nexosv1.NewStackServiceClient(cc).ListStacks(ctx, &nexosv1.ListStacksRequest{})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listOrEmpty(resp.GetStacks()))
}

// handleGetStack returns compose files (operator) and the .env file, which
// usually holds secrets, only to admins.
func (s *Server) handleGetStack(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	name, ok := param(w, r, "sname", stackNameRe)
	if !ok {
		return
	}
	includeEnv := principalFrom(r.Context()).user.Role == "admin"
	ctx, cancel := rpcCtx(r)
	defer cancel()
	resp, err := nexosv1.NewStackServiceClient(cc).GetStack(ctx, &nexosv1.GetStackRequest{Name: name, IncludeEnv: includeEnv})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"stack": resp.GetStack(), "files": listOrEmpty(resp.GetFiles()),
		"compose": resp.GetCompose(), "env": resp.GetEnv(), "env_visible": includeEnv,
	})
}

func (s *Server) saveStack(w http.ResponseWriter, r *http.Request, name string, create bool) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	var body struct {
		Name    string `json:"name"`
		Compose string `json:"compose"`
		Env     string `json:"env"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if create {
		name = body.Name
	}
	if !stackNameRe.MatchString(name) {
		writeError(w, http.StatusBadRequest, "invalid stack name: lowercase letters, digits, '-' and '_' (max 63)")
		return
	}
	ctx, cancel := rpcCtx(r)
	defer cancel()
	resp, err := nexosv1.NewStackServiceClient(cc).SaveStack(ctx, &nexosv1.SaveStackRequest{Name: name, Compose: body.Compose, Env: body.Env, Create: create})
	action := "stack.update"
	if create {
		action = "stack.create"
	}
	s.audit(r, action, name, chi.URLParam(r, "id"), grpcErr(err), "")
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp.GetStack())
}

func (s *Server) handleCreateStack(w http.ResponseWriter, r *http.Request) {
	s.saveStack(w, r, "", true)
}

func (s *Server) handleUpdateStack(w http.ResponseWriter, r *http.Request) {
	name, ok := param(w, r, "sname", stackNameRe)
	if !ok {
		return
	}
	s.saveStack(w, r, name, false)
}

var stackOps = map[string]nexosv1.StackOperation{
	"up":      nexosv1.StackOperation_STACK_OPERATION_UP,
	"down":    nexosv1.StackOperation_STACK_OPERATION_DOWN,
	"pull":    nexosv1.StackOperation_STACK_OPERATION_PULL,
	"restart": nexosv1.StackOperation_STACK_OPERATION_RESTART,
	"stop":    nexosv1.StackOperation_STACK_OPERATION_STOP,
	"start":   nexosv1.StackOperation_STACK_OPERATION_START,
}

func stackOutputJSON(o *nexosv1.StackOutput) any {
	switch m := o.GetMsg().(type) {
	case *nexosv1.StackOutput_Line:
		return map[string]string{"line": m.Line}
	case *nexosv1.StackOutput_ExitCode:
		return map[string]int32{"exit_code": m.ExitCode}
	}
	return map[string]string{}
}

// streamStack relays a compose operation and records its outcome.
func (s *Server) streamStack(w http.ResponseWriter, r *http.Request, action, name, detail string, open func(context.Context) (func() (*nexosv1.StackOutput, error), error)) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	recv, err := open(ctx)
	if err != nil {
		s.audit(r, action, name, chi.URLParam(r, "id"), grpcErr(err), detail)
		writeGRPCError(w, err)
		return
	}
	n := newNDJSON(w)
	exit := int32(-1)
	err = relay(n, recv, func(o *nexosv1.StackOutput) any {
		if c, ok := o.GetMsg().(*nexosv1.StackOutput_ExitCode); ok {
			exit = c.ExitCode
		}
		return stackOutputJSON(o)
	})
	if err == nil && exit != 0 {
		err = errors.New("exit code " + strconv.Itoa(int(exit)))
	}
	s.audit(r, action, name, chi.URLParam(r, "id"), err, detail)
}

func (s *Server) handleRunStack(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	name, ok := param(w, r, "sname", stackNameRe)
	if !ok {
		return
	}
	var body struct {
		Operation     string `json:"operation"`
		Pull          bool   `json:"pull"`
		RemoveVolumes bool   `json:"remove_volumes"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	op, ok := stackOps[body.Operation]
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown operation")
		return
	}
	// Deleting volumes destroys data: admin only.
	if body.RemoveVolumes && principalFrom(r.Context()).user.Role != "admin" {
		writeError(w, http.StatusForbidden, "removing volumes requires the admin role")
		return
	}
	detail := "pull=" + strconv.FormatBool(body.Pull) + " volumes=" + strconv.FormatBool(body.RemoveVolumes)
	s.streamStack(w, r, "stack."+body.Operation, name, detail, func(ctx context.Context) (func() (*nexosv1.StackOutput, error), error) {
		stream, err := nexosv1.NewStackServiceClient(cc).RunStack(ctx, &nexosv1.RunStackRequest{Name: name, Operation: op, Pull: body.Pull, RemoveVolumes: body.RemoveVolumes})
		if err != nil {
			return nil, err
		}
		return firstThen(stream, func(m *nexosv1.RunStackResponse) *nexosv1.StackOutput { return m.GetOutput() })
	})
}

func (s *Server) handleDeleteStack(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	name, ok := param(w, r, "sname", stackNameRe)
	if !ok {
		return
	}
	q := r.URL.Query()
	down, volumes := q.Get("down") == "true", q.Get("volumes") == "true"
	detail := "down=" + strconv.FormatBool(down) + " volumes=" + strconv.FormatBool(volumes)
	s.streamStack(w, r, "stack.delete", name, detail, func(ctx context.Context) (func() (*nexosv1.StackOutput, error), error) {
		stream, err := nexosv1.NewStackServiceClient(cc).DeleteStack(ctx, &nexosv1.DeleteStackRequest{Name: name, Down: down, RemoveVolumes: volumes})
		if err != nil {
			return nil, err
		}
		return firstThen(stream, func(m *nexosv1.DeleteStackResponse) *nexosv1.StackOutput { return m.GetOutput() })
	})
}

// firstThen waits for the first message so that errors raised before any
// output (validation, not found, busy) become a proper HTTP error status
// instead of a 200 stream. The returned function replays it, then continues.
func firstThen[T any](stream grpc.ServerStreamingClient[T], get func(*T) *nexosv1.StackOutput) (func() (*nexosv1.StackOutput, error), error) {
	first, err := stream.Recv()
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	pending := first
	eof := errors.Is(err, io.EOF)
	return func() (*nexosv1.StackOutput, error) {
		if pending != nil {
			m := get(pending)
			pending = nil
			return m, nil
		}
		if eof {
			return nil, io.EOF
		}
		m, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		return get(m), nil
	}, nil
}
