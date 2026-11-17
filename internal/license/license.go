// Package license talks to the HM license server (Cloudflare Worker,
// hm-license-manager) and decides what this install may do.
//
// Contract (POST /v1/neosell/license):
//
//	request  {key, nonce, version, fingerprint, os, arch, hostname}
//	response {payload, sig, your_ip}  or  {error} with a Persian message
//
// payload is base64(JSON claims) signed with Ed25519; the public key is
// embedded below. The client re-checks every 24h; a token lives at most 48h
// (and never past the license's own expiry), so short outages are harmless.
// The local clock is guarded by a monotonic high-water mark.
//
// All decisions (tier, modules, limits, expiry, device lock) are made by the
// license server. Agents never contact it; only the panel does.
package license

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	"neosell/internal/db"
	"neosell/internal/secret"
	"neosell/internal/version"
)

const (
	CheckEvery = 24 * time.Hour
	// RetryEvery is how soon a failed check is retried.
	RetryEvery = 15 * time.Minute
	// WarnAfter: no successful check for this long shows a warning to the owner.
	WarnAfter = 30 * time.Hour
	apiPath   = "/v1/neosell/license"
)

// pubKeyB64 verifies license tokens. The private half exists only as the
// NEOSELL_LICENSE_PRIVATE_KEY secret of the license Worker.
const pubKeyB64 = "sMtmVwiwXlMYe5zxpYHdbQ8+dsgr0ya/7A4wrNrm+bw="

// Hostnames are XOR-obfuscated to keep them out of plain `strings` output.
// This is obfuscation, not secrecy.
var obfKey = []byte("neosell-guard-7c1f-2026")

var endpointsEnc = []string{
	"AgwMFgsfCQMPGBMTHQNHEV4=",                     // license.hmray.pro
	"AgwMFgsfCQMPGBMTHV5SEUcDXxxTXVs=",             // license.hmrayserver.com
	"AgwMFgsfCQMPGBMTHUxHEx8FQl8=",                 // license.hmrayapp.com
	"AgwMFgsfCQMPGhIBAURZTlxSGwQeRVkcDgoBFkIISBE=", // license.hossein-m466.workers.dev
}

func deobf(s string) string {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return ""
	}
	for i := range b {
		b[i] ^= obfKey[i%len(obfKey)]
	}
	return string(b)
}

// DefaultEndpoints returns the built-in license server base URLs.
func DefaultEndpoints() []string {
	out := make([]string, 0, len(endpointsEnc))
	for _, e := range endpointsEnc {
		if h := deobf(e); h != "" {
			out = append(out, "https://"+h)
		}
	}
	return out
}

// Module ids. The license server decides which ones a license gets.
const (
	ModPorts      = "ports"       // sell tunnel ports
	ModOutbound   = "outbound"    // sell 3x-ui outbounds
	ModBot        = "bot"         // Telegram bot
	ModGateways   = "gateways"    // online payment gateways
	ModPayg       = "payg"        // pay-as-you-go billing
	ModReseller   = "reseller"    // reseller accounts
	ModMultiAdmin = "multi_admin" // admins besides the owner, custom roles
	ModWhiteLabel = "white_label" // remove Neo Sell branding
)

// Limit ids. 0 or missing means "not allowed"; -1 means unlimited.
const (
	LimNodes          = "max_nodes"
	LimXUIPanels      = "max_xui_panels"
	LimActiveServices = "max_active_services"
	LimAdmins         = "max_admins"
)

type Claims struct {
	Key              string           `json:"key"`
	Product          string           `json:"product"`
	Fingerprint      string           `json:"fingerprint"`
	BoundIP          string           `json:"bound_ip"`
	Tier             string           `json:"tier"`
	Modules          []string         `json:"modules"`
	Limits           map[string]int64 `json:"limits"`
	LicenseExpiresAt int64            `json:"license_expires_at"`
	IssuedAt         int64            `json:"issued_at"`
	ExpiresAt        int64            `json:"expires_at"`
	Nonce            string           `json:"nonce"`
}

// State is the coarse license state the rest of the app acts on.
type State string

const (
	StateUnactivated State = "unactivated" // no key yet: only the activation screen works
	StateActive      State = "active"
	StateLocked      State = "locked" // sales stop; existing customer services keep running
)

