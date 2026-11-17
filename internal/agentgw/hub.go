// Package agentgw is the panel side of the agent connection: agents dial in
// over WebSocket, the hub pushes their desired state and feeds their usage,
// events and tunnel status into the services layer.
package agentgw

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"neosell/internal/agentproto"
	"neosell/internal/license"
	"neosell/internal/services"
)

type Hub struct {
	M       *services.Manager
	License *license.Manager

	mu      sync.Mutex
	conns   map[int64]*conn
	pending map[int64]*time.Timer
	seq     int64
}

func New(m *services.Manager, lic *license.Manager) *Hub {
	return &Hub{M: m, License: lic, conns: map[int64]*conn{}, pending: map[int64]*time.Timer{}}
}

type conn struct {
	nodeID  int64
	ws      *websocket.Conn
	wmu     sync.Mutex
	waiters sync.Map // id → chan json.RawMessage
}

func (c *conn) send(typ, id string, v any) error {
	msg, err := agentproto.Wrap(typ, id, v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.ws.SetWriteDeadline(time.Now().Add(20 * time.Second))
	return c.ws.WriteMessage(websocket.TextMessage, msg)
}

var upgrader = websocket.Upgrader{
	ReadBufferSize: 8192, WriteBufferSize: 8192,
	CheckOrigin: func(*http.Request) bool { return true }, // agents are not browsers; auth is the bearer token
}

// ServeHTTP handles GET /agent/ws.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tok == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	nodeID, err := h.M.NodeByAgentToken(r.Context(), tok)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Enforce the licensed number of tunnel servers by connection order.
	if lim := h.License.Limit(license.LimNodes); lim >= 0 {
		var rank int64
		h.M.DB.QueryRowContext(r.Context(), `SELECT COUNT(1) FROM nodes WHERE kind = 'tunnel' AND agent_token_hash IS NOT NULL AND id <= ?`, nodeID).Scan(&rank)
		if lim == 0 || rank > lim {
			h.M.SetNodeStatus(r.Context(), nodeID, "error", "سقف تعداد سرور در لایسنس", nil)
			http.Error(w, "license node limit reached", http.StatusForbidden)
			return
		}
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	ws.SetReadLimit(8 << 20)
	c := &conn{nodeID: nodeID, ws: ws}
	h.mu.Lock()
	if old := h.conns[nodeID]; old != nil {
		old.ws.Close()
	}
	h.conns[nodeID] = c
	h.mu.Unlock()
	slog.Info("agent connected", "node", nodeID, "remote", r.RemoteAddr)

	defer func() {
		ws.Close()
		h.mu.Lock()
		if h.conns[nodeID] == c {
			delete(h.conns, nodeID)
			h.M.SetNodeStatus(context.Background(), nodeID, "offline", "", nil)
		}
		h.mu.Unlock()
		slog.Info("agent disconnected", "node", nodeID)
	}()

	ws.SetReadDeadline(time.Now().Add(120 * time.Second))
	ws.SetPingHandler(func(data string) error {
		ws.SetReadDeadline(time.Now().Add(120 * time.Second))
		c.wmu.Lock()
		defer c.wmu.Unlock()
		return ws.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(10*time.Second))
	})
	ctx := context.Background()
	for {
		_, raw, err := ws.ReadMessage()
		if err != nil {
			return
		}
		ws.SetReadDeadline(time.Now().Add(120 * time.Second))
		var env agentproto.Envelope
		if json.Unmarshal(raw, &env) != nil {
			continue
		}
		switch env.Type {
		case agentproto.TypeHello:
			var hello agentproto.Hello
			json.Unmarshal(env.Data, &hello)
			detail := ""
			if !hello.NFT {
				detail = "nftables نصب نیست (apt install nftables)"
			}
			h.M.SetNodeStatus(ctx, nodeID, "online", detail, hello)
			h.sendState(ctx, c)
		case agentproto.TypeApplied:
			var a agentproto.Applied
			json.Unmarshal(env.Data, &a)
			if len(a.Errors) > 0 {
				h.M.SetNodeStatus(ctx, nodeID, "error", strings.Join(a.Errors, "; "), nil)
			} else {
				h.M.SetNodeStatus(ctx, nodeID, "online", "", nil)
			}
		case agentproto.TypeUsage:
			var u agentproto.Usage
			if json.Unmarshal(env.Data, &u) == nil {
				if err := h.M.ApplyAgentUsage(ctx, nodeID, u); err != nil {
					slog.Error("agent usage", "node", nodeID, "err", err)
				}
			}
		case agentproto.TypeEvents:
			var evs []agentproto.Event
			if json.Unmarshal(env.Data, &evs) == nil {
				h.M.ApplyAgentEvents(ctx, nodeID, evs)
			}
		case agentproto.TypeTunnels:
			var sts []agentproto.TunnelStatus
			if json.Unmarshal(env.Data, &sts) == nil {
				h.M.TunnelStatus(ctx, nodeID, sts)
			}
		case agentproto.TypePortsResult:
			if ch, ok := c.waiters.LoadAndDelete(env.ID); ok {
				ch.(chan json.RawMessage) <- env.Data
			}
		}
	}
}

func (h *Hub) sendState(ctx context.Context, c *conn) {
	st, err := h.M.NodeState(ctx, c.nodeID)
	if err != nil {
		// Send what could be built; the error is shown on the node.
		h.M.SetNodeStatus(ctx, c.nodeID, "error", err.Error(), nil)
	}
	if err := c.send(agentproto.TypeState, "", st); err != nil {
		slog.Warn("agent state push failed", "node", c.nodeID, "err", err)
	}
}

// Push sends a node its new state (debounced: bursts of changes → one push).
func (h *Hub) Push(nodeID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t := h.pending[nodeID]; t != nil {
		t.Reset(300 * time.Millisecond)
		return
	}
	h.pending[nodeID] = time.AfterFunc(300*time.Millisecond, func() {
		h.mu.Lock()
		delete(h.pending, nodeID)
		c := h.conns[nodeID]
		h.mu.Unlock()
		if c != nil {
			h.sendState(context.Background(), c)
		}
	})
}

func (h *Hub) Online(nodeID int64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conns[nodeID] != nil
}

// CheckPorts asks the agent which ports are already in use on its server.
func (h *Hub) CheckPorts(ctx context.Context, nodeID int64, ports []int, proto string) ([]int, error) {
	h.mu.Lock()
	c := h.conns[nodeID]
	h.seq++
	id := "p" + strconv.FormatInt(h.seq, 10)
	h.mu.Unlock()
	if c == nil {
		return nil, errors.New("agent offline")
	}
	ch := make(chan json.RawMessage, 1)
	c.waiters.Store(id, ch)
	defer c.waiters.Delete(id)
	if err := c.send(agentproto.TypeCheckPorts, id, agentproto.CheckPorts{Ports: ports, Proto: proto}); err != nil {
		return nil, err
	}
	select {
	case raw := <-ch:
		var res agentproto.PortsResult
		json.Unmarshal(raw, &res)
		return res.Busy, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
