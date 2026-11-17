package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"neosell/internal/audit"
	"neosell/internal/auth"
	"neosell/internal/license"
	"neosell/internal/rbac"
	"neosell/internal/settings"
	"neosell/internal/version"
)

var setupMu sync.Mutex

type meResponse struct {
	*auth.User
	Permissions []rbac.Perm `json:"permissions"`
}

func (s *Server) me(ctx context.Context, u *auth.User) meResponse {
	ps, _ := s.RBAC.Perms(ctx, u)
	if ps == nil {
		ps = []rbac.Perm{}
	}
	return meResponse{User: u, Permissions: ps}
}

// GET /api/bootstrap — everything the UI needs on first paint.
func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hasOwner, err := s.Users.HasOwner(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	st := s.License.Status()
	lic := map[string]any{"state": st.State, "modules": st.Modules}
	out := map[string]any{
		"needs_setup": !hasOwner,
		"license":     lic,
		"version":     version.Version,
	}
	pub, _ := s.Settings.All(ctx, true)
	out["settings"] = pub
	if u := s.loadUser(r); u != nil {
		out["user"] = s.me(ctx, u)
		if u.IsAdmin {
			lic["warning"], lic["reason"] = st.Warning, st.Reason
		}
	}
	writeJSON(w, 200, out)
}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

// POST /api/setup — create the owner account once, on a fresh install.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	setupMu.Lock()
	defer setupMu.Unlock()
	ctx := r.Context()
	if has, _ := s.Users.HasOwner(ctx); has {
		writeErr(w, http.StatusConflict, "پنل قبلاً راه‌اندازی شده است")
		return
	}
	var in credentials
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	u, err := s.Users.Create(ctx, auth.NewUser{Email: in.Email, Name: in.Name, Password: in.Password, IsOwner: true})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	tok, err := s.Users.CreateSession(ctx, u.ID, clientIP(r), r.UserAgent())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.setSessionCookie(w, r, tok)
	s.Audit.Write(ctx, audit.Entry{ActorID: u.ID, Action: "setup.owner_created", TargetType: "user", TargetID: strconv.FormatInt(u.ID, 10), IP: clientIP(r)})
	writeJSON(w, 200, s.me(ctx, u))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ip := clientIP(r)
	if !s.loginLimiter.Allow("ip:" + ip) {
		writeErr(w, http.StatusTooManyRequests, "تلاش‌های زیاد؛ چند دقیقه بعد دوباره امتحان کنید")
		return
	}
	var in credentials
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	u, err := s.Users.Authenticate(ctx, in.Email, in.Password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidLogin) || errors.Is(err, auth.ErrBlocked) {
			writeErr(w, http.StatusUnauthorized, err.Error())
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	s.loginLimiter.Reset("ip:" + ip)
	tok, err := s.Users.CreateSession(ctx, u.ID, ip, r.UserAgent())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.setSessionCookie(w, r, tok)
	if u.IsAdmin {
		s.Audit.Write(ctx, audit.Entry{ActorID: u.ID, Action: "auth.admin_login", IP: ip})
	}
	writeJSON(w, 200, s.me(ctx, u))
}

// POST /api/auth/register — customer sign-up (needs an active license and open registration).
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if writeLicenseErr(w, s.License.RequireActive()) {
		return
	}
	if mode := s.Settings.String(ctx, "registration.mode"); mode != "open" {
		writeErr(w, http.StatusForbidden, "ثبت‌نام در حال حاضر بسته است")
		return
	}
	if !s.loginLimiter.Allow("reg:" + clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "تلاش‌های زیاد؛ کمی بعد امتحان کنید")
		return
	}
	var in credentials
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	u, err := s.Users.Create(ctx, auth.NewUser{Email: in.Email, Name: in.Name, Password: in.Password})
	if err != nil {
		code := 400
		if errors.Is(err, auth.ErrEmailTaken) {
			code = http.StatusConflict
		}
		writeErr(w, code, err.Error())
		return
	}
	tok, err := s.Users.CreateSession(ctx, u.ID, clientIP(r), r.UserAgent())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.setSessionCookie(w, r, tok)
	writeJSON(w, 200, s.me(ctx, u))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.Users.DeleteSession(r.Context(), c.Value)
	}
	clearSessionCookie(w)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.me(r.Context(), currentUser(r)))
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := currentUser(r)
	var in struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if _, err := s.Users.Authenticate(ctx, u.Email, in.Current); err != nil {
		writeErr(w, 400, "رمز فعلی اشتباه است")
		return
	}
	if err := s.Users.SetPassword(ctx, u.ID, in.New); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	tok, _ := s.Users.CreateSession(ctx, u.ID, clientIP(r), r.UserAgent())
	s.setSessionCookie(w, r, tok)
	s.Audit.Write(ctx, audit.Entry{ActorID: u.ID, Action: "auth.password_changed", IP: clientIP(r)})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// --- license ---

