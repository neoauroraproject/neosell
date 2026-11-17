package agentgw

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"neosell/internal/agent"
	"neosell/internal/catalog"
	"neosell/internal/db"
	"neosell/internal/jobs"
	"neosell/internal/license"
	"neosell/internal/pricing"
	"neosell/internal/secret"
	"neosell/internal/services"
	"neosell/internal/settings"
	"neosell/internal/wallet"
)

type fakeNFT struct {
	mu       sync.Mutex
	script   string
	counters map[string]int64
	quotas   map[string]int64
}

func (f *fakeNFT) Available() bool { return true }
func (f *fakeNFT) Apply(s string) error {
	f.mu.Lock()
	f.script = s
	f.mu.Unlock()
	return nil
}
func (f *fakeNFT) ResetCounters() (map[string]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.counters
	f.counters = map[string]int64{}
	return c, nil
}
func (f *fakeNFT) Quotas() (map[string]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]int64{}
	for k, v := range f.quotas {
		out[k] = v
	}
	return out, nil
}
func (f *fakeNFT) get() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.script
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestAgentEndToEnd(t *testing.T) {
	ctx := context.Background()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ls := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		now := time.Now().Unix()
		c := license.Claims{Key: in["key"], Product: "neosell", Fingerprint: in["fingerprint"], Modules: []string{"ports"},
			Limits: map[string]int64{"max_nodes": 1, "max_active_services": -1}, IssuedAt: now, ExpiresAt: now + 3600, Nonce: in["nonce"]}
		p, _ := json.Marshal(c)
		json.NewEncoder(w).Encode(map[string]string{"payload": base64.StdEncoding.EncodeToString(p), "sig": base64.StdEncoding.EncodeToString(ed25519.Sign(priv, p))})
	}))
	defer ls.Close()
	d, _ := db.OpenPath(":memory:")
	defer d.Close()
	box, _ := secret.New(make([]byte, 32))
	lic, _ := license.New(d, box, license.Options{Endpoints: []string{ls.URL}, PublicKey: pub, Fingerprint: "fp"})
	lic.Activate(ctx, "HM-E")
	w := wallet.New(d)
	m := &services.Manager{DB: d, Box: box, Wallet: w, Catalog: catalog.New(d), Settings: settings.NewStore(d), Jobs: jobs.New(d), License: lic}
	hub := New(m, lic)
	m.Hub = hub
	srv := httptest.NewServer(hub)
	defer srv.Close()

	d.Exec(`INSERT INTO users(id, email, created_at, updated_at) VALUES (1, 'c@x.io', 0, 0)`)
	w.Apply(ctx, wallet.Entry{UserID: 1, Amount: 1_000_000, Type: "topup"})
	m.Catalog.SavePortClass(ctx, catalog.PortClass{Name: "r", Kind: "random"})
	pid, _ := m.Catalog.SaveProduct(ctx, catalog.Product{Type: "port", Title: "P", Enabled: true, Stock: -1,
		Options: pricing.Options{Models: []pricing.Model{pricing.ModelBuilder}, Durations: []int{30}, Cores: []string{"neotunnel"}}})
	m.Catalog.SaveRule(ctx, pricing.Rule{Component: "port", Value: 1000, Enabled: true})
	m.Catalog.SaveRule(ctx, pricing.Rule{Component: "gb", Value: 10, Enabled: true})
	nid, _ := m.SaveNode(ctx, services.Node{Name: "ir", Kind: "tunnel", PublicHost: "1.1.1.1", Enabled: true, SaleEnabled: true,
		Options: services.NodeOptions{PortRanges: "40000-40100"}, Secrets: &services.NodeSecrets{NTBaseURL: "https://127.0.0.1:1/x", NTToken: "t"}})
	m.SaveSharedTunnel(ctx, services.SharedTunnel{NodeID: nid, Core: "neotunnel", Name: "sh", NTTunnel: "t1"})
	token, _ := m.RotateAgentToken(ctx, nid)

	// Wrong token is refused.
	resp, _ := http.Get(srv.URL)
	if resp.StatusCode != 401 {
		t.Fatalf("no token: %d", resp.StatusCode)
	}

	nft := &fakeNFT{counters: map[string]int64{}, quotas: map[string]int64{}}
	ag, err := agent.New(t.TempDir(), nft)
	if err != nil {
		t.Fatal(err)
	}
	actx, cancel := context.WithCancel(ctx)
	defer cancel()
	go ag.Run(actx, agent.Config{PanelURL: srv.URL, Token: token})
	waitFor(t, "agent online", func() bool { return hub.Online(nid) })

	o, err := m.PlaceOrder(ctx, 1, services.OrderRequest{ProductID: pid, Selection: pricing.Selection{Model: "builder", Days: 30, GB: 1, Ports: []pricing.PortSel{{}}}})
	if err != nil {
		t.Fatal(err)
	}
	svc, _ := m.ServiceFor(ctx, o.ServiceID, 1)
	port := svc.Ports[0].Port
	waitFor(t, "ruleset with sold port", func() bool {
		return strings.Contains(nft.get(), "proto-dst { "+itoa(port)+" }") && strings.Contains(nft.get(), "quota s"+itoa(o.ServiceID)+"_q { over 1073741824 bytes")
	})
	var status string
	d.QueryRow(`SELECT status FROM nodes WHERE id = ?`, nid).Scan(&status)
	if status == "offline" {
		t.Fatalf("node status %s", status)
	}

	// Traffic counted in the kernel reaches the panel.
	sfx := itoa(o.ServiceID)
	nft.mu.Lock()
	nft.counters = map[string]int64{"s" + sfx + "_up": 100 << 20, "s" + sfx + "_down": 300 << 20}
	nft.mu.Unlock()
	usage, _, _ := ag.Tick()
	if len(usage.Counters) == 0 {
		t.Fatal("no usage")
	}
	// Push the report through a state round-trip (agent reports usage after applying a state).
	hub.Push(nid)
	waitFor(t, "usage on panel", func() bool {
		s, _ := m.ServiceFor(ctx, o.ServiceID, 1)
		return s.UsedUp == 100<<20 && s.UsedDown == 300<<20
	})

	// The agent cuts at quota on its own and the panel learns about it.
	nft.mu.Lock()
	nft.quotas = map[string]int64{"s" + sfx + "_q": 1 << 30}
	nft.mu.Unlock()
	_, events, _ := ag.Tick()
	if len(events) != 1 {
		t.Fatalf("events %v", events)
	}
	m.ApplyAgentEvents(ctx, nid, events)
	waitFor(t, "limited", func() bool {
		s, _ := m.ServiceFor(ctx, o.ServiceID, 1)
		return s.Status == services.StLimited
	})
	waitFor(t, "blocked chain", func() bool { return strings.Contains(nft.get(), "chain s"+sfx+"_c {\n\t\tdrop") })

	// A second tunnel server beyond the licensed limit (1) is refused.
	nid2, _ := m.SaveNode(ctx, services.Node{Name: "ir2", Kind: "tunnel", Enabled: true})
	tok2, _ := m.RotateAgentToken(ctx, nid2)
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer "+tok2)
	r2, _ := http.DefaultClient.Do(req)
	if r2.StatusCode != 403 {
		t.Fatalf("node over license limit: %d", r2.StatusCode)
	}
}

func itoa(n any) string { b, _ := json.Marshal(n); return string(b) }
