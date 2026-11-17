package license

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"neosell/internal/db"
	"neosell/internal/secret"
)

type fakeServer struct {
	priv    ed25519.PrivateKey
	status  int
	errMsg  string
	modules []string
	limits  map[string]int64
	licExp  int64
	ttl     int64
	fp      string // if set, overrides the fingerprint in the token
	product string
	badSig  bool
	hits    atomic.Int32
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.hits.Add(1)
	if r.URL.Path != apiPath || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var in map[string]string
	json.NewDecoder(r.Body).Decode(&in)
	if f.status != 0 && f.status != 200 {
		w.WriteHeader(f.status)
		json.NewEncoder(w).Encode(map[string]string{"error": f.errMsg})
		return
	}
	now := time.Now().Unix()
	ttl := f.ttl
	if ttl == 0 {
		ttl = 48 * 3600
	}
	fp := in["fingerprint"]
	if f.fp != "" {
		fp = f.fp
	}
	product := "neosell"
	if f.product != "" {
		product = f.product
	}
	c := Claims{Key: in["key"], Product: product, Fingerprint: fp, BoundIP: "1.2.3.4", Tier: "pro",
		Modules: f.modules, Limits: f.limits, LicenseExpiresAt: f.licExp,
		IssuedAt: now, ExpiresAt: now + ttl, Nonce: in["nonce"]}
	payload, _ := json.Marshal(c)
	sig := ed25519.Sign(f.priv, payload)
	if f.badSig {
		sig[0] ^= 0xff
	}
	json.NewEncoder(w).Encode(map[string]string{
		"payload": base64.StdEncoding.EncodeToString(payload),
		"sig":     base64.StdEncoding.EncodeToString(sig),
		"your_ip": "1.2.3.4",
	})
}

func setup(t *testing.T, f *fakeServer, endpoints ...string) (*Manager, *db.DB, ed25519.PublicKey) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	f.priv = priv
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	d, err := db.OpenPath(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	box, _ := secret.New(make([]byte, 32))
	if len(endpoints) == 0 {
		endpoints = []string{srv.URL}
	} else {
		endpoints = append(endpoints, srv.URL)
	}
	m, err := New(d, box, Options{Endpoints: endpoints, PublicKey: pub, Fingerprint: "fp-test"})
	if err != nil {
		t.Fatal(err)
	}
	return m, d, pub
}

func TestUnactivatedByDefault(t *testing.T) {
	m, _, _ := setup(t, &fakeServer{})
	if s := m.Status(); s.State != StateUnactivated {
		t.Fatalf("state = %s", s.State)
	}
	if !errors.Is(m.RequireActive(), ErrUnactivated) {
		t.Fatal("expected ErrUnactivated")
	}
}

func TestActivateGivesModulesAndLimits(t *testing.T) {
	f := &fakeServer{modules: []string{ModPorts, ModBot}, limits: map[string]int64{LimNodes: 2, LimActiveServices: -1}}
	m, _, _ := setup(t, f)
	s, err := m.Activate(context.Background(), " hm-aaaa-bbbb-cccc ")
	if err != nil {
		t.Fatal(err)
	}
	if s.State != StateActive || s.KeyHint != "****CCCC" {
		t.Fatalf("status %+v", s)
	}
	if !m.HasModule(ModPorts) || m.HasModule(ModOutbound) {
		t.Fatal("module resolution wrong")
	}
	if err := m.RequireModule(ModOutbound); err == nil {
		t.Fatal("outbound should be refused")
	}
	if err := m.RequireBelow(LimNodes, 1); err != nil {
		t.Fatal(err)
	}
	var le LimitError
	if err := m.RequireBelow(LimNodes, 2); !errors.As(err, &le) {
		t.Fatalf("expected limit error, got %v", err)
	}
	if err := m.RequireBelow(LimActiveServices, 1_000_000); err != nil {
		t.Fatal("unlimited should pass")
	}
	if err := m.RequireBelow(LimAdmins, 0); err == nil {
		t.Fatal("missing limit means none allowed")
	}
}

