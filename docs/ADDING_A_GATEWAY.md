# Adding a payment gateway

This guide adds a fictional vendor, **Bayarku**, by hand. You will touch
exactly three places:

1. `internal/domain/provider.go`: one constant
2. `internal/gateway/bayarku/`: the new adapter package
3. `internal/app/app.go`: one line to register it

The domain rules, use cases, HTTP layer and database stay the same.

## 1. Name the provider

```go
// internal/domain/provider.go
const (
	ProviderMidtrans Provider = "midtrans"
	ProviderXendit   Provider = "xendit"
	ProviderDOKU     Provider = "doku"
	ProviderBayarku  Provider = "bayarku" // new
)
```

If the vendor offers a channel the hub doesn't know yet (for example a new
e-wallet), add a `Channel` constant in the same file.

## 2. Write the adapter

Create `internal/gateway/bayarku/`. Use `internal/gateway/midtrans` as the
template. It is the simplest adapter. A typical split:

| File | Contents |
|---|---|
| `bayarku.go` | `Gateway` struct, `Options`, `New`, `Provider`, `ValidateCredentials`, `GetStatus`, the `call` helper, `mapStatus` |
| `charge.go` (or similar) | vendor request/response structs and `build…` that maps a `gateway.CreatePaymentRequest` to the vendor body |
| `webhook.go` | `ParseWebhook`: verify the signature, then map to `gateway.WebhookEvent` |
| `bayarku_test.go` | tests against an `httptest.Server` |

### The skeleton

```go
package bayarku

import (
	"context"
	"net/http"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
	"github.com/taufiksoleh/payment-gateway-hub/internal/gateway"
)

const SecretAPIKey = "api_key" // document every secret key here

type Options struct {
	HTTPClient *http.Client
	BaseURL    string // tests point this at httptest
}

type Gateway struct {
	client  *http.Client
	baseURL string
}

func New(opts Options) *Gateway {
	g := &Gateway{client: opts.HTTPClient, baseURL: opts.BaseURL}
	if g.client == nil {
		g.client = &http.Client{Timeout: 30 * time.Second}
	}
	if g.baseURL == "" {
		g.baseURL = "https://api.bayarku.example"
	}
	return g
}

// The compiler checks the contract for you.
var _ gateway.Gateway = (*Gateway)(nil)

func (g *Gateway) Provider() domain.Provider { return domain.ProviderBayarku }

func (g *Gateway) ValidateCredentials(cred domain.GatewayCredential) error {
	return gateway.RequireSecrets(cred, SecretAPIKey)
}

func (g *Gateway) CreatePayment(ctx context.Context, cred domain.GatewayCredential, req gateway.CreatePaymentRequest) (*gateway.CreatePaymentResult, error) {
	// 1. Map req.Method / req.Channel to the vendor's codes. If the
	//    combination isn't supported, return
	//    gateway.UnsupportedChannel(domain.ProviderBayarku, req.Method, req.Channel).
	// 2. Send req.PaymentID as the vendor's order/reference id.
	// 3. Call the API with gateway.Do, decode with gateway.DecodeJSON.
	// 4. Fill CreatePaymentResult.Action with whatever the customer needs.
	panic("todo")
}

func (g *Gateway) GetStatus(ctx context.Context, cred domain.GatewayCredential, ref gateway.PaymentRef) (*gateway.StatusResult, error) {
	panic("todo")
}

func (g *Gateway) ParseWebhook(ctx context.Context, cred domain.GatewayCredential, req gateway.WebhookRequest) (*gateway.WebhookEvent, error) {
	// Verify FIRST, parse second. Use crypto/subtle or hmac.Equal.
	// Bad signature → return nil, domain.ErrInvalidSignature
	// Valid but irrelevant event → return nil, gateway.ErrEventIgnored
	panic("todo")
}
```

### Rules every adapter must follow

These rules are the contract. Breaking one breaks the hub in subtle ways.

1. **Send `req.PaymentID` as the vendor reference**, and return it as
   `WebhookEvent.PaymentID`. This is how webhooks find the payment.
2. **Translate every vendor status** in one `mapStatus` function. Unknown
   statuses map to `domain.StatusPending`, never to `paid`.
3. **Bad signature → `domain.ErrInvalidSignature`.** Compare secrets in
   constant time.
4. **Vendor errors → `*gateway.ProviderError`.** This lets the API answer
   502 with the vendor's message.
5. **Bad merchant input → `domain.Invalid(field, msg)`.** For example a
   missing phone number for OVO.
6. **No storage, no logging of secrets, no global state.** Credentials
   arrive on every call because each merchant has its own.

### Optional: refunds

If the vendor has a refund API, also implement `gateway.Refunder`:

```go
var _ gateway.Refunder = (*Gateway)(nil)

func (g *Gateway) Refund(ctx context.Context, cred domain.GatewayCredential, req gateway.RefundRequest) (*gateway.RefundResult, error) {
	// use req.RefundID as the vendor idempotency key
}
```

If you leave it out, `POST /v1/payments/{id}/refunds` answers
`422 unsupported` for this provider automatically.

## 3. Register it

```go
// internal/app/app.go
gateways := gateway.NewRegistry(
	midtrans.New(midtrans.Options{HTTPClient: providerHTTP}),
	xendit.New(xendit.Options{HTTPClient: providerHTTP}),
	doku.New(doku.Options{HTTPClient: providerHTTP}),
	bayarku.New(bayarku.Options{HTTPClient: providerHTTP}), // new
)
```

That's it. `GET /v1/providers` now lists `bayarku`. Merchants can connect it
with `PUT /v1/gateways/bayarku`, and vendor webhooks arrive at
`/webhooks/bayarku/{merchant_id}`.

## 4. Test it

Copy the pattern from `internal/gateway/midtrans/midtrans_test.go`:

- **Create**: start an `httptest.Server`, assert the path, auth header and
  JSON body the adapter sends, return a canned vendor response, and assert
  the mapped `CreatePaymentResult`.
- **Vendor error**: return a 4xx and assert `errors.Is(err, domain.ErrProvider)`.
- **Unsupported channel**: assert `errors.Is(err, domain.ErrUnsupported)`.
- **Webhook**: build a correctly signed payload and assert the event, then
  tamper with one byte and assert `domain.ErrInvalidSignature`.

Finally, try it against the vendor's sandbox with real test keys before
enabling it in production.
