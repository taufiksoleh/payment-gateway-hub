// Package id generates random identifiers and secrets.
package id

import (
	"crypto/rand"
	"encoding/hex"
)

// New returns a prefixed id with 96 random bits, short enough for every
// provider's order-reference limit.
func New(prefix string) string {
	return prefix + "_" + randomHex(12)
}

// Secret returns a prefixed secret with 256 random bits.
func Secret(prefix string) string {
	return prefix + "_" + randomHex(32)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // never fails on supported platforms
	return hex.EncodeToString(b)
}
