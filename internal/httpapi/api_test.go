package httpapi

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"

	"neosell/internal/audit"
	"neosell/internal/auth"
	"neosell/internal/catalog"
	"neosell/internal/config"
	"neosell/internal/db"
	"neosell/internal/jobs"
	"neosell/internal/license"
	"neosell/internal/payments"
	"neosell/internal/rbac"
	"neosell/internal/secret"
	"neosell/internal/settings"
	"neosell/internal/wallet"
)

type env struct {
	t    *testing.T
	srv  *httptest.Server
	lic  *license.Manager
	mods []string
}

func newEnv(t *testing.T, modules []string, limits map[string]int64) *env {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ls := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		now := time.Now().Unix()
		c := license.Claims{Key: in["key"], Product: "neosell", Fingerprint: in["fingerprint"], Tier: "pro",
			Modules: modules, Limits: limits, IssuedAt: now, ExpiresAt: now + 3600, Nonce: in["nonce"]}
		p, _ := json.Marshal(c)
		json.NewEncoder(w).Encode(map[string]string{"payload": base64.StdEncoding.EncodeToString(p),
			"sig": base64.StdEncoding.EncodeToString(ed25519.Sign(priv, p))})
	}))
	t.Cleanup(ls.Close)
	d, err := db.OpenPath(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	box, _ := secret.New(make([]byte, 32))
	lic, err := license.New(d, box, license.Options{Endpoints: []string{ls.URL}, PublicKey: pub, Fingerprint: "fp"})
	if err != nil {
		t.Fatal(err)
	}
	w := wallet.New(d)
	s := &Server{DB: d, Users: auth.NewStore(d), RBAC: rbac.NewStore(d), Settings: settings.NewStore(d),
		Audit: audit.New(d), License: lic, Jobs: jobs.New(d), Catalog: catalog.New(d), Wallet: w,
		Payments: payments.New(d, w), Cfg: config.Config{DataDir: t.TempDir()}}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, lic: lic}
}

type client struct {
	e *env
	c *http.Client
}

func (e *env) client() *client {
	jar, _ := cookiejar.New(nil)
	return &client{e: e, c: &http.Client{Jar: jar}}
}

func (c *client) do(method, path string, body any, csrf bool) (int, map[string]any) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.e.srv.URL+path, rd)
	if csrf {
		req.Header.Set("X-Requested-With", "neosell")
	}
	resp, err := c.c.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (c *client) req(method, path string, body any) (int, map[string]any) {
	return c.do(method, path, body, true)
}