type Status struct {
	State            State            `json:"state"`
	Warning          string           `json:"warning,omitempty"`
	Reason           string           `json:"reason,omitempty"`
	KeyHint          string           `json:"key_hint,omitempty"`
	Tier             string           `json:"tier,omitempty"`
	Modules          []string         `json:"modules"`
	Limits           map[string]int64 `json:"limits"`
	LicenseExpiresAt int64            `json:"license_expires_at,omitempty"`
	TokenExpiresAt   int64            `json:"token_expires_at,omitempty"`
	LastCheck        int64            `json:"last_check,omitempty"`
	LastError        string           `json:"last_error,omitempty"`
	Fingerprint      string           `json:"fingerprint"`
	BoundIP          string           `json:"bound_ip,omitempty"`
}

type Options struct {
	Endpoints   []string // base URLs; default DefaultEndpoints()
	Proxy       string   // socks5:// or http:// for servers without direct access
	PublicKey   ed25519.PublicKey
	Fingerprint string
	HTTPTimeout time.Duration
	Now         func() time.Time
}

type Manager struct {
	db   *db.DB
	box  *secret.Box
	opts Options

	mu       sync.RWMutex
	key      string
	claims   *Claims
	seenTime int64
	lastOK   int64
	lastErr  string
	onChange []func(Status)
}

// New loads the stored license (if any) from the database.
func New(d *db.DB, box *secret.Box, o Options) (*Manager, error) {
	if len(o.Endpoints) == 0 {
		o.Endpoints = DefaultEndpoints()
	}
	if o.PublicKey == nil {
		raw, err := base64.StdEncoding.DecodeString(pubKeyB64)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, errors.New("license: bad embedded public key")
		}
		o.PublicKey = ed25519.PublicKey(raw)
	}
	if o.Fingerprint == "" {
		o.Fingerprint = Fingerprint()
	}
	if o.HTTPTimeout == 0 {
		o.HTTPTimeout = 15 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	m := &Manager{db: d, box: box, opts: o}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) load() error {
	var keyEnc, payloadEnc, sig, lastErr string
	var seen, lastOK int64
	err := m.db.QueryRow(`SELECT key_enc, payload_enc, sig, seen_time, last_ok, last_error FROM license_state WHERE id = 1`).
		Scan(&keyEnc, &payloadEnc, &sig, &seen, &lastOK, &lastErr)
	if err != nil {
		return err
	}
	m.seenTime, m.lastOK, m.lastErr = seen, lastOK, lastErr
	if keyEnc == "" {
		return nil
	}
	key, err := m.box.OpenString(keyEnc)
	if err != nil {
		slog.Warn("license: stored key unreadable", "err", err)
		return nil
	}
	m.key = key
	if payloadEnc == "" {
		return nil
	}
	payloadB64, err := m.box.OpenString(payloadEnc)
	if err != nil {
		return nil
	}
	// Re-verify the stored token on every start: a tampered row is simply ignored.
	if c, err := m.verify(payloadB64, sig); err == nil && c.Fingerprint == m.opts.Fingerprint {
		m.claims = c
	}
	return nil
}

func (m *Manager) verify(payloadB64, sigB64 string) (*Claims, error) {
	payload, err := base64.StdEncoding.DecodeString(payloadB64)
	if err != nil {
		return nil, errors.New("توکن لایسنس خراب است")
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return nil, errors.New("امضای لایسنس خراب است")
	}
	if !ed25519.Verify(m.opts.PublicKey, payload, sig) {
		return nil, errors.New("امضای لایسنس نامعتبر است")
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, errors.New("محتوای لایسنس نامعتبر است")
	}
	if c.Product != "neosell" {
		return nil, errors.New("این لایسنس برای Neo Sell نیست")
	}
	return &c, nil
}

// effectiveNow never goes back in time: max(local clock, newest trusted time).
func (m *Manager) effectiveNow() int64 {
	now := m.opts.Now().Unix()
	if m.seenTime > now {
		return m.seenTime
	}
	return now
}

func (m *Manager) Status() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.statusLocked()
}

