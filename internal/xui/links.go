package xui

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Endpoint is where customers reach an inbound (may differ from the panel's
// own address, e.g. behind a CDN or a domain).
type Endpoint struct {
	Host string
	Port int
	Tag  string // outbound tag / link remark
}

type stream struct {
	Network         string         `json:"network"`
	Security        string         `json:"security"`
	TCPSettings     map[string]any `json:"tcpSettings"`
	WSSettings      map[string]any `json:"wsSettings"`
	GRPCSettings    map[string]any `json:"grpcSettings"`
	HTTPUpgrade     map[string]any `json:"httpupgradeSettings"`
	XHTTPSettings   map[string]any `json:"xhttpSettings"`
	TLSSettings     map[string]any `json:"tlsSettings"`
	RealitySettings map[string]any `json:"realitySettings"`
}

func str(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	switch v := m[k].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

func sub(m map[string]any, k string) map[string]any {
	if m == nil {
		return nil
	}
	v, _ := m[k].(map[string]any)
	return v
}

func first(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	switch v := m[k].(type) {
	case []any:
		if len(v) > 0 {
			s, _ := v[0].(string)
			return s
		}
	case string:
		return strings.Split(v, ",")[0]
	}
	return ""
}

func strs(m map[string]any, k string) []string {
	var out []string
	if arr, ok := m[k].([]any); ok {
		for _, x := range arr {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// transport holds the client-side view of an inbound's stream settings.
type transport struct {
	Network, Security                   string
	Path, Host, ServiceName, HeaderType string
	SNI, FP, ALPN, PBK, SID, SpiderX    string
	Mode                                string
}

func parseStream(raw string) transport {
	var s stream
	json.Unmarshal([]byte(raw), &s)
	t := transport{Network: s.Network, Security: s.Security}
	if t.Network == "" {
		t.Network = "tcp"
	}
	switch t.Network {
	case "tcp":
		h := sub(s.TCPSettings, "header")
		t.HeaderType = str(h, "type")
		if t.HeaderType == "http" {
			req := sub(h, "request")
			t.Path = first(req, "path")
			t.Host = first(sub(req, "headers"), "Host")
		}
	case "ws":
		t.Path = str(s.WSSettings, "path")
		t.Host = str(s.WSSettings, "host")
		if t.Host == "" {
			t.Host = str(sub(s.WSSettings, "headers"), "Host")
		}
	case "grpc":
		t.ServiceName = str(s.GRPCSettings, "serviceName")
	case "httpupgrade":
		t.Path, t.Host = str(s.HTTPUpgrade, "path"), str(s.HTTPUpgrade, "host")
	case "xhttp", "splithttp":
		t.Path, t.Host, t.Mode = str(s.XHTTPSettings, "path"), str(s.XHTTPSettings, "host"), str(s.XHTTPSettings, "mode")
	}
	switch t.Security {
	case "tls":
		t.SNI = str(s.TLSSettings, "serverName")
		t.FP = str(sub(s.TLSSettings, "settings"), "fingerprint")
		t.ALPN = strings.Join(strs(s.TLSSettings, "alpn"), ",")
	case "reality":
		rs := s.RealitySettings
		set := sub(rs, "settings")
		t.SNI = first(rs, "serverNames")
		if v := str(set, "serverName"); v != "" {
			t.SNI = v
		}
		t.FP = str(set, "fingerprint")
		t.PBK = str(set, "publicKey")
		t.SID = first(rs, "shortIds")
		t.SpiderX = str(set, "spiderX")
	}
	if t.FP == "" && (t.Security == "tls" || t.Security == "reality") {
		t.FP = "chrome"
	}
	return t
}

func (t transport) query(q url.Values) {
	q.Set("type", t.Network)
	sec := t.Security
	if sec == "" {
		sec = "none"
	}
	q.Set("security", sec)
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	set("path", t.Path)
	set("host", t.Host)
	set("serviceName", t.ServiceName)
	set("mode", t.Mode)
	if t.HeaderType != "" && t.HeaderType != "none" {
		q.Set("headerType", t.HeaderType)
	}
	set("sni", t.SNI)
	set("fp", t.FP)
	set("alpn", t.ALPN)
	set("pbk", t.PBK)
	set("sid", t.SID)
	set("spx", t.SpiderX)
}

// ssMethod reads the inbound-level shadowsocks method.
func ssMethod(settings string) string {
	var s struct {
		Method string `json:"method"`
	}
	json.Unmarshal([]byte(settings), &s)
	return s.Method
}

func hostPort(e Endpoint) string { return net.JoinHostPort(e.Host, strconv.Itoa(e.Port)) }

// ShareLink renders the standard client link for one client on an inbound.
func ShareLink(in *Inbound, cl XClient, e Endpoint) (string, error) {
	t := parseStream(in.StreamSettings)
	frag := "#" + url.PathEscape(e.Tag)
	switch in.Protocol {
	case "vless":
		q := url.Values{"encryption": {"none"}}
		t.query(q)
		if cl.Flow != "" {
			q.Set("flow", cl.Flow)
		}
		return "vless://" + cl.ID + "@" + hostPort(e) + "?" + q.Encode() + frag, nil
	case "trojan":
		q := url.Values{}
		t.query(q)
		return "trojan://" + url.PathEscape(cl.Password) + "@" + hostPort(e) + "?" + q.Encode() + frag, nil
	case "vmess":
		v := map[string]string{"v": "2", "ps": e.Tag, "add": e.Host, "port": strconv.Itoa(e.Port), "id": cl.ID, "aid": "0",
			"scy": "auto", "net": t.Network, "type": t.HeaderType, "host": t.Host, "path": t.Path, "tls": t.Security,
			"sni": t.SNI, "alpn": t.ALPN, "fp": t.FP}
		if t.Network == "grpc" {
			v["path"] = t.ServiceName
		}
		if v["type"] == "" {
			v["type"] = "none"
		}
		b, _ := json.Marshal(v)
		return "vmess://" + base64.StdEncoding.EncodeToString(b), nil
	case "shadowsocks":
		method := ssMethod(in.Settings)
		if cl.Method != "" {
			method = cl.Method
		}
		userinfo := base64.RawURLEncoding.EncodeToString([]byte(method + ":" + cl.Password))
		return "ss://" + userinfo + "@" + hostPort(e) + frag, nil
	}
	return "", fmt.Errorf("پروتکل %s پشتیبانی نمی‌شود", in.Protocol)
}

// Outbound renders an xray outbound object a customer can paste into their
// own xray config (e.g. on their Iran server) to chain through this inbound.
func Outbound(in *Inbound, cl XClient, e Endpoint) (map[string]any, error) {
	t := parseStream(in.StreamSettings)
	ob := map[string]any{"tag": e.Tag, "protocol": in.Protocol}
	switch in.Protocol {
	case "vless":
		user := map[string]any{"id": cl.ID, "encryption": "none"}
		if cl.Flow != "" {
			user["flow"] = cl.Flow
		}
		ob["settings"] = map[string]any{"vnext": []any{map[string]any{"address": e.Host, "port": e.Port, "users": []any{user}}}}
	case "vmess":
		ob["settings"] = map[string]any{"vnext": []any{map[string]any{"address": e.Host, "port": e.Port,
			"users": []any{map[string]any{"id": cl.ID, "alterId": 0, "security": "auto"}}}}}
	case "trojan":
		ob["settings"] = map[string]any{"servers": []any{map[string]any{"address": e.Host, "port": e.Port, "password": cl.Password}}}
	case "shadowsocks":
		method := ssMethod(in.Settings)
		if cl.Method != "" {
			method = cl.Method
		}
		ob["settings"] = map[string]any{"servers": []any{map[string]any{"address": e.Host, "port": e.Port, "method": method, "password": cl.Password}}}
	default:
		return nil, fmt.Errorf("پروتکل %s پشتیبانی نمی‌شود", in.Protocol)
	}
	ss := map[string]any{"network": t.Network}
	sec := t.Security
	if sec == "" {
		sec = "none"
	}
	ss["security"] = sec
	switch t.Network {
	case "tcp":
		if t.HeaderType == "http" {
			req := map[string]any{"path": []string{nz(t.Path, "/")}}
			if t.Host != "" {
				req["headers"] = map[string]any{"Host": []string{t.Host}}
			}
			ss["tcpSettings"] = map[string]any{"header": map[string]any{"type": "http", "request": req}}
		}
	case "ws":
		ws := map[string]any{"path": nz(t.Path, "/")}
		if t.Host != "" {
			ws["host"] = t.Host
		}
		ss["wsSettings"] = ws
	case "grpc":
		ss["grpcSettings"] = map[string]any{"serviceName": t.ServiceName}
	case "httpupgrade":
		ss["httpupgradeSettings"] = map[string]any{"path": nz(t.Path, "/"), "host": t.Host}
	case "xhttp", "splithttp":
		x := map[string]any{"path": nz(t.Path, "/"), "host": t.Host}
		if t.Mode != "" {
			x["mode"] = t.Mode
		}
		ss["xhttpSettings"] = x
	}
	switch sec {
	case "tls":
		tl := map[string]any{"serverName": nz(t.SNI, e.Host), "fingerprint": t.FP}
		if t.ALPN != "" {
			tl["alpn"] = strings.Split(t.ALPN, ",")
		}
		ss["tlsSettings"] = tl
	case "reality":
		ss["realitySettings"] = map[string]any{"serverName": t.SNI, "fingerprint": t.FP, "publicKey": t.PBK, "shortId": t.SID, "spiderX": t.SpiderX}
	}
	ob["streamSettings"] = ss
	return ob, nil
}

func nz(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// SupportedProtocol reports whether Neo Sell can sell clients on this protocol.
func SupportedProtocol(p string) bool {
	switch p {
	case "vless", "vmess", "trojan", "shadowsocks":
		return true
	}
	return false
}
