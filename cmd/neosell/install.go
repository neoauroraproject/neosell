package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

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
# Hardening: the panel only needs its data dir.
NoNewPrivileges=true
ProtectSystem=full
ProtectHome=true
PrivateTmp=true
ReadWritePaths=%s %s

[Install]
WantedBy=multi-user.target
`

// install writes the config file and a systemd unit, then starts the service.
// The owner account and the license key are set in the web setup wizard.
func install(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	cfgPath := cfgFlag(fs)
	port := fs.Int("port", 2087, "panel port")
	publicURL := fs.String("public-url", "", "public URL of the panel (default https://<ip>:<port>)")
	tlsMode := fs.String("tls", "self", "self | off | file")
	cert := fs.String("cert", "", "certificate file (tls=file)")
	key := fs.String("key", "", "key file (tls=file)")
	dataDir := fs.String("data", "/var/lib/neosell", "data directory")
	proxy := fs.String("proxy", "", "outbound proxy for license/Telegram (socks5://… or http://…)")
	noStart := fs.Bool("no-start", false, "only write files")
	fs.Parse(args)

	if runtime.GOOS != "linux" {
		return errors.New("install is only supported on Linux")
	}
	if os.Geteuid() != 0 {
		return errors.New("run as root (sudo)")
	}
	switch *tlsMode {
	case "self", "off":
	case "file":
		if *cert == "" || *key == "" {
			return errors.New("-tls file needs -cert and -key")
		}
	default:
		return errors.New("-tls must be self, off or file")
	}

	cfg, _ := config.Load(*cfgPath)
	cfg.Listen = fmt.Sprintf(":%d", *port)
	cfg.DataDir = *dataDir
	cfg.TLS, cfg.CertFile, cfg.KeyFile = *tlsMode, *cert, *key
	cfg.OutboundProxy = *proxy
	if *publicURL != "" {
		cfg.PublicURL = strings.TrimRight(*publicURL, "/")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return err
	}
	if err := config.Save(*cfgPath, cfg); err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	unit := fmt.Sprintf(unitTemplate, exe, *cfgPath, cfg.DataDir, filepath.Dir(*cfgPath))
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return err
	}
	fmt.Println("config:", *cfgPath)
	fmt.Println("unit:  ", unitPath)
	if *noStart {
		return nil
	}
	for _, c := range [][]string{{"daemon-reload"}, {"enable", "--now", "neosell"}, {"restart", "neosell"}} {
		if out, err := exec.Command("systemctl", c...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s: %v: %s", strings.Join(c, " "), err, out)
		}
	}
	scheme := "https"
	if *tlsMode == "off" {
		scheme = "http"
	}
	url := cfg.PublicURL
	if url == "" {
		url = fmt.Sprintf("%s://<server-ip>:%d", scheme, *port)
	}
	fmt.Printf("\nNeo Sell is running.\nOpen %s to create the owner account and activate your license.\n", url)
	return nil
}