func (s *Server) handleLicenseStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.License.Status())
}

func (s *Server) handleLicenseActivate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Key string `json:"key"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	st, err := s.License.Activate(ctx, in.Key)
	s.Audit.Write(r.Context(), audit.Entry{ActorID: currentUser(r).ID, Action: "license.activate",
		After: map[string]any{"ok": err == nil, "tier": st.Tier, "key_hint": st.KeyHint}, IP: clientIP(r)})
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error(), "status": st})
		return
	}
	writeJSON(w, 200, st)
}

func (s *Server) handleLicenseRecheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	err := s.License.Recheck(ctx)
	st := s.License.Status()
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error(), "status": st})
		return
	}
	writeJSON(w, 200, st)
}

func (s *Server) handleLicenseDeactivate(w http.ResponseWriter, r *http.Request) {
	if err := s.License.Deactivate(); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Audit.Write(r.Context(), audit.Entry{ActorID: currentUser(r).ID, Action: "license.deactivate", IP: clientIP(r)})
	writeJSON(w, 200, s.License.Status())
}

// --- settings ---

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	vals, err := s.Settings.All(r.Context(), false)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"defs": settings.Defs, "values": vals})
}

func (s *Server) handleSettingsPut(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in map[string]any
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	before := map[string]any{}
	for k := range in {
		if v, err := s.Settings.Get(ctx, k); err == nil {
			before[k] = v
		}
	}
	saved, err := s.Settings.SetMany(ctx, in)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.Audit.Write(ctx, audit.Entry{ActorID: currentUser(r).ID, Action: "settings.update", TargetType: "settings", Before: before, After: saved, IP: clientIP(r)})
	vals, _ := s.Settings.All(ctx, false)
	writeJSON(w, 200, map[string]any{"values": vals})
}

// --- audit ---

func (s *Server) handleAuditList(w http.ResponseWriter, r *http.Request) {
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := s.Audit.List(r.Context(), before, limit)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": rows})
}

// --- roles & admins ---

func (s *Server) handleRolesList(w http.ResponseWriter, r *http.Request) {
	roles, err := s.RBAC.ListRoles(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": roles, "permissions": rbac.All})
}

func (s *Server) handleRoleSave(w http.ResponseWriter, r *http.Request) {
	if writeLicenseErr(w, s.License.RequireModule(license.ModMultiAdmin)) {
		return
	}
	var in rbac.Role
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if id := r.PathValue("id"); id != "" {
		in.ID, _ = strconv.ParseInt(id, 10, 64)
	}
	if strings.TrimSpace(in.Name) == "" {
		writeErr(w, 400, "نام نقش را وارد کنید")
		return
	}
	id, err := s.RBAC.SaveRole(r.Context(), in)
	if err != nil {
		writeErr(w, 400, "نام نقش تکراری است")
		return
	}
	s.Audit.Write(r.Context(), audit.Entry{ActorID: currentUser(r).ID, Action: "role.save", TargetType: "role", TargetID: strconv.FormatInt(id, 10), After: in, IP: clientIP(r)})
	writeJSON(w, 200, map[string]int64{"id": id})
}

func (s *Server) handleRoleDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := s.RBAC.DeleteRole(r.Context(), id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Audit.Write(r.Context(), audit.Entry{ActorID: currentUser(r).ID, Action: "role.delete", TargetType: "role", TargetID: r.PathValue("id"), IP: clientIP(r)})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

type adminRow struct {
	ID      int64   `json:"id"`
	Email   string  `json:"email"`
	Name    string  `json:"name"`
	IsOwner bool    `json:"is_owner"`
	Status  string  `json:"status"`
	RoleIDs []int64 `json:"role_ids"`
}

func (s *Server) handleAdminsList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `SELECT u.id, COALESCE(u.email,''), u.name, u.is_owner, u.status,
		COALESCE((SELECT group_concat(role_id) FROM user_roles WHERE user_id = u.id), '')
		FROM users u WHERE u.is_admin = 1 ORDER BY u.is_owner DESC, u.id`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []adminRow{}
	for rows.Next() {
		var a adminRow
		var roles string
		if err := rows.Scan(&a.ID, &a.Email, &a.Name, &a.IsOwner, &a.Status, &roles); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		a.RoleIDs = []int64{}
		for _, p := range strings.Split(roles, ",") {
			if id, err := strconv.ParseInt(p, 10, 64); err == nil {
				a.RoleIDs = append(a.RoleIDs, id)
			}
		}
		out = append(out, a)
	}
	writeJSON(w, 200, map[string]any{"items": out, "limit": s.License.Limit(license.LimAdmins)})
}

func (s *Server) countStaff(ctx context.Context) int64 {
	var n int64
	s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM users WHERE is_admin = 1 AND is_owner = 0 AND status = 'active'`).Scan(&n)
	return n
}

