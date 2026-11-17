package services

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"neosell/internal/agentproto"
	"neosell/internal/catalog"
	"neosell/internal/db"
	"neosell/internal/jobs"
	"neosell/internal/license"
	"neosell/internal/pricing"
	"neosell/internal/secret"
	"neosell/internal/settings"
	"neosell/internal/wallet"
	"neosell/internal/xui"
)

type fakeHub struct {
	mu     sync.Mutex
	pushed map[int64]int
	busy   map[int]bool
}

func (h *fakeHub) Push(id int64) {
	h.mu.Lock()
	h.pushed[id]++
	h.mu.Unlock()
}
func (h *fakeHub) Online(int64) bool { return true }
func (h *fakeHub) CheckPorts(_ context.Context, _ int64, ports []int, _ string) ([]int, error) {
	var b []int
	for _, p := range ports {
		if h.busy[p] {
			b = append(b, p)
		}
	}
	return b, nil
}

type fakeNotify struct {
	mu    sync.Mutex
	kinds []string
}

func (n *fakeNotify) Notify(_ context.Context, _ int64, kind, _, _ string) {
	n.mu.Lock()
	n.kinds = append(n.kinds, kind)
	n.mu.Unlock()
}
func (n *fakeNotify) has(k string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, x := range n.kinds {
		if x == k {
			return true
		}
	}
	return false
}

type env struct {
	m      *Manager
	d      *db.DB
	hub    *fakeHub
	notes  *fakeNotify
	now    time.Time
	userID int64
	ctx    context.Context
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ls := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		now := time.Now().Unix()
		c := license.Claims{Key: in["key"], Product: "neosell", Fingerprint: in["fingerprint"], Modules: []string{"ports", "outbound", "payg"},
			Limits: map[string]int64{"max_active_services": -1}, IssuedAt: now, ExpiresAt: now + 86400*400, Nonce: in["nonce"]}
		p, _ := json.Marshal(c)
		json.NewEncoder(w).Encode(map[string]string{"payload": base64.StdEncoding.EncodeToString(p), "sig": base64.StdEncoding.EncodeToString(ed25519.Sign(priv, p))})
	}))
	t.Cleanup(ls.Close)
	d, err := db.OpenPath(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	box, _ := secret.New(make([]byte, 32))
	lic, _ := license.New(d, box, license.Options{Endpoints: []string{ls.URL}, PublicKey: pub, Fingerprint: "fp"})
	if _, err := lic.Activate(context.Background(), "HM-T"); err != nil {
		t.Fatal(err)
	}
	e := &env{d: d, hub: &fakeHub{pushed: map[int64]int{}, busy: map[int]bool{}}, notes: &fakeNotify{}, now: time.Unix(1_800_000_000, 0), ctx: context.Background()}
	w := wallet.New(d)
	e.m = &Manager{DB: d, Box: box, Wallet: w, Catalog: catalog.New(d), Settings: settings.NewStore(d), Jobs: jobs.New(d),
		License: lic, Hub: e.hub, Notifier: e.notes, PublicURL: "https://panel.example", Now: func() time.Time { return e.now }}
	e.m.RegisterJobs()
	d.Exec(`INSERT INTO users(id, email, created_at, updated_at) VALUES (1, 'c@x.io', 0, 0)`)
	e.userID = 1
	w.Apply(e.ctx, wallet.Entry{UserID: 1, Amount: 10_000_000, Type: "topup"})
	return e
}

func (e *env) balance() int64 {
	b, _ := e.m.Wallet.Balance(e.ctx, e.userID)
	return b
}

