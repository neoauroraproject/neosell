package agent

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"neosell/internal/agentproto"
	"neosell/internal/version"
)

// Config is /etc/neosell/agent.json, written by `neosell agent join`.
type Config struct {
	PanelURL   string `json:"panel_url"`
	Token      string `json:"token"`
	CertSHA256 string `json:"cert_sha256,omitempty"` // pin of the panel's TLS certificate (self-signed panels)
	DataDir    string `json:"data_dir"`
}

// Run enforces the stored state immediately (so a reboot never leaves sold
// ports open or unlimited), then stays connected to the panel forever.
func (a *Agent) Run(ctx context.Context, cfg Config) error {
	if errs := a.apply(ctx); len(errs) > 0 {
		logErrs(errs)
	}

	conn := &connHolder{}
	// Local loop: counters, quota/expiry cuts — independent of the panel link.
	go func() {
		t := time.NewTicker(tickEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			usage, events, _ := a.Tick()
			conn.send(agentproto.TypeUsage, "", usage)
			if len(events) > 0 {
				conn.send(agentproto.TypeEvents, "", events)
			}
			conn.send(agentproto.TypeTunnels, "", a.TunnelStatuses())
		}
	}()

	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := a.session(ctx, cfg, conn)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		slog.Warn("agent: disconnected from panel", "err", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
	return nil
}

type connHolder struct {
	mu sync.Mutex
	ws *websocket.Conn
}

func (c *connHolder) set(ws *websocket.Conn) {
	c.mu.Lock()
	c.ws = ws
	c.mu.Unlock()
}

func (c *connHolder) send(typ, id string, v any) error {
	msg, err := agentproto.Wrap(typ, id, v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ws == nil {
		return errors.New("not connected")
	}
	c.ws.SetWriteDeadline(time.Now().Add(15 * time.Second))
	return c.ws.WriteMessage(websocket.TextMessage, msg)
}

func dialer(cfg Config) *websocket.Dialer {
	d := &websocket.Dialer{HandshakeTimeout: 20 * time.Second, Proxy: http.ProxyFromEnvironment}
	if pin := strings.ToLower(strings.ReplaceAll(cfg.CertSHA256, ":", "")); pin != "" {
		// Self-signed panel: trust exactly the pinned certificate.
		d.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true,
			VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
				if len(raw) == 0 {
					return errors.New("no certificate")
				}
				sum := sha256.Sum256(raw[0])
				if hex.EncodeToString(sum[:]) != pin {
					return errors.New("panel certificate does not match the pinned fingerprint")
				}
				return nil
			},
		}
	}
	return d
}

func wsURL(panel string) string {
	u := strings.TrimRight(panel, "/")
	u = strings.Replace(u, "https://", "wss://", 1)
	u = strings.Replace(u, "http://", "ws://", 1)
	return u + "/agent/ws"
}

func (a *Agent) session(ctx context.Context, cfg Config, conn *connHolder) error {
	hdr := http.Header{"Authorization": {"Bearer " + cfg.Token}}
	ws, resp, err := dialer(cfg).DialContext(ctx, wsURL(cfg.PanelURL), hdr)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("%w (HTTP %d)", err, resp.StatusCode)
		}
		return err
	}
	defer ws.Close()
	ws.SetReadLimit(16 << 20)
	conn.set(ws)
	defer conn.set(nil)

	host, _ := os.Hostname()
	if err := conn.send(agentproto.TypeHello, "", agentproto.Hello{Proto: agentproto.Version, Version: version.Version,
		Arch: runtime.GOARCH, Hostname: host, IPs: a.ips(), NFT: a.nft.Available()}); err != nil {
		return err
	}
	slog.Info("agent: connected to panel", "url", cfg.PanelURL)
	if ev := a.PendingEvents(); len(ev) > 0 {
		conn.send(agentproto.TypeEvents, "", ev)
	}

	// Keepalive: the panel answers pings; a dead link is noticed within ~90s.
	ws.SetReadDeadline(time.Now().Add(90 * time.Second))
	ws.SetPongHandler(func(string) error { ws.SetReadDeadline(time.Now().Add(90 * time.Second)); return nil })
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				conn.mu.Lock()
				if conn.ws != nil {
					conn.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
				}
				conn.mu.Unlock()
			}
		}
	}()

	for {
		_, raw, err := ws.ReadMessage()
		if err != nil {
			return err
		}
		ws.SetReadDeadline(time.Now().Add(90 * time.Second))
		var env agentproto.Envelope
		if json.Unmarshal(raw, &env) != nil {
			continue
		}
		switch env.Type {
		case agentproto.TypeState:
			var s agentproto.State
			if err := json.Unmarshal(env.Data, &s); err != nil {
				conn.send(agentproto.TypeError, env.ID, err.Error())
				continue
			}
			res := a.SetState(ctx, s)
			logErrs(res.Errors)
			conn.send(agentproto.TypeApplied, env.ID, res)
			conn.send(agentproto.TypeTunnels, "", a.TunnelStatuses())
			usage, _, _ := a.Tick()
			conn.send(agentproto.TypeUsage, "", usage)
			a.DropTotals()
		case agentproto.TypeCheckPorts:
			var c agentproto.CheckPorts
			json.Unmarshal(env.Data, &c)
			conn.send(agentproto.TypePortsResult, env.ID, a.CheckPorts(c))
		}
	}
}
