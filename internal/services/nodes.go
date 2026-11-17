package services

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"text/template"

	"neosell/internal/agentproto"
	"neosell/internal/db"
	"neosell/internal/xui"
)

// NodeOptions are admin settings per server (stored as JSON).
type NodeOptions struct {
	PortRanges    string   `json:"port_ranges"`    // random-port pool, e.g. "20000-60000"
	ReservedPorts string   `json:"reserved_ports"` // never sold, e.g. "22,80,443,2087"
	BridgeRange   string   `json:"bridge_range"`   // dedicated tunnel listen ports, e.g. "40000-45000"
	Capacity      int      `json:"capacity"`       // max live services (0 = unlimited)
	Weight        int      `json:"weight"`         // higher = preferred for new services
	Cores         []string `json:"cores"`          // cores available on this tunnel server
	NTMode        string   `json:"nt_mode"`        // NeoTunnel tunnel mode for dedicated tunnels
	Insecure      bool     `json:"insecure"`       // 3x-ui: self-signed certificate
}

// NodeSecrets are encrypted at rest and never returned by the API.
type NodeSecrets struct {
	XUIUsername string `json:"xui_username,omitempty"`
	XUIPassword string `json:"xui_password,omitempty"`
	XUIBaseURL  string `json:"xui_base_url,omitempty"`
	NTBaseURL   string `json:"nt_base_url,omitempty"`
	NTToken     string `json:"nt_token,omitempty"`
}

type Node struct {
	ID           int64           `json:"id"`
	Name         string          `json:"name"`
	Kind         string          `json:"kind"` // tunnel | xui
	LocationID   int64           `json:"location_id"`
	PublicHost   string          `json:"public_host"`
	Enabled      bool            `json:"enabled"`
	SaleEnabled  bool            `json:"sale_enabled"`
	Options      NodeOptions     `json:"options"`
	Status       string          `json:"status"`
	StatusDetail string          `json:"status_detail"`
	Info         json.RawMessage `json:"info"`
	LastSeenAt   int64           `json:"last_seen_at"`
	HasAgent     bool            `json:"has_agent"`
	// write-only secrets (empty strings keep the stored value)
	Secrets *NodeSecrets `json:"secrets,omitempty"`
	// read-only, safe hints
	XUIBaseURL string `json:"xui_base_url,omitempty"`
	NTBaseURL  string `json:"nt_base_url,omitempty"`
	Live       int    `json:"live_services"`
}

const nodeCols = `n.id, n.name, n.kind, COALESCE(n.location_id,0), n.public_host, n.enabled, n.sale_enabled, n.options, n.status,
	n.status_detail, n.info, n.last_seen_at, n.agent_token_hash IS NOT NULL, n.config_enc,
	(SELECT COUNT(1) FROM services s WHERE s.node_id = n.id AND s.status NOT IN ('deleted','failed'))`

func (m *Manager) scanNode(row interface{ Scan(...any) error }) (*Node, error) {
	n := &Node{}
	var opts, info, enc string
	if err := row.Scan(&n.ID, &n.Name, &n.Kind, &n.LocationID, &n.PublicHost, &n.Enabled, &n.SaleEnabled, &opts, &n.Status,
		&n.StatusDetail, &info, &n.LastSeenAt, &n.HasAgent, &enc, &n.Live); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(opts), &n.Options)
	n.Info = json.RawMessage(info)
	if sec, err := m.secrets(enc); err == nil {
		n.XUIBaseURL, n.NTBaseURL = sec.XUIBaseURL, sec.NTBaseURL
	}
	return n, nil
}

func (m *Manager) secrets(enc string) (NodeSecrets, error) {
	var s NodeSecrets
	if enc == "" {
		return s, nil
	}
	raw, err := m.Box.Open(enc)
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(raw, &s)
}

