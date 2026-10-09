package access

import (
	"context"
	"net/http"
	"sync"
)

// Manager coordinates authentication providers.
type Manager struct {
	mu        sync.RWMutex
	providers []Provider
}

// NewManager constructs an empty manager.
func NewManager() *Manager {
	return &Manager{}
}

// SetProviders replaces the active provider list.
func (m *Manager) SetProviders(providers []Provider) {
	if m == nil {
		return
	}
	cloned := make([]Provider, len(providers))
	copy(cloned, providers)
	m.mu.Lock()
	m.providers = cloned
	m.mu.Unlock()
}

// Providers returns a snapshot of the active providers.
func (m *Manager) Providers() []Provider {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	snapshot := make([]Provider, len(m.providers))
	copy(snapshot, m.providers)
	return snapshot
}

// Authenticate evaluates providers until one succeeds.
func (m *Manager) Authenticate(ctx context.Context, r *http.Request) (*Result, *AuthError) {
	if m == nil {
		return nil, nil
	}
	providers := m.Providers()
	if len(providers) == 0 {
		return nil, nil
	}

	var (
		missing bool
		invalid bool
	)

	for _, provider := range providers {
		if provider == nil {
			continue
		}
		res, authErr := provider.Authenticate(ctx, r)
		if authErr == nil {
			return res, nil
		}
		if IsAuthErrorCode(authErr, AuthErrorCodeNotHandled) {
			continue
		}
		if IsAuthErrorCode(authErr, AuthErrorCodeNoCredentials) {
			missing = true
			continue
		}
		if IsAuthErrorCode(authErr, AuthErrorCodeInvalidCredential) {
			invalid = true
			continue
		}
		return nil, authErr
	}

	if invalid {
		return nil, NewInvalidCredentialError()
	}
	if missing {
		return nil, NewNoCredentialsError()
	}
	return nil, NewNoCredentialsError()
}

// RequiresFrontendAuthentication reports whether credential shortcuts must be disabled.
func (m *Manager) RequiresFrontendAuthentication() bool {
	for _, provider := range m.Providers() {
		if guard, ok := provider.(interface{ RequireFrontendAuthentication() bool }); ok && guard.RequireFrontendAuthentication() {
			return true
		}
	}
	return false
}

// ReauthorizeDelegatedPrincipal rechecks a locally verified credential's issuer
// against every required frontend policy. Providers must explicitly opt in.
func (m *Manager) ReauthorizeDelegatedPrincipal(ctx context.Context, r *http.Request, principal, issuerProvider string) (*Result, *AuthError) {
	var result *Result
	for _, provider := range m.Providers() {
		guard, required := provider.(interface{ RequireFrontendAuthentication() bool })
		if !required || !guard.RequireFrontendAuthentication() {
			continue
		}
		authorizer, ok := provider.(interface {
			ReauthorizeDelegatedPrincipal(context.Context, *http.Request, string, string) (*Result, *AuthError)
		})
		if !ok {
			return nil, NewInvalidCredentialError()
		}
		checked, err := authorizer.ReauthorizeDelegatedPrincipal(ctx, r, principal, issuerProvider)
		if err != nil {
			return nil, err
		}
		if checked == nil || checked.Principal != principal || checked.Provider != issuerProvider {
			return nil, NewInvalidCredentialError()
		}
		result = checked
	}
	if result == nil {
		return nil, NewInvalidCredentialError()
	}
	return result, nil
}
