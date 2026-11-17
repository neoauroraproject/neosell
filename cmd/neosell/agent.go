package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"neosell/internal/agent"
)

const agentConfigPath = "/etc/neosell/agent.json"
const agentUnitPath = "/etc/systemd/system/neosell-agent.service"

const agentUnit = `[Unit]
Description=Neo Sell agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s agent run -config %s
Restart=always
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
`

// neosell agent join|run
func agentCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: neosell agent join -panel URL -token TOKEN [-pin SHA256] | neosell agent run")
	}
	switch args[0] {
	case "join":
		return agentJoin(args[1:])
	case "run":
		return agentRun(args[1:])
	}
	return fmt.Errorf("unknown agent command %q", args[0])
}

func agentJoin(args []string) error {
	fs := flag.NewFlagSet("agent join", flag.ExitOnError)
	panel := fs.String("panel", "", "panel URL")
	token := fs.String("token", "", "agent token from the panel")
	pin := fs.String("pin", "", "sha256 of the panel certificate (self-signed panels)")
	cfgPath := fs.String("config", agentConfigPath, "agent config file")
	data := fs.String("data", "/var/lib/neosell-agent", "agent data directory")
	fs.Parse(args)
	if *panel == "" || *token == "" {
		return errors.New("-panel and -token are required")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return errors.New("run on Linux as root")
	}
	cfg := agent.Config{PanelURL: strings.TrimRight(*panel, "/"), Token: *token, CertSHA256: *pin, DataDir: *data}
	if err := os.MkdirAll(filepath.Dir(*cfgPath), 0o750); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(*cfgPath, raw, 0o600); err != nil {
		return err
	}
	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	if err := os.WriteFile(agentUnitPath, []byte(fmt.Sprintf(agentUnit, exe, *cfgPath)), 0o644); err != nil {
		return err
	}
	for _, c := range [][]string{{"daemon-reload"}, {"enable", "neosell-agent"}, {"restart", "neosell-agent"}} {
		if out, err := exec.Command("systemctl", c...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s: %v: %s", strings.Join(c, " "), err, out)
		}
	}
	fmt.Println("Neo Agent installed and connecting to", cfg.PanelURL)
	fmt.Println("status: systemctl status neosell-agent   logs: journalctl -u neosell-agent -f")
	return nil
}

func agentRun(args []string) error {
	fs := flag.NewFlagSet("agent run", flag.ExitOnError)
	cfgPath := fs.String("config", agentConfigPath, "agent config file")
	fs.Parse(args)
	raw, err := os.ReadFile(*cfgPath)
	if err != nil {
		return fmt.Errorf("read %s: %w (run `neosell agent join` first)", *cfgPath, err)
	}
	var cfg agent.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "/var/lib/neosell-agent"
	}
	a, err := agent.New(cfg.DataDir, agent.NewNFT())
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return a.Run(ctx, cfg)
}
