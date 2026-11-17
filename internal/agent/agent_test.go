package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"neosell/internal/agentproto"
)

type fakeNFT struct {
	mu       sync.Mutex
	scripts  []string
	counters map[string]int64
	quotas   map[string]int64
}

func (f *fakeNFT) Available() bool { return true }
func (f *fakeNFT) Apply(s string) error {
	f.mu.Lock()
	f.scripts = append(f.scripts, s)
	f.mu.Unlock()
	return nil
}
func (f *fakeNFT) ResetCounters() (map[string]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.counters
	f.counters = map[string]int64{}
	return out, nil
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
func (f *fakeNFT) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.scripts[len(f.scripts)-1]
}

func TestRulesetShape(t *testing.T) {
	s := BuildRuleset([]svcRuntime{
		{Service: agentproto.Service{ID: 7, Ports: []agentproto.Port{{Port: 8443, Proto: "tcp"}, {Port: 443, Proto: "tcp"}, {Port: 53, Proto: "both"}},
			QuotaBytes: 1000, RateMbit: 50, MaxIPs: 2}, QuotaUsed: 400},
		{Service: agentproto.Service{ID: 9, Ports: []agentproto.Port{{Port: 2053, Proto: "udp"}}}, Block: true},
	}, []string{"1.2.3.4", "127.0.0.1", "2001:db8::1"})
	for _, want := range []string{
		"delete table inet neosell",
		"set local4 { type ipv4_addr; elements = { 1.2.3.4 } }",
		"set local6 { type ipv6_addr; elements = { 2001:db8::1 } }",
		"quota s7_q { over 1000 bytes used 400 bytes }",
		"limit s7_lu { rate over 6250 kbytes/second",
		"set s7_ip4 { type ipv4_addr; size 2; flags dynamic, timeout; timeout 5m; }",
		"ct original ip daddr @local4 meta l4proto tcp ct original proto-dst { 443, 8443 } jump s7_c",
		"meta l4proto { tcp, udp } ct original proto-dst { 53 } jump s7_c",
		"meta l4proto udp ct original proto-dst { 2053 } jump s9_c",
		"quota name \"s7_q\" drop",
		"ct direction original counter name \"s7_up\"",
		"ct direction reply counter name \"s7_down\"",
		"chain pre { type filter hook prerouting priority -150",
		"chain out { type filter hook output priority -150",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	// Blocked service: its chain only drops (no counting).
	i := strings.Index(s, "chain s9_c {")
	if i < 0 || !strings.HasPrefix(strings.TrimSpace(s[i+len("chain s9_c {"):]), "drop") {
		t.Errorf("blocked chain wrong:\n%s", s)
	}
	if strings.Contains(s, "127.0.0.1") {
		t.Error("loopback must not be in local set")
	}
}

func newAgent(t *testing.T) (*Agent, *fakeNFT) {
	f := &fakeNFT{counters: map[string]int64{}, quotas: map[string]int64{}}
	a, err := New(t.TempDir(), f)
	if err != nil {
		t.Fatal(err)
	}
	a.ips = func() []string { return []string{"1.2.3.4"} }
	a.drivers = map[string]Driver{} // no tunnels in these tests
	return a, f
}

func TestCountersFoldIntoAbsoluteTotals(t *testing.T) {
	a, f := newAgent(t)
	ctx := context.Background()
	a.SetState(ctx, agentproto.State{Rev: 1, Services: []agentproto.Service{{ID: 1, Ports: []agentproto.Port{{Port: 443, Proto: "tcp"}}, Period: 100}}})
	f.counters = map[string]int64{"s1_up": 100, "s1_down": 1000}
	u, _, _ := a.Tick()
	f.counters = map[string]int64{"s1_up": 5, "s1_down": 50}
	u, _, _ = a.Tick()
	if len(u.Counters) != 1 || u.Counters[0].Up != 105 || u.Counters[0].Down != 1050 {
		t.Fatalf("totals %+v", u)
	}
	// Survives a restart (state.json).
	a2, _ := New(a.dir, f)
	a2.drivers = map[string]Driver{}
	u2, _, _ := a2.Tick()
	if u2.Counters[0].Down != 1050 {
		t.Fatalf("after restart %+v", u2)
	}
}

func TestQuotaHitBlocksAndReports(t *testing.T) {
	a, f := newAgent(t)
	ctx := context.Background()
	a.SetState(ctx, agentproto.State{Rev: 1, Services: []agentproto.Service{{ID: 2, Ports: []agentproto.Port{{Port: 443, Proto: "tcp"}}, QuotaBytes: 1000, UsedBytes: 300, Period: 1}}})
	if !strings.Contains(f.last(), "quota s2_q { over 1000 bytes used 300 bytes }") {
		t.Fatalf("quota not preloaded:\n%s", f.last())
	}
	f.quotas = map[string]int64{"s2_q": 1000}
	_, ev, changed := a.Tick()
	if !changed || len(ev) != 1 || ev[0].Kind != "quota" {
		t.Fatalf("events %+v", ev)
	}
	if !strings.Contains(f.last(), "chain s2_c {\n\t\tdrop") {
		t.Fatalf("not blocked after quota:\n%s", f.last())
	}
	// Reported once, not every tick.
	if _, ev, _ := a.Tick(); len(ev) != 0 {
		t.Fatal("event repeated")
	}
	// Same period: a lower panel number never lowers local consumption.
	a.SetState(ctx, agentproto.State{Rev: 2, Services: []agentproto.Service{{ID: 2, Ports: []agentproto.Port{{Port: 443, Proto: "tcp"}}, QuotaBytes: 1000, UsedBytes: 500, Period: 1}}})
	if !strings.Contains(f.last(), "used 1000 bytes") {
		t.Fatalf("consumption went back:\n%s", f.last())
	}
	// Renewal (new period) resets it and unblocks.
	f.quotas = map[string]int64{}
	a.SetState(ctx, agentproto.State{Rev: 3, Services: []agentproto.Service{{ID: 2, Ports: []agentproto.Port{{Port: 443, Proto: "tcp"}}, QuotaBytes: 2000, UsedBytes: 0, Period: 2}}})
	if !strings.Contains(f.last(), "quota s2_q { over 2000 bytes used 0 bytes }") || strings.Contains(f.last(), "chain s2_c {\n\t\tdrop") {
		t.Fatalf("renewal not applied:\n%s", f.last())
	}
}

func TestExpiryCutsOffline(t *testing.T) {
	a, f := newAgent(t)
	now := time.Unix(1000, 0)
	a.now = func() time.Time { return now }
	a.SetState(context.Background(), agentproto.State{Services: []agentproto.Service{{ID: 3, Ports: []agentproto.Port{{Port: 80, Proto: "tcp"}}, ExpiresAt: 1500}}})
	now = time.Unix(1600, 0)
	_, ev, _ := a.Tick()
	if len(ev) != 1 || ev[0].Kind != "expired" || !strings.Contains(f.last(), "chain s3_c {\n\t\tdrop") {
		t.Fatalf("expiry not enforced: %+v\n%s", ev, f.last())
	}
	if p := a.PendingEvents(); len(p) != 1 {
		t.Fatal("hit should be pending until the panel blocks it")
	}
	a.SetState(context.Background(), agentproto.State{Services: []agentproto.Service{{ID: 3, Ports: []agentproto.Port{{Port: 80, Proto: "tcp"}}, ExpiresAt: 1500, Blocked: true}}})
	if p := a.PendingEvents(); len(p) != 0 {
		t.Fatal("pending event not cleared")
	}
}

func TestTemplateDriver(t *testing.T) {
	dir := t.TempDir()
	units := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "cores", "kariz"), 0o755)
	os.WriteFile(filepath.Join(dir, "cores", "kariz", "kariz"), []byte("bin"), 0o755)
	var calls []string
	d := &TemplateDriver{Dir: dir, UnitDir: units, Run: func(n string, a ...string) error { calls = append(calls, n+" "+strings.Join(a, " ")); return nil }}
	tun := agentproto.Tunnel{Name: "ns5", Core: "kariz", Driver: "template", Config: "port=443\n", ConfigExt: "toml", Exec: "{bin} -c {config}"}
	_, book, err := d.Sync(context.Background(), SyncInput{Tunnels: []agentproto.Tunnel{tun}})
	if err != nil {
		t.Fatal(err)
	}
	unit, _ := os.ReadFile(filepath.Join(units, "neosell-t-ns5.service"))
	if !strings.Contains(string(unit), filepath.Join(dir, "tunnels", "ns5.toml")) || !strings.Contains(strings.Join(calls, "\n"), "restart neosell-t-ns5.service") {
		t.Fatalf("unit %s calls %v", unit, calls)
	}
	// Unchanged config: no restart.
	calls = nil
	_, book, _ = d.Sync(context.Background(), SyncInput{Tunnels: []agentproto.Tunnel{tun}, Book: book})
	if len(calls) != 0 {
		t.Fatalf("restarted without change: %v", calls)
	}
	// Removed: disabled and deleted.
	_, _, _ = d.Sync(context.Background(), SyncInput{Book: book})
	if _, err := os.Stat(filepath.Join(units, "neosell-t-ns5.service")); !os.IsNotExist(err) || !strings.Contains(strings.Join(calls, "\n"), "disable --now neosell-t-ns5.service") {
		t.Fatalf("not removed: %v", calls)
	}
	if _, _, err := d.Sync(context.Background(), SyncInput{Tunnels: []agentproto.Tunnel{{Name: "../x", Core: "kariz"}}}); err == nil {
		t.Fatal("unsafe name accepted")
	}
}