func (m *Manager) Nodes(ctx context.Context, kind string) ([]Node, error) {
	q := `SELECT ` + nodeCols + ` FROM nodes n`
	args := []any{}
	if kind != "" {
		q += ` WHERE n.kind = ?`
		args = append(args, kind)
	}
	rows, err := m.DB.QueryContext(ctx, q+` ORDER BY n.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Node{}
	for rows.Next() {
		n, err := m.scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

func (m *Manager) Node(ctx context.Context, id int64) (*Node, error) {
	n, err := m.scanNode(m.DB.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes n WHERE n.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return n, err
}

func (m *Manager) nodeSecrets(ctx context.Context, id int64) (NodeSecrets, error) {
	var enc string
	if err := m.DB.QueryRowContext(ctx, `SELECT config_enc FROM nodes WHERE id = ?`, id).Scan(&enc); err != nil {
		return NodeSecrets{}, err
	}
	return m.secrets(enc)
}

// SaveNode creates or updates a server. Empty secret fields keep stored values.
func (m *Manager) SaveNode(ctx context.Context, n Node) (int64, error) {
	n.Name = strings.TrimSpace(n.Name)
	if n.Name == "" {
		return 0, errors.New("نام سرور را وارد کنید")
	}
	if n.Kind != "tunnel" && n.Kind != "xui" {
		return 0, errors.New("نوع سرور باید tunnel یا xui باشد")
	}
	for _, r := range []string{n.Options.PortRanges, n.Options.BridgeRange} {
		if _, err := parseRanges(r); err != nil {
			return 0, err
		}
	}
	if _, err := parsePortSet(n.Options.ReservedPorts); err != nil {
		return 0, err
	}
	var cur NodeSecrets
	if n.ID > 0 {
		var err error
		if cur, err = m.nodeSecrets(ctx, n.ID); err != nil {
			return 0, ErrNotFound
		}
	}
	if s := n.Secrets; s != nil {
		merge := func(dst *string, v string) {
			if v != "" {
				*dst = strings.TrimSpace(v)
			}
		}
		merge(&cur.XUIBaseURL, s.XUIBaseURL)
		merge(&cur.XUIUsername, s.XUIUsername)
		merge(&cur.XUIPassword, s.XUIPassword)
		merge(&cur.NTBaseURL, s.NTBaseURL)
		merge(&cur.NTToken, s.NTToken)
	}
	if n.Kind == "xui" && (cur.XUIBaseURL == "" || cur.XUIUsername == "" || cur.XUIPassword == "") {
		return 0, errors.New("آدرس، نام کاربری و رمز پنل ثنایی لازم است")
	}
	raw, _ := json.Marshal(cur)
	enc, err := m.Box.Seal(raw)
	if err != nil {
		return 0, err
	}
	opts, _ := json.Marshal(n.Options)
	var loc any
	if n.LocationID > 0 {
		loc = n.LocationID
	}
	now := db.Now()
	if n.ID == 0 {
		res, err := m.DB.ExecContext(ctx, `INSERT INTO nodes(name, kind, location_id, public_host, enabled, sale_enabled, config_enc, options, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, n.Name, n.Kind, loc, strings.TrimSpace(n.PublicHost), n.Enabled, n.SaleEnabled, enc, string(opts), now, now)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err = m.DB.ExecContext(ctx, `UPDATE nodes SET name=?, location_id=?, public_host=?, enabled=?, sale_enabled=?, config_enc=?, options=?, updated_at=? WHERE id=?`,
		n.Name, loc, strings.TrimSpace(n.PublicHost), n.Enabled, n.SaleEnabled, enc, string(opts), now, n.ID)
	m.dropXUI(n.ID)
	if n.Kind == "tunnel" {
		m.push(n.ID)
	}
	return n.ID, err
}

func (m *Manager) DeleteNode(ctx context.Context, id int64) error {
	var live int
	m.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM services WHERE node_id = ? AND status NOT IN ('deleted','failed')`, id).Scan(&live)
	var tpl int
	m.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM service_location_clients c JOIN outbound_templates t ON t.id = c.template_id
		JOIN service_locations l ON l.id = c.service_location_id JOIN services s ON s.id = l.service_id
		WHERE t.node_id = ? AND s.status NOT IN ('deleted','failed')`, id).Scan(&tpl)
	if live+tpl > 0 {
		return errors.New("این سرور سرویس فعال دارد؛ اول فروش را متوقف و سرویس‌ها را منتقل یا حذف کنید")
	}
	_, err := m.DB.ExecContext(ctx, `DELETE FROM nodes WHERE id = ?`, id)
	m.dropXUI(id)
	return err
}

