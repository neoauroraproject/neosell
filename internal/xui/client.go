// Package xui talks to 3x-ui (Sanaei) panels: inbounds, clients and traffic.
package xui

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

type Config struct {
	BaseURL  string `json:"base_url"` // https://host:port/<webBasePath>
	Username string `json:"username"`
	Password string `json:"password"`
	Insecure bool   `json:"insecure"` // self-signed panel certificate
	Proxy    string `json:"proxy,omitempty"`
}

type Client struct {
	cfg  Config
	http *http.Client
	mu   sync.Mutex
	in   bool
}

func New(cfg Config) *Client {
	jar, _ := cookiejar.New(nil)
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.Insecure}}
	if cfg.Proxy != "" {
		if u, err := url.Parse(cfg.Proxy); err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	return &Client{cfg: cfg, http: &http.Client{Jar: jar, Timeout: 30 * time.Second, Transport: tr}}
}

type ClientStat struct {
	ID         int64  `json:"id"`
	InboundID  int64  `json:"inboundId"`
	Email      string `json:"email"`
	Up         int64  `json:"up"`
	Down       int64  `json:"down"`
	Total      int64  `json:"total"`
	ExpiryTime int64  `json:"expiryTime"`
	Enable     bool   `json:"enable"`
}

type Inbound struct {
	ID             int64        `json:"id"`
	Remark         string       `json:"remark"`
	Enable         bool         `json:"enable"`
	Port           int          `json:"port"`
	Listen         string       `json:"listen"`
	Protocol       string       `json:"protocol"`
	Settings       string       `json:"settings"`
	StreamSettings string       `json:"streamSettings"`
	ClientStats    []ClientStat `json:"clientStats"`
}

type resp struct {
	Success bool            `json:"success"`
	Msg     string          `json:"msg"`
	Obj     json.RawMessage `json:"obj"`
}

var ErrAuth = errors.New("ورود به پنل ثنایی ناموفق بود (نام کاربری/رمز)")

func (c *Client) base() string { return strings.TrimRight(c.cfg.BaseURL, "/") }

func (c *Client) login(ctx context.Context) error {
	form := url.Values{"username": {c.cfg.Username}, "password": {c.cfg.Password}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+"/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var r resp
	json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&r)
	if res.StatusCode != 200 || !r.Success {
		return ErrAuth
	}
	c.in = true
	return nil
}

// call performs an API request, logging in first and once more on 401/redirect.
func (c *Client) call(ctx context.Context, method, path string, body any, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		if !c.in {
			if err := c.login(ctx); err != nil {
				return err
			}
		}
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.base()+path, rd)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := c.http.Do(req)
		if err != nil {
			return err
		}
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 32<<20))
		res.Body.Close()
		if res.StatusCode == 401 || res.StatusCode == 404 && attempt == 0 && !json.Valid(raw) {
			c.in = false
			continue
		}
		var r resp
		if err := json.Unmarshal(raw, &r); err != nil {
			// Expired sessions get an HTML login page.
			c.in = false
			if attempt == 0 {
				continue
			}
			return fmt.Errorf("پاسخ نامعتبر از پنل ثنایی (HTTP %d)", res.StatusCode)
		}
		if !r.Success {
			return fmt.Errorf("پنل ثنایی: %s", r.Msg)
		}
		if out != nil && len(r.Obj) > 0 && string(r.Obj) != "null" {
			return json.Unmarshal(r.Obj, out)
		}
		return nil
	}
	return ErrAuth
}

func (c *Client) Inbounds(ctx context.Context) ([]Inbound, error) {
	var out []Inbound
	err := c.call(ctx, http.MethodGet, "/panel/api/inbounds/list", nil, &out)
	return out, err
}

func (c *Client) Inbound(ctx context.Context, id int64) (*Inbound, error) {
	var out Inbound
	if err := c.call(ctx, http.MethodGet, fmt.Sprintf("/panel/api/inbounds/get/%d", id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// XClient is a client entry inside an inbound's settings.
type XClient struct {
	ID         string `json:"id,omitempty"`       // vless/vmess uuid
	Password   string `json:"password,omitempty"` // trojan / shadowsocks
	Method     string `json:"method,omitempty"`   // shadowsocks (per-client, older panels)
	Flow       string `json:"flow,omitempty"`
	Email      string `json:"email"`
	LimitIP    int    `json:"limitIp"`
	TotalGB    int64  `json:"totalGB"`    // bytes, despite the name
	ExpiryTime int64  `json:"expiryTime"` // unix ms, 0 = never
	Enable     bool   `json:"enable"`
	TgID       string `json:"tgId"`
	SubID      string `json:"subId"`
	Comment    string `json:"comment,omitempty"`
	Reset      int    `json:"reset"`
}

func settingsJSON(clients ...XClient) map[string]any {
	b, _ := json.Marshal(map[string]any{"clients": clients})
	return map[string]any{"settings": string(b)}
}

func (c *Client) AddClient(ctx context.Context, inboundID int64, cl XClient) error {
	body := settingsJSON(cl)
	body["id"] = inboundID
	return c.call(ctx, http.MethodPost, "/panel/api/inbounds/addClient", body, nil)
}

// ClientKey is what 3x-ui uses to address a client in update/delete calls.
func ClientKey(protocol string, cl XClient) string {
	switch protocol {
	case "trojan":
		return cl.Password
	case "shadowsocks":
		return cl.Email
	default:
		return cl.ID
	}
}

func (c *Client) UpdateClient(ctx context.Context, inboundID int64, protocol string, cl XClient) error {
	body := settingsJSON(cl)
	body["id"] = inboundID
	return c.call(ctx, http.MethodPost, "/panel/api/inbounds/updateClient/"+url.PathEscape(ClientKey(protocol, cl)), body, nil)
}

func (c *Client) DelClient(ctx context.Context, inboundID int64, protocol string, cl XClient) error {
	err := c.call(ctx, http.MethodPost, fmt.Sprintf("/panel/api/inbounds/%d/delClient/%s", inboundID, url.PathEscape(ClientKey(protocol, cl))), nil, nil)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "not found") {
		return nil
	}
	return err
}

func (c *Client) ResetClientTraffic(ctx context.Context, inboundID int64, email string) error {
	return c.call(ctx, http.MethodPost, fmt.Sprintf("/panel/api/inbounds/%d/resetClientTraffic/%s", inboundID, url.PathEscape(email)), nil, nil)
}

// Stats returns traffic of every client on the panel, by email, in one request.
func (c *Client) Stats(ctx context.Context) (map[string]ClientStat, error) {
	ins, err := c.Inbounds(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]ClientStat{}
	for _, in := range ins {
		for _, s := range in.ClientStats {
			out[s.Email] = s
		}
	}
	return out, nil
}