func (s *Server) handleAdminCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if writeLicenseErr(w, s.License.RequireModule(license.ModMultiAdmin)) {
		return
	}
	if writeLicenseErr(w, s.License.RequireBelow(license.LimAdmins, s.countStaff(ctx))) {
		return
	}
	var in struct {
		credentials
		RoleIDs []int64 `json:"role_ids"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	u, err := s.Users.Create(ctx, auth.NewUser{Email: in.Email, Name: in.Name, Password: in.Password, IsAdmin: true})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.RBAC.SetUserRoles(ctx, u.ID, in.RoleIDs); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.Audit.Write(ctx, audit.Entry{ActorID: currentUser(r).ID, Action: "admin.create", TargetType: "user", TargetID: strconv.FormatInt(u.ID, 10),
		After: map[string]any{"email": u.Email, "role_ids": in.RoleIDs}, IP: clientIP(r)})
	writeJSON(w, 200, map[string]int64{"id": u.ID})
}

func (s *Server) handleAdminUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	target, err := s.Users.ByID(ctx, id)
	if err != nil || !target.IsAdmin {
		writeErr(w, 404, "ادمین پیدا نشد")
		return
	}
	if target.IsOwner {
		writeErr(w, 400, "مالک پنل قابل ویرایش نیست")
		return
	}
	var in struct {
		RoleIDs []int64 `json:"role_ids"`
		Status  string  `json:"status"` // active | blocked
		Remove  bool    `json:"remove"` // demote to a normal user
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if in.Remove {
		s.DB.ExecContext(ctx, `UPDATE users SET is_admin = 0 WHERE id = ?`, id)
		s.DB.ExecContext(ctx, `DELETE FROM user_roles WHERE user_id = ?`, id)
		s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id)
	} else {
		if in.Status == "active" || in.Status == "blocked" {
			if in.Status == "active" && target.Status != "active" {
				if writeLicenseErr(w, s.License.RequireBelow(license.LimAdmins, s.countStaff(ctx))) {
					return
				}
			}
			s.DB.ExecContext(ctx, `UPDATE users SET status = ? WHERE id = ?`, in.Status, id)
			if in.Status == "blocked" {
				s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id)
			}
		}
		if in.RoleIDs != nil {
			if err := s.RBAC.SetUserRoles(ctx, id, in.RoleIDs); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
		}
	}
	s.Audit.Write(ctx, audit.Entry{ActorID: currentUser(r).ID, Action: "admin.update", TargetType: "user", TargetID: strconv.FormatInt(id, 10), After: in, IP: clientIP(r)})
	writeJSON(w, 200, map[string]bool{"ok": true})
}
