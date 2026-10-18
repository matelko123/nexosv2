package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// acceptWS upgrades an authenticated request. Authentication is the session
// cookie (SameSite=Strict) and the library rejects cross-origin handshakes
// by default, which prevents cross-site WebSocket hijacking.
func (s *Server) acceptWS(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(1 << 20)
	return c, nil
}

// superviseWS pings the browser and re-validates the session periodically,
// closing the connection when either fails, so logout or session expiry
// ends long-lived streams. It returns when ctx ends.
func (s *Server) superviseWS(ctx context.Context, c *websocket.Conn, token string) {
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	recheck := time.NewTicker(time.Minute)
	defer recheck.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pctx)
			cancel()
			if err != nil {
				c.CloseNow()
				return
			}
		case <-recheck.C:
			sess, err := s.store.SessionByToken(ctx, token)
			if err != nil || time.Now().After(sess.ExpiresAt) {
				c.Close(websocket.StatusPolicyViolation, "session ended")
				return
			}
		}
	}
}

func writeWSJSON(ctx context.Context, c *websocket.Conn, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.Write(wctx, websocket.MessageText, raw)
}

// handleWS streams live hub events (host status, metrics, Docker events).
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	c, err := s.acceptWS(w, r)
	if err != nil {
		return
	}
	defer c.CloseNow()
	p := principalFrom(r.Context())
	ctx := c.CloseRead(r.Context())
	go s.superviseWS(ctx, c, p.token)

	events, cancel := s.bus.Subscribe()
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if err := writeWSJSON(ctx, c, ev); err != nil {
				return
			}
		}
	}
}