func (e *env) runJobs(t *testing.T) {
	for i := 0; i < 20; i++ {
		ran, err := e.m.Jobs.RunOne(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !ran {
			return
		}
	}
}

func setupPorts(t *testing.T, e *env) (productID, nodeID int64) {
	c := e.m.Catalog
	c.SavePortClass(e.ctx, catalog.PortClass{Name: "random", Kind: "random"})
	c.SavePortClass(e.ctx, catalog.PortClass{Name: "normal", Kind: "range", Expr: "1000-60000"})
	productID, err := c.SaveProduct(e.ctx, catalog.Product{Type: "port", Title: "Tehran", Enabled: true, Stock: -1,
		Options: pricing.Options{Models: []pricing.Model{pricing.ModelBuilder, pricing.ModelPayg}, Durations: []int{30}, Cores: []string{"neotunnel"}, Deliveries: []string{"shared", "dedicated"}}})
	if err != nil {
		t.Fatal(err)
	}
	c.SaveRule(e.ctx, pricing.Rule{Component: "port", Value: 100000, Enabled: true})
	c.SaveRule(e.ctx, pricing.Rule{Component: "gb", Value: 1000, Enabled: true})
	c.SaveRule(e.ctx, pricing.Rule{Component: "payg_gb", Value: 5000, Enabled: true})
	nodeID, err = e.m.SaveNode(e.ctx, Node{Name: "ir1", Kind: "tunnel", PublicHost: "5.5.5.5", Enabled: true, SaleEnabled: true,
		Options: NodeOptions{PortRanges: "30000-30010", ReservedPorts: "22"}, Secrets: &NodeSecrets{NTBaseURL: "https://127.0.0.1:2096/x", NTToken: "nt"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.SaveSharedTunnel(e.ctx, SharedTunnel{NodeID: nodeID, Core: "neotunnel", Name: "shared1", NTTunnel: "t1"}); err != nil {
		t.Fatal(err)
	}
	return
}

func TestPortServiceLifecycle(t *testing.T) {
	e := newEnv(t)
	pid, nid := setupPorts(t, e)
	before := e.balance()

	// Busy port on the server is refused; reserved port is refused.
	e.hub.busy[8443] = true
	_, err := e.m.PlaceOrder(e.ctx, 1, OrderRequest{ProductID: pid, Selection: pricing.Selection{Model: "builder", Days: 30, GB: 10, Ports: []pricing.PortSel{{Port: "8443"}}}})
	var oe OrderError
	if !errors.As(err, &oe) {
		t.Fatalf("busy port: %v", err)
	}

	o, err := e.m.PlaceOrder(e.ctx, 1, OrderRequest{ProductID: pid, IdempotencyKey: "k1",
		Selection: pricing.Selection{Model: "builder", Days: 30, GB: 10, Ports: []pricing.PortSel{{Port: "2053", ToPort: 443}, {}}}})
	if err != nil {
		t.Fatal(err)
	}
	if o.Amount != 2*100000+10*1000 || before-e.balance() != o.Amount {
		t.Fatalf("charged %d, balance moved %d", o.Amount, before-e.balance())
	}
	// Retried request: same order, charged once.
	o2, _ := e.m.PlaceOrder(e.ctx, 1, OrderRequest{ProductID: pid, IdempotencyKey: "k1",
		Selection: pricing.Selection{Model: "builder", Days: 30, GB: 10, Ports: []pricing.PortSel{{Port: "2053"}, {}}}})
	if o2.ID != o.ID || before-e.balance() != o.Amount {
		t.Fatal("idempotency broken")
	}
	if e.hub.pushed[nid] == 0 {
		t.Fatal("node not pushed")
	}
	svc, err := e.m.ServiceFor(e.ctx, o.ServiceID, 1)
	if err != nil || svc.Status != StActive || len(svc.Ports) != 2 || svc.QuotaBytes != 10<<30 {
		t.Fatalf("service %+v %v", svc, err)
	}
	random := svc.Ports[0].Port
	if random == 2053 {
		random = svc.Ports[1].Port
	}
	if random < 30000 || random > 30010 {
		t.Fatalf("random port %d outside pool", random)
	}
	// The same port cannot be sold twice.
	if _, err := e.m.PlaceOrder(e.ctx, 1, OrderRequest{ProductID: pid, Selection: pricing.Selection{Model: "builder", Days: 30, GB: 10, Ports: []pricing.PortSel{{Port: "2053"}}}}); !errors.As(err, &oe) {
		t.Fatalf("double sale: %v", err)
	}

	st, err := e.m.NodeState(e.ctx, nid)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Services) != 1 || st.Services[0].Tunnel != "shared1" || st.Services[0].QuotaBytes != 10<<30 || len(st.Tunnels) != 1 ||
		st.Tunnels[0].NTTunnel != "t1" || st.NeoTunnel == nil || st.NeoTunnel.Token != "nt" {
		t.Fatalf("state %+v", st)
	}
	found := false
	for _, p := range st.Services[0].Ports {
		if p.Port == 2053 && p.ToPort == 443 {
			found = true
		}
	}
	if !found {
		t.Fatalf("ports %+v", st.Services[0].Ports)
	}

	// Usage: absolute counters → deltas; a repeated report adds nothing.
	sid := o.ServiceID
	e.m.ApplyAgentUsage(e.ctx, nid, agentproto.Usage{Counters: []agentproto.Counter{{ServiceID: sid, Up: 1 << 30, Down: 4 << 30}}})
	e.m.ApplyAgentUsage(e.ctx, nid, agentproto.Usage{Counters: []agentproto.Counter{{ServiceID: sid, Up: 1 << 30, Down: 4 << 30}}})
	svc, _ = e.m.ServiceFor(e.ctx, sid, 1)
	if svc.UsedUp+svc.UsedDown != 5<<30 || !e.notes.has("usage.threshold") {
		t.Fatalf("used %d", svc.UsedUp+svc.UsedDown)
	}
	// Agent restarted with lost totals: its new total is new traffic.
	e.m.ApplyAgentUsage(e.ctx, nid, agentproto.Usage{Counters: []agentproto.Counter{{ServiceID: sid, Up: 0, Down: 5 << 30}}})
	svc, _ = e.m.ServiceFor(e.ctx, sid, 1)
	if svc.Status != StLimited || !e.notes.has("service.limited") {
		t.Fatalf("not limited at quota: %s %d", svc.Status, svc.UsedUp+svc.UsedDown)
	}
	st, _ = e.m.NodeState(e.ctx, nid)
	if !st.Services[0].Blocked {
		t.Fatal("limited service not blocked on agent")
	}

	// Renewal: new period, usage reset, active again, agent unblocked.
	if _, err := e.m.PlaceOrder(e.ctx, 1, OrderRequest{Kind: "renew", ServiceID: sid}); err != nil {
		t.Fatal(err)
	}
	svc, _ = e.m.ServiceFor(e.ctx, sid, 1)
	if svc.Status != StActive || svc.UsedUp+svc.UsedDown != 0 || svc.ExpiresAt != e.now.Unix()+60*86400 {
		t.Fatalf("after renew %+v", svc)
	}
	st, _ = e.m.NodeState(e.ctx, nid)
	if st.Services[0].Blocked || st.Services[0].Period != e.now.Unix() {
		t.Fatalf("renewal not in agent state: %+v", st.Services[0])
	}
	// Add-on: +5 GB.
	if _, err := e.m.PlaceOrder(e.ctx, 1, OrderRequest{Kind: "addon", ServiceID: sid, Selection: pricing.Selection{AddonKind: "gb", AddonQty: 5}}); err != nil {
		t.Fatal(err)
	}
	svc, _ = e.m.ServiceFor(e.ctx, sid, 1)
	if svc.QuotaBytes != 15<<30 {
		t.Fatalf("addon gb: %d", svc.QuotaBytes)
	}
	// Someone else's service is invisible.
	e.d.Exec(`INSERT INTO users(id, email, created_at, updated_at) VALUES (2, 'z@x.io', 0, 0)`)
	if _, err := e.m.ServiceFor(e.ctx, sid, 2); err == nil {
		t.Fatal("foreign service visible")
	}
	if _, err := e.m.PlaceOrder(e.ctx, 2, OrderRequest{Kind: "renew", ServiceID: sid}); err == nil {
		t.Fatal("renewed someone else's service")
	}

	// Expiry → suspended → deleted; ports are freed for resale.
	e.now = e.now.Add(61 * 24 * time.Hour)
	e.m.Lifecycle(e.ctx)
	if s, _ := e.m.ServiceFor(e.ctx, sid, 0); s.Status != StExpired {
		t.Fatalf("expected expired, got %s", s.Status)
	}
	e.now = e.now.Add(4 * 24 * time.Hour)
	e.m.Lifecycle(e.ctx)
	if s, _ := e.m.ServiceFor(e.ctx, sid, 0); s.Status != StSuspended {
		t.Fatalf("expected suspended, got %s", s.Status)
	}
	e.now = e.now.Add(8 * 24 * time.Hour)
	e.m.Lifecycle(e.ctx)
	if s, _ := e.m.ServiceFor(e.ctx, sid, 0); s.Status != StDeleted {
		t.Fatalf("expected deleted, got %s", s.Status)
	}
	if st, _ := e.m.NodeState(e.ctx, nid); len(st.Services) != 0 {
		t.Fatal("deleted service still on agent")
	}
	if _, err := e.m.PlaceOrder(e.ctx, 1, OrderRequest{ProductID: pid, Selection: pricing.Selection{Model: "builder", Days: 30, GB: 10, Ports: []pricing.PortSel{{Port: "2053"}}}}); err != nil {
		t.Fatalf("port not freed: %v", err)
	}
}

func TestDedicatedTimeoutRefunds(t *testing.T) {
	e := newEnv(t)
	pid, nid := setupPorts(t, e)
	before := e.balance()
	if _, err := e.m.PlaceOrder(e.ctx, 1, OrderRequest{ProductID: pid, Selection: pricing.Selection{Model: "builder", Days: 30, GB: 10, Delivery: "dedicated", PeerAddress: "10.0.0.1", Ports: []pricing.PortSel{{}}}}); err == nil {
		t.Fatal("private peer address accepted")
	}
	o, err := e.m.PlaceOrder(e.ctx, 1, OrderRequest{ProductID: pid, Selection: pricing.Selection{Model: "builder", Days: 30, GB: 10, Delivery: "dedicated", PeerAddress: "8.8.4.4", Ports: []pricing.PortSel{{}}}})
	if err != nil {
		t.Fatal(err)
	}
	svc, _ := e.m.ServiceFor(e.ctx, o.ServiceID, 1)
	if svc.Status != StWaitingPeer || svc.Tunnel == nil || svc.Tunnel.InstallCmd == "" {
		t.Fatalf("dedicated %+v", svc)
	}
	st, _ := e.m.NodeState(e.ctx, nid)
	var ded *agentproto.Tunnel
	for i := range st.Tunnels {
		if st.Tunnels[i].Mode == "dedicated" {
			ded = &st.Tunnels[i]
		}
	}
	if ded == nil || ded.NTPeerAddress != "8.8.4.4" {
		t.Fatalf("tunnels %+v", st.Tunnels)
	}
	// Agent reports the peer setup; the install script serves it.
	e.m.TunnelStatus(e.ctx, nid, []agentproto.TunnelStatus{{Name: ded.Name, Status: "waiting_peer", PeerSetup: "sudo neotunnel apply KEY"}})
	tok := svc.Tunnel.InstallCmd[strings.LastIndex(svc.Tunnel.InstallCmd, "/")+1 : len(svc.Tunnel.InstallCmd)-1]
	script, err := e.m.InstallScript(e.ctx, tok)
	if err != nil || !strings.Contains(script, "sudo neotunnel apply KEY") {
		t.Fatalf("script %q %v", script, err)
	}
	e.now = e.now.Add(73 * time.Hour)
	e.m.Lifecycle(e.ctx)
	if s, _ := e.m.ServiceFor(e.ctx, o.ServiceID, 0); s.Status != StFailed || e.balance() != before || !e.notes.has("service.cancelled") {
		t.Fatalf("not refunded: %s balance %d/%d", s.Status, e.balance(), before)
	}
}

func TestDedicatedConnects(t *testing.T) {
	e := newEnv(t)
	pid, nid := setupPorts(t, e)
	o, _ := e.m.PlaceOrder(e.ctx, 1, OrderRequest{ProductID: pid, Selection: pricing.Selection{Model: "builder", Days: 30, GB: 10, Delivery: "dedicated", PeerAddress: "8.8.4.4", Ports: []pricing.PortSel{{}}}})
	e.m.TunnelStatus(e.ctx, nid, []agentproto.TunnelStatus{{Name: "ns" + itoa(o.ServiceID), Status: "connected"}})
	if s, _ := e.m.ServiceFor(e.ctx, o.ServiceID, 1); s.Status != StActive || !e.notes.has("service.active") {
		t.Fatalf("status %s", s.Status)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestPayg(t *testing.T) {
	e := newEnv(t)
	pid, nid := setupPorts(t, e)
	o, err := e.m.PlaceOrder(e.ctx, 1, OrderRequest{ProductID: pid, Selection: pricing.Selection{Model: "payg", Ports: []pricing.PortSel{{}}}})
	if err != nil {
		t.Fatal(err)
	}
	if o.Amount != 0 {
		t.Fatalf("payg charged upfront %d", o.Amount)
	}
	before := e.balance()
	e.m.ApplyAgentUsage(e.ctx, nid, agentproto.Usage{Counters: []agentproto.Counter{{ServiceID: o.ServiceID, Down: 2 << 30}}})
	e.now = e.now.Add(2 * time.Hour)
	e.m.Lifecycle(e.ctx)
	if before-e.balance() != 10000 {
		t.Fatalf("payg billed %d", before-e.balance())
	}
	// Empty wallet → limited.
	e.m.Wallet.Apply(e.ctx, wallet.Entry{UserID: 1, Amount: -e.balance(), Type: "admin"})
	e.m.ApplyAgentUsage(e.ctx, nid, agentproto.Usage{Counters: []agentproto.Counter{{ServiceID: o.ServiceID, Down: 3 << 30}}})
	e.now = e.now.Add(2 * time.Hour)
	e.m.Lifecycle(e.ctx)
	if s, _ := e.m.ServiceFor(e.ctx, o.ServiceID, 1); s.Status != StLimited {
		t.Fatalf("payg without money: %s", s.Status)
	}
}

// ---------- outbound against a fake 3x-ui ----------

type fakePanel struct {
	mu      sync.Mutex
	clients map[string]xui.XClient
	fail    bool
}

func (f *fakePanel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ok := func(obj any) { json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": obj}) }
	if f.fail {
		w.WriteHeader(502)
		return
	}
	switch {
	case r.URL.Path == "/login":
		http.SetCookie(w, &http.Cookie{Name: "s", Value: "1", Path: "/"})
		ok(nil)
	case strings.HasPrefix(r.URL.Path, "/panel/api/inbounds/get/"):
		ok(xui.Inbound{ID: 1, Protocol: "vless", Port: 2083, StreamSettings: `{"network":"tcp","security":"reality","realitySettings":{"serverNames":["a.com"],"shortIds":["1"],"settings":{"publicKey":"K"}}}`})
	case r.URL.Path == "/panel/api/inbounds/list":
		var stats []xui.ClientStat
		for e := range f.clients {
			stats = append(stats, xui.ClientStat{Email: e, Up: 1 << 30, Down: 2 << 30})
		}
		ok([]xui.Inbound{{ID: 1, Protocol: "vless", ClientStats: stats}})
	case r.URL.Path == "/panel/api/inbounds/addClient" || strings.HasPrefix(r.URL.Path, "/panel/api/inbounds/updateClient/"):
		var body struct{ Settings string }
		json.NewDecoder(r.Body).Decode(&body)
		var s struct{ Clients []xui.XClient }
		json.Unmarshal([]byte(body.Settings), &s)
		for _, c := range s.Clients {
			f.clients[c.Email] = c
		}
		ok(nil)
	case strings.Contains(r.URL.Path, "/delClient/"):
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		for e, c := range f.clients {
			if c.ID == id {
				delete(f.clients, e)
			}
		}
		ok(nil)
	case strings.Contains(r.URL.Path, "/resetClientTraffic/"):
		ok(nil)
	default:
		http.NotFound(w, r)
	}
}

func TestOutboundMultiLocation(t *testing.T) {
	e := newEnv(t)
	panel := &fakePanel{clients: map[string]xui.XClient{}}
	srv := httptest.NewServer(panel)
	defer srv.Close()
	c := e.m.Catalog
	de, _ := c.SaveLocation(e.ctx, catalog.Location{Name: "Germany", Flag: "🇩🇪"})
	nl, _ := c.SaveLocation(e.ctx, catalog.Location{Name: "Netherlands", Flag: "🇳🇱"})
	nid, err := e.m.SaveNode(e.ctx, Node{Name: "de-xui", Kind: "xui", PublicHost: "de.example.com", Enabled: true, SaleEnabled: true,
		Secrets: &NodeSecrets{XUIBaseURL: srv.URL, XUIUsername: "a", XUIPassword: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.SaveOutboundTemplate(e.ctx, OutboundTemplate{NodeID: nid, InboundID: 1, LocationID: de, Label: "Reality", Flow: "xtls-rprx-vision", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.SaveOutboundTemplate(e.ctx, OutboundTemplate{NodeID: nid, InboundID: 1, LocationID: nl, Enabled: true}); err == nil {
		t.Fatal("same inbound twice accepted")
	}
	// Give NL its own inbound id 1 on another panel record would be realistic; here reuse id via a second node.
	nid2, _ := e.m.SaveNode(e.ctx, Node{Name: "nl-xui", Kind: "xui", PublicHost: "nl.example.com", Enabled: true, SaleEnabled: true,
		Secrets: &NodeSecrets{XUIBaseURL: srv.URL, XUIUsername: "a", XUIPassword: "b"}})
	e.m.SaveOutboundTemplate(e.ctx, OutboundTemplate{NodeID: nid2, InboundID: 1, LocationID: nl, Enabled: true})

	pid, _ := c.SaveProduct(e.ctx, catalog.Product{Type: "outbound", Title: "Outbound", Enabled: true, Stock: -1,
		Options: pricing.Options{Models: []pricing.Model{pricing.ModelBuilder}, Durations: []int{30}}})
	c.SaveRule(e.ctx, pricing.Rule{Component: "gb", Value: 1000, Enabled: true})

	o, err := e.m.PlaceOrder(e.ctx, 1, OrderRequest{ProductID: pid, Selection: pricing.Selection{Model: "builder", Days: 30,
		Locations: []pricing.LocSel{{LocationID: de, GB: 10}, {LocationID: nl, GB: 5}}}})
	if err != nil {
		t.Fatal(err)
	}
	e.runJobs(t)
	svc, _ := e.m.ServiceFor(e.ctx, o.ServiceID, 1)
	if svc.Status != StActive || len(svc.Locations) != 2 || len(panel.clients) != 2 {
		t.Fatalf("outbound %+v clients %d", svc, len(panel.clients))
	}
	for _, cl := range panel.clients {
		if !cl.Enable || cl.ExpiryTime != svc.ExpiresAt*1000 || cl.SubID == "" {
			t.Fatalf("client %+v", cl)
		}
	}
	tok := svc.SubURL[strings.LastIndex(svc.SubURL, "/")+1:]
	sub, err := e.m.Subscription(e.ctx, tok, 0)
	if err != nil || len(sub.Links) != 2 || len(sub.Outbounds) != 2 || sub.Total != 15<<30 {
		t.Fatalf("sub %+v %v", sub, err)
	}
	only, _ := e.m.Subscription(e.ctx, tok, de)
	if len(only.Links) != 1 || !strings.Contains(only.Links[0], "de.example.com") || only.Total != 10<<30 {
		t.Fatalf("per-location sub %+v", only)
	}

	// Usage: each client reports 3 GB. NL (5 GB) is fine; poll twice → NL 6 GB > 5 GB → limited.
	e.m.PollXUI(e.ctx)
	svc, _ = e.m.ServiceFor(e.ctx, o.ServiceID, 1)
	if svc.UsedUp+svc.UsedDown != 6<<30 {
		t.Fatalf("used %d", svc.UsedUp+svc.UsedDown)
	}
	e.d.Exec(`UPDATE service_location_clients SET last_up = 0, last_down = 0`) // panel counters doubled
	e.m.PollXUI(e.ctx)
	svc, _ = e.m.ServiceFor(e.ctx, o.ServiceID, 1)
	var nlStatus, deStatus string
	for _, l := range svc.Locations {
		if l.LocationID == nl {
			nlStatus = l.Status
		} else {
			deStatus = l.Status
		}
	}
	if nlStatus != "limited" || deStatus != "active" || svc.Status != StActive {
		t.Fatalf("location quotas: nl=%s de=%s svc=%s", nlStatus, deStatus, svc.Status)
	}
	e.runJobs(t)
	for email, cl := range panel.clients {
		wantOn := !strings.Contains(email, "-"+itoa(nl)+"-")
		if cl.Enable != wantOn {
			t.Fatalf("client %s enable=%v want %v", email, cl.Enable, wantOn)
		}
	}
	// Extra GB for NL re-enables it.
	if _, err := e.m.PlaceOrder(e.ctx, 1, OrderRequest{Kind: "addon", ServiceID: o.ServiceID, Selection: pricing.Selection{AddonKind: "gb", AddonQty: 10, Locations: []pricing.LocSel{{LocationID: nl}}}}); err != nil {
		t.Fatal(err)
	}
	e.runJobs(t)
	for _, cl := range panel.clients {
		if !cl.Enable {
			t.Fatal("NL not re-enabled after add-on")
		}
	}
	// Delete removes clients from the panel.
	e.m.Delete(e.ctx, o.ServiceID, "test")
	e.runJobs(t)
	if len(panel.clients) != 0 {
		t.Fatalf("clients left: %d", len(panel.clients))
	}
	if _, err := e.m.Subscription(e.ctx, tok, 0); err == nil {
		t.Fatal("sub of deleted service still served")
	}
}

func TestOutboundProvisionFailureRefunds(t *testing.T) {
	e := newEnv(t)
	panel := &fakePanel{clients: map[string]xui.XClient{}}
	srv := httptest.NewServer(panel)
	defer srv.Close()
	c := e.m.Catalog
	de, _ := c.SaveLocation(e.ctx, catalog.Location{Name: "DE"})
	nid, _ := e.m.SaveNode(e.ctx, Node{Name: "x", Kind: "xui", PublicHost: "h", Enabled: true, Secrets: &NodeSecrets{XUIBaseURL: srv.URL, XUIUsername: "a", XUIPassword: "b"}})
	e.m.SaveOutboundTemplate(e.ctx, OutboundTemplate{NodeID: nid, InboundID: 1, LocationID: de, Enabled: true})
	pid, _ := c.SaveProduct(e.ctx, catalog.Product{Type: "outbound", Title: "O", Enabled: true, Stock: 1,
		Options: pricing.Options{Models: []pricing.Model{pricing.ModelBuilder}, Durations: []int{30}}})
	c.SaveRule(e.ctx, pricing.Rule{Component: "gb", Value: 1000, Enabled: true})
	before := e.balance()
	panel.fail = true
	o, err := e.m.PlaceOrder(e.ctx, 1, OrderRequest{ProductID: pid, Selection: pricing.Selection{Model: "builder", Days: 30, Locations: []pricing.LocSel{{LocationID: de, GB: 10}}}})
	if err != nil {
		t.Fatal(err)
	}
	// Stock 1 is now used up.
	if _, err := e.m.PlaceOrder(e.ctx, 1, OrderRequest{ProductID: pid, Selection: pricing.Selection{Model: "builder", Days: 30, Locations: []pricing.LocSel{{LocationID: de, GB: 10}}}}); err == nil {
		t.Fatal("stock not enforced")
	}
	e.runJobs(t)
	e.now = e.now.Add(2 * time.Hour)
	e.m.Lifecycle(e.ctx)
	if s, _ := e.m.ServiceFor(e.ctx, o.ServiceID, 0); s.Status != StFailed || e.balance() != before {
		t.Fatalf("status %s balance %d/%d", s.Status, e.balance(), before)
	}
	if bad, _ := e.m.Wallet.Verify(e.ctx); len(bad) != 0 {
		t.Fatal("ledger mismatch")
	}
}
