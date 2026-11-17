package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"neosell/internal/config"
)

const unitPath = "/etc/systemd/system/neosell.service"

const unitTemplate = `[Unit]
Description=Neo Sell panel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s serve -config %s
Restart=always
RestartSec=3
LimitNOFILE=65535
NoNewPrivileges=true
ProtectSystem=full
ProtectHome=true
PrivateTmp=true
ReadWritePaths=%s %s %s
AmbientCapabilities=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
`

// ---------- small terminal helpers ----------

var in = bufio.NewReader(os.Stdin)

func ask(label, def string) string {
	if def != "" {
		fmt.Printf("  %s [%s]: ", label, def)
	} else {
		fmt.Printf("  %s: ", label)
	}
	line, _ := in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func confirm(label string, def bool) bool {
	d := "y/N"
	if def {
		d = "Y/n"
	}
	a := strings.ToLower(ask(label+" ("+d+")", ""))
	if a == "" {
		return def
	}
	return a == "y" || a == "yes"
}

func isTTY() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

const (
	cReset = "\033[0m"
	cBold  = "\033[1m"
	cDim   = "\033[2m"
	cCoral = "\033[38;5;209m"
	cGreen = "\033[32m"
	cRed   = "\033[31m"
)

func okf(format string, a ...any)   { fmt.Printf("  "+cGreen+"✓"+cReset+" "+format+"\n", a...) }
func warnf(format string, a ...any) { fmt.Printf("  "+cCoral+"!"+cReset+" "+format+"\n", a...) }

// ---------- environment ----------

// randomPort picks a free TCP port that is not a well-known service port.
func randomPort() int {
	for i := 0; i < 200; i++ {
		p := 10000 + rand.IntN(50000)
		if portFree(p) {
			return p
		}
	}
	return 2087
}

func portFree(p int) bool {
	l, err := net.Listen("tcp", ":"+strconv.Itoa(p))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

// publicIP asks a public echo service, then falls back to the first global interface address.
func publicIP() string {
	c := &http.Client{Timeout: 4 * time.Second}
	for _, u := range []string{"https://api.ipify.org", "https://ifconfig.me/ip"} {
		if r, err := c.Get(u); err == nil {
			b, _ := io.ReadAll(io.LimitReader(r.Body, 64))
			r.Body.Close()
			if ip := net.ParseIP(strings.TrimSpace(string(b))); ip != nil {
				return ip.String()
			}
		}
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.IsGlobalUnicast() && !n.IP.IsPrivate() {
			return n.IP.String()
		}
	}
	return "<server-ip>"
}

func panelURL(c config.Config, ip string) string {
	port := strings.TrimPrefix(c.Listen, ":")
	host := ip
	if c.Domain != "" {
		host = c.Domain
	}
	scheme := "https"
	if c.TLS == "off" {
		scheme = "http"
	}
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		return fmt.Sprintf("%s://%s", scheme, host)
	}
	return fmt.Sprintf("%s://%s:%s", scheme, host, port)
}

func systemctl(args ...string) error {
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func requireRootLinux() error {
	if runtime.GOOS != "linux" {
		return errors.New("only supported on Linux")
	}
	if os.Geteuid() != 0 {
		return errors.New("run as root (sudo)")
	}
	return nil
}

// checkDomain warns when the domain does not point at this server (Let's Encrypt would fail).
func checkDomain(domain, ip string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(ctx, domain)
	if err != nil {
		warnf("%s does not resolve yet (%v). Point an A record to %s.", domain, err, ip)
		return false
	}
	for _, a := range addrs {
		if a == ip {
			okf("%s points to this server (%s)", domain, ip)
			return true
		}
	}
	warnf("%s points to %s, not to this server (%s).", domain, strings.Join(addrs, ", "), ip)
	warnf("If you use a CDN proxy (orange cloud), turn it off for this record so the certificate can be issued.")
	return false
}

// ---------- install ----------

// install writes the config and a systemd unit, then starts the panel.
// Interactive by default; flags make it scriptable (-yes skips questions).
func install(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	cfgPath := cfgFlag(fs)
	port := fs.Int("port", 0, "panel port (0 = random free port)")
	domain := fs.String("domain", "", "domain for the panel (enables Let's Encrypt)")
	email := fs.String("email", "", "email for Let's Encrypt notices (optional)")
	tlsMode := fs.String("tls", "", "acme | self | off | file (default: acme with a domain, else self)")
	cert := fs.String("cert", "", "certificate file (tls=file)")
	key := fs.String("key", "", "key file (tls=file)")
	dataDir := fs.String("data", "/var/lib/neosell", "data directory")
	proxy := fs.String("proxy", "", "outbound proxy for license/Telegram (socks5://… or http://…)")
	yes := fs.Bool("yes", false, "do not ask questions")
	fs.Parse(args)
	if err := requireRootLinux(); err != nil {
		return err
	}

	cfg, _ := config.Load(*cfgPath)
	_, existed := os.Stat(*cfgPath)
	interactive := isTTY() && !*yes

	fmt.Println()
	fmt.Println("  " + cBold + "Neo Sell installer" + cReset)
	fmt.Println("  " + cDim + "port tunnel & outbound sales panel" + cReset)
	fmt.Println()
	ip := publicIP()

	if *domain == "" && interactive {
		*domain = ask("Domain for the panel (empty = use the server IP)", cfg.Domain)
	}
	*domain = strings.TrimSuffix(strings.TrimSpace(strings.ToLower(*domain)), ".")
	if *domain != "" && *tlsMode == "" {
		*tlsMode = "acme"
	}
	if *tlsMode == "" {
		*tlsMode = "self"
	}
	if *tlsMode == "acme" {
		if *domain == "" {
			return errors.New("-tls acme needs -domain")
		}
		checkDomain(*domain, ip)
		if !portFree(80) && existed != nil {
			warnf("port 80 is busy; Let's Encrypt needs it free (or run the panel on port 443).")
		}
		if *email == "" && interactive {
			*email = ask("Email for SSL expiry notices (optional)", cfg.ACMEEmail)
		}
	}
	if *tlsMode == "file" && (*cert == "" || *key == "") {
		return errors.New("-tls file needs -cert and -key")
	}

	if *port == 0 {
		if existed == nil && cfg.Listen != "" {
			*port, _ = strconv.Atoi(strings.TrimPrefix(cfg.Listen, ":"))
		}
		if *port == 0 {
			*port = randomPort()
		}
		if interactive {
			p, _ := strconv.Atoi(ask("Panel port (enter = keep)", strconv.Itoa(*port)))
			if p > 0 && p < 65536 {
				*port = p
			}
		}
	}

	cfg.Listen = fmt.Sprintf(":%d", *port)
	cfg.DataDir = *dataDir
	cfg.TLS, cfg.CertFile, cfg.KeyFile = *tlsMode, *cert, *key
	cfg.Domain, cfg.ACMEEmail = *domain, *email
	if *proxy != "" {
		cfg.OutboundProxy = *proxy
	}
	cfg.PublicURL = panelURL(cfg, ip)
	if err := writeAndStart(*cfgPath, cfg); err != nil {
		return err
	}

	fmt.Println()
	okf("Neo Sell is running")
	fmt.Println()
	fmt.Println("  Open:  " + cBold + cfg.PublicURL + cReset)
	if cfg.TLS == "self" {
		fmt.Println("  " + cDim + "Self-signed certificate: your browser will warn once; continue to the site." + cReset)
	}
	if cfg.TLS == "acme" {
		fmt.Println("  " + cDim + "The SSL certificate is issued on the first visit (takes a few seconds)." + cReset)
	}
	fmt.Println("  Then create the owner account and enter your license key.")
	fmt.Println("  Manage later with:  " + cCoral + "neosell" + cReset)
	fmt.Println()
	return nil
}

// writeAndStart saves the config, (re)writes the unit and restarts the service.
func writeAndStart(cfgPath string, cfg config.Config) error {
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return err
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	unit := fmt.Sprintf(unitTemplate, exe, cfgPath, cfg.DataDir, filepath.Dir(cfgPath), filepath.Dir(exe))
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return err
	}
	for _, c := range [][]string{{"daemon-reload"}, {"enable", "neosell"}, {"restart", "neosell"}} {
		if err := systemctl(c...); err != nil {
			return err
		}
	}
	return nil
}
