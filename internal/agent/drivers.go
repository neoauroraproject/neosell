package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"neosell/internal/agentproto"
)

// Driver keeps one kind of tunnel core in the desired shape.
type Driver interface {
	// Sync converges to in.Tunnels (all tunnels of this driver). Book is the
	// driver's own bookkeeping from the last run, returned updated.
	Sync(ctx context.Context, in SyncInput) ([]agentproto.TunnelStatus, json.RawMessage, error)
}

type SyncInput struct {
	Tunnels   []agentproto.Tunnel
	Ports     map[string][]agentproto.Port // by tunnel name
	NeoTunnel *agentproto.NeoTunnel
	Book      json.RawMessage
}

var safeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,40}$`)

// ---------------- template driver (Kariz, Backpack, any CLI core) ----------------

// TemplateDriver runs a core binary as a systemd service per tunnel, with the
// config the panel rendered from the admin's template.
type TemplateDriver struct {
	Dir string
	// Run executes a command (systemctl); replaced in tests.
	Run func(name string, args ...string) error
	// UnitDir is /etc/systemd/system by default.
	UnitDir string
	HTTP    *http.Client
}

type templateBook struct {
	Units map[string]string `json:"units"` // tunnel name → config hash
}

func (d *TemplateDriver) run(name string, args ...string) error {
	if d.Run != nil {
		return d.Run(name, args...)
	}
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return nil
}

func (d *TemplateDriver) unitDir() string {
	if d.UnitDir != "" {
		return d.UnitDir
	}
	return "/etc/systemd/system"
}

func unitName(tunnel string) string { return "neosell-t-" + tunnel + ".service" }

func (d *TemplateDriver) Sync(ctx context.Context, in SyncInput) ([]agentproto.TunnelStatus, json.RawMessage, error) {
	var book templateBook
	_ = json.Unmarshal(in.Book, &book)
	if book.Units == nil {
		book.Units = map[string]string{}
	}
	if len(in.Tunnels) == 0 && len(book.Units) == 0 {
		return nil, nil, nil
	}
	if runtime.GOOS != "linux" && d.Run == nil {
		return nil, in.Book, errors.New("tunnel cores need Linux")
	}
	var statuses []agentproto.TunnelStatus
	var errs []string
	want := map[string]bool{}
	reload := false
	type restart struct{ name string }
	var restarts []restart

	for _, t := range in.Tunnels {
		if !safeName.MatchString(t.Name) {
			errs = append(errs, "bad tunnel name "+t.Name)
			continue
		}
		want[t.Name] = true
		st := agentproto.TunnelStatus{Name: t.Name, Status: "running"}
		bin, err := d.ensureBinary(ctx, t)
		if err != nil {
			st.Status, st.Error = "error", err.Error()
			statuses = append(statuses, st)
			errs = append(errs, t.Name+": "+err.Error())
			continue
		}
		ext := t.ConfigExt
		if ext == "" {
			ext = "conf"
		}
		cfgPath := filepath.Join(d.Dir, "tunnels", t.Name+"."+strings.TrimPrefix(ext, "."))
		os.MkdirAll(filepath.Dir(cfgPath), 0o750)
		execLine := strings.NewReplacer("{bin}", bin, "{config}", cfgPath).Replace(t.Exec)
		if strings.TrimSpace(execLine) == "" {
			execLine = bin + " -c " + cfgPath
		}
		unit := fmt.Sprintf("[Unit]\nDescription=Neo Sell tunnel %s (%s)\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nExecStart=%s\nRestart=always\nRestartSec=3\nLimitNOFILE=1048576\n\n[Install]\nWantedBy=multi-user.target\n", t.Name, t.Core, execLine)
		sum := sha256.Sum256([]byte(t.Config + "\x00" + unit))
		hash := hex.EncodeToString(sum[:])
		if book.Units[t.Name] != hash {
			if err := os.WriteFile(cfgPath, []byte(t.Config), 0o600); err != nil {
				errs = append(errs, err.Error())
				continue
			}
			if err := os.WriteFile(filepath.Join(d.unitDir(), unitName(t.Name)), []byte(unit), 0o644); err != nil {
				errs = append(errs, err.Error())
				continue
			}
			reload = true
			restarts = append(restarts, restart{t.Name})
			book.Units[t.Name] = hash
		}
		statuses = append(statuses, st)
	}
	// Remove tunnels no longer wanted.
	var gone []string
	for name := range book.Units {
		if !want[name] {
			gone = append(gone, name)
		}
	}
	sort.Strings(gone)
	for _, name := range gone {
		d.run("systemctl", "disable", "--now", unitName(name))
		os.Remove(filepath.Join(d.unitDir(), unitName(name)))
		matches, _ := filepath.Glob(filepath.Join(d.Dir, "tunnels", name+".*"))
		for _, m := range matches {
			os.Remove(m)
		}
		delete(book.Units, name)
		reload = true
	}
	if reload {
		if err := d.run("systemctl", "daemon-reload"); err != nil {
			errs = append(errs, err.Error())
		}
	}
	for _, r := range restarts {
		if err := d.run("systemctl", "enable", unitName(r.name)); err != nil {
			errs = append(errs, err.Error())
		}
		if err := d.run("systemctl", "restart", unitName(r.name)); err != nil {
			errs = append(errs, err.Error())
		}
	}
	raw, _ := json.Marshal(book)
	if len(errs) > 0 {
		return statuses, raw, errors.New(strings.Join(errs, "; "))
	}
	return statuses, raw, nil
}

func (d *TemplateDriver) ensureBinary(ctx context.Context, t agentproto.Tunnel) (string, error) {
	if !safeName.MatchString(t.Core) {
		return "", errors.New("bad core name")
	}
	bin := filepath.Join(d.Dir, "cores", t.Core, t.Core)
	if _, err := os.Stat(bin); err == nil {
		if t.BinarySHA256 == "" || fileSHA(bin) == strings.ToLower(t.BinarySHA256) {
			return bin, nil
		}
	}
	if t.BinaryURL == "" {
		return "", fmt.Errorf("binary for %s is not installed and no download URL is set", t.Core)
	}
	url := strings.ReplaceAll(t.BinaryURL, "{arch}", runtime.GOARCH)
	if !strings.HasPrefix(url, "https://") {
		return "", errors.New("core download URL must be https")
	}
	c := d.HTTP
	if c == nil {
		c = &http.Client{Timeout: 5 * time.Minute}
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", t.Core, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download %s: HTTP %d", t.Core, resp.StatusCode)
	}
	os.MkdirAll(filepath.Dir(bin), 0o750)
	tmp := bin + ".download"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 512<<20)); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	f.Close()
	if want := strings.ToLower(t.BinarySHA256); want != "" && hex.EncodeToString(h.Sum(nil)) != want {
		os.Remove(tmp)
		return "", fmt.Errorf("checksum mismatch for %s", t.Core)
	}
	return bin, os.Rename(tmp, bin)
}

func fileSHA(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil))
}

// ---------------- NeoTunnel driver ----------------

// NeoTunnelDriver uses the NeoTunnel panel on this server only as transport:
// it creates peers/tunnels and forwards. Quotas, expiry, speed and IP limits
// are NOT set in NeoTunnel; the agent enforces them in its own nft table.
type NeoTunnelDriver struct {
	HTTP *http.Client
}

type ntForward struct {
	Tunnel string `json:"tunnel"`
	Ports  string `json:"ports"`
	Proto  string `json:"proto"`
	To     int    `json:"to"`
}

type ntBook struct {
	Forwards []ntForward       `json:"forwards"`
	Peers    map[string]string `json:"peers"`   // tunnel name → peer setup command
	Tunnels  map[string]bool   `json:"tunnels"` // dedicated tunnels created in NeoTunnel
}

func (f ntForward) key() string {
	return f.Tunnel + "|" + f.Ports + "|" + f.Proto + "|" + strconv.Itoa(f.To)
}

func (d *NeoTunnelDriver) client() *http.Client {
	if d.HTTP != nil {
		return d.HTTP
	}
	// The NeoTunnel panel on 127.0.0.1 usually has a self-signed certificate.
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
}

func (d *NeoTunnelDriver) call(ctx context.Context, nt *agentproto.NeoTunnel, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(nt.BaseURL, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+nt.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return fmt.Errorf("neotunnel %s %s: %s", method, path, e.Error)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (d *NeoTunnelDriver) Sync(ctx context.Context, in SyncInput) ([]agentproto.TunnelStatus, json.RawMessage, error) {
	var book ntBook
	_ = json.Unmarshal(in.Book, &book)
	if book.Peers == nil {
		book.Peers = map[string]string{}
	}
	if book.Tunnels == nil {
		book.Tunnels = map[string]bool{}
	}
	if len(in.Tunnels) == 0 && len(book.Forwards) == 0 && len(book.Tunnels) == 0 {
		return nil, nil, nil
	}
	if in.NeoTunnel == nil || in.NeoTunnel.BaseURL == "" {
		raw, _ := json.Marshal(book)
		return nil, raw, errors.New("NeoTunnel API address/token is not set for this server")
	}
	nt := in.NeoTunnel
	var statuses []agentproto.TunnelStatus
	var errs []string

	desired := map[string]ntForward{}
	wantTunnels := map[string]bool{}
	for _, t := range in.Tunnels {
		st := agentproto.TunnelStatus{Name: t.Name, Status: "running"}
		target := t.NTTunnel
		if t.Mode == "dedicated" {
			wantTunnels[t.Name] = true
			target = t.Name
			if !book.Tunnels[t.Name] {
				if _, ok := book.Peers[t.Name]; !ok {
					var setup struct {
						Apply  string `json:"apply"`
						GitHub string `json:"github"`
					}
					if err := d.call(ctx, nt, "POST", "/api/peers", map[string]string{"name": t.NTPeerName, "address": t.NTPeerAddress}, &setup); err != nil {
						st.Status, st.Error = "error", err.Error()
						statuses = append(statuses, st)
						errs = append(errs, err.Error())
						continue
					}
					cmd := setup.GitHub
					if cmd == "" {
						cmd = setup.Apply
					}
					book.Peers[t.Name] = cmd
				}
				// Creating the tunnel succeeds only once the customer's server ran the setup.
				mode := t.NTMode
				if mode == "" {
					mode = "tcp"
				}
				if err := d.call(ctx, nt, "POST", "/api/tunnels", map[string]any{"peer": t.NTPeerName, "mode": mode, "name": t.Name}, nil); err != nil {
					st.Status, st.PeerSetup = "waiting_peer", book.Peers[t.Name]
					statuses = append(statuses, st)
					continue
				}
				book.Tunnels[t.Name] = true
			}
			st.Status = "connected"
		}
		if target == "" {
			st.Status, st.Error = "error", "NeoTunnel tunnel name is empty"
			statuses = append(statuses, st)
			continue
		}
		for _, p := range in.Ports[t.Name] {
			f := ntForward{Tunnel: target, Ports: strconv.Itoa(p.Port), Proto: p.Proto, To: p.ToPort}
			if f.Proto == "" {
				f.Proto = "tcp"
			}
			desired[f.key()] = f
		}
		statuses = append(statuses, st)
	}

	// Remove forwards we created that are no longer wanted, then add missing ones.
	var kept []ntForward
	have := map[string]bool{}
	for _, f := range book.Forwards {
		if _, ok := desired[f.key()]; ok {
			kept = append(kept, f)
			have[f.key()] = true
			continue
		}
		if err := d.call(ctx, nt, "POST", "/api/forwards/remove", map[string]string{"tunnel": f.Tunnel, "ports": f.Ports}, nil); err != nil && !strings.Contains(err.Error(), "not found") {
			kept = append(kept, f) // retry next time
			errs = append(errs, err.Error())
		}
	}
	keys := make([]string, 0, len(desired))
	for k := range desired {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if have[k] {
			continue
		}
		f := desired[k]
		if err := d.call(ctx, nt, "POST", "/api/forwards", f, nil); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		kept = append(kept, f)
	}
	book.Forwards = kept

	// Dedicated tunnels no longer wanted: remove tunnel and peer.
	for name := range book.Tunnels {
		if !wantTunnels[name] {
			d.call(ctx, nt, "DELETE", "/api/tunnels/"+name, nil, nil)
			delete(book.Tunnels, name)
		}
	}
	for name := range book.Peers {
		if !wantTunnels[name] {
			d.call(ctx, nt, "DELETE", "/api/peers/"+name, nil, nil)
			delete(book.Peers, name)
		}
	}
	raw, _ := json.Marshal(book)
	if len(errs) > 0 {
		return statuses, raw, errors.New(strings.Join(errs, "; "))
	}
	return statuses, raw, nil
}
