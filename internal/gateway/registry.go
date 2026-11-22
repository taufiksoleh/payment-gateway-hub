package gateway

import (
	"fmt"
	"sort"

	"github.com/taufiksoleh/payment-gateway-hub/internal/domain"
)

// Registry maps provider names to adapters.
type Registry struct {
	gateways map[domain.Provider]Gateway
}

// NewRegistry builds a Registry from adapters. A duplicate provider panics,
// because that is a wiring bug found at startup.
func NewRegistry(gateways ...Gateway) *Registry {
	r := &Registry{gateways: make(map[domain.Provider]Gateway, len(gateways))}
	for _, g := range gateways {
		if _, dup := r.gateways[g.Provider()]; dup {
			panic(fmt.Sprintf("gateway: provider %q registered twice", g.Provider()))
		}
		r.gateways[g.Provider()] = g
	}
	return r
}

// Get returns the adapter for a provider.
func (r *Registry) Get(p domain.Provider) (Gateway, error) {
	g, ok := r.gateways[p]
	if !ok {
		return nil, fmt.Errorf("%w: provider %q", domain.ErrUnsupported, p)
	}
	return g, nil
}

// Providers lists registered providers in a stable order.
func (r *Registry) Providers() []domain.Provider {
	out := make([]domain.Provider, 0, len(r.gateways))
	for p := range r.gateways {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
