// Package config loads the small bootstrap config file. Everything else
// (prices, messages, payment methods, …) lives in the database and is edited
// from the admin panel.
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const DefaultPath = "/etc/neosell/config.json"

type Config struct {
	// Listen is the panel's HTTP(S) address, e.g. ":2087".
	Listen string `json:"listen"`
	// DataDir holds the SQLite database, the secret key and uploaded files.
	DataDir string `json:"data_dir"`
	// PublicURL is how customers reach the panel (used in links, sub URLs, install scripts).
	PublicURL string `json:"public_url"`
	// TLS: "off" (behind a proxy), "self" (self-signed) or "file" (CertFile/KeyFile).
	TLS      string `json:"tls"`
	CertFile string `json:"cert_file,omitempty"`
	KeyFile  string `json:"key_file,omitempty"`
	// OutboundProxy (socks5://host:port or http://host:port) is used for the
	// license server and Telegram when the server has no direct international access.
	OutboundProxy string `json:"outbound_proxy,omitempty"`
	// LicenseURLs overrides the built-in license server hosts (development only).
	LicenseURLs []string `json:"license_urls,omitempty"`
}

func Default() Config {
	return Config{Listen: ":2087", DataDir: "/var/lib/neosell", TLS: "self"}
}

// Load reads path; a missing file yields the defaults.
func Load(path string) (Config, error) {
	c := Default()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, err
	}
	if c.Listen == "" {
		c.Listen = Default().Listen
	}
	if c.DataDir == "" {
		c.DataDir = Default().DataDir
	}
	return c, nil
}

func Save(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(c, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
