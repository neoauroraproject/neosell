package xui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

const realityStream = `{"network":"tcp","security":"reality","tcpSettings":{"header":{"type":"none"}},
 "realitySettings":{"serverNames":["www.speedtest.net"],"shortIds":["ab12"],"settings":{"publicKey":"PBK","fingerprint":"firefox","spiderX":"/"}}}`

func TestLinksAndOutbounds(t *testing.T) {
	in := &Inbound{Protocol: "vless", Port: 2083, StreamSettings: realityStream}
	cl := XClient{ID: "1111-uuid", Flow: "xtls-rprx-vision", Email: "e"}
	e := Endpoint{Host: "de.example.com", Port: 2083, Tag: "DE-1"}
	link, err := ShareLink(in, cl, e)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"vless://1111-uuid@de.example.com:2083?", "security=reality", "pbk=PBK", "sid=ab12", "sni=www.speedtest.net", "fp=firefox", "flow=xtls-rprx-vision", "type=tcp", "#DE-1"} {
		if !strings.Contains(link, want) {
			t.Errorf("link missing %q: %s", want, link)
		}
	}
	ob, _ := Outbound(in, cl, e)
	b, _ := json.Marshal(ob)
	s := string(b)
	for _, want := range []string{`"tag":"DE-1"`, `"protocol":"vless"`, `"address":"de.example.com"`, `"flow":"xtls-rprx-vision"`, `"realitySettings":{"fingerprint":"firefox","publicKey":"PBK","serverName":"www.speedtest.net","shortId":"ab12","spiderX":"/"}`} {
		if !strings.Contains(s, want) {
			t.Errorf("outbound missing %q: %s", want, s)
		}
	}

	ws := &Inbound{Protocol: "vmess", StreamSettings: `{"network":"ws","security":"tls","wsSettings":{"path":"/ws","headers":{"Host":"cdn.x"}},"tlsSettings":{"serverName":"cdn.x","alpn":["h2","http/1.1"]}}`}
	vm, _ := ShareLink(ws, XClient{ID: "u2"}, Endpoint{Host: "cdn.x", Port: 443, Tag: "NL"})
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(vm, "vmess://"))
	var v map[string]string
	json.Unmarshal(raw, &v)
	if v["net"] != "ws" || v["path"] != "/ws" || v["host"] != "cdn.x" || v["tls"] != "tls" || v["alpn"] != "h2,http/1.1" {
		t.Fatalf("vmess %v", v)
	}
	tr, _ := ShareLink(&Inbound{Protocol: "trojan", StreamSettings: `{"network":"grpc","security":"tls","grpcSettings":{"serviceName":"g"}}`}, XClient{Password: "p@ss"}, Endpoint{Host: "1.2.3.4", Port: 8443, Tag: "FI"})
	if !strings.HasPrefix(tr, "trojan://p@ss@1.2.3.4:8443?") || !strings.Contains(tr, "serviceName=g") {
		t.Fatalf("trojan %s", tr)
	}
	ss, _ := ShareLink(&Inbound{Protocol: "shadowsocks", Settings: `{"method":"2022-blake3-aes-256-gcm"}`}, XClient{Password: "K"}, Endpoint{Host: "h", Port: 1, Tag: "S"})
	if !strings.HasPrefix(ss, "ss://") {
		t.Fatalf("ss %s", ss)
	}
	if _, err := ShareLink(&Inbound{Protocol: "wireguard"}, cl, e); err == nil {
		t.Fatal("unsupported protocol accepted")
	}
}

// fake3xui mimics the 3x-ui API: cookie login, inbounds with clientStats, add/update/del.
type fake3xui struct {
	mu      sync.Mutex
	clients map[string]XClient
	logins  int
}

func (f *fake3xui) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ok := func(obj any) { json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": obj}) }
	if r.URL.Path == "/xp/login" {
		r.ParseForm()
		if r.Form.Get("password") != "pw" {
			json.NewEncoder(w).Encode(map[string]any{"success": false, "msg": "wrong"})
			return
		}
		f.logins++
		http.SetCookie(w, &http.Cookie{Name: "3x-ui", Value: "s", Path: "/"})
		ok(nil)
		return
	}
	if c, err := r.Cookie("3x-ui"); err != nil || c.Value != "s" {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>login</html>"))
		return
	}
	switch {
	case r.URL.Path == "/xp/panel/api/inbounds/list":
		var stats []ClientStat
		for e := range f.clients {
			stats = append(stats, ClientStat{Email: e, Up: 10, Down: 20, Enable: true})
		}
		ok([]Inbound{{ID: 1, Protocol: "vless", Port: 2083, StreamSettings: realityStream, ClientStats: stats}})
	case r.URL.Path == "/xp/panel/api/inbounds/addClient":
		var body struct {
			ID       int64
			Settings string
		}
		json.NewDecoder(r.Body).Decode(&body)
		var s struct{ Clients []XClient }
		json.Unmarshal([]byte(body.Settings), &s)
		for _, c := range s.Clients {
			f.clients[c.Email] = c
		}
		ok(nil)
	case strings.HasPrefix(r.URL.Path, "/xp/panel/api/inbounds/updateClient/"):
		var body struct{ Settings string }
		json.NewDecoder(r.Body).Decode(&body)
		var s struct{ Clients []XClient }
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
	default:
		http.NotFound(w, r)
	}
}

func TestClientAgainstFakePanel(t *testing.T) {
	f := &fake3xui{clients: map[string]XClient{}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	ctx := context.Background()
	if _, err := New(Config{BaseURL: srv.URL + "/xp", Username: "a", Password: "bad"}).Inbounds(ctx); err != ErrAuth {
		t.Fatalf("bad password: %v", err)
	}
	c := New(Config{BaseURL: srv.URL + "/xp/", Username: "a", Password: "pw"})
	cl := XClient{ID: "u1", Email: "ns1-de-1", TotalGB: 1 << 30, Enable: true, SubID: "sub1"}
	if err := c.AddClient(ctx, 1, cl); err != nil {
		t.Fatal(err)
	}
	st, err := c.Stats(ctx)
	if err != nil || st["ns1-de-1"].Down != 20 {
		t.Fatalf("stats %v %v", st, err)
	}
	cl.Enable = false
	if err := c.UpdateClient(ctx, 1, "vless", cl); err != nil || f.clients["ns1-de-1"].Enable {
		t.Fatalf("update: %v", err)
	}
	// Session expiry → HTML page → transparent re-login.
	c.http.Jar.SetCookies(mustURL(srv.URL), []*http.Cookie{{Name: "3x-ui", Value: "expired", Path: "/"}})
	if _, err := c.Inbounds(ctx); err != nil || f.logins != 2 {
		t.Fatalf("relogin: %v logins=%d", err, f.logins)
	}
	if err := c.DelClient(ctx, 1, "vless", cl); err != nil || len(f.clients) != 0 {
		t.Fatalf("delete: %v %v", err, f.clients)
	}
}

func mustURL(s string) *url.URL {
	u, _ := url.Parse(s)
	return u
}
