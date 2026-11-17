// Command neosell is the whole Neo Sell product in one binary:
//
//	neosell [serve]              run the panel (default)
//	neosell install              write config + systemd unit and start the service
//	neosell agent join|run       Neo Agent on a tunnel server
//	neosell admin reset-password <email> <new-password>
//	neosell backup -out FILE       consistent copy of the database
//	neosell license status|recheck
//	neosell version
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"neosell/internal/app"
	"neosell/internal/config"
	"neosell/internal/version"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve(args)
	case "install":
		err = install(args)
	case "agent":
		err = agentCmd(args)
	case "admin":
		err = adminCmd(args)
	case "backup":
		err = backupCmd(args)
	case "license":
		err = licenseCmd(args)
	case "version", "--version":
		fmt.Printf("neosell %s (%s, %s)\n", version.Version, version.Commit, version.Date)
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`Neo Sell — port tunnel & outbound sales panel

usage:
  neosell [serve] [-config path]
  neosell install [-port 2087] [-public-url https://…] [-tls self|off|file]
  neosell agent join -panel URL -token TOKEN [-pin SHA256]   (on a tunnel server)
  neosell agent run
  neosell admin reset-password <email> <new-password>
  neosell backup -out /root/neosell-backup.db
  neosell license status|recheck
  neosell version`)
}

func cfgFlag(fs *flag.FlagSet) *string {
	return fs.String("config", envOr("NEOSELL_CONFIG", config.DefaultPath), "config file")
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func openApp(path string) (*app.App, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	return app.Open(cfg)
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgPath := cfgFlag(fs)
	fs.Parse(args)
	a, err := openApp(*cfgPath)
	if err != nil {
		return err
	}
	defer a.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return a.Serve(ctx)
}

func adminCmd(args []string) error {
	fs := flag.NewFlagSet("admin", flag.ExitOnError)
	cfgPath := cfgFlag(fs)
	fs.Parse(args)
	rest := fs.Args()
	if len(rest) != 3 || rest[0] != "reset-password" {
		return fmt.Errorf("usage: neosell admin reset-password <email> <new-password>")
	}
	a, err := openApp(*cfgPath)
	if err != nil {
		return err
	}
	defer a.Close()
	ctx := context.Background()
	u, err := a.Users.ByEmail(ctx, rest[1])
	if err != nil {
		return fmt.Errorf("user not found")
	}
	if err := a.Users.SetPassword(ctx, u.ID, rest[2]); err != nil {
		return err
	}
	fmt.Println("password updated; all sessions of", u.Email, "were signed out")
	return nil
}

func licenseCmd(args []string) error {
	fs := flag.NewFlagSet("license", flag.ExitOnError)
	cfgPath := cfgFlag(fs)
	fs.Parse(args)
	a, err := openApp(*cfgPath)
	if err != nil {
		return err
	}
	defer a.Close()
	if len(fs.Args()) > 0 && fs.Args()[0] == "recheck" {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := a.License.Recheck(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "recheck failed:", err)
		}
	}
	out, _ := json.MarshalIndent(a.License.Status(), "", "  ")
	fmt.Println(string(out))
	return nil
}

// backupCmd writes a consistent copy of the live database (safe while running).
// Restore: stop the service, copy the file over <data>/neosell.db, start again.
// Keep <data>/secret.key too: it decrypts stored credentials.
func backupCmd(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	cfgPath := cfgFlag(fs)
	out := fs.String("out", "", "destination file (must not exist)")
	fs.Parse(args)
	if *out == "" {
		return fmt.Errorf("-out is required")
	}
	if _, err := os.Stat(*out); err == nil {
		return fmt.Errorf("%s already exists", *out)
	}
	a, err := openApp(*cfgPath)
	if err != nil {
		return err
	}
	defer a.Close()
	if _, err := a.DB.Exec(`VACUUM INTO ?`, *out); err != nil {
		return err
	}
	fmt.Println("backup written to", *out, "(also keep", a.Cfg.DataDir+"/secret.key)")
	return nil
}
