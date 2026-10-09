package pluginhost

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func requiredPolicyRecord(id string, interceptor pluginapi.RequestInterceptor) capabilityRecord {
	return capabilityRecord{id: id, plugin: pluginapi.Plugin{SchemaVersion: pluginabi.SchemaVersionRequiredPolicy, Capabilities: pluginapi.Capabilities{
		FrontendAuthProvider: frontendAuthProviderFunc{identifier: "tenant", authenticate: func(context.Context, pluginapi.FrontendAuthRequest) (pluginapi.FrontendAuthResponse, error) {
			return pluginapi.FrontendAuthResponse{Authenticated: true, Principal: id}, nil
		}},
		FrontendAuthProviderExclusive: true, RequestInterceptor: interceptor, RequestInterceptorRequired: true,
	}}}
}
func requirePolicyConfig(h *Host, id string) {
	enabled := true
	h.runtimeConfig = &config.Config{Plugins: config.PluginsConfig{Enabled: true, Configs: map[string]config.PluginInstanceConfig{id: {Enabled: &enabled, Required: true}}}}
}
func passingPolicy() pluginapi.RequestInterceptor {
	return requestInterceptorFunc(func(context.Context, pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
		return pluginapi.RequestInterceptResponse{}, nil
	})
}

func TestRequiredPolicyMissingOrDisabledNeverAllowsExecution(t *testing.T) {
	for _, state := range []string{"missing", "disabled", "global-disabled", "missing-auth", "missing-interceptor", "not-exclusive", "fused", "old-schema"} {
		t.Run(state, func(t *testing.T) {
			record := requiredPolicyRecord("tenant", passingPolicy())
			switch state {
			case "old-schema":
				record.plugin.SchemaVersion = pluginabi.SchemaVersionRequiredPolicy - 1
			case "missing-auth":
				record.plugin.Capabilities.FrontendAuthProvider = nil
			case "missing-interceptor":
				record.plugin.Capabilities.RequestInterceptor = nil
			case "not-exclusive":
				record.plugin.Capabilities.FrontendAuthProviderExclusive = false
			}
			h := newHostWithRecords(record)
			requirePolicyConfig(h, "tenant")
			switch state {
			case "missing":
				setHostSnapshotForTest(h, true)
			case "disabled":
				item := h.runtimeConfig.Plugins.Configs["tenant"]
				disabled := false
				item.Enabled = &disabled
				h.runtimeConfig.Plugins.Configs["tenant"] = item
			case "global-disabled":
				h.runtimeConfig.Plugins.Enabled = false
			case "fused":
				h.fusePlugin("tenant", "test", "panic")
			}
			if !h.HasRequestInterceptors() {
				t.Fatal("required policy disappeared from handler detection")
			}
			response := h.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{})
			if !response.Terminate || response.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("response = %#v", response)
			}
			_, errAuth := (&requiredPolicyAccess{host: h}).Authenticate(context.Background(), httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
			if errAuth == nil {
				t.Fatal("unavailable policy authenticated request")
			}
		})
	}
}

func TestRequiredPolicyInterceptorErrorsAndPanicsReject(t *testing.T) {
	for _, panicCall := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "panic"}[panicCall], func(t *testing.T) {
			h := newHostWithRecords(requiredPolicyRecord("tenant", requestInterceptorFunc(func(context.Context, pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
				if panicCall {
					panic("failure")
				}
				return pluginapi.RequestInterceptResponse{}, errors.New("failure")
			})))
			requirePolicyConfig(h, "tenant")
			response := h.InterceptRequestAfterAuth(context.Background(), pluginapi.RequestInterceptRequest{})
			if !response.Terminate || response.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("response = %#v", response)
			}
		})
	}
}

