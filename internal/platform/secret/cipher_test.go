package secret_test

import (
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/taufiksoleh/payment-gateway-hub/internal/platform/secret"
)

func TestCipher_RoundTrip(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	c, err := secret.NewCipherFromBase64(key)
	if err != nil {
		t.Fatal(err)
	}

	plain := []byte(`{"server_key":"SB-Mid-server-abc"}`)
	a, _ := c.Encrypt(plain)
	b, _ := c.Encrypt(plain)
	if bytes.Equal(a, b) {
		t.Fatal("same plaintext should encrypt differently (random nonce)")
	}

	got, err := c.Decrypt(a)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("decrypt: %q %v", got, err)
	}

	a[len(a)-1] ^= 1
	if _, err := c.Decrypt(a); err == nil {
		t.Fatal("tampered ciphertext should fail")
	}
}

func TestNewCipher_RejectsShortKey(t *testing.T) {
	if _, err := secret.NewCipher([]byte("short")); err == nil {
		t.Fatal("expected error for short key")
	}
}
