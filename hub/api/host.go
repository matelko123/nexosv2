package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	nexosv1 "nexos/internal/gen/nexos/v1"
)

var unitParam = regexp.MustCompile(`^[a-zA-Z0-9@._:\\-]{1,200}\.(service|socket|timer|mount|target|path)$`)

// Command lines may contain credentials: listing processes is operator+.
func (s *Server) handleListProcesses(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	ctx, cancel := rpcCtx(r)
	defer cancel()
	resp, err := nexosv1.NewHostServiceClient(cc).ListProcesses(ctx, &nexosv1.ListProcessesRequest{})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"processes": listOrEmpty(resp.GetProcesses()), "cpu_cores": resp.GetCpuCores(), "memory_total_bytes": resp.GetMemoryTotalBytes(),
	})
}

func (s *Server) handleSignalProcess(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	pid, err := strconv.Atoi(chi.URLParam(r, "pid"))
	if err != nil || pid <= 1 {
		writeError(w, http.StatusBadRequest, "invalid pid")
		return
	}
	var body struct {
		Signal      string `json:"signal"`
		StartedUnix int64  `json:"started_unix"`
		Name        string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	sig := nexosv1.ProcessSignal_PROCESS_SIGNAL_TERM
	if body.Signal == "kill" {
		sig = nexosv1.ProcessSignal_PROCESS_SIGNAL_KILL
	}
	ctx, cancel := rpcCtx(r)
	defer cancel()
	_, err = nexosv1.NewHostServiceClient(cc).SignalProcess(ctx, &nexosv1.SignalProcessRequest{Pid: int32(pid), Signal: sig, StartedUnix: body.StartedUnix})
	s.audit(r, "host.process.signal", auditTarget(body.Name, strconv.Itoa(pid)), chi.URLParam(r, "id"), grpcErr(err), "signal="+body.Signal)
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListUnits(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	ctx, cancel := rpcCtx(r)
	defer cancel()
	resp, err := nexosv1.NewHostServiceClient(cc).ListUnits(ctx, &nexosv1.ListUnitsRequest{})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listOrEmpty(resp.GetUnits()))
}

var unitActions = map[string]nexosv1.UnitActionType{
	"start":   nexosv1.UnitActionType_UNIT_ACTION_TYPE_START,
	"stop":    nexosv1.UnitActionType_UNIT_ACTION_TYPE_STOP,
	"restart": nexosv1.UnitActionType_UNIT_ACTION_TYPE_RESTART,
	"reload":  nexosv1.UnitActionType_UNIT_ACTION_TYPE_RELOAD,
}

func (s *Server) handleUnitAction(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	unit, ok := param(w, r, "unit", unitParam)
	if !ok {
		return
	}
	name := chi.URLParam(r, "action")
	action, ok := unitActions[name]
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown action")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	resp, err := nexosv1.NewHostServiceClient(cc).UnitAction(ctx, &nexosv1.UnitActionRequest{Name: unit, Action: action})
	s.audit(r, "host.unit."+name, unit, chi.URLParam(r, "id"), grpcErr(err), "result="+resp.GetResult())
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"result": resp.GetResult()})
}

// handleJournal streams journald lines over a WebSocket. The journal holds
// authentication and sudo logs, hence operator+.
func (s *Server) handleJournal(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	unit := q.Get("unit")
	if unit != "" && !unitParam.MatchString(unit) {
		writeError(w, http.StatusBadRequest, "invalid unit")
		return
	}
	lines, _ := strconv.Atoi(q.Get("lines"))
	c, err := s.acceptWS(w, r)
	if err != nil {
		return
	}
	defer c.CloseNow()
	ctx := c.CloseRead(r.Context())
	go s.superviseWS(ctx, c, principalFrom(r.Context()).token)

	stream, err := nexosv1.NewHostServiceClient(cc).StreamJournal(ctx, &nexosv1.StreamJournalRequest{Unit: unit, Lines: uint32(max(lines, 0)), Follow: q.Get("follow") != "false"})
	if err != nil {
		_ = writeWSJSON(ctx, c, map[string]string{"error": grpcErr(err).Error()})
		return
	}
	for {
		resp, err := stream.Recv()
		if err != nil {
			if ctx.Err() == nil {
				if errors.Is(err, io.EOF) {
					_ = writeWSJSON(ctx, c, map[string]bool{"end": true})
				} else {
					_ = writeWSJSON(ctx, c, map[string]string{"error": grpcErr(err).Error()})
				}
			}
			c.Close(websocket.StatusNormalClosure, "")
			return
		}
		if err := writeWSJSON(ctx, c, map[string]any{"lines": resp.GetLines()}); err != nil {
			return
		}
	}
}

func (s *Server) handlePackageUpdates(w http.ResponseWriter, r *http.Request) {
	cc, ok := s.agentConn(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	resp, err := nexosv1.NewHostServiceClient(cc).ListPackageUpdates(ctx, &nexosv1.ListPackageUpdatesRequest{})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"updates": listOrEmpty(resp.GetUpdates()), "lists_updated_unix": resp.GetListsUpdatedUnix(),
		"reboot_required": resp.GetRebootRequired(), "reboot_packages": listOrEmpty(resp.GetRebootPackages()),
		"distribution": resp.GetDistribution(),
	})
}
