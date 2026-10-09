package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
)

type requiredFrontendTestProvider struct{}

func (requiredFrontendTestProvider) Identifier() string                  { return "required-policy" }
func (requiredFrontendTestProvider) RequireFrontendAuthentication() bool { return true }
func (requiredFrontendTestProvider) Authenticate(context.Context, *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	return nil, sdkaccess.NewInternalAuthError("required policy rejects realtime", nil)
}

func TestRealtimeRequiredPolicyPrecedesClientSecretAuthentication(t *testing.T) {
	manager := sdkaccess.NewManager()
	manager.SetProviders([]sdkaccess.Provider{requiredFrontendTestProvider{}})
	router := gin.New()
	// A nil client-secret handler makes any shortcut invocation fail immediately.
	router.POST("/v1/realtime", realtimeAuthMiddleware(manager, nil), func(c *gin.Context) { t.Fatal("required realtime guard was bypassed") })
	request := httptest.NewRequest(http.MethodPost, "/v1/realtime", nil)
	request.Header.Set("Authorization", "Bearer reusable-client-secret")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable && recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, body=%s", recorder.Code, recorder.Body.String())
	}
}

type authenticatedPrincipalTestProvider struct{}

func (authenticatedPrincipalTestProvider) Identifier() string { return "tenant" }
func (authenticatedPrincipalTestProvider) Authenticate(context.Context, *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	return &sdkaccess.Result{Principal: "trusted-principal", Provider: "tenant"}, nil
}

func TestFrontendAuthenticationStoresTrustedPrincipalInRequestContext(t *testing.T) {
	manager := sdkaccess.NewManager()
	manager.SetProviders([]sdkaccess.Provider{authenticatedPrincipalTestProvider{}})
	router := gin.New()
	router.POST("/v1/responses", AuthMiddleware(manager), func(c *gin.Context) {
		if got := sdkaccess.AuthenticatedPrincipal(c.Request.Context()); got != "trusted-principal" {
			t.Fatalf("principal=%q", got)
		}
		c.Status(http.StatusOK)
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	request.Header.Set("X-Authenticated-Principal", "attacker")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d", recorder.Code)
	}
}

func TestAttachedWebsocketRequiredPolicyOverridesDisabledWSAuth(t *testing.T) {
	manager := sdkaccess.NewManager()
	manager.SetProviders([]sdkaccess.Provider{requiredFrontendTestProvider{}})
	server := &Server{engine: gin.New(), accessManager: manager, wsRoutes: make(map[string]struct{})}
	server.wsAuthEnabled.Store(false)
	server.AttachWebsocketRoute("/v1/ws", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("ws-auth:false bypassed required policy") }))
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/ws", nil))
	if recorder.Code != http.StatusServiceUnavailable && recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", recorder.Code)
	}
}