func tokenHash(t string) string {
	s := sha256.Sum256([]byte(t))
	return hex.EncodeToString(s[:])
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// RotateAgentToken issues a new agent token (the old one stops working).
func (m *Manager) RotateAgentToken(ctx context.Context, nodeID int64) (string, error) {
	tok := "nsa_" + randHex(24)
	res, err := m.DB.ExecContext(ctx, `UPDATE nodes SET agent_token_hash = ? WHERE id = ? AND kind = 'tunnel'`, tokenHash(tok), nodeID)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrNotFound
	}
	return tok, nil
}

func (m *Manager) NodeByAgentToken(ctx context.Context, tok string) (int64, error) {
	var id int64
	err := m.DB.QueryRowContext(ctx, `SELECT id FROM nodes WHERE agent_token_hash = ? AND kind = 'tunnel'`, tokenHash(tok)).Scan(&id)
	return id, err
}

func (m *Manager) SetNodeStatus(ctx context.Context, id int64, status, detail string, info any) {
	if info != nil {
		raw, _ := json.Marshal(info)
		m.DB.ExecContext(ctx, `UPDATE nodes SET status=?, status_detail=?, info=?, last_seen_at=? WHERE id=?`, status, detail, string(raw), db.Now(), id)
		return
	}
	m.DB.ExecContext(ctx, `UPDATE nodes SET status=?, status_detail=?, last_seen_at=? WHERE id=?`, status, detail, db.Now(), id)
}

// --- 3x-ui clients (cached per node) ---

type xuiEntry struct {
	c   *xui.Client
	key string
}

var xuiCache sync.Map // nodeID → xuiEntry

func (m *Manager) dropXUI(id int64) { xuiCache.Delete(id) }