func (m *Manager) statusLocked() Status {
	s := Status{
		Modules:     []string{},
		Limits:      map[string]int64{},
		LastCheck:   m.lastOK,
		LastError:   m.lastErr,
		Fingerprint: m.opts.Fingerprint,
	}
	if m.key == "" {
		s.State = StateUnactivated
		s.Reason = "برای شروع، کلید لایسنس را وارد کنید"
		return s
	}
	s.KeyHint = keyHint(m.key)
	c := m.claims
	if c == nil {
		s.State = StateLocked
		s.Reason = "لایسنس هنوز تأیید نشده است"
		if m.lastErr != "" {
			s.Reason = m.lastErr
		}
		return s
	}
	s.Tier, s.BoundIP = c.Tier, c.BoundIP
	s.LicenseExpiresAt, s.TokenExpiresAt = c.LicenseExpiresAt, c.ExpiresAt
	now := m.effectiveNow()
	switch {
	case c.LicenseExpiresAt > 0 && now >= c.LicenseExpiresAt:
		s.State = StateLocked
		s.Reason = "لایسنس منقضی شده است؛ برای ادامه‌ی فروش تمدید کنید"
	case now >= c.ExpiresAt:
		s.State = StateLocked
		s.Reason = "اتصال به سرور لایسنس برقرار نشد و مهلت توکن تمام شد"
		if m.lastErr != "" {
			s.Reason += " (" + m.lastErr + ")"
		}
	default:
		s.State = StateActive
		s.Modules = append(s.Modules, c.Modules...)
		for k, v := range c.Limits {
			s.Limits[k] = v
		}
		if m.lastOK > 0 && now-m.lastOK > int64(WarnAfter.Seconds()) {
			s.Warning = "مدتی است سرور لایسنس در دسترس نیست؛ اگر تا " +
				time.Unix(c.ExpiresAt, 0).Format("2006-01-02 15:04") + " وصل نشود، فروش متوقف می‌شود"
		}
		if c.LicenseExpiresAt > 0 && c.LicenseExpiresAt-now < 7*86400 {
			s.Warning = "لایسنس کمتر از ۷ روز دیگر منقضی می‌شود"
		}
	}
	return s
}

func keyHint(k string) string {
	if len(k) <= 4 {
		return "****"
	}
	return "****" + k[len(k)-4:]
}

// OnChange registers a callback run after every activation or re-check.
func (m *Manager) OnChange(fn func(Status)) {
	m.mu.Lock()
	m.onChange = append(m.onChange, fn)
	m.mu.Unlock()
}

// Activate validates key with the license server and stores it on success.
func (m *Manager) Activate(ctx context.Context, key string) (Status, error) {
	key = NormalizeKey(key)
	if key == "" {
		return m.Status(), errors.New("کلید لایسنس را وارد کنید")
	}
	c, payload, sig, err := m.contact(ctx, key, "activate")
	if err != nil {
		m.recordError(err)
		return m.Status(), err
	}
	if err := m.adopt(key, c, payload, sig); err != nil {
		return m.Status(), err
	}
	return m.Status(), nil
}

// Recheck re-validates the stored key. No-op when nothing is activated.
func (m *Manager) Recheck(ctx context.Context) error {
	m.mu.RLock()
	key := m.key
	m.mu.RUnlock()
	if key == "" {
		return nil
	}
	c, payload, sig, err := m.contact(ctx, key, "heartbeat")
	if err != nil {
		m.recordError(err)
		return err
	}
	return m.adopt(key, c, payload, sig)
}

// Deactivate forgets the key locally (the device slot is freed from the license portal).
func (m *Manager) Deactivate() error {
	m.mu.Lock()
	m.key, m.claims, m.lastOK, m.lastErr = "", nil, 0, ""
	_, err := m.db.Exec(`UPDATE license_state SET key_enc='', payload_enc='', sig='', last_ok=0, last_error='' WHERE id=1`)
	st := m.statusLocked()
	cbs := append([]func(Status){}, m.onChange...)
	m.mu.Unlock()
	for _, fn := range cbs {
		fn(st)
	}
	return err
}

func (m *Manager) recordError(err error) {
	m.mu.Lock()
	m.lastErr = err.Error()
	m.db.Exec(`UPDATE license_state SET last_error=?, last_try=? WHERE id=1`, m.lastErr, m.opts.Now().Unix())
	m.mu.Unlock()
}

