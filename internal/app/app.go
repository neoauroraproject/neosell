// Package app wires every component of the panel into one process.
package app

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"neosell/internal/agentgw"
	"neosell/internal/audit"
	"neosell/internal/auth"
	"neosell/internal/bot"
	"neosell/internal/catalog"
	"neosell/internal/config"
	"neosell/internal/db"
	"neosell/internal/httpapi"
	"neosell/internal/jobs"
	"neosell/internal/license"
	"neosell/internal/notify"
	"neosell/internal/payments"
	"neosell/internal/rbac"
	"neosell/internal/secret"
	"neosell/internal/services"
	"neosell/internal/settings"
	"neosell/internal/version"
	"neosell/internal/wallet"
	"neosell/internal/webui"
)

type App struct {
	Cfg      config.Config
	DB       *db.DB
	Box      *secret.Box
	License  *license.Manager
	Jobs     *jobs.Queue
	Users    *auth.Store
	RBAC     *rbac.Store
	Settings *settings.Store
	Audit    *audit.Log
	Catalog  *catalog.Store
	Wallet   *wallet.Wallet
	Payments *payments.Service
	Services *services.Manager
	Hub      *agentgw.Hub
	Notify   *notify.Center
	Bot      *bot.Bot
}

// Open builds the app (database, secrets, license) without starting anything.
func Open(cfg config.Config) (*App, error) {
	d, err := db.Open(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	box, err := secret.Open(cfg.DataDir)
	if err != nil {
		d.Close()
		return nil, fmt.Errorf("secret key: %w", err)
	}
	lic, err := license.New(d, box, license.Options{Endpoints: cfg.LicenseURLs, Proxy: cfg.OutboundProxy})
	if err != nil {
		d.Close()
		return nil, fmt.Errorf("license: %w", err)
	}
	a := &App{Cfg: cfg, DB: d, Box: box, License: lic,
		Jobs: jobs.New(d), Users: auth.NewStore(d), RBAC: rbac.NewStore(d), Audit: audit.New(d), Catalog: catalog.New(d)}
	a.Settings = settings.NewStore(d)
	a.Settings.Box = box
	a.Wallet = wallet.New(d)
	a.Payments = payments.New(d, a.Wallet)
	a.Notify = &notify.Center{DB: d}
	a.Services = &services.Manager{DB: d, Box: box, Wallet: a.Wallet, Catalog: a.Catalog, Settings: a.Settings, Jobs: a.Jobs,
		License: lic, Notifier: a.Notify, Proxy: cfg.OutboundProxy, PublicURL: a.publicURL()}
	a.Services.RegisterJobs()
	a.Hub = agentgw.New(a.Services, lic)
	a.Services.Hub = a.Hub
	a.Bot = &bot.Bot{DB: d, Users: a.Users, RBAC: a.RBAC, Settings: a.Settings, Wallet: a.Wallet, Payments: a.Payments,
		Catalog: a.Catalog, Services: a.Services, License: lic, FilesDir: filepath.Join(cfg.DataDir, "files"), Proxy: cfg.OutboundProxy,
		PublicURL: a.publicURL}
	a.Notify.Sender = a.Bot
	a.Bot.OnPayment = a.onPayment
	return a, nil
}

// publicURL: the admin setting wins over the install-time config.
func (a *App) publicURL() string {
	if a.Settings != nil {
		if u := strings.TrimRight(a.Settings.String(context.Background(), "panel.public_url"), "/"); u != "" {
			return u
		}
	}
	return strings.TrimRight(a.Cfg.PublicURL, "/")
}

func (a *App) Close() error { return a.DB.Close() }

// onPayment notifies the customer and resumes pay-as-you-go services.
func (a *App) onPayment(ctx context.Context, p *payments.Payment, approved bool) {
	if approved {
		a.Notify.Notify(ctx, p.UserID, "payment.approved", "شارژ کیف پول", fmt.Sprintf("واریز %d تأیید شد و کیف پول شما شارژ شد.", p.PayAmount))
		a.Services.ResumePayg(ctx, p.UserID)
	} else {
		body := "رسید شما تأیید نشد."
		if p.AdminNote != "" {
			body += " " + p.AdminNote
		}
		a.Notify.Notify(ctx, p.UserID, "payment.rejected", "رسید رد شد", body)
	}
}

// Serve runs the panel until ctx is cancelled.
func (a *App) Serve(ctx context.Context) error {
	var web fs.FS
	if w, err := webui.FS(); err == nil {
		web = w
	}
	srv := &httpapi.Server{Cfg: a.Cfg, DB: a.DB, Users: a.Users, RBAC: a.RBAC, Settings: a.Settings,
		Audit: a.Audit, License: a.License, Jobs: a.Jobs, Web: web,
		Catalog: a.Catalog, Wallet: a.Wallet, Payments: a.Payments,
		Services: a.Services, AgentHub: a.Hub, Notifications: a.Notify, BotName: a.Bot.Username}
	srv.Notify = func(ctx context.Context, event string, payload any) {
		p, _ := payload.(*payments.Payment)
		switch event {
		case "payment.submitted":
			a.Bot.NotifyAdminsPayment(ctx, p)
		case "payment.approved":
			a.onPayment(ctx, p, true)
		case "payment.rejected":
			a.onPayment(ctx, p, false)
		}
	}

	hs := &http.Server{
		Addr:              a.Cfg.Listen,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	switch a.Cfg.TLS {
	case "off", "file":
	case "acme":
		if a.Cfg.Domain == "" {
			return errors.New("tls=acme needs a domain (run: neosell menu → domain & SSL)")
		}
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(a.Cfg.Domain),
			Cache:      autocert.DirCache(filepath.Join(a.Cfg.DataDir, "acme")),
			Email:      a.Cfg.ACMEEmail,
		}
		hs.TLSConfig = m.TLSConfig()
		hs.TLSConfig.MinVersion = tls.VersionTLS12
		// Port 80 answers Let's Encrypt HTTP-01 challenges and redirects to the panel.
		go func() {
			redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, a.publicURL()+r.URL.RequestURI(), http.StatusMovedPermanently)
			})
			h80 := &http.Server{Addr: ":80", Handler: m.HTTPHandler(redirect), ReadHeaderTimeout: 10 * time.Second}
			if err := h80.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Warn("port 80 unavailable; Let's Encrypt falls back to TLS-ALPN (works only when the panel listens on 443)", "err", err)
			}
		}()
	default:
		cert, err := selfSigned(a.Cfg.DataDir)
		if err != nil {
			return err
		}
		hs.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		if leaf, err := x509.ParseCertificate(cert.Certificate[0]); err == nil {
			sum := sha256.Sum256(leaf.Raw)
			spki := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
			srv.CertPin, srv.CertSPKI = hex.EncodeToString(sum[:]), base64.StdEncoding.EncodeToString(spki[:])
		}
	}
	hs.Handler = srv.Handler()

	// Public URL may be changed in settings at runtime.
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			a.Services.PublicURL = a.publicURL()
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	go a.License.Loop(ctx)
	go a.Jobs.Run(ctx, 4)
	go a.Services.Loop(ctx)
	go a.Bot.Run(ctx)
	go a.housekeeping(ctx)

	errc := make(chan error, 1)
	go func() {
		slog.Info("neosell panel listening", "addr", a.Cfg.Listen, "tls", a.Cfg.TLS, "version", version.Version)
		var err error
		switch a.Cfg.TLS {
		case "off":
			err = hs.ListenAndServe()
		case "file":
			err = hs.ListenAndServeTLS(a.Cfg.CertFile, a.Cfg.KeyFile)
		case "acme":
			err = hs.ListenAndServeTLS("", "")
		default:
			err = hs.ListenAndServeTLS("", "")
		}
		if !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
		close(errc)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return hs.Shutdown(shutdownCtx)
	case err := <-errc:
		return err
	}
}

func (a *App) housekeeping(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		a.Users.PurgeExpiredSessions(ctx)
		a.Payments.ExpireStale(ctx)
		now := time.Now()
		a.DB.ExecContext(ctx, `DELETE FROM jobs WHERE status = 'done' AND updated_at < ?`, now.Add(-7*24*time.Hour).Unix())
		a.DB.ExecContext(ctx, `DELETE FROM telegram_links WHERE expires_at < ?`, now.Unix())
		a.DB.ExecContext(ctx, `DELETE FROM usage_hourly WHERE hour < ?`, now.Add(-400*24*time.Hour).Unix())
		if bad, err := a.Wallet.Verify(ctx); err == nil && len(bad) > 0 {
			slog.Error("wallet ledger mismatch", "users", bad)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
