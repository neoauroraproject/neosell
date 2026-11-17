package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"neosell/internal/agentproto"
)

var tplFuncs = template.FuncMap{
	"join": strings.Join,
	"add":  func(a, b int) int { return a + b },
}

// TplData is what core config templates can use:
//
//	{{.Tunnel.Name}} {{.Tunnel.BridgePort}} {{.Tunnel.Secret}} {{.Tunnel.Mode}}
//	{{.Node.PublicHost}}  {{.PortList}} ("443,8443")
//	{{range .Ports}}{{.Port}} {{.ToPort}} {{.Proto}}{{end}}
type TplData struct {
	Tunnel struct {
		Name       string
		BridgePort int
		Secret     string
		Mode       string
	}
	Node     struct{ PublicHost string }
	Ports    []agentproto.Port
	PortList string
}

func render(body string, d TplData) (string, error) {
	t, err := template.New("t").Funcs(tplFuncs).Option("missingkey=error").Parse(body)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, d); err != nil {
		return "", err
	}
	return b.String(), nil
}

type tunnelRow struct {
	ID, NodeID, ServiceID         int64
	Core, Name, Mode, Secret      string
	BridgePort                    int
	PeerToken, PeerStatus, Params string
}

func (m *Manager) tunnelsOf(ctx context.Context, nodeID int64) ([]tunnelRow, error) {
	rows, err := m.DB.QueryContext(ctx, `SELECT t.id, t.node_id, COALESCE(t.service_id,0), t.core, t.name, t.mode, t.secret, t.bridge_port,
		COALESCE(t.peer_token,''), t.peer_status, t.params FROM tunnels t
		LEFT JOIN services s ON s.id = t.service_id
		WHERE t.node_id = ? AND (t.service_id IS NULL OR s.status NOT IN ('deleted','failed')) ORDER BY t.id`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tunnelRow
	for rows.Next() {
		var t tunnelRow
		if err := rows.Scan(&t.ID, &t.NodeID, &t.ServiceID, &t.Core, &t.Name, &t.Mode, &t.Secret, &t.BridgePort, &t.PeerToken, &t.PeerStatus, &t.Params); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// NodeState computes everything a node's agent must run and enforce.
func (m *Manager) NodeState(ctx context.Context, nodeID int64) (agentproto.State, error) {
	st := agentproto.State{Rev: time.Now().UnixNano(), Services: []agentproto.Service{}, Tunnels: []agentproto.Tunnel{}}
	node, err := m.Node(ctx, nodeID)
	if err != nil {
		return st, err
	}
	sec, _ := m.nodeSecrets(ctx, nodeID)
	if sec.NTBaseURL != "" {
		st.NeoTunnel = &agentproto.NeoTunnel{BaseURL: sec.NTBaseURL, Token: sec.NTToken}
	}
	tunnels, err := m.tunnelsOf(ctx, nodeID)
	if err != nil {
		return st, err
	}
	sharedByCore := map[string]string{}
	dedicatedBySvc := map[int64]string{}
	for _, t := range tunnels {
		if t.ServiceID == 0 {
			if _, ok := sharedByCore[t.Core]; !ok {
				sharedByCore[t.Core] = t.Name
			}
		} else {
			dedicatedBySvc[t.ServiceID] = t.Name
		}
	}

	rows, err := m.DB.QueryContext(ctx, `SELECT `+svcCols+` FROM services WHERE node_id = ? AND type = 'port' AND status NOT IN ('deleted','failed','provisioning')`, nodeID)
	if err != nil {
		return st, err
	}
	var svcs []*svcRow
	for rows.Next() {
		s, err := scanSvc(rows)
		if err != nil {
			rows.Close()
			return st, err
		}
		svcs = append(svcs, s)
	}
	rows.Close()

	portsByTunnel := map[string][]agentproto.Port{}
	for _, s := range svcs {
		tunnel := sharedByCore[s.Core]
		if s.Delivery == "dedicated" {
			tunnel = dedicatedBySvc[s.ID]
		}
		as := agentproto.Service{ID: s.ID, Tunnel: tunnel, QuotaBytes: s.QuotaBytes, UsedBytes: s.UsedUp + s.UsedDown,
			Period: s.PeriodStart, ExpiresAt: s.ExpiresAt, RateMbit: s.RateMbit, MaxIPs: s.MaxIPs,
			Blocked: blockedStatus(s.Status)}
		prows, err := m.DB.QueryContext(ctx, `SELECT port, to_port FROM service_ports WHERE service_id = ? AND active = 1 ORDER BY port`, s.ID)
		if err != nil {
			return st, err
		}
		for prows.Next() {
			p := agentproto.Port{Proto: s.Proto}
			prows.Scan(&p.Port, &p.ToPort)
			as.Ports = append(as.Ports, p)
		}
		prows.Close()
		st.Services = append(st.Services, as)
		if tunnel != "" && !as.Blocked {
			portsByTunnel[tunnel] = append(portsByTunnel[tunnel], as.Ports...)
		}
	}

	var errs []string
	for _, t := range tunnels {
		ct, err := m.coreTemplate(ctx, t.Core)
		if err != nil || !ct.Enabled {
			errs = append(errs, fmt.Sprintf("tunnel %s: core %s not available", t.Name, t.Core))
			continue
		}
		at := agentproto.Tunnel{Name: t.Name, Core: t.Core, Driver: ct.Driver, Mode: t.Mode}
		var params map[string]string
		json.Unmarshal([]byte(t.Params), &params)
		if ct.Driver == "neotunnel" {
			at.NTTunnel = params["nt_tunnel"]
			if t.Mode == "dedicated" {
				at.NTPeerName = t.Name
				at.NTPeerAddress = params["peer_address"]
				at.NTMode = node.Options.NTMode
			}
		} else {
			ports := portsByTunnel[t.Name]
			sort.Slice(ports, func(i, j int) bool { return ports[i].Port < ports[j].Port })
			d := tplData(t, node.PublicHost, ports)
			cfg, err := render(ct.ServerConfig, d)
			if err != nil {
				errs = append(errs, fmt.Sprintf("tunnel %s: server config template: %v", t.Name, err))
				continue
			}
			execLine, err := render(ct.ServerExec, d)
			if err != nil {
				errs = append(errs, fmt.Sprintf("tunnel %s: exec template: %v", t.Name, err))
				continue
			}
			at.BinaryURL, at.BinarySHA256, at.Config, at.ConfigExt, at.Exec = ct.BinaryURL, ct.BinarySHA256, cfg, ct.ConfigExt, strings.TrimSpace(execLine)
		}
		st.Tunnels = append(st.Tunnels, at)
	}
	if len(errs) > 0 {
		return st, errors.New(strings.Join(errs, "; "))
	}
	return st, nil
}

func tplData(t tunnelRow, host string, ports []agentproto.Port) TplData {
	var d TplData
	d.Tunnel.Name, d.Tunnel.BridgePort, d.Tunnel.Secret, d.Tunnel.Mode = t.Name, t.BridgePort, t.Secret, t.Mode
	d.Node.PublicHost = host
	d.Ports = ports
	list := make([]string, len(ports))
	for i, p := range ports {
		list[i] = strconv.Itoa(p.Port)
	}
	d.PortList = strings.Join(list, ",")
	if d.Ports == nil {
		d.Ports = []agentproto.Port{}
	}
	return d
}

// InstallScript returns the bash script a customer (dedicated) or the admin
// (shared tunnel's foreign side) runs on the foreign server: /i/<token>.
func (m *Manager) InstallScript(ctx context.Context, token string) (string, error) {
	var t tunnelRow
	var setup string
	err := m.DB.QueryRowContext(ctx, `SELECT t.id, t.node_id, COALESCE(t.service_id,0), t.core, t.name, t.mode, t.secret, t.bridge_port, t.peer_setup
		FROM tunnels t LEFT JOIN services s ON s.id = t.service_id
		WHERE t.peer_token = ? AND (t.service_id IS NULL OR s.status NOT IN ('deleted','failed'))`, token).
		Scan(&t.ID, &t.NodeID, &t.ServiceID, &t.Core, &t.Name, &t.Mode, &t.Secret, &t.BridgePort, &setup)
	if err != nil {
		return "", ErrNotFound
	}
	ct, err := m.coreTemplate(ctx, t.Core)
	if err != nil {
		return "", err
	}
	node, err := m.Node(ctx, t.NodeID)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n# Neo Sell — foreign-side setup for tunnel " + t.Name + " (" + t.Core + ")\nset -euo pipefail\n")
	b.WriteString("[ \"$(id -u)\" = 0 ] || { echo 'run as root (sudo)'; exit 1; }\n")
	if ct.Driver == "neotunnel" {
		if setup == "" {
			b.WriteString("echo 'The Iran server has not prepared this tunnel yet. Try again in a minute.'; exit 1\n")
			return b.String(), nil
		}
		b.WriteString(setup + "\n")
		return b.String(), nil
	}
	if ct.ClientConfig == "" {
		return "", errors.New("قالب سمت خارج این هسته تعریف نشده است")
	}
	// The client side sees every port of this tunnel (for its forward rules).
	ports := []agentproto.Port{}
	st, _ := m.NodeState(ctx, t.NodeID)
	for _, s := range st.Services {
		if s.Tunnel == t.Name && !s.Blocked {
			ports = append(ports, s.Ports...)
		}
	}
	d := tplData(t, node.PublicHost, ports)
	cfg, err := render(ct.ClientConfig, d)
	if err != nil {
		return "", err
	}
	execLine, err := render(ct.ClientExec, d)
	if err != nil {
		return "", err
	}
	dir := "/opt/neosell-tunnel"
	cfgPath := dir + "/" + t.Name + "." + nz(ct.ConfigExt, "conf")
	bin := dir + "/" + t.Core
	execLine = strings.NewReplacer("{bin}", bin, "{config}", cfgPath).Replace(strings.TrimSpace(execLine))
	if execLine == "" {
		execLine = bin + " -c " + cfgPath
	}
	url := ct.BinaryURL
	fmt.Fprintf(&b, "mkdir -p %s\n", dir)
	b.WriteString("case \"$(uname -m)\" in x86_64|amd64) ARCH=amd64;; aarch64|arm64) ARCH=arm64;; *) echo unsupported arch; exit 1;; esac\n")
	if url != "" {
		fmt.Fprintf(&b, "curl -fsSL %q -o %s.tmp\n", strings.ReplaceAll(url, "{arch}", "'\"$ARCH\"'"), bin)
		if ct.BinarySHA256 != "" {
			fmt.Fprintf(&b, "echo %q | sha256sum -c - || { echo 'checksum mismatch'; exit 1; }\n", ct.BinarySHA256+"  "+bin+".tmp")
		}
		fmt.Fprintf(&b, "chmod +x %s.tmp && mv %s.tmp %s\n", bin, bin, bin)
	}
	fmt.Fprintf(&b, "cat > %s <<'NEOSELL_EOF'\n%s\nNEOSELL_EOF\nchmod 600 %s\n", cfgPath, strings.TrimRight(cfg, "\n"), cfgPath)
	unit := fmt.Sprintf("[Unit]\nDescription=Neo Sell tunnel %s\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nExecStart=%s\nRestart=always\nRestartSec=3\nLimitNOFILE=1048576\n\n[Install]\nWantedBy=multi-user.target", t.Name, execLine)
	fmt.Fprintf(&b, "cat > /etc/systemd/system/neosell-%s.service <<'NEOSELL_EOF'\n%s\nNEOSELL_EOF\n", t.Name, unit)
	fmt.Fprintf(&b, "systemctl daemon-reload\nsystemctl enable --now neosell-%s.service\nsystemctl restart neosell-%s.service\n", t.Name, t.Name)
	fmt.Fprintf(&b, "echo 'Tunnel %s is running. Check: systemctl status neosell-%s'\n", t.Name, t.Name)
	return b.String(), nil
}

// TunnelStatus applies what an agent reports about its tunnels.
func (m *Manager) TunnelStatus(ctx context.Context, nodeID int64, sts []agentproto.TunnelStatus) {
	for _, s := range sts {
		var tid, sid int64
		var peer string
		if m.DB.QueryRowContext(ctx, `SELECT id, COALESCE(service_id,0), peer_status FROM tunnels WHERE node_id = ? AND name = ?`, nodeID, s.Name).Scan(&tid, &sid, &peer) != nil {
			continue
		}
		if s.PeerSetup != "" {
			m.DB.ExecContext(ctx, `UPDATE tunnels SET peer_setup = ? WHERE id = ?`, s.PeerSetup, tid)
		}
		if s.Status == "connected" && peer != "connected" {
			m.DB.ExecContext(ctx, `UPDATE tunnels SET peer_status = 'connected' WHERE id = ?`, tid)
			if sid > 0 {
				if svc, err := m.service(ctx, sid); err == nil && svc.Status == StWaitingPeer {
					m.SetStatus(ctx, sid, StActive, "")
				}
			}
		}
	}
}
