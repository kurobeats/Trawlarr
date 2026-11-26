// Package secret wraps AES-256-GCM so sensitive values (captured form data,
// SMTP passwords) are never stored in the clear.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
)

const prefix = "v1:"

// Box seals and opens short strings with a single key.
type Box struct {
	aead cipher.AEAD
}

// New builds a Box from a 32 byte key.
func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, errors.New("secret: key must be 32 bytes")
	}
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

// Seal encrypts plaintext and returns "v1:"+base64(nonce||ciphertext).
func (b *Box) Seal(plaintext []byte) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := b.aead.Seal(nonce, nonce, plaintext, nil)
	return prefix + base64.StdEncoding.EncodeToString(out), nil
}

// Open decrypts a value produced by Seal. Values without the prefix are
// returned unchanged so imported plaintext still reads correctly.
func (b *Box) Open(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	if !strings.HasPrefix(s, prefix) {
		return []byte(s), nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, prefix))
	if err != nil {
		return nil, err
	}
	ns := b.aead.NonceSize()
	if len(raw) < ns {
		return nil, errors.New("secret: ciphertext too short")
	}
	return b.aead.Open(nil, raw[:ns], raw[ns:], nil)
}