func (m *Manager) XUI(ctx context.Context, nodeID int64) (*xui.Client, error) {
	n, err := m.Node(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if n.Kind != "xui" {
		return nil, errors.New("این سرور پنل ثنایی نیست")
	}
	sec, err := m.nodeSecrets(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	cfg := xui.Config{BaseURL: sec.XUIBaseURL, Username: sec.XUIUsername, Password: sec.XUIPassword, Insecure: n.Options.Insecure, Proxy: m.Proxy}
	key := fmt.Sprintf("%v", cfg)
	if v, ok := xuiCache.Load(nodeID); ok && v.(xuiEntry).key == key {
		return v.(xuiEntry).c, nil
	}
	c := xui.New(cfg)
	xuiCache.Store(nodeID, xuiEntry{c: c, key: key})
	return c, nil
}

// --- core templates ---

type CoreTemplate struct {
	ID           int64  `json:"id"`
	Core         string `json:"core"`
	Title        string `json:"title"`
	Driver       string `json:"driver"`
	BinaryURL    string `json:"binary_url"`
	BinarySHA256 string `json:"binary_sha256"`
	ServerConfig string `json:"server_config"`
	ServerExec   string `json:"server_exec"`
	ClientConfig string `json:"client_config"`
	ClientExec   string `json:"client_exec"`
	ConfigExt    string `json:"config_ext"`
	Enabled      bool   `json:"enabled"`
}

func (m *Manager) CoreTemplates(ctx context.Context) ([]CoreTemplate, error) {
	rows, err := m.DB.QueryContext(ctx, `SELECT id, core, title, driver, binary_url, COALESCE(json_extract(options,'$.sha256'),''), server_config, server_exec,
		client_config, client_exec, config_ext, enabled FROM core_templates ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CoreTemplate{}
	for rows.Next() {
		var c CoreTemplate
		if err := rows.Scan(&c.ID, &c.Core, &c.Title, &c.Driver, &c.BinaryURL, &c.BinarySHA256, &c.ServerConfig, &c.ServerExec,
			&c.ClientConfig, &c.ClientExec, &c.ConfigExt, &c.Enabled); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (m *Manager) coreTemplate(ctx context.Context, core string) (*CoreTemplate, error) {
	all, err := m.CoreTemplates(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Core == core {
			return &all[i], nil
		}
	}
	return nil, fmt.Errorf("هسته‌ی %s تعریف نشده است", core)
}

func (m *Manager) SaveCoreTemplate(ctx context.Context, c CoreTemplate) (int64, error) {
	c.Core = strings.ToLower(strings.TrimSpace(c.Core))
	if !safeIdent(c.Core) {
		return 0, errors.New("نام هسته فقط حروف لاتین، عدد و - باشد")
	}
	if c.Driver != "neotunnel" && c.Driver != "template" {
		return 0, errors.New("درایور باید neotunnel یا template باشد")
	}
	if c.BinaryURL != "" && !strings.HasPrefix(c.BinaryURL, "https://") {
		return 0, errors.New("آدرس دانلود هسته باید https باشد")
	}
	for name, body := range map[string]string{"server": c.ServerConfig, "client": c.ClientConfig, "server_exec": c.ServerExec, "client_exec": c.ClientExec} {
		if _, err := template.New(name).Funcs(tplFuncs).Parse(body); err != nil {
			return 0, fmt.Errorf("قالب %s: %v", name, err)
		}
	}
	opts, _ := json.Marshal(map[string]string{"sha256": strings.ToLower(strings.TrimSpace(c.BinarySHA256))})
	if c.ConfigExt == "" {
		c.ConfigExt = "toml"
	}
	if c.ID == 0 {
		res, err := m.DB.ExecContext(ctx, `INSERT INTO core_templates(core, title, driver, binary_url, server_config, server_exec, client_config, client_exec, config_ext, options, enabled, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, c.Core, c.Title, c.Driver, c.BinaryURL, c.ServerConfig, c.ServerExec, c.ClientConfig, c.ClientExec, c.ConfigExt, string(opts), c.Enabled, db.Now())
		if err != nil {
			return 0, errors.New("این هسته قبلاً تعریف شده است")
		}
		return res.LastInsertId()
	}
	_, err := m.DB.ExecContext(ctx, `UPDATE core_templates SET core=?, title=?, driver=?, binary_url=?, server_config=?, server_exec=?, client_config=?, client_exec=?, config_ext=?, options=?, enabled=?, updated_at=? WHERE id=?`,
		c.Core, c.Title, c.Driver, c.BinaryURL, c.ServerConfig, c.ServerExec, c.ClientConfig, c.ClientExec, c.ConfigExt, string(opts), c.Enabled, db.Now(), c.ID)
	m.pushAll(ctx)
	return c.ID, err
}

func safeIdent(s string) bool {
	if s == "" || len(s) > 30 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// --- shared tunnels (seller's own foreign servers) ---

type SharedTunnel struct {
	ID         int64  `json:"id"`
	NodeID     int64  `json:"node_id"`
	Core       string `json:"core"`
	Name       string `json:"name"`
	NTTunnel   string `json:"nt_tunnel"`   // NeoTunnel: existing tunnel (e.g. t1)
	BridgePort int    `json:"bridge_port"` // template cores: port the foreign server connects to
	Secret     string `json:"secret,omitempty"`
	PeerToken  string `json:"peer_token,omitempty"`
}

func (m *Manager) SharedTunnels(ctx context.Context) ([]SharedTunnel, error) {
	rows, err := m.DB.QueryContext(ctx, `SELECT id, node_id, core, name, COALESCE(json_extract(params,'$.nt_tunnel'),''), bridge_port, secret, COALESCE(peer_token,'')
		FROM tunnels WHERE service_id IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SharedTunnel{}
	for rows.Next() {
		var t SharedTunnel
		if err := rows.Scan(&t.ID, &t.NodeID, &t.Core, &t.Name, &t.NTTunnel, &t.BridgePort, &t.Secret, &t.PeerToken); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (m *Manager) SaveSharedTunnel(ctx context.Context, t SharedTunnel) (int64, error) {
	if _, err := m.Node(ctx, t.NodeID); err != nil {
		return 0, errors.New("سرور پیدا نشد")
	}
	ct, err := m.coreTemplate(ctx, t.Core)
	if err != nil {
		return 0, err
	}
	t.Name = strings.TrimSpace(t.Name)
	if !safeIdent(strings.ToLower(t.Name)) {
		return 0, errors.New("نام تانل فقط حروف لاتین، عدد و - باشد")
	}
	if ct.Driver == "neotunnel" && t.NTTunnel == "" {
		return 0, errors.New("نام تانل موجود در NeoTunnel (مثلاً t1) را وارد کنید")
	}
	if ct.Driver == "template" && (t.BridgePort < 1 || t.BridgePort > 65535) {
		return 0, errors.New("پورت اتصال تانل را وارد کنید")
	}
	params, _ := json.Marshal(map[string]string{"nt_tunnel": t.NTTunnel})
	if t.ID == 0 {
		res, err := m.DB.ExecContext(ctx, `INSERT INTO tunnels(node_id, core, name, mode, bridge_port, secret, peer_token, params, created_at)
			VALUES (?, ?, ?, 'shared', ?, ?, ?, ?, ?)`, t.NodeID, t.Core, t.Name, t.BridgePort, randHex(16), randHex(20), string(params), db.Now())
		if err != nil {
			return 0, errors.New("نام تانل روی این سرور تکراری است")
		}
		m.push(t.NodeID)
		return res.LastInsertId()
	}
	_, err = m.DB.ExecContext(ctx, `UPDATE tunnels SET core=?, name=?, bridge_port=?, params=? WHERE id=? AND service_id IS NULL`, t.Core, t.Name, t.BridgePort, string(params), t.ID)
	m.push(t.NodeID)
	return t.ID, err
}

func (m *Manager) DeleteSharedTunnel(ctx context.Context, id int64) error {
	var node int64
	var core string
	if err := m.DB.QueryRowContext(ctx, `SELECT node_id, core FROM tunnels WHERE id = ? AND service_id IS NULL`, id).Scan(&node, &core); err != nil {
		return ErrNotFound
	}
	var n int
	m.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM services WHERE node_id = ? AND core = ? AND delivery = 'shared' AND status NOT IN ('deleted','failed')`, node, core).Scan(&n)
	if n > 0 {
		return errors.New("سرویس‌های فعال از این تانل استفاده می‌کنند")
	}
	_, err := m.DB.ExecContext(ctx, `DELETE FROM tunnels WHERE id = ?`, id)
	m.push(node)
	return err
}

// --- outbound templates (3x-ui inbounds) ---

type OutboundTemplate struct {
	ID         int64  `json:"id"`
	NodeID     int64  `json:"node_id"`
	InboundID  int64  `json:"inbound_id"`
	LocationID int64  `json:"location_id"`
	Label      string `json:"label"`
	PublicHost string `json:"public_host"`
	PublicPort int    `json:"public_port"`
	Tag        string `json:"tag"`
	Flow       string `json:"flow"`
	Protocol   string `json:"protocol"`
	Network    string `json:"network"`
	Security   string `json:"security"`
	Enabled    bool   `json:"enabled"`
}

const tplCols = `id, node_id, inbound_id, location_id, label, public_host, public_port, tag,
	COALESCE(json_extract(info,'$.flow'),''), COALESCE(json_extract(info,'$.protocol'),''), COALESCE(json_extract(info,'$.network'),''),
	COALESCE(json_extract(info,'$.security'),''), enabled`

func scanTpl(row interface{ Scan(...any) error }) (*OutboundTemplate, error) {
	t := &OutboundTemplate{}
	err := row.Scan(&t.ID, &t.NodeID, &t.InboundID, &t.LocationID, &t.Label, &t.PublicHost, &t.PublicPort, &t.Tag, &t.Flow, &t.Protocol, &t.Network, &t.Security, &t.Enabled)
	return t, err
}

func (m *Manager) OutboundTemplates(ctx context.Context) ([]OutboundTemplate, error) {
	rows, err := m.DB.QueryContext(ctx, `SELECT `+tplCols+` FROM outbound_templates ORDER BY location_id, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OutboundTemplate{}
	for rows.Next() {
		t, err := scanTpl(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (m *Manager) outboundTemplate(ctx context.Context, id int64) (*OutboundTemplate, error) {
	return scanTpl(m.DB.QueryRowContext(ctx, `SELECT `+tplCols+` FROM outbound_templates WHERE id = ?`, id))
}

// SaveOutboundTemplate validates against the live panel (the inbound must
// exist and use a supported protocol) and caches its transport details.
func (m *Manager) SaveOutboundTemplate(ctx context.Context, t OutboundTemplate) (int64, error) {
	c, err := m.XUI(ctx, t.NodeID)
	if err != nil {
		return 0, err
	}
	in, err := c.Inbound(ctx, t.InboundID)
	if err != nil {
		return 0, fmt.Errorf("اینباند %d روی پنل پیدا نشد: %v", t.InboundID, err)
	}
	if !xui.SupportedProtocol(in.Protocol) {
		return 0, fmt.Errorf("پروتکل %s پشتیبانی نمی‌شود", in.Protocol)
	}
	var loc int
	m.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM locations WHERE id = ?`, t.LocationID).Scan(&loc)
	if loc == 0 {
		return 0, errors.New("لوکیشن را انتخاب کنید")
	}
	var st struct{ Network, Security string }
	json.Unmarshal([]byte(in.StreamSettings), &st)
	if t.PublicPort == 0 {
		t.PublicPort = in.Port
	}
	if t.PublicHost == "" {
		n, _ := m.Node(ctx, t.NodeID)
		t.PublicHost = n.PublicHost
	}
	if t.PublicHost == "" {
		return 0, errors.New("آدرس عمومی (IP یا دامنه) را وارد کنید")
	}
	if t.Tag == "" {
		t.Tag = fmt.Sprintf("%s-%s-%d", in.Protocol, nz(st.Network, "tcp"), t.PublicPort)
	}
	info, _ := json.Marshal(map[string]string{"flow": t.Flow, "protocol": in.Protocol, "network": nz(st.Network, "tcp"), "security": st.Security})
	if t.ID == 0 {
		res, err := m.DB.ExecContext(ctx, `INSERT INTO outbound_templates(node_id, inbound_id, location_id, label, public_host, public_port, tag, info, enabled, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, t.NodeID, t.InboundID, t.LocationID, t.Label, t.PublicHost, t.PublicPort, t.Tag, string(info), t.Enabled, db.Now())
		if err != nil {
			return 0, errors.New("این اینباند قبلاً ثبت شده است")
		}
		return res.LastInsertId()
	}
	_, err = m.DB.ExecContext(ctx, `UPDATE outbound_templates SET location_id=?, label=?, public_host=?, public_port=?, tag=?, info=?, enabled=? WHERE id=?`,
		t.LocationID, t.Label, t.PublicHost, t.PublicPort, t.Tag, string(info), t.Enabled, t.ID)
	return t.ID, err
}

func (m *Manager) DeleteOutboundTemplate(ctx context.Context, id int64) error {
	var n int
	m.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM service_location_clients c JOIN service_locations l ON l.id = c.service_location_id
		JOIN services s ON s.id = l.service_id WHERE c.template_id = ? AND s.status NOT IN ('deleted','failed')`, id).Scan(&n)
	if n > 0 {
		return errors.New("سرویس‌های فعال روی این اینباند هستند؛ آن را غیرفعال کنید")
	}
	_, err := m.DB.ExecContext(ctx, `DELETE FROM outbound_templates WHERE id = ?`, id)
	return err
}

func nz(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// --- port parsing helpers ---

type portRange struct{ lo, hi int }

func parseRanges(expr string) ([]portRange, error) {
	var out []portRange
	for _, part := range strings.Split(strings.ReplaceAll(expr, " ", ""), ",") {
		if part == "" {
			continue
		}
		a, b, isRange := strings.Cut(part, "-")
		lo, err1 := strconv.Atoi(a)
		hi := lo
		var err2 error
		if isRange {
			hi, err2 = strconv.Atoi(b)
		}
		if err1 != nil || err2 != nil || lo < 1 || hi > 65535 || lo > hi {
			return nil, fmt.Errorf("بازه‌ی پورت نامعتبر: %s", part)
		}
		out = append(out, portRange{lo, hi})
	}
	return out, nil
}

func parsePortSet(expr string) (map[int]bool, error) {
	rs, err := parseRanges(expr)
	if err != nil {
		return nil, err
	}
	out := map[int]bool{}
	for _, r := range rs {
		for p := r.lo; p <= r.hi && p-r.lo < 70000; p++ {
			out[p] = true
		}
	}
	return out, nil
}

var _ = agentproto.Version
