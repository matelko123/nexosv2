package api

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"nexos/hub/agents"
	nexosv1 "nexos/internal/gen/nexos/v1"
	"nexos/internal/version"
)

type hostVersion struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	AgentVersion    string `json:"agent_version,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
	RunsHub         bool   `json:"runs_hub"`
}

// hubLocator caches which agent shares its Docker host with the hub.
type hubLocator struct {
	mu    sync.Mutex
	id    string
	until time.Time
}

// hubHost finds the agent whose Docker runs the container labelled
// nexos.role=hub; that agent performs hub updates.
func (s *Server) hubHost(ctx context.Context) string {
	s.hubLoc.mu.Lock()
	defer s.hubLoc.mu.Unlock()
	if time.Now().Before(s.hubLoc.until) {
		return s.hubLoc.id
	}
	s.hubLoc.id = ""
	list, err := s.store.Agents(ctx)
	if err != nil {
		return ""
	}
	for _, a := range list {
		cc, err := s.agents.Client(a.ID)
		if err != nil {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		resp, err := nexosv1.NewContainerServiceClient(cc).ListContainers(cctx, &nexosv1.ListContainersRequest{All: true})
		cancel()
		if err != nil {
			continue
		}
		for _, c := range resp.GetContainers() {
			if c.GetLabels()["nexos.role"] == "hub" {
				s.hubLoc.id = a.ID
			}
		}
		if s.hubLoc.id != "" {
			break
		}
	}
	s.hubLoc.until = time.Now().Add(time.Minute)
	return s.hubLoc.id
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	st := s.releases.Status()
	list, err := s.store.Agents(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	hubID := s.hubHost(r.Context())
	hosts := make([]hostVersion, 0, len(list))
	for _, a := range list {
		if a.EnrolledAt == nil {
			continue
		}
		state := s.agents.State(a.ID)
		hv := hostVersion{ID: a.ID, Name: a.Name, Status: string(state.Status), RunsHub: a.ID == hubID}
		if state.Info != nil {
			hv.AgentVersion = state.Info.GetAgentVersion()
		}
		if st.Latest != nil && hv.AgentVersion != "" {
			hv.UpdateAvailable = version.Newer(st.Latest.Version, hv.AgentVersion)
		}
		hosts = append(hosts, hv)
	}
	writeJSON(w, http.StatusOK, map[string]any{"hub": st, "hosts": hosts, "hub_host_id": hubID})
}

func (s *Server) handleCheckVersion(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	err := s.releases.Check(ctx)
	s.audit(r, "nexos.release.check", "", "", err, "")
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.handleVersion(w, r)
}

// runUpdate streams an UpdateComponent call on the given agent as NDJSON.
func (s *Server) runUpdate(w http.ResponseWriter, r *http.Request, agentID, role string) {
	st := s.releases.Status()
	if st.Latest == nil {
		writeError(w, http.StatusConflict, "no release information; check for updates first")
		return
	}
	cc, err := s.agents.Client(agentID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "host unavailable: "+err.Error())
		return
	}
	image := s.releases.Image(role, st.Latest.Version)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	stream, err := nexosv1.NewUpdateServiceClient(cc).UpdateComponent(ctx, &nexosv1.UpdateComponentRequest{
		Role: role, Image: image, RegistryAuth: s.releases.RegistryAuth(),
	})
	if err != nil {
		s.audit(r, "nexos.update."+role, image, agentID, grpcErr(err), "")
		writeGRPCError(w, err)
		return
	}
	recv, err := firstThenUpdate(stream)
	if err != nil {
		s.audit(r, "nexos.update."+role, image, agentID, grpcErr(err), "")
		writeGRPCError(w, err)
		return
	}
	n := newNDJSON(w)
	done := false
	err = relay(n, recv, func(m *nexosv1.UpdateComponentResponse) any {
		if m.GetDone() {
			done = true
			return map[string]any{"line": "aktualizacja rozpoczęta: " + image, "exit_code": 0}
		}
		return map[string]string{"line": m.GetLine()}
	})
	// Updating the agent replaces it mid-stream; that is expected.
	if err != nil && role == "agent" && !done {
		_ = n.send(map[string]any{"line": "połączenie z agentem zakończone (restart agenta)", "exit_code": 0})
		err = nil
	}
	s.audit(r, "nexos.update."+role, image, agentID, err, "from="+st.HubVersion)
	s.hubLoc.mu.Lock()
	s.hubLoc.until = time.Time{}
	s.hubLoc.mu.Unlock()
}

// firstThenUpdate surfaces errors raised before the first message as a
// proper HTTP status (same idea as firstThen for stack operations).
func firstThenUpdate(stream interface {
	Recv() (*nexosv1.UpdateComponentResponse, error)
}) (func() (*nexosv1.UpdateComponentResponse, error), error) {
	first, err := stream.Recv()
	if err != nil {
		return nil, err
	}
	pending := first
	return func() (*nexosv1.UpdateComponentResponse, error) {
		if pending != nil {
			m := pending
			pending = nil
			return m, nil
		}
		return stream.Recv()
	}, nil
}

func (s *Server) handleUpdateHub(w http.ResponseWriter, r *http.Request) {
	id := s.hubHost(r.Context())
	if id == "" {
		writeError(w, http.StatusConflict, "no connected agent runs the hub container (label nexos.role=hub); update the hub manually")
		return
	}
	s.runUpdate(w, r, id, "hub")
}

func (s *Server) handleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if s.agents.State(id).Status != agents.StatusOnline {
		writeError(w, http.StatusConflict, "host is not online")
		return
	}
	s.runUpdate(w, r, id, "agent")
}
