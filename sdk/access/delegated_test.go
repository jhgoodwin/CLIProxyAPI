package access

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type delegatedGuard struct{ allowed bool }

func (*delegatedGuard) Identifier() string { return "issuer" }
func (*delegatedGuard) Authenticate(context.Context, *http.Request) (*Result, *AuthError) {
	return nil, NewInvalidCredentialError()
}
func (*delegatedGuard) RequireFrontendAuthentication() bool { return true }
func (g *delegatedGuard) ReauthorizeDelegatedPrincipal(_ context.Context, _ *http.Request, principal, provider string) (*Result, *AuthError) {
	if !g.allowed {
		return nil, NewInvalidCredentialError()
	}
	return &Result{Principal: principal, Provider: provider}, nil
}
func TestDelegationCannotSkipAnyRequiredProvider(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/realtime", nil)
	for _, tc := range []struct {
		name      string
		providers []Provider
		accepted  bool
	}{
		{"none", nil, false},
		{"accepting", []Provider{&delegatedGuard{allowed: true}}, true},
		{"denying", []Provider{&delegatedGuard{allowed: false}}, false},
		{"unsupported-required", []Provider{&delegatedGuard{allowed: true}, unavailableExclusiveProvider{}}, false},
		{"second-denies", []Provider{&delegatedGuard{allowed: true}, &delegatedGuard{allowed: false}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager()
			m.SetProviders(tc.providers)
			_, err := m.ReauthorizeDelegatedPrincipal(context.Background(), req, "principal", "issuer")
			if (err == nil) != tc.accepted {
				t.Fatalf("error=%v, accepted=%v", err, tc.accepted)
			}
		})
	}
}
