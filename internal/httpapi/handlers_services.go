package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"neosell/internal/audit"
	"neosell/internal/license"
	"neosell/internal/payments"
	"neosell/internal/pricing"
	"neosell/internal/rbac"
	"neosell/internal/services"
)

func (s *Server) registerServices(mux *http.ServeMux) {
	// customer
	mux.Handle("GET /api/services", s.user(s.handleMyServices))
	mux.Handle("GET /api/services/{id}", s.user(s.handleMyService))
	mux.Handle("GET /api/services/{id}/usage", s.user(s.handleServiceUsage))
	mux.Handle("POST /api/services/{id}/rotate-sub", s.user(s.handleRotateSub))
	mux.Handle("POST /api/orders/quote", s.user(s.handleOrderQuote))
	mux.Handle("POST /api/orders", s.user(s.handleOrderPlace))
	mux.Handle("GET /api/orders", s.user(s.handleMyOrders))
	mux.Handle("GET /api/notifications", s.user(s.handleNotifications))
	mux.Handle("POST /api/notifications/read", s.user(s.handleNotificationsRead))
	mux.Handle("POST /api/me/telegram-link", s.user(s.handleTelegramLink))
	mux.Handle("DELETE /api/me/telegram", s.user(s.handleTelegramUnlink))
	mux.HandleFunc("GET /api/auth/telegram-login", s.handleTelegramLogin)
	mux.Handle("GET /api/tickets", s.user(s.handleMyTickets))
	mux.Handle("POST /api/tickets", s.user(s.handleTicketCreate))
	mux.Handle("GET /api/tickets/{id}", s.user(s.handleTicketGet(false)))
	mux.Handle("POST /api/tickets/{id}/reply", s.user(s.handleTicketReply(false)))

	// public
	mux.HandleFunc("GET /sub/{token}", s.handleSub)
	mux.HandleFunc("GET /sub/{token}/{loc}", s.handleSub)
	mux.HandleFunc("GET /i/{token}", s.handleInstall)
	mux.HandleFunc("GET /agent.sh", s.handleAgentScript)
	mux.HandleFunc("GET /dl/{name}", s.handleDownload)
	if s.AgentHub != nil {
		mux.Handle("GET /agent/ws", s.AgentHub)
	}

	// admin
	mux.Handle("GET /api/admin/stats", s.admin("", s.handleStats))
	mux.Handle("GET /api/admin/nodes", s.admin(rbac.NodesManage, s.handleNodes))
	mux.Handle("POST /api/admin/nodes", s.admin(rbac.NodesManage, s.handleNodeSave))
	mux.Handle("PUT /api/admin/nodes/{id}", s.admin(rbac.NodesManage, s.handleNodeSave))
	mux.Handle("DELETE /api/admin/nodes/{id}", s.admin(rbac.NodesManage, s.handleNodeDelete))
	mux.Handle("POST /api/admin/nodes/{id}/agent-token", s.admin(rbac.NodesManage, s.handleAgentToken))
	mux.Handle("GET /api/admin/nodes/{id}/inbounds", s.admin(rbac.NodesManage, s.handleNodeInbounds))
	register(s, mux, resource[services.CoreTemplate]{path: "/api/admin/cores", perm: rbac.NodesManage, kind: "core",
		list: s.Services.CoreTemplates, save: s.Services.SaveCoreTemplate,
		del: func(context.Context, int64) error {
			return errors.New("هسته‌ها حذف نمی‌شوند؛ غیرفعالشان کنید")
		},
		withID: func(v *services.CoreTemplate, id int64) { v.ID = id }})
	register(s, mux, resource[services.SharedTunnel]{path: "/api/admin/tunnels", perm: rbac.NodesManage, kind: "tunnel",
		list: s.sharedTunnelsWithCmd, save: s.Services.SaveSharedTunnel, del: s.Services.DeleteSharedTunnel,
		withID: func(v *services.SharedTunnel, id int64) { v.ID = id }})
	register(s, mux, resource[services.OutboundTemplate]{path: "/api/admin/outbounds", perm: rbac.NodesManage, kind: "outbound_template",
		list: s.Services.OutboundTemplates, save: s.Services.SaveOutboundTemplate, del: s.Services.DeleteOutboundTemplate,
		withID: func(v *services.OutboundTemplate, id int64) { v.ID = id }})
	mux.Handle("GET /api/admin/services", s.admin(rbac.ServicesManage, s.handleAdminServices))
	mux.Handle("GET /api/admin/services/{id}", s.admin(rbac.ServicesManage, s.handleAdminService))
	mux.Handle("PUT /api/admin/services/{id}", s.admin(rbac.ServicesManage, s.handleAdminServiceUpdate))
	mux.Handle("DELETE /api/admin/services/{id}", s.admin(rbac.ServicesManage, s.handleAdminServiceDelete))
	mux.Handle("GET /api/admin/orders", s.admin(rbac.OrdersView, s.handleAdminOrders))
	mux.Handle("POST /api/admin/orders/{id}/refund", s.admin(rbac.UsersWallet, s.handleAdminRefund))
	mux.Handle("GET /api/admin/tickets", s.admin(rbac.TicketsManage, s.handleAdminTickets))
	mux.Handle("GET /api/admin/tickets/{id}", s.admin(rbac.TicketsManage, s.handleTicketGet(true)))
	mux.Handle("POST /api/admin/tickets/{id}/reply", s.admin(rbac.TicketsManage, s.handleTicketReply(true)))
	mux.Handle("POST /api/admin/tickets/{id}/close", s.admin(rbac.TicketsManage, s.handleTicketClose))
	mux.Handle("POST /api/admin/broadcast", s.admin(rbac.BroadcastSend, s.handleBroadcast))
}

