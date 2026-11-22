// Package idempotency defines how the hub remembers requests sent with an
// Idempotency-Key header, so a client that retries (for example after a
// timeout) gets the first response back instead of doing the work twice.
//
// The flow, implemented by the HTTP middleware:
//
//	Claim     first request with this key wins the claim and runs
//	Complete  a 2xx response is stored for later replays
//	Release   any other outcome drops the claim so the client can retry
//
// This package only holds the types and the storage port. Implementations
// live in internal/storage/{memory,postgres}.
package idempotency

import (
	"context"
	"time"
)

const (
	// KeyTTL is how long a completed key is remembered. Afterwards the same
	// key may be used again.
	KeyTTL = 24 * time.Hour

	// LockTimeout is how long a claim may stay in progress before it is
	// presumed abandoned (e.g. the process crashed) and can be reclaimed.
	// It is well above the longest request (provider timeout plus retries).
	LockTimeout = 5 * time.Minute
)

// Record is one remembered request.
type Record struct {
	MerchantID  string
	Key         string
	RequestHash string    // fingerprint of method + path + body
	Token       string    // identifies the request currently holding the claim
	StatusCode  int       // 0 while the first request is still running
	Body        []byte    // stored response body once completed
	CreatedAt   time.Time // when the claim was taken
}

// InProgress reports whether the first request is still running.
func (r Record) InProgress() bool { return r.StatusCode == 0 }

// Reclaim says when an existing record may be taken over by a new claim.
type Reclaim struct {
	AbandonedBefore time.Time // in-progress claims taken before this are dead
	ExpiredBefore   time.Time // any record created before this is forgotten
}

// ReclaimAt computes the cutoffs for a claim made at now.
func ReclaimAt(now time.Time) Reclaim {
	return Reclaim{
		AbandonedBefore: now.Add(-LockTimeout),
		ExpiredBefore:   now.Add(-KeyTTL),
	}
}

// Store persists records. Implementations must make Claim atomic: of two
// concurrent claims for the same (merchant, key), exactly one may win.
type Store interface {
	// Claim stores rec as in progress if no live record exists for
	// (rec.MerchantID, rec.Key), and returns nil. Otherwise it returns the
	// existing record and stores nothing. Records matching reclaim are
	// treated as absent and replaced.
	Claim(ctx context.Context, rec Record, reclaim Reclaim) (*Record, error)

	// Complete stores the response, but only if token still holds the
	// claim (a request presumed dead must not overwrite its successor).
	Complete(ctx context.Context, merchantID, key, token string, statusCode int, body []byte) error

	// Release deletes the claim if token still holds it.
	Release(ctx context.Context, merchantID, key, token string) error

	// Purge deletes every record created before cutoff and returns how many.
	Purge(ctx context.Context, before time.Time) (int64, error)
}