func TestRequiredPolicyRunsAfterRewritersAndCannotBeSkipped(t *testing.T) {
	checked := false
	policy := requiredPolicyRecord("tenant", requestInterceptorFunc(func(_ context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
		checked = true
		if string(req.Body) != "rewritten" {
			t.Fatalf("checked body = %q", req.Body)
		}
		return pluginapi.RequestInterceptResponse{}, nil
	}))
	policy.priority = 100
	rewriter := capabilityRecord{id: "rewrite", priority: 0, plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{RequestInterceptor: requestInterceptorFunc(func(context.Context, pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
		return pluginapi.RequestInterceptResponse{Body: []byte("rewritten")}, nil
	})}}}
	h := newHostWithRecords(policy, rewriter)
	requirePolicyConfig(h, "tenant")
	response := h.InterceptRequestAfterAuthExcept(context.Background(), pluginapi.RequestInterceptRequest{Body: []byte("original")}, "tenant")
	if response.Terminate || !checked {
		t.Fatalf("policy skipped: checked=%v, response=%#v", checked, response)
	}
}

func TestRequiredPolicyReloadDuringInterceptionRejects(t *testing.T) {
	var h *Host
	policy := requiredPolicyRecord("tenant", passingPolicy())
	rewriter := capabilityRecord{id: "rewrite", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{RequestInterceptor: requestInterceptorFunc(func(context.Context, pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
		setHostSnapshotForTest(h, true)
		return pluginapi.RequestInterceptResponse{}, nil
	})}}}
	h = newHostWithRecords(policy, rewriter)
	requirePolicyConfig(h, "tenant")
	response := h.InterceptRequestAfterAuth(context.Background(), pluginapi.RequestInterceptRequest{})
	if !response.Terminate {
		t.Fatal("plugin unload allowed unchecked execution")
	}
}

func TestRequiredPolicyAuthenticatesConfiguredIdentityAndDeniesUnsupportedPaths(t *testing.T) {
	optional := requiredPolicyRecord("optional", passingPolicy())
	optional.priority = 100
	h := newHostWithRecords(optional, requiredPolicyRecord("tenant", passingPolicy()))
	requirePolicyConfig(h, "tenant")
	adapter := &requiredPolicyAccess{host: h}
	result, errAuth := adapter.Authenticate(context.Background(), httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	if errAuth != nil || result.Principal != "tenant" {
		t.Fatalf("result=%#v, error=%v", result, errAuth)
	}
	for _, path := range []string{"/v1/responses/compact", "/v1/images/generations", "/v1/messages/count_tokens", "/v1/realtime", "/v1/realtime/client_secrets", "/v1/models"} {
		if _, errAuth = adapter.Authenticate(context.Background(), httptest.NewRequest(http.MethodPost, path, nil)); errAuth == nil {
			t.Fatalf("allowed unsupported path %s", path)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	request.Header.Set("Upgrade", "websocket")
	if _, errAuth = adapter.Authenticate(context.Background(), request); errAuth == nil {
		t.Fatal("allowed inbound websocket")
	}
	h.runtimeConfig.Home.Enabled = true
	if _, errAuth = adapter.Authenticate(context.Background(), httptest.NewRequest(http.MethodPost, "/v1/responses", nil)); errAuth == nil {
		t.Fatal("allowed Home execution")
	}
}

func TestRequiredPolicyStaleFrontendManagerFailsClosedOnUnload(t *testing.T) {
	h := newHostWithRecords(requiredPolicyRecord("tenant", passingPolicy()))
	requirePolicyConfig(h, "tenant")
	manager := sdkaccess.NewManager()
	manager.SetProviders([]sdkaccess.Provider{&requiredPolicyAccess{host: h}})
	setHostSnapshotForTest(h, true)
	if _, errAuth := manager.Authenticate(context.Background(), httptest.NewRequest(http.MethodPost, "/v1/responses", nil)); errAuth == nil {
		t.Fatal("stale manager authenticated after required plugin unload")
	}
	if !manager.RequiresFrontendAuthentication() {
		t.Fatal("realtime credential shortcut remains enabled")
	}
}