func writeSvcErr(w http.ResponseWriter, err error) {
	var oe services.OrderError
	var pe pricing.Error
	switch {
	case errors.As(err, &oe):
		writeErr(w, 400, oe.Msg)
	case errors.As(err, &pe):
		writeErr(w, 400, pe.Msg)
	case errors.Is(err, services.ErrNotFound):
		writeErr(w, 404, "پیدا نشد")
	default:
		if !writeLicenseErr(w, err) {
			writeErr(w, 500, err.Error())
		}
	}
}

// ---------- customer: services & orders ----------

func (s *Server) handleMyServices(w http.ResponseWriter, r *http.Request) {
	list, err := s.Services.Services(r.Context(), services.ListFilter{UserID: currentUser(r).ID, Limit: 100})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": list})
}

func (s *Server) handleMyService(w http.ResponseWriter, r *http.Request) {
	v, err := s.Services.ServiceFor(r.Context(), pathID(r), currentUser(r).ID)
	if err != nil {
		writeSvcErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) handleServiceUsage(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	uid := u.ID
	if u.IsAdmin && s.RBAC.Can(r.Context(), u, rbac.ServicesManage) {
		uid = 0
	}
	if _, err := s.Services.ServiceFor(r.Context(), pathID(r), uid); err != nil {
		writeSvcErr(w, err)
		return
	}
	daily := r.URL.Query().Get("range") == "30d"
	hours := 24
	if daily {
		hours = 30 * 24
	}
	pts, err := s.Services.UsageSeries(r.Context(), pathID(r), hours, daily)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"points": pts})
}

func (s *Server) handleRotateSub(w http.ResponseWriter, r *http.Request) {
	url, err := s.Services.RotateSub(r.Context(), pathID(r), currentUser(r).ID)
	if err != nil {
		writeSvcErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"sub_url": url})
}

func (s *Server) handleOrderQuote(w http.ResponseWriter, r *http.Request) {
	var req services.OrderRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if writeLicenseErr(w, s.License.RequireActive()) {
		return
	}
	_, q, err := s.Services.Quote(r.Context(), currentUser(r).ID, &req)
	if err != nil {
		writeSvcErr(w, err)
		return
	}
	bal, _ := s.Wallet.Balance(r.Context(), currentUser(r).ID)
	writeJSON(w, 200, map[string]any{"quote": q, "selection": req.Selection, "balance": bal})
}

