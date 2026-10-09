//go:build linux && cgo

package pluginhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

// Exercise the native library and production registration boundary: direct
// handleMethod tests cannot catch incomplete registration metadata or ABI wiring.
func TestTenantPolicyNativeIdentityRegistrationAndAttribution(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "plugins")
	if err := os.Mkdir(pluginDir, 0700); err != nil {
		t.Fatal(err)
	}
	library := filepath.Join(pluginDir, "tenant-policy.so")
	build := exec.Command("go", "build", "-buildmode=c-shared", "-o", library, "./plugins/tenant-policy")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build native tenant policy: %v\n%s", err, out)
	}
	stateDir := filepath.Join(dir, "private")
	if err := os.Mkdir(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(stateDir, "ledger.json")
	sum := sha256.Sum256([]byte("native-test-key"))
	hash := hex.EncodeToString(sum[:])
	ledger := map[string]any{
		"users": map[string]any{"openwebui": map[string]any{"providers": []string{"p", "codex"}, "limit_usd": "0", "spent_usd": "0"}},
		"keys":  map[string]any{hash: map[string]any{"user": "openwebui", "label": "OpenWebUI"}},
	}
	writeLedger := func() {
		t.Helper()
		b, err := json.Marshal(ledger)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(state, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeLedger()
	cfgBytes, err := yaml.Marshal(map[string]any{"plugins": map[string]any{"enabled": true, "dir": pluginDir, "configs": map[string]any{"tenant-policy": map[string]any{"enabled": true, "required": true, "mode": "identity", "state": state}}}})
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	if err = yaml.Unmarshal(cfgBytes, &cfg); err != nil {
		t.Fatal(err)
	}
	h := New()
	defer h.ShutdownAll()
	h.ApplyConfig(context.Background(), &cfg)
	required, unavailable := h.requiredPolicyState()
	if !required || unavailable {
		t.Fatal("native plugin rejected by production registration or required-policy capability validation")
	}
	records := h.activeRecords()
	if len(records) != 1 || !validPlugin(records[0].plugin) {
		t.Fatalf("invalid native registration: %#v", records)
	}
	a := &requiredPolicyAccess{host: h}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer native-test-key")
	result, errAuth := a.Authenticate(context.Background(), req)
	if errAuth != nil || result == nil || result.Principal != "tenant-policy:"+hash {
		t.Fatalf("native identity authentication: %#v, %v", result, errAuth)
	}
	realtimeReq := httptest.NewRequest(http.MethodGet, "/v1/realtime", nil)
	// The token itself is deliberately not a tenant key. Only the host-verified
	// issuer is sent through native authorization, without revealing the raw key.
	realtimeReq.Header.Set("Authorization", "Bearer ek_fixture")
	manager := sdkaccess.NewManager()
	manager.SetProviders([]sdkaccess.Provider{a})
	reauthorize := func() *sdkaccess.AuthError {
		t.Helper()
		checked, err := manager.ReauthorizeDelegatedPrincipal(context.Background(), realtimeReq, result.Principal, result.Provider)
		if err == nil && (checked == nil || checked.Principal != result.Principal) {
			t.Fatal("delegated attribution changed")
		}
		return err
	}
	if err := reauthorize(); err != nil {
		t.Fatalf("native delegated authorization: %v", err)
	}
	spoof := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls", strings.NewReader(`{"VerifiedPrincipal":"`+result.Principal+`"}`))
	spoof.Header.Set("Authorization", "Bearer ek_fixture")
	spoof.Header.Set("VerifiedPrincipal", result.Principal)
	spoof.Header.Set("X-Authenticated-Principal", result.Principal)
	if _, err := a.Authenticate(context.Background(), spoof); err == nil {
		t.Fatal("HTTP caller spoofed verified principal")
	}
	item := h.runtimeConfig.Plugins.Configs["tenant-policy"]
	identityRaw := item.Raw
	if err := yaml.Unmarshal([]byte("mode: quota\n"), &item.Raw); err != nil {
		t.Fatal(err)
	}
	h.runtimeConfig.Plugins.Configs["tenant-policy"] = item
	if err := reauthorize(); err == nil {
		t.Fatal("strict host policy allowed delegation")
	}
	item.Raw = identityRaw
	h.runtimeConfig.Plugins.Configs["tenant-policy"] = item
	if _, err := a.Authenticate(context.Background(), realtimeReq); err == nil {
		t.Fatal("unverified secret authenticated directly")
	}
	if _, err := manager.ReauthorizeDelegatedPrincipal(context.Background(), realtimeReq, result.Principal, "other-provider"); err == nil {
		t.Fatal("wrong issuer provider authorized")
	}
	user := ledger["users"].(map[string]any)["openwebui"].(map[string]any)
	user["providers"] = []string{"p"}
	writeLedger()
	if err := reauthorize(); err == nil {
		t.Fatal("missing Codex grant authorized")
	}
	user["providers"] = []string{"p", "codex"}
	user["disabled"] = true
	writeLedger()
	if err := reauthorize(); err == nil {
		t.Fatal("disabled user authorized")
	}
	user["disabled"] = false
	writeLedger()
	if err := reauthorize(); err != nil {
		t.Fatalf("restored issuer: %v", err)
	}
	ctx := sdkaccess.WithAuthenticatedPrincipal(context.Background(), result.Principal)
	turn := pluginapi.RequestInterceptRequest{RequestedModel: "new-registry-model(high)", Model: "model", Provider: "p", AttemptID: "native-attempt", RequestID: "native-request", Body: []byte(`{"input":[{"type":"input_image","image_url":"https://example.test/image"}]}`), Metadata: map[string]any{"request_path": "/v1/responses"}}
	if response := h.InterceptRequestBeforeAuth(ctx, turn); response.Terminate {
		t.Fatalf("native before authorization: %#v", response)
	}
	if response := h.InterceptRequestAfterAuth(ctx, turn); response.Terminate {
		t.Fatalf("native after authorization: %#v", response)
	}
	records[0].plugin.Capabilities.UsagePlugin.HandleUsage(ctx, pluginapi.UsageRecord{AttemptID: turn.AttemptID, RequestID: turn.RequestID, Provider: "p", Model: "model", Detail: pluginapi.UsageDetail{InputTokens: 12, OutputTokens: 3}})
	saved, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(saved, &ledger); err != nil {
		t.Fatal(err)
	}
	debits := ledger["debits"].(map[string]any)
	debit, ok := debits[turn.AttemptID].(map[string]any)
	if !ok || debit["identity_only"] != true || debit["user"] != "openwebui" || debit["key_hash"] != hash {
		t.Fatalf("native attempt attribution: %#v", debits)
	}
	metrics := ledger["metrics"].(map[string]any)
	if len(metrics) != 1 {
		t.Fatalf("native usage attribution absent: %#v", metrics)
	}
	for _, raw := range metrics {
		metric := raw.(map[string]any)
		if metric["user"] != "openwebui" || metric["key_hash"] != hash || metric["billed_usd"] != nil || metric["estimated_api_equivalent_usd"] != nil {
			t.Fatalf("native metric attribution/cost: %#v", metric)
		}
	}
	keys := ledger["keys"].(map[string]any)
	keys[hash].(map[string]any)["revoked"] = true
	writeLedger()
	if err := reauthorize(); err == nil {
		t.Fatal("revoked delegated issuer authorized")
	}
	if _, errAuth = a.Authenticate(context.Background(), req); errAuth == nil {
		t.Fatal("native revoked key authenticated")
	}
}