func (m *Manager) adopt(key string, c *Claims, payloadB64, sig string) error {
	if c.Fingerprint != "" && c.Fingerprint != m.opts.Fingerprint {
		return errors.New("این لایسنس برای دستگاه دیگری صادر شده است")
	}
	keyEnc, err := m.box.SealString(key)
	if err != nil {
		return err
	}
	payloadEnc, err := m.box.SealString(payloadB64)
	if err != nil {
		return err
	}
	m.mu.Lock()
	now := m.opts.Now().Unix()
	seen := m.seenTime
	for _, t := range []int64{now, c.IssuedAt} {
		if t > seen {
			seen = t
		}
	}
	_, err = m.db.Exec(`UPDATE license_state SET key_enc=?, payload_enc=?, sig=?, seen_time=?, last_ok=?, last_error='', last_try=? WHERE id=1`,
		keyEnc, payloadEnc, sig, seen, now, now)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	m.key, m.claims, m.seenTime, m.lastOK, m.lastErr = key, c, seen, now, ""
	st := m.statusLocked()
	cbs := append([]func(Status){}, m.onChange...)
	m.mu.Unlock()
	for _, fn := range cbs {
		fn(st)
	}
	return nil
}

func (m *Manager) contact(ctx context.Context, key, mode string) (*Claims, string, string, error) {
	nb := make([]byte, 16)
	_, _ = rand.Read(nb)
	nonce := hex.EncodeToString(nb)
	host, _ := hostname()
	body, _ := json.Marshal(map[string]string{
		"key":         key,
		"nonce":       nonce,
		"version":     version.Version,
		"fingerprint": m.opts.Fingerprint,
		"os":          runtime.GOOS,
		"arch":        runtime.GOARCH,
		"hostname":    host,
		"mode":        mode,
	})
	var lastErr error
	for _, base := range m.opts.Endpoints {
		c, payload, sig, err := m.try(ctx, strings.TrimRight(base, "/")+apiPath, body, nonce)
		if err == nil {
			return c, payload, sig, nil
		}
		lastErr = err
		var se serverError
		if errors.As(err, &se) {
			// The server answered with a decision (invalid, expired, other device):
			// asking a mirror would give the same answer.
			return nil, "", "", err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("هیچ سرور لایسنسی در دسترس نبود")
	}
	return nil, "", "", lastErr
}

type serverError struct{ msg string }

func (e serverError) Error() string { return e.msg }

func (m *Manager) client() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	if m.opts.Proxy != "" {
		if u, err := url.Parse(m.opts.Proxy); err == nil {
			tr.Proxy = http.ProxyURL(u) // net/http supports http(s):// and socks5://
		}
	}
	return &http.Client{Timeout: m.opts.HTTPTimeout, Transport: tr}
}

func (m *Manager) try(ctx context.Context, u string, body []byte, nonce string) (*Claims, string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "neosell/"+version.Version)
	resp, err := m.client().Do(req)
	if err != nil {
		return nil, "", "", errors.New("سرور لایسنس در دسترس نیست")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var out struct {
		Payload string `json:"payload"`
		Sig     string `json:"sig"`
		YourIP  string `json:"your_ip"`
		Error   string `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode != http.StatusOK {
		if out.Error != "" && resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return nil, "", "", serverError{out.Error}
		}
		return nil, "", "", errors.New("سرور لایسنس پاسخ نامعتبر داد")
	}
	c, err := m.verify(out.Payload, out.Sig)
	if err != nil {
		return nil, "", "", err
	}
	if c.Nonce != nonce {
		return nil, "", "", errors.New("پاسخ لایسنس تازه نیست")
	}
	if NormalizeKey(c.Key) != NormalizeKey(strings.TrimSpace(jsonKey(body))) {
		return nil, "", "", errors.New("پاسخ لایسنس برای کلید دیگری است")
	}
	return c, out.Payload, out.Sig, nil
}

func jsonKey(body []byte) string {
	var b struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(body, &b)
	return b.Key
}

// NormalizeKey upper-cases and trims a license key ("hm-abcd-…" → "HM-ABCD-…").
func NormalizeKey(k string) string {
	return strings.ToUpper(strings.TrimSpace(k))
}

// Loop re-checks every 24h (sooner after a failure) until ctx ends.
func (m *Manager) Loop(ctx context.Context) {
	next := m.nextCheck()
	for {
		t := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		if err := m.Recheck(ctx); err != nil {
			slog.Warn("license: re-check failed", "err", err)
			next = time.Now().Add(RetryEvery)
			continue
		}
		next = time.Now().Add(CheckEvery)
	}
}

func (m *Manager) nextCheck() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.key == "" {
		return time.Now().Add(CheckEvery)
	}
	if m.claims == nil || m.lastOK == 0 {
		return time.Now().Add(5 * time.Second)
	}
	n := time.Unix(m.lastOK, 0).Add(CheckEvery)
	if n.Before(time.Now()) {
		return time.Now().Add(5 * time.Second)
	}
	return n
}