func (s *Server) handleOrderPlace(w http.ResponseWriter, r *http.Request) {
	var req services.OrderRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	o, err := s.Services.PlaceOrder(r.Context(), currentUser(r).ID, req)
	if err != nil {
		writeSvcErr(w, err)
		return
	}
	writeJSON(w, 200, o)
}

func (s *Server) handleMyOrders(w http.ResponseWriter, r *http.Request) {
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	list, err := s.Services.Orders(r.Context(), currentUser(r).ID, before, 30)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": list})
}

// ---------- notifications & Telegram ----------

func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request) {
	if s.Notifications == nil {
		writeJSON(w, 200, map[string]any{"items": []any{}, "unread": 0})
		return
	}
	items, unread, err := s.Notifications.List(r.Context(), currentUser(r).ID, 50)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "unread": unread})
}

func (s *Server) handleNotificationsRead(w http.ResponseWriter, r *http.Request) {
	if s.Notifications != nil {
		s.Notifications.MarkRead(r.Context(), currentUser(r).ID)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleTelegramLink(w http.ResponseWriter, r *http.Request) {
	name := ""
	if s.BotName != nil {
		name = s.BotName()
	}
	if name == "" {
		writeErr(w, 400, "ربات تلگرام فعال نیست")
		return
	}
	tok, err := s.Users.OneTimeToken(r.Context(), currentUser(r).ID, "link_", 600)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"url": "https://t.me/" + name + "?start=" + tok})
}

