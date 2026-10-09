package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	codexlive "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/live"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
)

type delegatedRealtimePolicy struct {
	revoked bool
	allow   bool
	checks  int
}

func (*delegatedRealtimePolicy) Identifier() string                  { return "test-policy" }
func (*delegatedRealtimePolicy) RequireFrontendAuthentication() bool { return true }
func (p *delegatedRealtimePolicy) Authenticate(_ context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if r.Header.Get("Authorization") != "Bearer tenant-key" || p.revoked {
		return nil, sdkaccess.NewInvalidCredentialError()
	}
	return &sdkaccess.Result{Principal: "tenant-policy:hash", Provider: p.Identifier()}, nil
}
func (p *delegatedRealtimePolicy) ReauthorizeDelegatedPrincipal(_ context.Context, _ *http.Request, principal, provider string) (*sdkaccess.Result, *sdkaccess.AuthError) {
	p.checks++
	if !p.allow || p.revoked || principal != "tenant-policy:hash" || provider != p.Identifier() {
		return nil, sdkaccess.NewInvalidCredentialError()
	}
	return &sdkaccess.Result{Principal: principal, Provider: provider}, nil
}

func TestRealtimeSecretRechecksRequiredIssuerAndRestoresIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := sdkaccess.NewManager()
	policy := &delegatedRealtimePolicy{allow: true}
	manager.SetProviders([]sdkaccess.Provider{policy})
	handler := codexlive.NewHandler(nil, nil)
	router := gin.New()
	router.POST("/v1/realtime/client_secrets", realtimeStandardAuthMiddleware(manager), handler.CreateClientSecret)
	for _, route := range []struct{ method, path string }{{http.MethodPost, "/v1/realtime/calls"}, {http.MethodGet, "/v1/realtime"}} {
		router.Handle(route.method, route.path, realtimeAuthMiddleware(manager, handler), func(c *gin.Context) {
			if got := sdkaccess.AuthenticatedPrincipal(c.Request.Context()); got != "tenant-policy:hash" {
				t.Errorf("authenticated principal = %q", got)
			}
			if got, _ := c.Get("userApiKey"); got != "tenant-policy:hash" {
				t.Errorf("usage principal = %v", got)
			}
			if _, ok := c.Get(codexlive.ClientSecretSessionContextKey); !ok {
				t.Error("missing secret session")
			}
			if _, ok := c.Get(codexlive.ClientSecretPrincipalContextKey); !ok {
				t.Error("missing session binding")
			}
			c.Status(http.StatusNoContent)
		})
	}
	issue := httptest.NewRequest(http.MethodPost, "/v1/realtime/client_secrets", strings.NewReader(`{"session":{"type":"realtime","model":"gpt-realtime"}}`))
	issue.Header.Set("Authorization", "Bearer tenant-key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, issue)
	if response.Code != http.StatusOK {
		t.Fatalf("issue: %d %s", response.Code, response.Body.String())
	}
	var secret struct{ Value string }
	if err := json.Unmarshal(response.Body.Bytes(), &secret); err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct{ method, path string }{{http.MethodPost, "/v1/realtime/calls"}, {http.MethodGet, "/v1/realtime"}} {
		req := httptest.NewRequest(route.method, route.path, nil)
		req.Header.Set("Authorization", "Bearer "+secret.Value)
		response = httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != http.StatusNoContent {
			t.Fatalf("use: %d %s", response.Code, response.Body.String())
		}
	}
	if policy.checks != 2 {
		t.Fatalf("issuer authorization checks = %d", policy.checks)
	}
	for _, tc := range []struct {
		name, token    string
		revoked, allow bool
	}{{"revoked", secret.Value, true, true}, {"strict-policy", secret.Value, false, false}, {"forged", "ek_invalid", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			policy.revoked, policy.allow = tc.revoked, tc.allow
			req := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls", nil)
			req.Header.Set("Authorization", "Bearer "+tc.token)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("unauthorized use: %d %s", response.Code, response.Body.String())
			}
		})
	}
}
