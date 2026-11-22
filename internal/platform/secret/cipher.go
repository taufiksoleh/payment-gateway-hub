// Package secret encrypts small values (gateway credentials) at rest.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// Cipher is AES-256-GCM. Output layout: nonce || ciphertext+tag.
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher builds a Cipher from a 32-byte key.
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secret: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// NewCipherFromBase64 decodes a standard base64 key (as stored in env vars).
// Generate one with: openssl rand -base64 32
func NewCipherFromBase64(encoded string) (*Cipher, error) {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("secret: key is not valid base64: %w", err)
	}
	return NewCipher(key)
}

// Encrypt seals plaintext with a fresh random nonce.
func (c *Cipher) Encrypt(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt opens data produced by Encrypt.
func (c *Cipher) Decrypt(data []byte) ([]byte, error) {
	n := c.aead.NonceSize()
	if len(data) < n {
		return nil, errors.New("secret: ciphertext too short")
	}
	return c.aead.Open(nil, data[:n], data[n:], nil)
}
