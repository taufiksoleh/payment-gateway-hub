package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
)

const maxResponseBytes = 1 << 20

// WIB is Western Indonesia Time (UTC+7), used in Midtrans and DOKU timestamps.
var WIB = time.FixedZone("WIB", 7*60*60)

// DefaultHTTPClient is used by adapters constructed without a client.
func DefaultHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

// Endpoints are a provider's base URLs per environment.
type Endpoints struct {
	Sandbox    string
	Production string
}

// Or fills empty URLs from defaults.
func (e Endpoints) Or(defaults Endpoints) Endpoints {
	if e.Sandbox == "" {
		e.Sandbox = defaults.Sandbox
	}
	if e.Production == "" {
		e.Production = defaults.Production
	}
	return e
}

func (e Endpoints) For(env domain.Environment) string {
	if env == domain.EnvironmentProduction {
		return e.Production
	}
	return e.Sandbox
}

// BasicAuth builds an Authorization value with key as the user name and an
// empty password, the scheme Midtrans and Xendit use.
func BasicAuth(key string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(key+":"))
}

// ProviderError is a provider's error answer. It matches domain.ErrProvider.
type ProviderError struct {
	Provider   domain.Provider
	StatusCode int
	Message    string
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("%s: http %d: %s", e.Provider, e.StatusCode, e.Message)
}

func (e *ProviderError) Is(target error) bool { return target == domain.ErrProvider }

// OutcomeUnknown reports whether a failed call may still have been carried
// out by the provider: the request was sent but no clear answer came back
// (network error, timeout, 5xx, unreadable response). Such calls must be
// retried with the same idempotency key, never treated as rejected.
func OutcomeUnknown(err error) bool {
	if !errors.Is(err, domain.ErrProvider) {
		return false
	}
	var pe *ProviderError
	if errors.As(err, &pe) {
		return pe.StatusCode == 0 || pe.StatusCode >= 500
	}
	return true
}

type HTTPRequest struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte // encoded JSON or nil
}

type HTTPResponse struct {
	StatusCode int
	Body       []byte
}

// Do sends req and returns the raw response. It fails only on transport
// errors: adapters decide what counts as an error response, since providers
// disagree (Midtrans reports errors inside HTTP 200 bodies).
func Do(ctx context.Context, client *http.Client, req HTTPRequest) (*HTTPResponse, error) {
	var body io.Reader
	if req.Body != nil {
		body = bytes.NewReader(req.Body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Accept", "application/json")
	if req.Body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", domain.ErrProvider, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: read body: %w", domain.ErrProvider, err)
	}
	return &HTTPResponse{StatusCode: resp.StatusCode, Body: raw}, nil
}

// EncodeJSON marshals a request body; a nil body encodes to nil.
func EncodeJSON(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	return b, nil
}

// DecodeJSON reports malformed provider responses as provider errors.
func DecodeJSON(p domain.Provider, raw []byte, out any) error {
	if err := json.Unmarshal(raw, out); err != nil {
		return &ProviderError{Provider: p, Message: "invalid JSON response: " + err.Error()}
	}
	return nil
}

// RequireSecrets validates the environment and that each key is present.
func RequireSecrets(cred domain.GatewayCredential, keys ...string) error {
	if !cred.Environment.Valid() {
		return domain.Invalid("environment", "must be sandbox or production")
	}
	for _, k := range keys {
		if cred.Secret(k) == "" {
			return domain.Invalid("secrets."+k, "is required")
		}
	}
	return nil
}

func UnsupportedChannel(p domain.Provider, m domain.Method, c domain.Channel) error {
	return fmt.Errorf("%w: %s does not support %s/%s", domain.ErrUnsupported, p, m, c)
}

// ParseTime returns nil for empty or malformed values.
func ParseTime(layout, value string, loc *time.Location) *time.Time {
	t, err := time.ParseInLocation(layout, value, loc)
	if err != nil {
		return nil
	}
	return &t
}
