package pluginhost

import (
	"context"
	"net/http"
	"strings"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const requiredPolicyAccessKey = "pluginhost:required-policy"

// requiredPolicyState retains configuration intent even when loading fails.
func (h *Host) requiredPolicyState() (required bool, unavailable bool) {
	if h == nil {
		return false, false
	}
	h.mu.Lock()
	cfg := h.runtimeConfig
	ids := make(map[string]bool)
	globallyEnabled := cfg != nil && cfg.Plugins.Enabled
	if cfg != nil {
		for id, item := range cfg.Plugins.Configs {
			if item.Required {
				ids[id] = globallyEnabled && item.Enabled != nil && *item.Enabled
			}
		}
	}
	h.mu.Unlock()
	if len(ids) == 0 {
		return false, false
	}
	records := h.activeRecords()
	for id, enabled := range ids {
		found := false
		for _, record := range records {
			if record.id == id && record.plugin.SchemaVersion >= pluginabi.SchemaVersionRequiredPolicy && enabled && !h.isPluginFused(id) && h.recordCurrent(record) && record.plugin.Capabilities.RequestInterceptor != nil && record.plugin.Capabilities.FrontendAuthProvider != nil && record.plugin.Capabilities.FrontendAuthProviderExclusive && record.plugin.Capabilities.RequestInterceptorRequired {
				found = true
				break
			}
		}
		if !found {
			return true, true
		}
	}
	return true, false
}

func policyUnavailableResponse() pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{Terminate: true, StatusCode: http.StatusServiceUnavailable, ResponseBody: []byte(`{"error":{"message":"required request policy unavailable","type":"server_error"}}`)}
}

// requiredPolicyAccess dynamically delegates authentication so reloads cannot leave
// stale frontend provider snapshots that silently drop the required policy.
type requiredPolicyAccess struct{ host *Host }

func (*requiredPolicyAccess) Identifier() string { return requiredPolicyAccessKey }
func (a *requiredPolicyAccess) Authenticate(ctx context.Context, req *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	required, unavailable := a.host.requiredPolicyState()
	if !required || unavailable {
		return nil, sdkaccess.NewInternalAuthError("required request policy unavailable", nil)
	}
	a.host.mu.Lock()
	homeEnabled := a.host.runtimeConfig != nil && a.host.runtimeConfig.Home.Enabled
	a.host.mu.Unlock()
	path := strings.TrimSuffix(req.URL.Path, "/")
	supported := req.Method == http.MethodPost && (path == "/v1/chat/completions" || path == "/v1/completions" || path == "/v1/messages" || path == "/v1/responses" || path == "/chat/completions" || path == "/completions" || path == "/messages" || path == "/responses")
	if homeEnabled || !supported || req.Header.Get("Upgrade") != "" {
		return nil, sdkaccess.NewInternalAuthError("endpoint unavailable with required request policy", nil)
	}
	for _, record := range a.host.activeRecords() {
		caps := record.plugin.Capabilities
		if a.host.configuredRequiredPlugin(record.id) && caps.FrontendAuthProvider != nil && caps.FrontendAuthProviderExclusive && caps.RequestInterceptorRequired {
			adapter := &accessAdapter{host: a.host, pluginID: record.id, path: record.path, version: record.version, provider: caps.FrontendAuthProvider}
			result, errAuth := adapter.Authenticate(ctx, req)
			if errAuth == nil && result != nil {
				return result, nil
			}
			return nil, sdkaccess.NewInvalidCredentialError()
		}
	}
	return nil, sdkaccess.NewInternalAuthError("required request policy unavailable", nil)
}

// HasRequiredRequestPolicy reports whether security interception must run for every attempt.
func (h *Host) HasRequiredRequestPolicy() bool {
	if required, _ := h.requiredPolicyState(); required {
		return true
	}
	if h != nil {
		for _, record := range h.activeRecords() {
			if record.plugin.Capabilities.RequestInterceptorRequired {
				return true
			}
		}
	}
	return false
}

func (h *Host) configuredRequiredPlugin(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.runtimeConfig != nil && h.runtimeConfig.Plugins.Configs[id].Required
}

// RequireFrontendAuthentication prevents reusable realtime secrets bypassing the policy.
func (*requiredPolicyAccess) RequireFrontendAuthentication() bool { return true }

func (h *Host) configuredRequiredIDs() map[string]bool {
	ids := make(map[string]bool)
	if h == nil {
		return ids
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.runtimeConfig != nil {
		for id, item := range h.runtimeConfig.Plugins.Configs {
			if item.Required {
				ids[id] = true
			}
		}
	}
	return ids
}
