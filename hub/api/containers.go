package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	nexosv1 "nexos/internal/gen/nexos/v1"
)

var containerIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

// agentConn resolves the {id} host to a live gRPC connection, writing an
// error response when it is not available.
func (s *Server) agentConn(w http.ResponseWriter, r *http.Request) (*grpc.ClientConn, bool) {
	cc, err := s.agents.Client(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "host unavailable: "+err.Error())
		return nil, false
	}
	return cc, true
}

func containerParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	cid := chi.URLParam(r, "cid")
	if !containerIDPattern.MatchString(cid) {
		writeError(w, http.StatusBadRequest, "invalid container id")
		return "", false
	}
	return cid, true
}

// writeGRPCError translates an agent error into an HTTP response.
func writeGRPCError(w http.ResponseWriter, err error) {
	st, _ := status.FromError(err)
	code := http.StatusBadGateway
	switch st.Code() {
	case codes.NotFound:
		code = http.StatusNotFound
	case codes.InvalidArgument:
		code = http.StatusBadRequest
	case codes.FailedPrecondition:
		code = http.StatusConflict
	case codes.Unavailable:
		code = http.StatusServiceUnavailable
	case codes.DeadlineExceeded:
		code = http.StatusGatewayTimeout
	}
	writeError(w, code, st.Message())
}

func (s *Server) handleListContainers(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	resp, err := nexosv1.NewContainerServiceClient(cc).ListContainers(ctx, &nexosv1.ListContainersRequest{All: true})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	list := resp.GetContainers()
	if list == nil {
		list = []*nexosv1.Container{}
	}
	writeJSON(w, http.StatusOK, list)
}