func (s *Server) handleTelegramUnlink(w http.ResponseWriter, r *http.Request) {
	s.Users.UnlinkTelegram(r.Context(), currentUser(r).ID)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// GET /api/auth/telegram-login?token= — one-time login link sent by the bot.
func (s *Server) handleTelegramLogin(w http.ResponseWriter, r *http.Request) {
	uid, err := s.Users.UseToken(r.Context(), r.URL.Query().Get("token"), "login_")
	if err != nil {
		http.Redirect(w, r, "/login?error=link", http.StatusFound)
		return
	}
	tok, err := s.Users.CreateSession(r.Context(), uid, clientIP(r), r.UserAgent())
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	s.setSessionCookie(w, r, tok)
	http.Redirect(w, r, "/", http.StatusFound)
}

// ---------- tickets ----------

type ticket struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"user_id"`
	UserEmail string `json:"user_email,omitempty"`
	Subject   string `json:"subject"`
	Status    string `json:"status"`
	UpdatedAt int64  `json:"updated_at"`
}

type ticketMsg struct {
	ID        int64  `json:"id"`
	IsStaff   bool   `json:"is_staff"`
	Body      string `json:"body"`
	CreatedAt int64  `json:"created_at"`
}

func (s *Server) listTickets(ctx context.Context, uid int64, status string) ([]ticket, error) {
	q := `SELECT t.id, t.user_id, COALESCE(u.email, u.telegram_username, ''), t.subject, t.status, t.updated_at FROM tickets t LEFT JOIN users u ON u.id = t.user_id WHERE 1=1`
	args := []any{}
	if uid > 0 {
		q += ` AND t.user_id = ?`
		args = append(args, uid)
	}
	if status != "" {
		q += ` AND t.status = ?`
		args = append(args, status)
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY t.updated_at DESC LIMIT 100`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ticket{}
	for rows.Next() {
		var t ticket
		rows.Scan(&t.ID, &t.UserID, &t.UserEmail, &t.Subject, &t.Status, &t.UpdatedAt)
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Server) handleMyTickets(w http.ResponseWriter, r *http.Request) {
	list, err := s.listTickets(r.Context(), currentUser(r).ID, "")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": list})
}

func (s *Server) handleAdminTickets(w http.ResponseWriter, r *http.Request) {
	list, err := s.listTickets(r.Context(), 0, r.URL.Query().Get("status"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": list})
}

func (s *Server) handleTicketCreate(w http.ResponseWriter, r *http.Request) {
	var in struct{ Subject, Body string }
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	in.Subject, in.Body = strings.TrimSpace(in.Subject), strings.TrimSpace(in.Body)
	if in.Subject == "" || in.Body == "" || len(in.Body) > 5000 {
		writeErr(w, 400, "موضوع و متن پیام را وارد کنید")
		return
	}
	if !s.loginLimiter.Allow("ticket:" + strconv.FormatInt(currentUser(r).ID, 10)) {
		writeErr(w, 429, "کمی بعد دوباره امتحان کنید")
		return
	}
	now := time.Now().Unix()
	res, err := s.DB.ExecContext(r.Context(), `INSERT INTO tickets(user_id, subject, status, created_at, updated_at) VALUES (?, ?, 'open', ?, ?)`, currentUser(r).ID, in.Subject, now, now)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	s.DB.ExecContext(r.Context(), `INSERT INTO ticket_messages(ticket_id, user_id, is_staff, body, created_at) VALUES (?, ?, 0, ?, ?)`, id, currentUser(r).ID, in.Body, now)
	writeJSON(w, 200, map[string]int64{"id": id})
}

func (s *Server) handleTicketGet(staff bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var t ticket
		err := s.DB.QueryRowContext(r.Context(), `SELECT t.id, t.user_id, COALESCE(u.email, u.telegram_username, ''), t.subject, t.status, t.updated_at
			FROM tickets t LEFT JOIN users u ON u.id = t.user_id WHERE t.id = ?`, pathID(r)).Scan(&t.ID, &t.UserID, &t.UserEmail, &t.Subject, &t.Status, &t.UpdatedAt)
		if err != nil || !staff && t.UserID != currentUser(r).ID {
			writeErr(w, 404, "تیکت پیدا نشد")
			return
		}
		rows, err := s.DB.QueryContext(r.Context(), `SELECT id, is_staff, body, created_at FROM ticket_messages WHERE ticket_id = ? ORDER BY id`, t.ID)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		defer rows.Close()
		msgs := []ticketMsg{}
		for rows.Next() {
			var m ticketMsg
			rows.Scan(&m.ID, &m.IsStaff, &m.Body, &m.CreatedAt)
			msgs = append(msgs, m)
		}
		writeJSON(w, 200, map[string]any{"ticket": t, "messages": msgs})
	}
}

func (s *Server) handleTicketReply(staff bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Body string }
		if err := readJSON(r, &in); err != nil || strings.TrimSpace(in.Body) == "" || len(in.Body) > 5000 {
			writeErr(w, 400, "متن پیام را وارد کنید")
			return
		}
		var owner int64
		var status string
		if s.DB.QueryRowContext(r.Context(), `SELECT user_id, status FROM tickets WHERE id = ?`, pathID(r)).Scan(&owner, &status) != nil || !staff && owner != currentUser(r).ID {
			writeErr(w, 404, "تیکت پیدا نشد")
			return
		}
		now := time.Now().Unix()
		next := "open"
		if staff {
			next = "answered"
		}
		s.DB.ExecContext(r.Context(), `INSERT INTO ticket_messages(ticket_id, user_id, is_staff, body, created_at) VALUES (?, ?, ?, ?, ?)`, pathID(r), currentUser(r).ID, staff, strings.TrimSpace(in.Body), now)
		s.DB.ExecContext(r.Context(), `UPDATE tickets SET status = ?, updated_at = ? WHERE id = ?`, next, now, pathID(r))
		if staff && s.Notifications != nil {
			s.Notifications.Notify(r.Context(), owner, "ticket.answer", "پاسخ پشتیبانی", fmt.Sprintf("به تیکت #%d پاسخ داده شد.", pathID(r)))
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	}
}

func (s *Server) handleTicketClose(w http.ResponseWriter, r *http.Request) {
	s.DB.ExecContext(r.Context(), `UPDATE tickets SET status = 'closed', updated_at = ? WHERE id = ?`, time.Now().Unix(), pathID(r))
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// POST /api/admin/broadcast {title, body} — to every active customer (panel + Telegram).
func (s *Server) handleBroadcast(w http.ResponseWriter, r *http.Request) {
	var in struct{ Title, Body string }
	if err := readJSON(r, &in); err != nil || strings.TrimSpace(in.Title) == "" {
		writeErr(w, 400, "عنوان پیام را وارد کنید")
		return
	}
	if s.Notifications == nil {
		writeErr(w, 500, "notifications unavailable")
		return
	}
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id FROM users WHERE status = 'active'`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	go func() {
		for _, id := range ids {
			s.Notifications.Notify(context.Background(), id, "broadcast", in.Title, in.Body)
			time.Sleep(40 * time.Millisecond) // stay under Telegram's ~30 msg/s
		}
	}()
	s.Audit.Write(r.Context(), audit.Entry{ActorID: currentUser(r).ID, Action: "broadcast", After: in, IP: clientIP(r)})
	writeJSON(w, 200, map[string]int{"recipients": len(ids)})
}

// ---------- public: subscription, install scripts, downloads ----------

func (s *Server) handleSub(w http.ResponseWriter, r *http.Request) {
	if !s.subLimiter().Allow("sub:" + clientIP(r)) {
		http.Error(w, "too many requests", 429)
		return
	}
	var loc int64
	if l := r.PathValue("loc"); l != "" {
		loc, _ = strconv.ParseInt(l, 10, 64)
	}
	sub, err := s.Services.Subscription(r.Context(), r.PathValue("token"), loc)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Subscription-Userinfo", fmt.Sprintf("upload=%d; download=%d; total=%d; expire=%d", sub.Up, sub.Down, sub.Total, sub.Expire))
	w.Header().Set("Profile-Title", "base64:"+base64.StdEncoding.EncodeToString([]byte(sub.Title)))
	w.Header().Set("Profile-Update-Interval", "12")
	switch r.URL.Query().Get("format") {
	case "json", "xray":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]any{"outbounds": sub.Outbounds})
	case "raw":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(strings.Join(sub.Links, "\n")))
	default:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(base64.StdEncoding.EncodeToString([]byte(strings.Join(sub.Links, "\n")))))
	}
}

func (s *Server) handleInstall(w http.ResponseWriter, r *http.Request) {
	script, err := s.Services.InstallScript(r.Context(), r.PathValue("token"))
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(404)
		w.Write([]byte("#!/bin/sh\necho 'invalid or expired install link'\nexit 1\n"))
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(script))
}

// GET /dl/neosell-linux-<arch> serves this very binary (when the arch matches),
// so tunnel servers install the agent from the panel with no external download.
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("name") != "neosell-linux-"+runtime.GOARCH || runtime.GOOS != "linux" {
		http.Error(w, "this panel only serves neosell-linux-"+runtime.GOARCH+"; install other architectures from the release page", 404)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		http.Error(w, "unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, exe)
}

const agentScript = `#!/usr/bin/env bash
# Neo Agent installer: bash <(curl …/agent.sh) <panel-url> <token> [cert-pin]
set -euo pipefail
PANEL="${1:?panel url}"; TOKEN="${2:?agent token}"; PIN="${3:-}"; SPKI="${4:-}"
[ "$(id -u)" = 0 ] || { echo "run as root (sudo)"; exit 1; }
case "$(uname -m)" in x86_64|amd64) ARCH=amd64;; aarch64|arm64) ARCH=arm64;; *) echo "unsupported arch"; exit 1;; esac
if ! command -v nft >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then DEBIAN_FRONTEND=noninteractive apt-get install -y nftables >/dev/null; fi
fi
CURL=(curl -fsSL)
if [ -n "$SPKI" ]; then CURL+=(--insecure --pinnedpubkey "sha256//$SPKI"); fi
"${CURL[@]}" "$PANEL/dl/neosell-linux-$ARCH" -o /usr/local/bin/neosell.tmp
chmod +x /usr/local/bin/neosell.tmp && mv /usr/local/bin/neosell.tmp /usr/local/bin/neosell
/usr/local/bin/neosell agent join -panel "$PANEL" -token "$TOKEN" -pin "$PIN"
`

func (s *Server) handleAgentScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Write([]byte(agentScript))
}

// ---------- admin: servers ----------

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.Services.Stats(r.Context()))
}

func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	list, err := s.Services.Nodes(r.Context(), r.URL.Query().Get("kind"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for i := range list {
		if list[i].Kind == "tunnel" && s.AgentHub != nil && s.AgentHub.Online(list[i].ID) && list[i].Status == "offline" {
			list[i].Status = "online"
		}
	}
	writeJSON(w, 200, map[string]any{"items": list, "limit_nodes": s.License.Limit(license.LimNodes), "limit_xui": s.License.Limit(license.LimXUIPanels)})
}

func (s *Server) handleNodeSave(w http.ResponseWriter, r *http.Request) {
	var n services.Node
	if err := readJSON(r, &n); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	n.ID = pathID(r)
	if n.ID == 0 && n.Kind == "xui" {
		var c int64
		s.DB.QueryRowContext(r.Context(), `SELECT COUNT(1) FROM nodes WHERE kind = 'xui'`).Scan(&c)
		if writeLicenseErr(w, s.License.RequireBelow(license.LimXUIPanels, c)) {
			return
		}
	}
	if n.ID == 0 && n.Kind == "tunnel" {
		var c int64
		s.DB.QueryRowContext(r.Context(), `SELECT COUNT(1) FROM nodes WHERE kind = 'tunnel'`).Scan(&c)
		if writeLicenseErr(w, s.License.RequireBelow(license.LimNodes, c)) {
			return
		}
	}
	id, err := s.Services.SaveNode(r.Context(), n)
	if err != nil {
		writeSvcErr(w, err)
		return
	}
	n.Secrets = nil // never log secrets
	s.Audit.Write(r.Context(), audit.Entry{ActorID: currentUser(r).ID, Action: "node.save", TargetType: "node", TargetID: strconv.FormatInt(id, 10), After: n, IP: clientIP(r)})
	writeJSON(w, 200, map[string]int64{"id": id})
}

func (s *Server) handleNodeDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Services.DeleteNode(r.Context(), pathID(r)); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.Audit.Write(r.Context(), audit.Entry{ActorID: currentUser(r).ID, Action: "node.delete", TargetType: "node", TargetID: r.PathValue("id"), IP: clientIP(r)})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// POST /api/admin/nodes/{id}/agent-token — (re)issue the agent token and
// return the one-line install command for the tunnel server.
func (s *Server) handleAgentToken(w http.ResponseWriter, r *http.Request) {
	tok, err := s.Services.RotateAgentToken(r.Context(), pathID(r))
	if err != nil {
		writeSvcErr(w, err)
		return
	}
	base := s.publicURL(r)
	curl := "curl -fsSL"
	args := fmt.Sprintf("%s %s", base, tok)
	if s.CertPin != "" && strings.HasPrefix(base, "https://") {
		curl = "curl -fsSL --insecure --pinnedpubkey sha256//" + s.CertSPKI
		args += " " + s.CertPin + " " + s.CertSPKI
	}
	s.Audit.Write(r.Context(), audit.Entry{ActorID: currentUser(r).ID, Action: "node.agent_token", TargetType: "node", TargetID: r.PathValue("id"), IP: clientIP(r)})
	writeJSON(w, 200, map[string]string{"token": tok, "command": fmt.Sprintf("bash <(%s %s/agent.sh) %s", curl, base, args)})
}

func (s *Server) handleNodeInbounds(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	c, err := s.Services.XUI(ctx, pathID(r))
	if err != nil {
		writeSvcErr(w, err)
		return
	}
	ins, err := c.Inbounds(ctx)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	type row struct {
		ID       int64  `json:"id"`
		Remark   string `json:"remark"`
		Protocol string `json:"protocol"`
		Port     int    `json:"port"`
		Enable   bool   `json:"enable"`
		Clients  int    `json:"clients"`
	}
	out := []row{}
	for _, in := range ins {
		out = append(out, row{in.ID, in.Remark, in.Protocol, in.Port, in.Enable, len(in.ClientStats)})
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

func (s *Server) sharedTunnelsWithCmd(ctx context.Context) ([]services.SharedTunnel, error) {
	list, err := s.Services.SharedTunnels(ctx)
	for i := range list {
		if list[i].PeerToken != "" {
			list[i].PeerToken = fmt.Sprintf("bash <(curl -fsSL %s/i/%s)", strings.TrimRight(s.Services.PublicURL, "/"), list[i].PeerToken)
		}
		list[i].Secret = ""
	}
	return list, err
}

// ---------- admin: services & orders ----------

func (s *Server) handleAdminServices(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	uid, _ := strconv.ParseInt(q.Get("user_id"), 10, 64)
	nid, _ := strconv.ParseInt(q.Get("node_id"), 10, 64)
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	f := services.ListFilter{Status: q.Get("status"), Type: q.Get("type"), NodeID: nid, Query: strings.TrimSpace(q.Get("q")), BeforeID: before}
	list, err := s.Services.Services(r.Context(), f)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if uid > 0 {
		list, _ = s.Services.Services(r.Context(), services.ListFilter{UserID: uid, Limit: 100})
	}
	writeJSON(w, 200, map[string]any{"items": list})
}

func (s *Server) handleAdminService(w http.ResponseWriter, r *http.Request) {
	v, err := s.Services.ServiceFor(r.Context(), pathID(r), 0)
	if err != nil {
		writeSvcErr(w, err)
		return
	}
	orders, _ := s.serviceOrders(r.Context(), v.ID)
	writeJSON(w, 200, map[string]any{"service": v, "orders": orders})
}

func (s *Server) serviceOrders(ctx context.Context, sid int64) ([]services.Order, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM orders WHERE service_id = ? ORDER BY id DESC`, sid)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	out := []services.Order{}
	for _, id := range ids {
		if o, err := s.Services.Order(ctx, id); err == nil {
			out = append(out, *o)
		}
	}
	return out, nil
}

