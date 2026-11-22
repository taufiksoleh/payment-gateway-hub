// Package id generates random identifiers and secrets.
package id

import (
	"crypto/rand"
	"encoding/hex"
)

// New returns a prefixed random id such as "pay_3f9a...". 12 random bytes
// give 24 hex characters, which keeps ids short enough for every provider's
// order-reference limit.
func New(prefix string) string {
	return prefix + "_" + randomHex(12)
}

// Secret returns a prefixed random secret with 32 bytes of entropy, for API
// keys and webhook signing secrets.
func Secret(prefix string) string {
	return prefix + "_" + randomHex(32)
}

func randomHex(n int) string {
	b := make([]byte, n)
	// crypto/rand.Read never returns an error on supported platforms.
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