// handleInspectContainer returns the raw inspect document. It contains
// environment variables (often secrets), so it requires the operator role.
func (s *Server) handleInspectContainer(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	cid, ok := containerParam(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	resp, err := nexosv1.NewContainerServiceClient(cc).InspectContainer(ctx, &nexosv1.InspectContainerRequest{Id: cid})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(resp.GetJson())
}

var containerActions = map[string]nexosv1.ContainerActionType{
	"start":   nexosv1.ContainerActionType_CONTAINER_ACTION_TYPE_START,
	"stop":    nexosv1.ContainerActionType_CONTAINER_ACTION_TYPE_STOP,
	"restart": nexosv1.ContainerActionType_CONTAINER_ACTION_TYPE_RESTART,
	"kill":    nexosv1.ContainerActionType_CONTAINER_ACTION_TYPE_KILL,
	"pause":   nexosv1.ContainerActionType_CONTAINER_ACTION_TYPE_PAUSE,
	"unpause": nexosv1.ContainerActionType_CONTAINER_ACTION_TYPE_UNPAUSE,
}

func (s *Server) handleContainerAction(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	cid, ok := containerParam(w, r)
	if !ok {
		return
	}
	name := chi.URLParam(r, "action")
	action, ok := containerActions[name]
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown action")
		return
	}
	var body struct {
		TimeoutSeconds int32  `json:"timeout_seconds"`
		Name           string `json:"name"` // for the audit log only
	}
	if r.ContentLength != 0 && !decodeJSON(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	_, err := nexosv1.NewContainerServiceClient(cc).ContainerAction(ctx, &nexosv1.ContainerActionRequest{
		Id: cid, Action: action, TimeoutSeconds: min(max(body.TimeoutSeconds, 0), 600),
	})
	s.audit(r, "container."+name, auditTarget(body.Name, cid), chi.URLParam(r, "id"), grpcErr(err), "")
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRemoveContainer(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	cid, ok := containerParam(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	force, volumes := q.Get("force") == "true", q.Get("volumes") == "true"
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	_, err := nexosv1.NewContainerServiceClient(cc).RemoveContainer(ctx, &nexosv1.RemoveContainerRequest{Id: cid, Force: force, RemoveVolumes: volumes})
	s.audit(r, "container.remove", auditTarget(q.Get("name"), cid), chi.URLParam(r, "id"), grpcErr(err),
		"force="+strconv.FormatBool(force)+" volumes="+strconv.FormatBool(volumes))
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func auditTarget(name, id string) string {
	if len(id) > 12 && !strings.ContainsAny(id, "_.-") {
		id = id[:12]
	}
	if name == "" || name == id {
		return id
	}
	if len(name) > 128 {
		name = name[:128]
	}
	return name + " (" + id + ")"
}

func grpcErr(err error) error {
	if err == nil {
		return nil
	}
	if st, ok := status.FromError(err); ok {
		return errors.New(st.Message())
	}
	return err
}

// handleContainerLogs streams logs over a WebSocket as JSON batches.
func (s *Server) handleContainerLogs(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	cid, ok := containerParam(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	tail, _ := strconv.Atoi(q.Get("tail"))
	if tail <= 0 || tail > 50000 {
		tail = 500
	}
	since, _ := strconv.ParseInt(q.Get("since"), 10, 64)

	c, err := s.acceptWS(w, r)
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx := c.CloseRead(r.Context())
	go s.superviseWS(ctx, c, principalFrom(r.Context()).token)

	stream, err := nexosv1.NewContainerServiceClient(cc).StreamLogs(ctx, &nexosv1.StreamLogsRequest{
		Id: cid, Follow: q.Get("follow") != "false", Tail: uint32(tail), SinceUnix: since, Timestamps: q.Get("timestamps") == "true",
	})
	if err != nil {
		_ = writeWSJSON(ctx, c, map[string]string{"error": grpcErr(err).Error()})
		return
	}
	for {
		resp, err := stream.Recv()
		if err != nil {
			if !errors.Is(err, io.EOF) && ctx.Err() == nil {
				_ = writeWSJSON(ctx, c, map[string]string{"error": grpcErr(err).Error()})
			} else if ctx.Err() == nil {
				_ = writeWSJSON(ctx, c, map[string]bool{"end": true})
			}
			c.Close(websocket.StatusNormalClosure, "")
			return
		}
		if err := writeWSJSON(ctx, c, map[string]any{"lines": resp.GetLines()}); err != nil {
			return
		}
	}
}

// handleContainerStats streams resource usage of containers on a host.
// Query ids=a,b limits the set; empty streams all running containers.
func (s *Server) handleContainerStats(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	var ids []string
	for _, id := range strings.Split(r.URL.Query().Get("ids"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			if !containerIDPattern.MatchString(id) {
				writeError(w, http.StatusBadRequest, "invalid container id")
				return
			}
			ids = append(ids, id)
		}
	}
	c, err := s.acceptWS(w, r)
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx := c.CloseRead(r.Context())
	go s.superviseWS(ctx, c, principalFrom(r.Context()).token)

	stream, err := nexosv1.NewContainerServiceClient(cc).StreamStats(ctx, &nexosv1.StreamStatsRequest{ContainerIds: ids, IntervalSeconds: 2})
	if err != nil {
		_ = writeWSJSON(ctx, c, map[string]string{"error": grpcErr(err).Error()})
		return
	}
	for {
		resp, err := stream.Recv()
		if err != nil {
			if ctx.Err() == nil {
				_ = writeWSJSON(ctx, c, map[string]string{"error": grpcErr(err).Error()})
			}
			return
		}
		if err := writeWSJSON(ctx, c, resp); err != nil {
			return
		}
	}
}

type execClientMsg struct {
	Type string `json:"type"` // "stdin" or "resize"
	Data string `json:"data,omitempty"`
	Cols uint32 `json:"cols,omitempty"`
	Rows uint32 `json:"rows,omitempty"`
}

// handleContainerExec bridges an interactive terminal between the browser
// and a process in the container. Output is sent as binary frames; control
// messages as JSON text frames. Admin only: a shell in a container is
// frequently root-equivalent on the host.
func (s *Server) handleContainerExec(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	cid, ok := containerParam(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	var cmd []string
	if raw := strings.TrimSpace(q.Get("cmd")); raw != "" {
		cmd = strings.Fields(raw)
	}
	user := q.Get("user")
	if len(user) > 64 {
		writeError(w, http.StatusBadRequest, "invalid user")
		return
	}
	cols, _ := strconv.Atoi(q.Get("cols"))
	rows, _ := strconv.Atoi(q.Get("rows"))

	c, err := s.acceptWS(w, r)
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go s.superviseWS(ctx, c, principalFrom(r.Context()).token)

	stream, err := nexosv1.NewContainerServiceClient(cc).Exec(ctx)
	if err == nil {
		err = stream.Send(&nexosv1.ExecRequest{Msg: &nexosv1.ExecRequest_Start{Start: &nexosv1.ExecStart{
			ContainerId: cid, Command: cmd, Tty: true, Cols: uint32(min(max(cols, 1), 1000)), Rows: uint32(min(max(rows, 1), 500)), User: user,
		}}})
	}
	s.audit(r, "container.exec", auditTarget(q.Get("name"), cid), chi.URLParam(r, "id"), grpcErr(err), "cmd="+strings.Join(cmd, " ")+" user="+user)
	if err != nil {
		_ = writeWSJSON(ctx, c, map[string]string{"type": "error", "error": grpcErr(err).Error()})
		return
	}

	// Browser -> agent.
	go func() {
		defer cancel()
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				_ = stream.CloseSend()
				return
			}
			if typ != websocket.MessageText {
				continue
			}
			var m execClientMsg
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			var req *nexosv1.ExecRequest
			switch m.Type {
			case "stdin":
				req = &nexosv1.ExecRequest{Msg: &nexosv1.ExecRequest_Stdin{Stdin: []byte(m.Data)}}
			case "resize":
				req = &nexosv1.ExecRequest{Msg: &nexosv1.ExecRequest_Resize{Resize: &nexosv1.ExecResize{
					Cols: min(max(m.Cols, 1), 1000), Rows: min(max(m.Rows, 1), 500),
				}}}
			default:
				continue
			}
			if stream.Send(req) != nil {
				return
			}
		}
	}()

	// Agent -> browser.
	for {
		resp, err := stream.Recv()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, io.EOF) {
				_ = writeWSJSON(ctx, c, map[string]string{"type": "error", "error": grpcErr(err).Error()})
			}
			c.Close(websocket.StatusNormalClosure, "")
			return
		}
		switch m := resp.GetMsg().(type) {
		case *nexosv1.ExecResponse_Output:
			wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
			err = c.Write(wctx, websocket.MessageBinary, m.Output)
			wcancel()
			if err != nil {
				return
			}
		case *nexosv1.ExecResponse_ExitCode:
			_ = writeWSJSON(ctx, c, map[string]any{"type": "exit", "code": m.ExitCode})
		}
	}
}
