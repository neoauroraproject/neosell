package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"neosell/internal/config"
	"neosell/internal/updater"
	"neosell/internal/version"
)

func serviceActive(name string) bool {
	return exec.Command("systemctl", "is-active", "--quiet", name).Run() == nil
}

func serviceExists(name string) bool {
	_, err := os.Stat("/etc/systemd/system/" + name + ".service")
	return err == nil
}

// menu is what `neosell` shows in a terminal: install, update and day-to-day tasks.
func menu(args []string) error {
	fs := flag.NewFlagSet("menu", flag.ExitOnError)
	cfgPath := cfgFlag(fs)
	fs.Parse(args)
	for {
		installed := serviceExists("neosell")
		agent := serviceExists("neosell-agent")
		cfg, _ := config.Load(*cfgPath)
		fmt.Print("\033[H\033[2J")
		fmt.Println()
		fmt.Println("  " + cCoral + "●" + cReset + " " + cBold + "Neo Sell" + cReset + "  " + cDim + "v" + version.Version + cReset)
		switch {
		case installed && serviceActive("neosell"):
			fmt.Println("  panel: " + cGreen + "running" + cReset + "  " + cfg.PublicURL)
		case installed:
			fmt.Println("  panel: " + cRed + "stopped" + cReset)
		default:
			fmt.Println("  panel: " + cDim + "not installed" + cReset)
		}
		if agent {
			st := cRed + "stopped" + cReset
			if serviceActive("neosell-agent") {
				st = cGreen + "running" + cReset
			}
			fmt.Println("  agent: " + st)
		}
		fmt.Println()
		items := []struct {
			key, label string
			fn         func() error
		}{
			{"1", map[bool]string{true: "Reinstall / reconfigure panel", false: "Install panel"}[installed], func() error { return install([]string{"-config", *cfgPath}) }},
			{"2", "Update to the latest version", func() error { return update(nil) }},
			{"3", "Set domain & SSL (Let's Encrypt)", func() error { return setDomain(*cfgPath) }},
			{"4", "Change panel port", func() error { return setPort(*cfgPath) }},
			{"5", "Show panel address", func() error { fmt.Println("\n  " + cfg.PublicURL); return nil }},
			{"6", "Reset an admin password", func() error { return resetPasswordPrompt(*cfgPath) }},
			{"7", "Restart panel", func() error { return systemctl("restart", "neosell") }},
			{"8", "Live logs (Ctrl+C to return)", func() error { return logs(agent && !installed) }},
			{"9", "Backup database", func() error { return backupPrompt(*cfgPath) }},
			{"10", "License status", func() error { return licenseCmd([]string{"-config", *cfgPath, "recheck"}) }},
			{"11", "Uninstall", func() error { return uninstall(*cfgPath) }},
			{"0", "Exit", nil},
		}
		for _, it := range items {
			fmt.Printf("  %s%2s%s  %s\n", cCoral, it.key, cReset, it.label)
		}
		fmt.Println()
		choice := ask("Choose", "")
		if choice == "0" || choice == "q" {
			return nil
		}
		for _, it := range items {
			if it.key == choice && it.fn != nil {
				fmt.Println()
				if err := it.fn(); err != nil {
					fmt.Println("  " + cRed + "error: " + err.Error() + cReset)
				}
				fmt.Print("\n  Press Enter to continue…")
				in.ReadString('\n')
			}
		}
	}
}

func restartPanelIfInstalled() {
	if serviceExists("neosell") {
		if err := systemctl("restart", "neosell"); err != nil {
			warnf("%v", err)
		} else {
			okf("panel restarted")
		}
	}
}

