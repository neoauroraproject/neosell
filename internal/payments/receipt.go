package payments

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const MaxReceiptBytes = 5 << 20

var receiptTypes = map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "application/pdf": ".pdf"}

var ErrReceiptType = errors.New("فقط عکس (JPG/PNG/WEBP) یا PDF تا ۵ مگابایت")

// StoreReceipt validates and writes a receipt under <filesDir>/receipts and
// returns its relative path. The content type is detected from the bytes.
func StoreReceipt(filesDir string, data []byte) (string, error) {
	if len(data) == 0 || len(data) > MaxReceiptBytes {
		return "", ErrReceiptType
	}
	ext, ok := receiptTypes[http.DetectContentType(data)]
	if !ok {
		return "", ErrReceiptType
	}
	rnd := make([]byte, 16)
	rand.Read(rnd)
	rel := filepath.Join("receipts", time.Now().Format("200601"), hex.EncodeToString(rnd)+ext)
	abs := filepath.Join(filesDir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return "", err
	}
	if err := os.WriteFile(abs, data, 0o640); err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

// IsImage reports whether a stored receipt is an image (for Telegram previews).
func IsImage(rel string) bool {
	switch filepath.Ext(rel) {
	case ".jpg", ".png", ".webp":
		return true
	}
	return false
}
