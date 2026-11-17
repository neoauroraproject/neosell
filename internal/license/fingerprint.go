package license

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// Fingerprint identifies this machine the same way HMPanel does:
// sha256(hostname | install path | /etc/machine-id). The license server locks
// a license to fingerprints (device slots), not IPs, so an IP change is fine.
func Fingerprint() string {
	host, _ := hostname()
	exe, _ := os.Executable()
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	mid := ""
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if b, err := os.ReadFile(p); err == nil {
			mid = strings.TrimSpace(string(b))
			break
		}
	}
	sum := sha256.Sum256([]byte(host + "|" + filepath.Dir(exe) + "|" + mid))
	return hex.EncodeToString(sum[:])
}

func hostname() (string, error) { return os.Hostname() }
