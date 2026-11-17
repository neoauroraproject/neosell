package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		tag, cur string
		want     bool
	}{
		{"v0.2.0", "0.1.9", true}, {"v0.1.9", "0.2.0", false}, {"v1.0.0", "1.0.0", false},
		{"v1.10.0", "1.9.3", true}, {"v0.1.0", "0.1.0-dev", true}, {"0.3", "0.2.9", true},
	}
	for _, c := range cases {
		if got := Newer(c.tag, c.cur); got != c.want {
			t.Errorf("Newer(%s,%s)=%v", c.tag, c.cur, got)
		}
	}
}

func fakeGitHub(t *testing.T, bin []byte, sum string) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/n/releases/latest":
			json.NewEncoder(w).Encode(map[string]any{"tag_name": "v9.0.0", "body": "notes", "assets": []map[string]string{
				{"name": AssetName(), "browser_download_url": srv.URL + "/bin"},
				{"name": "SHA256SUMS", "browser_download_url": srv.URL + "/sums"},
			}})
		case "/bin":
			w.Write(bin)
		case "/sums":
			fmt.Fprintf(w, "%s  %s\nffff  other-file\n", sum, AssetName())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestApplyVerifiesAndReplaces(t *testing.T) {
	bin := []byte("new binary")
	s := sha256.Sum256(bin)
	srv := fakeGitHub(t, bin, hex.EncodeToString(s[:]))
	APIBase = srv.URL
	c := &Client{Repo: "o/n"}
	r, err := c.Latest(context.Background())
	if err != nil || r.Tag != "v9.0.0" {
		t.Fatalf("latest: %v %+v", err, r)
	}
	exe := filepath.Join(t.TempDir(), "neosell")
	os.WriteFile(exe, []byte("old"), 0o755)
	if err := c.Apply(context.Background(), r, exe); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "new binary" {
		t.Fatalf("binary not replaced: %q", got)
	}
}

func TestApplyRejectsBadChecksum(t *testing.T) {
	srv := fakeGitHub(t, []byte("tampered"), "0000")
	APIBase = srv.URL
	c := &Client{Repo: "o/n"}
	r, _ := c.Latest(context.Background())
	exe := filepath.Join(t.TempDir(), "neosell")
	os.WriteFile(exe, []byte("old"), 0o755)
	if err := c.Apply(context.Background(), r, exe); err == nil {
		t.Fatal("bad checksum accepted")
	}
	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Fatal("old binary was touched")
	}
	if _, err := os.Stat(exe + ".new"); !os.IsNotExist(err) {
		t.Fatal("temp file left behind")
	}
}
