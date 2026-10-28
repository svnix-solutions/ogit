// Package secretbox encrypts small secrets for the database (TOTP seeds)
// with AES-256-GCM under a key derived from security.encryption_key.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

var ErrNoKey = errors.New("security.encryption_key is not set")

type Box struct{ aead cipher.AEAD }

// New derives the AES key from key (any length; configuration requires 32
// bytes or more). An empty key gives a nil Box: secrets can't be stored.
func New(key string) (*Box, error) {
	if key == "" {
		return nil, nil
	}
	sum := sha256.Sum256([]byte("ogit secretbox v1\x00" + key))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal returns base64(nonce | ciphertext).
func (b *Box) Seal(plain string) (string, error) {
	if b == nil {
		return "", ErrNoKey
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b.aead.Seal(nonce, nonce, []byte(plain), nil)), nil
}

func (b *Box) Open(sealed string) (string, error) {
	if b == nil {
		return "", ErrNoKey
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil || len(raw) < b.aead.NonceSize() {
		return "", errors.New("malformed secret")
	}
	plain, err := b.aead.Open(nil, raw[:b.aead.NonceSize()], raw[b.aead.NonceSize():], nil)
	if err != nil {
		return "", errors.New("cannot decrypt secret (was security.encryption_key changed?)")
	}
	return string(plain), nil
}
