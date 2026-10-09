package access

import (
	"context"
	"net/http"
	"strings"
	"sync"
)

// Provider validates credentials for incoming requests.
type Provider interface {
	Identifier() string
	Authenticate(ctx context.Context, r *http.Request) (*Result, *AuthError)
}

// Result conveys authentication outcome.
type Result struct {
	Provider  string
	Principal string
	Metadata  map[string]string
}

var (
	registryMu        sync.RWMutex
	registry          = make(map[string]Provider)
	order             []string
	exclusiveProvider string
)

// RegisterProvider registers a pre-built provider instance for a given type identifier.
func RegisterProvider(typ string, provider Provider) {
	normalizedType := strings.TrimSpace(typ)
	if normalizedType == "" || provider == nil {
		return
	}

	registryMu.Lock()
	if _, exists := registry[normalizedType]; !exists {
		order = append(order, normalizedType)
	}
	registry[normalizedType] = provider
	registryMu.Unlock()
}

// UnregisterProvider removes a provider by type identifier.
func UnregisterProvider(typ string) {
	normalizedType := strings.TrimSpace(typ)
	if normalizedType == "" {
		return
	}
	registryMu.Lock()
	if _, exists := registry[normalizedType]; !exists {
		registryMu.Unlock()
		return
	}
	delete(registry, normalizedType)
	for index := range order {
		if order[index] != normalizedType {
			continue
		}
		order = append(order[:index], order[index+1:]...)
		break
	}
	registryMu.Unlock()
}

// SetExclusiveProvider restricts RegisteredProviders to a single provider key when present.
func SetExclusiveProvider(typ string) {
	normalizedType := strings.TrimSpace(typ)
	registryMu.Lock()
	exclusiveProvider = normalizedType
	registryMu.Unlock()
}

// ClearExclusiveProvider removes any active provider restriction.
func ClearExclusiveProvider() {
	registryMu.Lock()
	exclusiveProvider = ""
	registryMu.Unlock()
}

// RegisteredProviders returns the global provider instances in registration order.
func RegisteredProviders() []Provider {
	registryMu.RLock()
	if exclusiveProvider != "" {
		if provider := registry[exclusiveProvider]; provider != nil {
			registryMu.RUnlock()
			return []Provider{provider}
		}
		registryMu.RUnlock()
		return []Provider{unavailableExclusiveProvider{}}
	}
	if len(order) == 0 {
		registryMu.RUnlock()
		return nil
	}
	providers := make([]Provider, 0, len(order))
	for _, providerType := range order {
		provider, exists := registry[providerType]
		if !exists || provider == nil {
			continue
		}
		providers = append(providers, provider)
	}
	registryMu.RUnlock()
	return providers
}

// Missing exclusive authentication must never fall back to public access.
type unavailableExclusiveProvider struct{}

func (unavailableExclusiveProvider) Identifier() string { return "unavailable-exclusive-provider" }
func (unavailableExclusiveProvider) Authenticate(context.Context, *http.Request) (*Result, *AuthError) {
	return nil, NewInternalAuthError("exclusive authentication provider unavailable", nil)
}

func (unavailableExclusiveProvider) RequireFrontendAuthentication() bool { return true }
