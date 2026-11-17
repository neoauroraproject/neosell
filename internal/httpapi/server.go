// Package httpapi serves the JSON API and the embedded web UI.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"neosell/internal/audit"
	"neosell/internal/auth"
	"neosell/internal/catalog"
	"neosell/internal/config"
	"neosell/internal/db"
	"neosell/internal/jobs"
	"neosell/internal/license"
	"neosell/internal/notify"
	"neosell/internal/payments"
	"neosell/internal/rbac"
	"neosell/internal/services"
	"neosell/internal/settings"
	"neosell/internal/wallet"
)

const cookieName = "ns_session"

// AgentHub is the agent WebSocket endpoint (agentgw.Hub).
type AgentHub interface {
	http.Handler
	Online(nodeID int64) bool
}

type Server struct {
	Cfg      config.Config
	DB       *db.DB
	Users    *auth.Store
	RBAC     *rbac.Store
	Settings *settings.Store
	Audit    *audit.Log
	License  *license.Manager
	Jobs     *jobs.Queue
	Catalog  *catalog.Store
	Wallet   *wallet.Wallet
	Payments *payments.Service
	Web      fs.FS // built SPA (index.html + assets); may be nil in tests
	// Notify receives admin-facing events (receipt submitted …).
	Notify func(ctx context.Context, event string, payload any)

	Services      *services.Manager
	AgentHub      AgentHub
	Notifications *notify.Center
	BotName       func() string
	// CertPin (hex sha256 of the certificate) and CertSPKI (base64 sha256 of
	// the public key) let agents trust a self-signed panel certificate.
	CertPin, CertSPKI string

	subLim *limiter

	loginLimiter *limiter
}

func (s *Server) subLimiter() *limiter { return s.subLim }

func (s *Server) Handler() http.Handler {
	s.loginLimiter = newLimiter(10, 15*time.Minute)
	s.subLim = newLimiter(120, time.Minute)
	mux := http.NewServeMux()

	// public
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"ok": "true"}) })
	mux.HandleFunc("GET /api/bootstrap", s.handleBootstrap)
	mux.HandleFunc("GET /api/public/catalog", s.handlePublicCatalog)
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/register", s.handleRegister)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)

	// signed-in
	mux.Handle("GET /api/me", s.user(s.handleMe))
	mux.Handle("POST /api/me/password", s.user(s.handleChangePassword))

	// license (owner only for changes)
	mux.Handle("GET /api/admin/license", s.admin("", s.handleLicenseStatus))
	mux.Handle("POST /api/admin/license/activate", s.owner(s.handleLicenseActivate))
	mux.Handle("POST /api/admin/license/recheck", s.owner(s.handleLicenseRecheck))
	mux.Handle("POST /api/admin/license/deactivate", s.owner(s.handleLicenseDeactivate))

	// admin
	mux.Handle("GET /api/admin/settings", s.admin(rbac.SettingsManage, s.handleSettingsGet))
	mux.Handle("PUT /api/admin/settings", s.admin(rbac.SettingsManage, s.handleSettingsPut))
	mux.Handle("GET /api/admin/audit", s.admin(rbac.AuditView, s.handleAuditList))
	mux.Handle("GET /api/admin/roles", s.admin(rbac.AdminsManage, s.handleRolesList))
	mux.Handle("POST /api/admin/roles", s.admin(rbac.AdminsManage, s.handleRoleSave))
	mux.Handle("PUT /api/admin/roles/{id}", s.admin(rbac.AdminsManage, s.handleRoleSave))
	mux.Handle("DELETE /api/admin/roles/{id}", s.admin(rbac.AdminsManage, s.handleRoleDelete))
	mux.Handle("GET /api/admin/admins", s.admin(rbac.AdminsManage, s.handleAdminsList))
	mux.Handle("POST /api/admin/admins", s.admin(rbac.AdminsManage, s.handleAdminCreate))
	mux.Handle("PUT /api/admin/admins/{id}", s.admin(rbac.AdminsManage, s.handleAdminUpdate))

	// shop & wallet (customers)
	mux.Handle("GET /api/shop/products", s.user(s.handleShopProducts))
	mux.Handle("POST /api/shop/quote", s.user(s.handleShopQuote))
	mux.Handle("GET /api/wallet", s.user(s.handleWallet))
	mux.Handle("POST /api/wallet/topup", s.user(s.handleTopup))
	mux.Handle("GET /api/wallet/payments", s.user(s.handleMyPayments))
	mux.Handle("POST /api/wallet/payments/{id}/receipt", s.user(s.handleReceiptUpload))
	mux.Handle("GET /api/wallet/payments/{id}/receipt", s.user(s.handleMyReceipt))

	s.registerCatalog(mux)
	if s.Services != nil {
		s.registerServices(mux)
	}
	s.registerSystem(mux)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeErr(w, 404, "مسیر پیدا نشد") })
	mux.Handle("/", s.spa())

	return s.secure(mux)
}

// --- middleware ---

type ctxKey int

const userKey ctxKey = 1

func currentUser(r *http.Request) *auth.User {
	u, _ := r.Context().Value(userKey).(*auth.User)
	return u
}

