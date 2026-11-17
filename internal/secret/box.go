// Package secret encrypts sensitive values at rest (license token, node and
// panel credentials) with AES-256-GCM under a per-install random key.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
)

type Box struct{ aead cipher.AEAD }

// Open loads <dir>/secret.key, creating a fresh 32-byte key on first run.
func Open(dir string) (*Box, error) {
	path := filepath.Join(dir, "secret.key")
	key, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, key, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("secret.key must be 32 bytes")
	}
	return New(key)
}

func New(key []byte) (*Box, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal returns base64(nonce|ciphertext). An empty input stays empty.
func (b *Box) Seal(plain []byte) (string, error) {
	if len(plain) == 0 {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := b.aead.Seal(nonce, nonce, plain, nil)
	return base64.StdEncoding.EncodeToString(out), nil
}

func (b *Box) Open(enc string) ([]byte, error) {
	if enc == "" {
		return nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return nil, err
	}
	ns := b.aead.NonceSize()
	if len(raw) < ns {
		return nil, errors.New("ciphertext too short")
	}
	return b.aead.Open(nil, raw[:ns], raw[ns:], nil)
}

func (b *Box) SealString(s string) (string, error) { return b.Seal([]byte(s)) }

func (b *Box) OpenString(enc string) (string, error) {
	p, err := b.Open(enc)
	return string(p), err
}
