package httpapi_test

import (
	"net/url"
	"testing"
	"time"
)

type paymentList struct {
	Payments []struct {
		ID      string `json:"id"`
		OrderID string `json:"order_id"`
		Status  string `json:"status"`
	} `json:"payments"`
	NextCursor string `json:"next_cursor"`
}

func TestListPayments(t *testing.T) {
	ts := newTestServer(t)
	apiKey, paidID := merchantWithPaidPayment(t, ts.Server, "Toko") // INV-1, paid
	for _, order := range []string{"INV-2", "INV-3"} {              // pending
		if code := call(t, ts.Server, "POST", "/v1/payments", apiKey, nil, paymentBody(order), nil); code != 201 {
			t.Fatalf("create %s: %d", order, code)
		}
	}

	var all paymentList
	if code := call(t, ts.Server, "GET", "/v1/payments", apiKey, nil, nil, &all); code != 200 {
		t.Fatalf("list: %d", code)
	}
	if len(all.Payments) != 3 || all.Payments[0].OrderID != "INV-3" || all.NextCursor != "" {
		t.Fatalf("all = %+v", all)
	}

	var paid paymentList
	call(t, ts.Server, "GET", "/v1/payments?status=paid&provider=fake&method=virtual_account", apiKey, nil, nil, &paid)
	if len(paid.Payments) != 1 || paid.Payments[0].ID != paidID {
		t.Fatalf("paid = %+v", paid)
	}

	var byOrder paymentList
	call(t, ts.Server, "GET", "/v1/payments?order_id=INV-2", apiKey, nil, nil, &byOrder)
	if len(byOrder.Payments) != 1 || byOrder.Payments[0].OrderID != "INV-2" {
		t.Fatalf("by order = %+v", byOrder)
	}

	// Time window, with an unencoded "+" offset as people type it.
	from := time.Now().Add(-time.Hour).In(time.FixedZone("WIB", 7*3600)).Format(time.RFC3339)
	var window paymentList
	if code := call(t, ts.Server, "GET", "/v1/payments?created_from="+from, apiKey, nil, nil, &window); code != 200 || len(window.Payments) != 3 {
		t.Fatalf("window: %d %+v", code, window)
	}
	future := url.QueryEscape(time.Now().Add(time.Hour).Format(time.RFC3339))
	call(t, ts.Server, "GET", "/v1/payments?created_from="+future, apiKey, nil, nil, &window)
	if len(window.Payments) != 0 {
		t.Fatalf("future window = %+v", window)
	}

	// Paging.
	var page1, page2 paymentList
	call(t, ts.Server, "GET", "/v1/payments?limit=2", apiKey, nil, nil, &page1)
	call(t, ts.Server, "GET", "/v1/payments?limit=2&cursor="+page1.NextCursor, apiKey, nil, nil, &page2)
	if len(page1.Payments) != 2 || page1.NextCursor == "" || len(page2.Payments) != 1 || page2.NextCursor != "" {
		t.Fatalf("pages = %+v / %+v", page1, page2)
	}
}

func TestListPayments_IsolationAndValidation(t *testing.T) {
	ts := newTestServer(t)
	keyA, _ := merchantWithPaidPayment(t, ts.Server, "Toko A")
	keyB, _ := merchantWithPaidPayment(t, ts.Server, "Toko B")

	var list paymentList
	call(t, ts.Server, "GET", "/v1/payments", keyB, nil, nil, &list)
	if len(list.Payments) != 1 {
		t.Fatalf("merchant B sees %d payments, want only its own 1", len(list.Payments))
	}

	var errBody struct {
		Error struct {
			Code  string `json:"code"`
			Field string `json:"field"`
		} `json:"error"`
	}
	for query, field := range map[string]string{
		"?status=lost":            "status",
		"?method=cash":            "method",
		"?provider=stripe":        "provider",
		"?created_from=yesterday": "created_from",
		"?limit=abc":              "limit",
		"?limit=1000":             "limit",
	} {
		code := call(t, ts.Server, "GET", "/v1/payments"+query, keyA, nil, nil, &errBody)
		if code != 422 || errBody.Error.Field != field {
			t.Errorf("%s: %d field=%q, want 422 on %q", query, code, errBody.Error.Field, field)
		}
	}
	if code := call(t, ts.Server, "GET", "/v1/payments", "", nil, nil, &errBody); code != 401 {
		t.Fatalf("without API key: %d, want 401", code)
	}
}