// secure adds security headers and CSRF protection: state-changing API calls
// must carry X-Requested-With (a header cross-site forms cannot set) and the
// session cookie is SameSite=Lax.
func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-Requested-With") != "neosell" {
				writeErr(w, http.StatusForbidden, "درخواست نامعتبر (CSRF)")
				return
			}
		}
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("http panic", "path", r.URL.Path, "err", rec)
				writeErr(w, 500, "خطای داخلی")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) loadUser(r *http.Request) *auth.User {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return nil
	}
	u, err := s.Users.SessionUser(r.Context(), c.Value)
	if err != nil {
		return nil
	}
	return u
}

func (s *Server) user(fn http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := s.loadUser(r)
		if u == nil {
			writeErr(w, http.StatusUnauthorized, "ابتدا وارد شوید")
			return
		}
		fn(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	})
}

// admin requires an admin with permission p ("" = any admin).
func (s *Server) admin(p rbac.Perm, fn http.HandlerFunc) http.Handler {
	return s.user(func(w http.ResponseWriter, r *http.Request) {
		u := currentUser(r)
		if !u.IsAdmin || (p != "" && !s.RBAC.Can(r.Context(), u, p)) {
			writeErr(w, http.StatusForbidden, "دسترسی ندارید")
			return
		}
		// Non-owner admins only work while the license includes multi_admin.
		if !u.IsOwner && !s.License.HasModule(license.ModMultiAdmin) {
			writeErr(w, http.StatusForbidden, "دسترسی ادمین‌های دیگر در لایسنس فعال نیست")
			return
		}
		fn(w, r)
	})
}

func (s *Server) owner(fn http.HandlerFunc) http.Handler {
	return s.user(func(w http.ResponseWriter, r *http.Request) {
		if !currentUser(r).IsOwner {
			writeErr(w, http.StatusForbidden, "فقط مالک پنل")
			return
		}
		fn(w, r)
	})
}

// --- helpers ---

type apiError struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, apiError{Error: msg})
}

// writeLicenseErr maps guard errors: locked → 402, module/limit → 403.
func writeLicenseErr(w http.ResponseWriter, err error) bool {
	var me license.ModuleError
	var le license.LimitError
	switch {
	case err == nil:
		return false
	case errors.Is(err, license.ErrLocked), errors.Is(err, license.ErrUnactivated):
		writeJSON(w, http.StatusPaymentRequired, apiError{Error: err.Error(), Code: "license_locked"})
	case errors.As(err, &me):
		writeJSON(w, http.StatusForbidden, apiError{Error: err.Error(), Code: "license_module"})
	case errors.As(err, &le):
		writeJSON(w, http.StatusForbidden, apiError{Error: err.Error(), Code: "license_limit"})
	default:
		writeErr(w, 500, err.Error())
	}
	return true
}

func readJSON(r *http.Request, v any) error {
	body := io.LimitReader(r.Body, 1<<20)
	if err := json.NewDecoder(body).Decode(v); err != nil {
		return errors.New("بدنه‌ی درخواست نامعتبر است")
	}
	return nil
}

// clientIP trusts X-Forwarded-For / X-Real-IP only from a local reverse proxy.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
			return xr
		}
		if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
			return strings.TrimSpace(strings.Split(xf, ",")[0])
		}
	}
	return host
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, tok string) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: tok, Path: "/", HttpOnly: true,
		Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode, MaxAge: int(auth.SessionTTL.Seconds())})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, MaxAge: -1, SameSite: http.SameSiteLaxMode})
}

// --- in-memory rate limiter (fixed window per key) ---

type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string]*window
}

type window struct {
	start time.Time
	n     int
}

func newLimiter(max int, w time.Duration) *limiter {
	return &limiter{max: max, window: w, hits: map[string]*window{}}
}

func (l *limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.hits) > 10000 { // bound memory
		for k, v := range l.hits {
			if now.Sub(v.start) > l.window {
				delete(l.hits, k)
			}
		}
	}
	h := l.hits[key]
	if h == nil || now.Sub(h.start) > l.window {
		l.hits[key] = &window{start: now, n: 1}
		return true
	}
	h.n++
	return h.n <= l.max
}

func (l *limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.hits, key)
	l.mu.Unlock()
}

// --- SPA ---

func (s *Server) spa() http.Handler {
	if s.Web == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			io.WriteString(w, "Neo Sell API is running (web UI not built).")
		})
	}
	files := http.FileServer(http.FS(s.Web))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if f, err := s.Web.Open(p); err == nil {
				st, _ := f.Stat()
				f.Close()
				if st != nil && !st.IsDir() {
					switch {
					case strings.HasSuffix(p, ".woff2"):
						w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
					default:
						// Scripts/styles change with every update: always revalidate (ETag via modtime).
						w.Header().Set("Cache-Control", "no-cache")
					}
					files.ServeHTTP(w, r)
					return
				}
			}
		}
		// Client-side routes get index.html.
		idx, err := fs.ReadFile(s.Web, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(idx)
	})
}