func TestNeoTunnelDriver(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	paired := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/sec/api/peers":
			json.NewEncoder(w).Encode(map[string]string{"github": "curl … | sudo bash -s -- --apply KEY"})
		case "/sec/api/tunnels":
			if !paired {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]string{"error": "peer not paired"})
				return
			}
		}
	}))
	defer srv.Close()
	d := &NeoTunnelDriver{HTTP: srv.Client()}
	nt := &agentproto.NeoTunnel{BaseURL: srv.URL + "/sec", Token: "tok"}
	shared := agentproto.Tunnel{Name: "shared1", Driver: "neotunnel", Mode: "shared", NTTunnel: "t1"}
	ded := agentproto.Tunnel{Name: "ns9", Driver: "neotunnel", Mode: "dedicated", NTPeerName: "c9", NTPeerAddress: "5.6.7.8"}
	in := SyncInput{Tunnels: []agentproto.Tunnel{shared, ded}, NeoTunnel: nt,
		Ports: map[string][]agentproto.Port{"shared1": {{Port: 443, Proto: "tcp"}}, "ns9": {{Port: 2053, Proto: "tcp", ToPort: 443}}}}
	st, book, _ := d.Sync(context.Background(), in)
	var dedSt agentproto.TunnelStatus
	for _, s := range st {
		if s.Name == "ns9" {
			dedSt = s
		}
	}
	if dedSt.Status != "waiting_peer" || !strings.Contains(dedSt.PeerSetup, "--apply") {
		t.Fatalf("dedicated status %+v", dedSt)
	}
	mu.Lock()
	paired = true
	calls = nil
	mu.Unlock()
	in.Book = book
	st, book, err := d.Sync(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "POST /sec/api/tunnels") || !strings.Contains(joined, "POST /sec/api/forwards") || strings.Contains(joined, "POST /sec/api/peers") {
		t.Fatalf("calls after pairing: %v", calls)
	}
	// Remove everything.
	mu.Lock()
	calls = nil
	mu.Unlock()
	d.Sync(context.Background(), SyncInput{NeoTunnel: nt, Book: book})
	joined = strings.Join(calls, "\n")
	if strings.Count(joined, "/api/forwards/remove") != 2 || !strings.Contains(joined, "DELETE /sec/api/tunnels/ns9") || !strings.Contains(joined, "DELETE /sec/api/peers/ns9") {
		t.Fatalf("cleanup calls: %v", calls)
	}
}
