package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
)

// maxResponseBytes stops a misbehaving provider from exhausting memory.
const maxResponseBytes = 1 << 20

// ProviderError is returned when a provider answers with an error.
// It matches domain.ErrProvider via errors.Is.
type ProviderError struct {
	Provider   domain.Provider
	StatusCode int
	Message    string
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("%s: http %d: %s", e.Provider, e.StatusCode, e.Message)
}

func (e *ProviderError) Is(target error) bool { return target == domain.ErrProvider }

// HTTPRequest describes one JSON call to a provider.
type HTTPRequest struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte // already-encoded JSON, or nil
}

// HTTPResponse is the raw answer.
type HTTPResponse struct {
	StatusCode int
	Body       []byte
}

// Do sends req and returns the raw response. It only fails on transport
// errors; callers decide which status codes are errors because providers
// disagree (Midtrans returns HTTP 200 with an error status_code in the body).
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

// DecodeJSON unmarshals a provider response and wraps failures as provider
// errors.
func DecodeJSON(p domain.Provider, raw []byte, out any) error {
	if err := json.Unmarshal(raw, out); err != nil {
		return &ProviderError{Provider: p, StatusCode: 0, Message: "invalid JSON response: " + err.Error()}
	}
	return nil
}

// RequireSecrets returns a validation error for the first missing key.
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

// UnsupportedChannel builds the standard error for a channel an adapter
// does not handle.
func UnsupportedChannel(p domain.Provider, m domain.Method, c domain.Channel) error {
	return fmt.Errorf("%w: %s does not support %s/%s", domain.ErrUnsupported, p, m, c)
}