func (s *Server) handleAdminServiceUpdate(w http.ResponseWriter, r *http.Request) {
	var c services.AdminChange
	if err := readJSON(r, &c); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	before, _ := s.Services.ServiceFor(r.Context(), pathID(r), 0)
	if err := s.Services.AdminUpdate(r.Context(), pathID(r), c); err != nil {
		writeSvcErr(w, err)
		return
	}
	after, _ := s.Services.ServiceFor(r.Context(), pathID(r), 0)
	s.Audit.Write(r.Context(), audit.Entry{ActorID: currentUser(r).ID, Action: "service.update", TargetType: "service", TargetID: r.PathValue("id"), Before: before, After: after, IP: clientIP(r)})
	writeJSON(w, 200, after)
}

func (s *Server) handleAdminServiceDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.Services.Delete(r.Context(), pathID(r), "حذف توسط ادمین"); err != nil {
		writeSvcErr(w, err)
		return
	}
	s.Audit.Write(r.Context(), audit.Entry{ActorID: currentUser(r).ID, Action: "service.delete", TargetType: "service", TargetID: r.PathValue("id"), IP: clientIP(r)})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleAdminOrders(w http.ResponseWriter, r *http.Request) {
	uid, _ := strconv.ParseInt(r.URL.Query().Get("user_id"), 10, 64)
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	list, err := s.Services.Orders(r.Context(), uid, before, 50)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": list})
}

func (s *Server) handleAdminRefund(w http.ResponseWriter, r *http.Request) {
	var in struct{ Reason string }
	readJSON(r, &in)
	if strings.TrimSpace(in.Reason) == "" {
		in.Reason = "بازگشت وجه توسط ادمین"
	}
	if err := s.Services.Refund(r.Context(), pathID(r), in.Reason); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.Audit.Write(r.Context(), audit.Entry{ActorID: currentUser(r).ID, Action: "order.refund", TargetType: "order", TargetID: r.PathValue("id"), After: in, IP: clientIP(r)})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// publicURL: the configured public URL, else the request's own origin.
func (s *Server) publicURL(r *http.Request) string {
	if u := strings.TrimRight(s.Services.PublicURL, "/"); u != "" {
		return u
	}
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

var _ = payments.StatusApproved