func TestPersistsAndReverifiesOnRestart(t *testing.T) {
	f := &fakeServer{modules: []string{ModPorts}}
	m, d, pub := setup(t, f)
	if _, err := m.Activate(context.Background(), "HM-1111-2222-3333"); err != nil {
		t.Fatal(err)
	}
	box, _ := secret.New(make([]byte, 32))
	m2, err := New(d, box, Options{Endpoints: []string{"http://127.0.0.1:1"}, PublicKey: pub, Fingerprint: "fp-test"})
	if err != nil {
		t.Fatal(err)
	}
	if s := m2.Status(); s.State != StateActive || !m2.HasModule(ModPorts) {
		t.Fatalf("after restart: %+v", s)
	}
	// Another machine copying the database is not licensed.
	m3, _ := New(d, box, Options{Endpoints: []string{"http://127.0.0.1:1"}, PublicKey: pub, Fingerprint: "other"})
	if s := m3.Status(); s.State != StateLocked {
		t.Fatalf("copied DB on another machine: %s", s.State)
	}
	// Tampering with the stored signature drops the token.
	d.Exec(`UPDATE license_state SET sig = 'AAAA' WHERE id = 1`)
	m4, _ := New(d, box, Options{Endpoints: []string{"http://127.0.0.1:1"}, PublicKey: pub, Fingerprint: "fp-test"})
	if s := m4.Status(); s.State != StateLocked {
		t.Fatalf("tampered sig: %s", s.State)
	}
}

func TestRejectsBadSignatureWrongProductAndOtherDevice(t *testing.T) {
	for name, f := range map[string]*fakeServer{
		"badsig":  {badSig: true},
		"product": {product: "neospoof"},
		"device":  {fp: "someone-else"},
	} {
		t.Run(name, func(t *testing.T) {
			m, _, _ := setup(t, f)
			if _, err := m.Activate(context.Background(), "HM-X"); err == nil {
				t.Fatal("expected failure")
			}
			if m.Status().State == StateActive {
				t.Fatal("must not be active")
			}
		})
	}
}

func TestServerDecisionIsNotRetriedOnMirror(t *testing.T) {
	f := &fakeServer{status: 403, errMsg: "لایسنس منقضی شده است"}
	m, _, _ := setup(t, f)
	_, err := m.Activate(context.Background(), "HM-X")
	if err == nil || err.Error() != "لایسنس منقضی شده است" {
		t.Fatalf("err = %v", err)
	}
}

func TestFallsBackToMirror(t *testing.T) {
	f := &fakeServer{modules: []string{ModPorts}}
	m, _, _ := setup(t, f, "http://127.0.0.1:1") // first endpoint is dead
	if _, err := m.Activate(context.Background(), "HM-X"); err != nil {
		t.Fatal(err)
	}
	if f.hits.Load() != 1 {
		t.Fatalf("hits = %d", f.hits.Load())
	}
}

func TestTokenExpiryLocksAndClockRollbackDoesNotHelp(t *testing.T) {
	f := &fakeServer{modules: []string{ModPorts}, ttl: 3600}
	m, _, _ := setup(t, f)
	if _, err := m.Activate(context.Background(), "HM-X"); err != nil {
		t.Fatal(err)
	}
	m.opts.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if s := m.Status(); s.State != StateLocked {
		t.Fatalf("expired token: %s", s.State)
	}
	if m.HasModule(ModPorts) {
		t.Fatal("locked license must expose no modules")
	}
	// Seen a later trusted time, then roll the clock back: still locked.
	m.seenTime = time.Now().Add(2 * time.Hour).Unix()
	m.opts.Now = func() time.Time { return time.Now().Add(-24 * time.Hour) }
	if s := m.Status(); s.State != StateLocked {
		t.Fatalf("rollback reopened license: %s", s.State)
	}
}

func TestLicenseExpiryLocks(t *testing.T) {
	f := &fakeServer{modules: []string{ModPorts}, licExp: time.Now().Add(time.Hour).Unix()}
	m, _, _ := setup(t, f)
	if _, err := m.Activate(context.Background(), "HM-X"); err != nil {
		t.Fatal(err)
	}
	if s := m.Status(); s.State != StateActive || s.Warning == "" {
		t.Fatalf("near expiry should warn: %+v", s)
	}
	m.opts.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if s := m.Status(); s.State != StateLocked {
		t.Fatalf("state %s", s.State)
	}
}

func TestDefaultEndpointsDecode(t *testing.T) {
	eps := DefaultEndpoints()
	if len(eps) != 4 || eps[0] != "https://license.hmray.pro" {
		t.Fatalf("%v", eps)
	}
}
