package pluginhost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

func TestIdentityRequiredPolicyAllowsOnlyRegisteredNativeRoutes(t *testing.T) {
	calls := 0
	policy := requiredPolicyRecord("tenant-policy", passingPolicy())
	policy.plugin.Capabilities.FrontendAuthProvider = frontendAuthProviderFunc{identifier: "tenant", authenticate: func(_ context.Context, req pluginapi.FrontendAuthRequest) (pluginapi.FrontendAuthResponse, error) {
		calls++
		return pluginapi.FrontendAuthResponse{Authenticated: req.Headers.Get("Authorization") == "Bearer tenant-key", Principal: "tenant"}, nil
	}}
	native := capabilityRecord{id: "native", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{NativeAPI: &nativePluginDouble{}}}}
	h := newHostWithRecords(policy, native)
	requirePolicyConfig(h, "tenant-policy")
	setPolicyMode := func(mode string) {
		item := h.runtimeConfig.Plugins.Configs["tenant-policy"]
		if err := yaml.Unmarshal([]byte("mode: "+mode+"\n"), &item.Raw); err != nil {
			t.Fatal(err)
		}
		if item.Raw.Kind == yaml.DocumentNode {
			item.Raw = *item.Raw.Content[0]
		}
		h.runtimeConfig.Plugins.Configs["tenant-policy"] = item
	}
	setPolicyMode("identity")
	h.mu.Lock()
	h.nativeRoutes[managementRouteKey(http.MethodPost, "/v1/native-test")] = nativeRouteRecord{pluginID: "native", path: h.activePluginPaths["native"], version: h.activePluginVersions["native"], route: pluginapi.NativeRoute{Method: http.MethodPost, Path: "/v1/native-test"}}
	h.mu.Unlock()
	a := &requiredPolicyAccess{host: h}

	authenticate := func(method, path, key string) *sdkaccess.AuthError {
		req := httptest.NewRequest(method, path, nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		_, err := a.Authenticate(context.Background(), req)
		return err
	}

	before := calls
	if err := authenticate(http.MethodPost, "/v1/native-test", "tenant-key"); err != nil || calls != before+1 {
		t.Fatalf("registered native route: error=%v calls=%d", err, calls-before)
	}
	// A missing credential must be a credential failure from the policy plugin, not a policy-unavailable error.
	before = calls
	if err := authenticate(http.MethodPost, "/v1/native-test", ""); err == nil || err.HTTPStatusCode() != http.StatusUnauthorized || calls != before+1 {
		t.Fatalf("unauthenticated native route: error=%v calls=%d", err, calls-before)
	}
	for _, tc := range []struct{ method, path string }{{http.MethodGet, "/v1/native-test"}, {http.MethodPost, "/v1/native-other"}, {http.MethodPost, "/v1/native-test/x"}} {
		before = calls
		if err := authenticate(tc.method, tc.path, "tenant-key"); err == nil || calls != before {
			t.Fatalf("unregistered %s %s reached authentication: error=%v calls=%d", tc.method, tc.path, err, calls-before)
		}
	}

	h.mu.Lock()
	h.fused["native"] = "panic"
	h.mu.Unlock()
	before = calls
	if err := authenticate(http.MethodPost, "/v1/native-test", "tenant-key"); err == nil || calls != before {
		t.Fatalf("fused native route reached authentication: error=%v calls=%d", err, calls-before)
	}
	h.mu.Lock()
	delete(h.fused, "native")
	h.mu.Unlock()

	// Billing-enforcing modes keep the strict endpoint allowlist.
	setPolicyMode("quota")
	before = calls
	if err := authenticate(http.MethodPost, "/v1/native-test", "tenant-key"); err == nil || calls != before {
		t.Fatalf("quota mode allowed native route: error=%v calls=%d", err, calls-before)
	}
}
