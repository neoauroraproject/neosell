// Package updater replaces the running binary with the latest GitHub release.
//
// A release must carry neosell-linux-<arch> assets and a SHA256SUMS file; the
// downloaded binary is verified against it before it replaces the old one
// (an atomic rename, so a failed update never leaves a broken binary).
package updater

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// APIBase is the GitHub API root (replaced in tests).
var APIBase = "https://api.github.com"

type Release struct {
	Tag         string            `json:"tag"`
	Name        string            `json:"name"`
	Notes       string            `json:"notes"`
	PublishedAt string            `json:"published_at"`
	Assets      map[string]string `json:"-"` // asset name → download URL
}

type Client struct {
	Repo  string // owner/name
	Proxy string // optional socks5:// or http:// proxy
	HTTP  *http.Client
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if c.Proxy != "" {
		if u, err := url.Parse(c.Proxy); err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	return &http.Client{Timeout: 10 * time.Minute, Transport: tr}
}

// Latest returns the newest published release.
func (c *Client) Latest(ctx context.Context) (*Release, error) {
	if c.Repo == "" {
		return nil, errors.New("update repository is not set")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(APIBase, "/"), c.Repo), nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "neosell-updater")
	res, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub در دسترس نیست: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, errors.New("نسخه‌ای منتشر نشده است (یا مخزن خصوصی است)")
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub: HTTP %d", res.StatusCode)
	}
	var raw struct {
		TagName     string `json:"tag_name"`
		Name        string `json:"name"`
		Body        string `json:"body"`
		PublishedAt string `json:"published_at"`
		Assets      []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&raw); err != nil {
		return nil, err
	}
	r := &Release{Tag: raw.TagName, Name: raw.Name, Notes: raw.Body, PublishedAt: raw.PublishedAt, Assets: map[string]string{}}
	for _, a := range raw.Assets {
		r.Assets[a.Name] = a.URL
	}
	return r, nil
}

// AssetName is the binary for this machine.
func AssetName() string { return "neosell-linux-" + runtime.GOARCH }

// Newer reports whether tag is a newer version than current. Development
// builds (anything with a suffix such as -dev) always accept a release.
func Newer(tag, current string) bool {
	if strings.Contains(current, "-") || current == "" {
		return true
	}
	a, b := parse(tag), parse(current)
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

func parse(v string) [3]int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out [3]int
	for i, p := range strings.SplitN(v, ".", 3) {
		out[i], _ = strconv.Atoi(p)
	}
	return out
}

// Apply downloads the release binary for this machine, verifies its SHA-256
// against the release's SHA256SUMS and atomically replaces exe.
func (c *Client) Apply(ctx context.Context, r *Release, exe string) error {
	name := AssetName()
	binURL, ok := r.Assets[name]
	if !ok {
		return fmt.Errorf("این نسخه فایل %s ندارد", name)
	}
	sumsURL, ok := r.Assets["SHA256SUMS"]
	if !ok {
		return errors.New("این نسخه SHA256SUMS ندارد؛ به‌روزرسانی بدون بررسی انجام نمی‌شود")
	}
	want, err := c.checksum(ctx, sumsURL, name)
	if err != nil {
		return err
	}
	tmp := exe + ".new"
	if err := c.download(ctx, binURL, tmp, want); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func (c *Client) get(ctx context.Context, u string) (*http.Response, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("User-Agent", "neosell-updater")
	res, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("download %s: HTTP %d", filepath.Base(u), res.StatusCode)
	}
	return res, nil
}

func (c *Client) checksum(ctx context.Context, sumsURL, name string) (string, error) {
	res, err := c.get(ctx, sumsURL)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(io.LimitReader(res.Body, 1<<20))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("SHA256SUMS برای %s مقدار ندارد", name)
}

func (c *Client) download(ctx context.Context, u, dst, wantSHA string) error {
	res, err := c.get(ctx, u)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(res.Body, 256<<20)); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantSHA {
		return errors.New("چک‌سام فایل دانلودشده درست نیست؛ به‌روزرسانی انجام نشد")
	}
	return nil
}
