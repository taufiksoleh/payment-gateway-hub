// Package idempotency defines the storage for Idempotency-Key requests. The
// first request with a key claims it; a 2xx response is stored and replayed
// to retries, any other outcome releases the key.
package idempotency

import (
	"context"
	"time"
)

const (
	KeyTTL = 24 * time.Hour
	// LockTimeout is how long a claim may stay in progress before it is
	// presumed abandoned; it is well above the longest request.
	LockTimeout = 5 * time.Minute
)

type Record struct {
	MerchantID  string
	Key         string
	RequestHash string // method + path + body
	Token       string // identifies the request holding the claim
	StatusCode  int    // 0 while in progress
	Body        []byte
	CreatedAt   time.Time
}

func (r Record) InProgress() bool { return r.StatusCode == 0 }

// Reclaim sets when an existing record may be taken over.
type Reclaim struct {
	AbandonedBefore time.Time // for in-progress claims
	ExpiredBefore   time.Time // for any record
}

func ReclaimAt(now time.Time) Reclaim {
	return Reclaim{
		AbandonedBefore: now.Add(-LockTimeout),
		ExpiredBefore:   now.Add(-KeyTTL),
	}
}

// Store must make Claim atomic: of concurrent claims for one key, exactly
// one wins.
type Store interface {
	// Claim stores rec and returns nil, or returns the live record holding
	// the key. Records matching reclaim count as absent.
	Claim(ctx context.Context, rec Record, reclaim Reclaim) (*Record, error)
	// Complete and Release act only while token still holds the claim.
	Complete(ctx context.Context, merchantID, key, token string, statusCode int, body []byte) error
	Release(ctx context.Context, merchantID, key, token string) error
	Purge(ctx context.Context, before time.Time) (int64, error)
}
