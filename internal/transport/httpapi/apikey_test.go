package httpapi_test

import (
	"testing"
	"time"
)

type apiKeyJSON struct {
	ID        string     `json:"id"`
	Hint      string     `json:"hint"`
	Status    string     `json:"status"`
	Current   bool       `json:"current"`
	ExpiresAt *time.Time `json:"expires_at"`
}

type rotated struct {
	APIKey               string     `json:"api_key"`
	Key                  apiKeyJSON `json:"key"`
	PreviousKeysExpireAt time.Time  `json:"previous_keys_expire_at"`
	Revoked              int        `json:"revoked"`
}

type errJSON struct {
	Error struct {
		Code  string `json:"code"`
		Field string `json:"field"`
	} `json:"error"`
}

func listKeys(t *testing.T, ts *testServer, apiKey string) []apiKeyJSON {
	t.Helper()
	var out struct {
		APIKeys []apiKeyJSON `json:"api_keys"`
	}
	if code := call(t, ts.Server, "GET", "/v1/api-keys", apiKey, nil, nil, &out); code != 200 {
		t.Fatalf("list keys: %d", code)
	}
	return out.APIKeys
}

func TestAPIKeys_RotationFlow(t *testing.T) {
	ts := newTestServer(t)
	key1 := registerMerchant(t, ts, "Toko")

	keys := listKeys(t, ts, key1)
	if len(keys) != 1 || !keys[0].Current || keys[0].Status != "active" || keys[0].Hint != "pgh_…"+key1[len(key1)-4:] {
		t.Fatalf("initial keys = %+v", keys)
	}

	// Rotate with the default grace period (24h).
	var r rotated
	if code := call(t, ts.Server, "POST", "/v1/api-keys/rotate", key1, nil, nil, &r); code != 201 {
		t.Fatalf("rotate: %d", code)
	}
	key2 := r.APIKey
	if key2 == "" || key2 == key1 {
		t.Fatalf("rotate returned %q", key2)
	}
	if d := time.Until(r.PreviousKeysExpireAt); d < 23*time.Hour || d > 25*time.Hour {
		t.Fatalf("previous keys expire in %v, want ~24h", d)
	}

	// Old key still works during the grace period and shows as expiring.
	keys = listKeys(t, ts, key1)
	if len(keys) != 2 || keys[0].Status != "active" || keys[1].Status != "expiring" || !keys[1].Current {
		t.Fatalf("keys during grace = %+v", keys)
	}

	// Rotate again with grace 0: every older key (including key2, which made
	// this request) stops working at once.
	if code := call(t, ts.Server, "POST", "/v1/api-keys/rotate", key2, nil, map[string]int{"grace_period_seconds": 0}, &r); code != 201 {
		t.Fatalf("rotate now: %d", code)
	}
	key3 := r.APIKey
	for _, old := range []string{key1, key2} {
		if code := call(t, ts.Server, "GET", "/v1/api-keys", old, nil, nil, nil); code != 401 {
			t.Fatalf("old key still accepted: %d", code)
		}
	}
	if len(listKeys(t, ts, key3)) != 3 {
		t.Fatal("key3 should list all three keys")
	}
}

func TestAPIKeys_RevokeAndValidation(t *testing.T) {
	ts := newTestServer(t)
	key1 := registerMerchant(t, ts, "Toko")
	first := listKeys(t, ts, key1)[0]

	var e errJSON
	if code := call(t, ts.Server, "DELETE", "/v1/api-keys/"+first.ID, key1, nil, nil, &e); code != 409 || e.Error.Code != "conflict" {
		t.Fatalf("revoke only key: %d %q", code, e.Error.Code)
	}
	if code := call(t, ts.Server, "DELETE", "/v1/api-keys/key_nope", key1, nil, nil, &e); code != 404 {
		t.Fatalf("revoke unknown: %d", code)
	}
	if code := call(t, ts.Server, "POST", "/v1/api-keys/rotate", key1, nil, map[string]int{"grace_period_seconds": 8 * 24 * 3600}, &e); code != 422 || e.Error.Field != "grace_period_seconds" {
		t.Fatalf("grace too long: %d %+v", code, e)
	}

	var r rotated
	call(t, ts.Server, "POST", "/v1/api-keys/rotate", key1, nil, nil, &r)
	if code := call(t, ts.Server, "DELETE", "/v1/api-keys/"+first.ID, r.APIKey, nil, nil, nil); code != 204 {
		t.Fatalf("revoke old key: %d", code)
	}
	if code := call(t, ts.Server, "GET", "/v1/api-keys", key1, nil, nil, nil); code != 401 {
		t.Fatalf("revoked key still accepted: %d", code)
	}
}

func TestAPIKeys_AdminIssue(t *testing.T) {
	ts := newTestServer(t)
	var m struct {
		ID     string `json:"id"`
		APIKey string `json:"api_key"`
	}
	call(t, ts.Server, "POST", "/v1/merchants", adminToken, nil, map[string]string{"name": "Toko"}, &m)
	path := "/v1/merchants/" + m.ID + "/api-keys"

	if code := call(t, ts.Server, "POST", path, m.APIKey, nil, nil, nil); code != 401 {
		t.Fatalf("merchant key on admin route: %d, want 401", code)
	}

	var r rotated
	if code := call(t, ts.Server, "POST", path, adminToken, nil, map[string]bool{"revoke_existing": true}, &r); code != 201 || r.Revoked != 1 {
		t.Fatalf("issue: %d revoked=%d", code, r.Revoked)
	}
	if code := call(t, ts.Server, "GET", "/v1/api-keys", m.APIKey, nil, nil, nil); code != 401 {
		t.Fatalf("leaked key still accepted: %d", code)
	}
	if code := call(t, ts.Server, "GET", "/v1/api-keys", r.APIKey, nil, nil, nil); code != 200 {
		t.Fatalf("issued key rejected: %d", code)
	}
	if code := call(t, ts.Server, "POST", "/v1/merchants/mch_nobody/api-keys", adminToken, nil, nil, nil); code != 404 {
		t.Fatalf("unknown merchant: %d", code)
	}
}