func setPort(cfgPath string) error {
	if err := requireRootLinux(); err != nil {
		return err
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	cur := strings.TrimPrefix(cfg.Listen, ":")
	v := ask("New panel port (r = random)", cur)
	var p int
	if v == "r" {
		p = randomPort()
	} else if p, err = strconv.Atoi(v); err != nil || p < 1 || p > 65535 {
		return errors.New("invalid port")
	}
	if strconv.Itoa(p) == cur {
		return nil
	}
	if !portFree(p) {
		return fmt.Errorf("port %d is already in use", p)
	}
	cfg.Listen = fmt.Sprintf(":%d", p)
	cfg.PublicURL = panelURL(cfg, hostOf(cfg))
	if err := config.Save(cfgPath, cfg); err != nil {
		return err
	}
	okf("panel port is now %d", p)
	restartPanelIfInstalled()
	fmt.Println("  Open: " + cBold + cfg.PublicURL + cReset)
	warnf("Tunnel servers keep working only if their agent can reach the new address; re-run the agent command if needed.")
	return nil
}

func hostOf(cfg config.Config) string {
	if cfg.Domain != "" {
		return cfg.Domain
	}
	return publicIP()
}

func setDomain(cfgPath string) error {
	if err := requireRootLinux(); err != nil {
		return err
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	d := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(ask("Domain (empty = remove, use IP + self-signed)", cfg.Domain))), ".")
	ip := publicIP()
	if d == "" {
		cfg.Domain, cfg.TLS = "", "self"
	} else {
		checkDomain(d, ip)
		cfg.Domain, cfg.TLS = d, "acme"
		cfg.ACMEEmail = ask("Email for SSL notices (optional)", cfg.ACMEEmail)
		if confirm("Serve the panel on port 443 (address without a port)?", false) {
			if !portFree(443) && cfg.Listen != ":443" {
				return errors.New("port 443 is in use")
			}
			cfg.Listen = ":443"
		}
	}
	cfg.PublicURL = panelURL(cfg, ip)
	if err := config.Save(cfgPath, cfg); err != nil {
		return err
	}
	okf("saved")
	restartPanelIfInstalled()
	fmt.Println("  Open: " + cBold + cfg.PublicURL + cReset)
	if cfg.TLS == "acme" {
		fmt.Println("  " + cDim + "The certificate is issued on the first visit. Port 80 must be reachable." + cReset)
	}
	return nil
}

func resetPasswordPrompt(cfgPath string) error {
	email := ask("Admin email", "")
	pw := ask("New password (min 8 characters)", "")
	if email == "" || pw == "" {
		return errors.New("cancelled")
	}
	return adminCmd([]string{"-config", cfgPath, "reset-password", email, pw})
}

func backupPrompt(cfgPath string) error {
	out := ask("Backup file", fmt.Sprintf("/root/neosell-%s.db", time.Now().Format("20060102-1504")))
	return backupCmd([]string{"-config", cfgPath, "-out", out})
}

func logs(agentOnly bool) error {
	unit := "neosell"
	if agentOnly {
		unit = "neosell-agent"
	}
	c := exec.Command("journalctl", "-u", unit, "-f", "-n", "50")
	c.Stdout, c.Stderr, c.Stdin = os.Stdout, os.Stderr, os.Stdin
	return c.Run()
}

func uninstall(cfgPath string) error {
	if err := requireRootLinux(); err != nil {
		return err
	}
	if !confirm("Remove Neo Sell from this server?", false) {
		return nil
	}
	cfg, _ := config.Load(cfgPath)
	for _, u := range []string{"neosell", "neosell-agent"} {
		if serviceExists(u) {
			exec.Command("systemctl", "disable", "--now", u).Run()
			os.Remove("/etc/systemd/system/" + u + ".service")
		}
	}
	exec.Command("systemctl", "daemon-reload").Run()
	okf("services removed")
	if confirm("Also delete all data (database, receipts, keys) in "+cfg.DataDir+"? This cannot be undone", false) {
		os.RemoveAll(cfg.DataDir)
		os.Remove(cfgPath)
		okf("data deleted")
	} else {
		fmt.Println("  data kept in " + cfg.DataDir)
	}
	if exe, err := os.Executable(); err == nil {
		os.Remove(exe)
	}
	okf("uninstalled")
	os.Exit(0)
	return nil
}

// update downloads the latest release, verifies it and restarts the services.
func update(args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	cfgPath := cfgFlag(fs)
	check := fs.Bool("check", false, "only show whether an update exists")
	force := fs.Bool("force", false, "reinstall even if already on the latest version")
	fs.Parse(args)
	cfg, _ := config.Load(*cfgPath)
	c := &updater.Client{Repo: cfg.UpdateRepo, Proxy: cfg.OutboundProxy}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	rel, err := c.Latest(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("  current: %s   latest: %s\n", version.Version, rel.Tag)
	if !updater.Newer(rel.Tag, version.Version) && !*force {
		okf("already up to date")
		return nil
	}
	if *check {
		return nil
	}
	if err := requireRootLinux(); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	fmt.Println("  downloading " + updater.AssetName() + " …")
	if err := c.Apply(ctx, rel, exe); err != nil {
		return err
	}
	okf("updated to %s (checksum verified)", rel.Tag)
	for _, u := range []string{"neosell", "neosell-agent"} {
		if serviceExists(u) {
			if err := systemctl("restart", u); err != nil {
				warnf("%v", err)
			} else {
				okf("%s restarted", u)
			}
		}
	}
	return nil
}
