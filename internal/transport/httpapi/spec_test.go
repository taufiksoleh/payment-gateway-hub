package httpapi

// These tests keep docs/api/openapi.yaml in step with the code: every route
// documented, every documented schema matching its Go type, every error code
// listed.

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	apidocs "github.com/taufiksoleh/payment-gateway-hub/docs/api"
	"github.com/taufiksoleh/payment-gateway-hub/internal/transport/dto"
)

type node = map[string]any

func loadSpec(t *testing.T) node {
	t.Helper()
	var spec node
	if err := yaml.Unmarshal(apidocs.Spec, &spec); err != nil {
		t.Fatalf("openapi.yaml: %v", err)
	}
	return spec
}

var pathParam = regexp.MustCompile(`\{[^}]+\}`)

func TestSpec_DocumentsEveryRoute(t *testing.T) {
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	inCode := map[string]bool{}
	for _, m := range regexp.MustCompile(`mux\.Handle(?:Func)?\("(\w+) ([^"]+)"`).FindAllStringSubmatch(string(src), -1) {
		inCode[m[1]+" "+pathParam.ReplaceAllString(m[2], "{}")] = true
	}
	if len(inCode) < 20 {
		t.Fatalf("found only %d routes in router.go; has the registration style changed?", len(inCode))
	}

	inSpec := map[string]bool{}
	for path, item := range loadSpec(t)["paths"].(node) {
		for method := range item.(node) {
			if method != "parameters" {
				inSpec[strings.ToUpper(method)+" "+pathParam.ReplaceAllString(path, "{}")] = true
			}
		}
	}

	for route := range inCode {
		if !inSpec[route] {
			t.Errorf("route %s is not in docs/api/openapi.yaml", route)
		}
	}
	for route := range inSpec {
		if !inCode[route] {
			t.Errorf("docs/api/openapi.yaml documents %s, which the router doesn't serve", route)
		}
	}
}

func TestSpec_OperationsAreComplete(t *testing.T) {
	ids := map[string]string{}
	for path, item := range loadSpec(t)["paths"].(node) {
		for method, raw := range item.(node) {
			if method == "parameters" {
				continue
			}
			op := raw.(node)
			where := strings.ToUpper(method) + " " + path
			id, _ := op["operationId"].(string)
			switch {
			case id == "":
				t.Errorf("%s has no operationId", where)
			case ids[id] != "":
				t.Errorf("operationId %s used by %s and %s", id, ids[id], where)
			}
			ids[id] = where
			if op["summary"] == nil {
				t.Errorf("%s has no summary", where)
			}
			if responses, _ := op["responses"].(node); len(responses) == 0 {
				t.Errorf("%s has no responses", where)
			}
		}
	}
}

func TestSpec_RefsResolve(t *testing.T) {
	spec := loadSpec(t)
	var walk func(v any)
	walk = func(v any) {
		switch v := v.(type) {
		case node:
			if ref, ok := v["$ref"].(string); ok {
				if resolve(spec, ref) == nil {
					t.Errorf("unresolved $ref %s", ref)
				}
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(spec)
}

func resolve(spec node, ref string) node {
	if !strings.HasPrefix(ref, "#/") {
		return nil
	}
	var cur any = spec
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		m, ok := cur.(node)
		if !ok {
			return nil
		}
		cur = m[part]
	}
	n, _ := cur.(node)
	return n
}

// TestSpec_SchemasMatchGoTypes compares each schema's properties with the
// JSON field names of the Go type that produces or accepts it.
func TestSpec_SchemasMatchGoTypes(t *testing.T) {
	spec := loadSpec(t)
	schemas := spec["components"].(node)["schemas"].(node)
	types := map[string]any{
		"Payment":                 dto.Payment{},
		"PaymentAction":           dto.Action{},
		"Customer":                dto.Customer{},
		"PaymentEvent":            dto.Event{},
		"PaymentList":             paymentListResponse{},
		"CreatePaymentRequest":    createPaymentRequest{},
		"RefundRequest":           refundRequest{},
		"Gateway":                 gatewayResponse{},
		"SaveGatewayRequest":      saveGatewayRequest{},
		"Settings":                settingsResponse{},
		"UpdateSettingsRequest":   updateSettingsRequest{},
		"APIKey":                  apiKeyResponse{},
		"IssuedAPIKey":            issuedKeyResponse{},
		"RotateAPIKeyRequest":     rotateRequest{},
		"RotateAPIKeyResponse":    rotateResponse{},
		"IssueAPIKeyRequest":      issueKeyRequest{},
		"IssueAPIKeyResponse":     issueKeyResponse{},
		"RegisterMerchantRequest": registerMerchantRequest{},
		"RegisteredMerchant":      registerMerchantResponse{},
		"Webhook":                 webhookResponse{},
		"WebhookList":             webhookListResponse{},
	}
	for name, v := range types {
		schema, ok := schemas[name].(node)
		if !ok {
			t.Errorf("schema %s missing", name)
			continue
		}
		want := jsonFields(reflect.TypeOf(v))
		got := properties(spec, schema)
		if !slices.Equal(want, got) {
			t.Errorf("schema %s properties = %v, Go type has %v", name, got, want)
		}
	}
}

// jsonFields returns the sorted JSON names of t's fields, flattening
// embedded structs as encoding/json does.
func jsonFields(t reflect.Type) []string {
	var out []string
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Anonymous {
			out = append(out, jsonFields(f.Type)...)
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// properties returns the sorted property names of a schema, following allOf.
func properties(spec, schema node) []string {
	var out []string
	if ref, ok := schema["$ref"].(string); ok {
		return properties(spec, resolve(spec, ref))
	}
	props, _ := schema["properties"].(node)
	for name := range props {
		out = append(out, name)
	}
	if all, ok := schema["allOf"].([]any); ok {
		for _, part := range all {
			out = append(out, properties(spec, part.(node))...)
		}
	}
	sort.Strings(out)
	return out
}

func TestSpec_ListsEveryErrorCode(t *testing.T) {
	codePattern := regexp.MustCompile(`(?:writeProblem\(w, http\.\w+, |code, (?:field, )?msg = http\.\w+, |status, code(?:, field)? := http\.\w+, )"(\w+)"`)
	inCode := map[string]bool{}
	for _, file := range []string{"respond.go", "idempotency.go", "ratelimit.go", "middleware.go"} {
		src, err := os.ReadFile(file) //nolint:gosec // a fixed list of this package's files
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range codePattern.FindAllStringSubmatch(string(src), -1) {
			inCode[m[1]] = true
		}
	}
	if len(inCode) < 10 {
		t.Fatalf("found only %d error codes; has respond.go changed shape?", len(inCode))
	}

	errSchema := loadSpec(t)["components"].(node)["schemas"].(node)["Error"].(node)
	var listed []string
	for _, c := range errSchema["properties"].(node)["error"].(node)["properties"].(node)["code"].(node)["enum"].([]any) {
		listed = append(listed, c.(string))
	}
	for code := range inCode {
		if !slices.Contains(listed, code) {
			t.Errorf("error code %q is returned but not listed in the Error schema", code)
		}
	}
}

func TestSpec_ServedByTheHub(t *testing.T) {
	h := NewRouter(Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	for path, wantType := range map[string]string{"/openapi.yaml": "application/yaml", "/docs": "text/html"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), wantType) || rec.Body.Len() == 0 {
			t.Errorf("%s: %d %q (%d bytes)", path, rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
		}
	}
}
