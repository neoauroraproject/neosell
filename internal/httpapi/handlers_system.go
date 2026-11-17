package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"neosell/internal/audit"
	"neosell/internal/rbac"
	"neosell/internal/updater"
	"neosell/internal/version"
)

// Exit is how the panel restarts after an update (systemd brings it back).
// Replaced in tests.
var Exit = os.Exit

type updateCache struct {
	mu   sync.Mutex
	at   time.Time
	rel  *updater.Release
	err  string
	busy bool
}

var upd updateCache

func (s *Server) registerSystem(mux *http.ServeMux) {
	mux.Handle("GET /api/admin/system", s.admin(rbac.SettingsManage, s.handleSystemInfo))
	mux.Handle("POST /api/admin/system/update", s.owner(s.handleSystemUpdate))
}

func (s *Server) updater() *updater.Client {
	return &updater.Client{Repo: s.Cfg.UpdateRepo, Proxy: s.Cfg.OutboundProxy}
}

func (s *Server) latest(ctx context.Context, fresh bool) (*updater.Release, string) {
	upd.mu.Lock()
	defer upd.mu.Unlock()
	if !fresh && time.Since(upd.at) < 10*time.Minute && (upd.rel != nil || upd.err != "") {
		return upd.rel, upd.err
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	rel, err := s.updater().Latest(cctx)
	upd.at, upd.rel, upd.err = time.Now(), rel, ""
	if err != nil {
		upd.err = err.Error()
	}
	return upd.rel, upd.err
}

// GET /api/admin/system[?check=1] — version, latest release and server facts.
func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	rel, errMsg := s.latest(r.Context(), r.URL.Query().Get("check") == "1")
	out := map[string]any{
		"version": version.Version, "commit": version.Commit, "built": version.Date,
		"os": runtime.GOOS, "arch": runtime.GOARCH, "repo": s.Cfg.UpdateRepo,
		"listen": s.Cfg.Listen, "domain": s.Cfg.Domain, "tls": s.Cfg.TLS, "public_url": s.Cfg.PublicURL,
		"systemd": os.Getenv("INVOCATION_ID") != "", "can_update": currentUser(r).IsOwner,
	}
	if rel != nil {
		out["latest"] = rel.Tag
		out["notes"] = rel.Notes
		out["published_at"] = rel.PublishedAt
		out["available"] = updater.Newer(rel.Tag, version.Version)
		_, hasBin := rel.Assets[updater.AssetName()]
		out["has_binary"] = hasBin
	}
	if errMsg != "" {
		out["error"] = errMsg
	}
	writeJSON(w, 200, out)
}

// POST /api/admin/system/update — owner only. Verifies and swaps the binary,
// answers, then exits so systemd restarts the panel on the new version.
func (s *Server) handleSystemUpdate(w http.ResponseWriter, r *http.Request) {
	if runtime.GOOS != "linux" {
		writeErr(w, 400, "به‌روزرسانی خودکار فقط روی لینوکس")
		return
	}
	upd.mu.Lock()
	if upd.busy {
		upd.mu.Unlock()
		writeErr(w, 409, "به‌روزرسانی در حال انجام است")
		return
	}
	upd.busy = true
	upd.mu.Unlock()
	defer func() { upd.mu.Lock(); upd.busy = false; upd.mu.Unlock() }()

	rel, errMsg := s.latest(r.Context(), true)
	if rel == nil {
		writeErr(w, 502, errMsg)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	exe, _ = filepath.EvalSymlinks(exe)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := s.updater().Apply(ctx, rel, exe); err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	s.Audit.Write(r.Context(), audit.Entry{ActorID: currentUser(r).ID, Action: "system.update",
		Before: map[string]string{"version": version.Version}, After: map[string]string{"version": rel.Tag}, IP: clientIP(r)})
	underSystemd := os.Getenv("INVOCATION_ID") != ""
	writeJSON(w, 200, map[string]any{"ok": true, "version": strings.TrimPrefix(rel.Tag, "v"), "restarting": underSystemd})
	if underSystemd {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		go func() {
			time.Sleep(1500 * time.Millisecond)
			slog.Info("restarting after update", "version", rel.Tag)
			Exit(0)
		}()
	}
}