func TestSetupLoginLicenseFlow(t *testing.T) {
	e := newEnv(t, []string{license.ModPorts}, map[string]int64{license.LimAdmins: 1})
	owner := e.client()

	code, boot := owner.req("GET", "/api/bootstrap", nil)
	if code != 200 || boot["needs_setup"] != true {
		t.Fatalf("bootstrap: %d %v", code, boot)
	}
	// CSRF: missing header is refused.
	if code, _ := owner.do("POST", "/api/setup", map[string]string{"email": "o@x.io", "password": "password1"}, false); code != 403 {
		t.Fatalf("csrf not enforced: %d", code)
	}
	if code, out := owner.req("POST", "/api/setup", map[string]string{"email": "o@x.io", "password": "short"}); code != 400 {
		t.Fatalf("weak password accepted: %d %v", code, out)
	}
	if code, out := owner.req("POST", "/api/setup", map[string]string{"email": "o@x.io", "password": "password1", "name": "Owner"}); code != 200 || out["is_owner"] != true {
		t.Fatalf("setup: %d %v", code, out)
	}
	// Second setup is refused.
	if code, _ := e.client().req("POST", "/api/setup", map[string]string{"email": "z@x.io", "password": "password1"}); code != 409 {
		t.Fatalf("second setup: %d", code)
	}

	// Registration is closed until the license is active.
	cust := e.client()
	if code, out := cust.req("POST", "/api/auth/register", map[string]string{"email": "c@x.io", "password": "password1"}); code != 402 || out["code"] != "license_locked" {
		t.Fatalf("register before license: %d %v", code, out)
	}
	if code, out := owner.req("POST", "/api/admin/license/activate", map[string]string{"key": "HM-AAAA"}); code != 200 || out["state"] != "active" {
		t.Fatalf("activate: %d %v", code, out)
	}
	if code, out := cust.req("POST", "/api/auth/register", map[string]string{"email": "c@x.io", "password": "password1"}); code != 200 || out["is_admin"] != false {
		t.Fatalf("register: %d %v", code, out)
	}
	if code, _ := e.client().req("POST", "/api/auth/register", map[string]string{"email": "C@x.io", "password": "password1"}); code != 409 {
		t.Fatalf("duplicate email (case-insensitive): %d", code)
	}

	// Customers cannot reach admin APIs or activate licenses.
	if code, _ := cust.req("GET", "/api/admin/settings", nil); code != 403 {
		t.Fatalf("customer admin access: %d", code)
	}
	if code, _ := cust.req("POST", "/api/admin/license/activate", map[string]string{"key": "x"}); code != 403 {
		t.Fatalf("customer license access: %d", code)
	}

	// Settings: validation and audit.
	if code, _ := owner.req("PUT", "/api/admin/settings", map[string]any{"registration.mode": "nope"}); code != 400 {
		t.Fatalf("bad enum accepted: %d", code)
	}
	if code, _ := owner.req("PUT", "/api/admin/settings", map[string]any{"registration.mode": "closed", "site.name": "Shop"}); code != 200 {
		t.Fatalf("settings: %d", code)
	}
	if code, _ := e.client().req("POST", "/api/auth/register", map[string]string{"email": "d@x.io", "password": "password1"}); code != 403 {
		t.Fatalf("closed registration: %d", code)
	}
	_, audit := owner.req("GET", "/api/admin/audit", nil)
	if items, _ := audit["items"].([]any); len(items) < 3 {
		t.Fatalf("audit entries: %v", audit)
	}

	// Extra admins need the multi_admin module.
	if code, out := owner.req("POST", "/api/admin/admins", map[string]any{"email": "a@x.io", "password": "password1"}); code != 403 || out["code"] != "license_module" {
		t.Fatalf("admin without module: %d %v", code, out)
	}

	// Login / logout / bad password / rate limit.
	anon := e.client()
	if code, _ := anon.req("POST", "/api/auth/login", map[string]string{"email": "c@x.io", "password": "wrongpass"}); code != 401 {
		t.Fatalf("bad login: %d", code)
	}
	if code, _ := anon.req("POST", "/api/auth/login", map[string]string{"email": "c@x.io", "password": "password1"}); code != 200 {
		t.Fatalf("login: %d", code)
	}
	if code, _ := anon.req("GET", "/api/me", nil); code != 200 {
		t.Fatalf("me: %d", code)
	}
	anon.req("POST", "/api/auth/logout", nil)
	if code, _ := anon.req("GET", "/api/me", nil); code != 401 {
		t.Fatalf("after logout: %d", code)
	}
	limited := 0
	for i := 0; i < 15; i++ {
		if code, _ := anon.req("POST", "/api/auth/login", map[string]string{"email": "c@x.io", "password": "nope-nope"}); code == 429 {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("login rate limit not applied")
	}
}

func TestStaffAdminsWithModule(t *testing.T) {
	e := newEnv(t, []string{license.ModMultiAdmin}, map[string]int64{license.LimAdmins: 1})
	owner := e.client()
	owner.req("POST", "/api/setup", map[string]string{"email": "o@x.io", "password": "password1"})
	owner.req("POST", "/api/admin/license/activate", map[string]string{"key": "HM-B"})

	code, out := owner.req("POST", "/api/admin/roles", map[string]any{"name": "support", "permissions": []string{"tickets.manage", "audit.view", "admins.manage", "bogus"}})
	if code != 200 {
		t.Fatalf("role: %d %v", code, out)
	}
	roleID := out["id"]
	if code, out := owner.req("POST", "/api/admin/admins", map[string]any{"email": "s@x.io", "password": "password1", "role_ids": []any{roleID}}); code != 200 {
		t.Fatalf("staff: %d %v", code, out)
	}
	if code, out := owner.req("POST", "/api/admin/admins", map[string]any{"email": "s2@x.io", "password": "password1"}); code != 403 || out["code"] != "license_limit" {
		t.Fatalf("admin limit: %d %v", code, out)
	}
	staff := e.client()
	staff.req("POST", "/api/auth/login", map[string]string{"email": "s@x.io", "password": "password1"})
	if code, _ := staff.req("GET", "/api/admin/audit", nil); code != 200 {
		t.Fatalf("granted perm: %d", code)
	}
	if code, _ := staff.req("GET", "/api/admin/settings", nil); code != 403 {
		t.Fatalf("missing perm: %d", code)
	}
	// admins.manage was stripped from the role: staff cannot manage admins.
	if code, _ := staff.req("GET", "/api/admin/admins", nil); code != 403 {
		t.Fatalf("staff managing admins: %d", code)
	}
	if code, _ := staff.req("POST", "/api/admin/license/deactivate", nil); code != 403 {
		t.Fatalf("staff license: %d", code)
	}
}
