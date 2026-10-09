//go:build linux && cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func policyFixture(t *testing.T) (string, pluginapi.RequestInterceptRequest) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ledger.json")
	hash := KeyHash("test-secret")
	l := emptyLedger()
	l.Users["u"] = User{Providers: []string{"p"}, LimitUSD: "0.3", SpentUSD: "0"}
	l.Keys[hash] = Key{User: "u"}
	b, _ := json.Marshal(l)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	configState.Lock()
	configState.cfg = policyConfig{State: path, Models: map[string]modelPolicy{"alias": {Provider: "p", UpstreamModel: "m", InputUSD: "0.001", OutputUSD: "0.002", charge: "0.1"}}}
	configState.Unlock()
	return path, pluginapi.RequestInterceptRequest{Principal: principal(hash), RequestedModel: "alias", Model: "m", Provider: "p", AttemptID: "a", Body: []byte(`{"messages":[]}`), Metadata: map[string]any{"request_path": "/v1/chat/completions"}}
}
func invokePolicy(t *testing.T, method string, r any) pluginapi.RequestInterceptResponse {
	t.Helper()
	b, _ := json.Marshal(r)
	raw, err := handleMethod(method, b)
	if err != nil {
		t.Fatal(err)
	}
	var e pluginabi.Envelope
	if err = json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	var result pluginapi.RequestInterceptResponse
	if err = json.Unmarshal(e.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func TestPolicyProviderRevocationAndUnsupportedPath(t *testing.T) {
	path, r := policyFixture(t)
	r.Provider = "other"
	if !invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r).Terminate {
		t.Fatal("provider bypass")
	}
	r.Provider = "p"
	r.Metadata["request_path"] = "/v1/images/generations"
	if !invokePolicy(t, pluginabi.MethodRequestInterceptBefore, r).Terminate {
		t.Fatal("image bypass")
	}
	r.Metadata["request_path"] = "/v1/chat/completions"
	if err := withLedger(path, true, func(l *Ledger) error {
		k := l.Keys[KeyHash("test-secret")]
		k.Revoked = true
		l.Keys[KeyHash("test-secret")] = k
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r).Terminate {
		t.Fatal("revocation bypass")
	}
}
func TestPolicyConcurrentReservationsAndUsageDedup(t *testing.T) {
	path, r := policyFixture(t)
	var wg sync.WaitGroup
	allowed := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			copy := r
			copy.AttemptID = string(rune('a' + i))
			b, _ := json.Marshal(copy)
			raw, err := handleMethod(pluginabi.MethodRequestInterceptAfter, b)
			if err != nil {
				allowed <- false
				return
			}
			var e pluginabi.Envelope
			_ = json.Unmarshal(raw, &e)
			var result pluginapi.RequestInterceptResponse
			_ = json.Unmarshal(e.Result, &result)
			allowed <- !result.Terminate
		}(i)
	}
	wg.Wait()
	close(allowed)
	n := 0
	for ok := range allowed {
		if ok {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("admitted %d, expected 3", n)
	}
	var attempt string
	if err := withLedger(path, false, func(l *Ledger) error {
		for id := range l.Debits {
			attempt = id
			break
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	usage := pluginapi.UsageRecord{AttemptID: attempt, RequestID: "event", Provider: "p", Model: "m", Detail: pluginapi.UsageDetail{InputTokens: 1, OutputTokens: 1}}
	if err := settle(usage); err != nil {
		t.Fatal(err)
	}
	if err := settle(usage); err != nil {
		t.Fatal(err)
	}
	if err := withLedger(path, false, func(l *Ledger) error {
		if l.Users["u"].SpentUSD != "0.3" {
			t.Fatal("released conservative reserve")
		}
		if l.Debits[attempt].ActualUSD != "0.003" {
			t.Fatal("incorrect actual usage")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	usage.RequestID = "second"
	usage.Detail.InputTokens = 500
	if err := settle(usage); err != nil {
		t.Fatal(err)
	}
	if err := withLedger(path, false, func(l *Ledger) error {
		if l.Users["u"].SpentUSD != "0.3" || !l.Users["u"].OverageBlocked {
			t.Fatalf("overage balance %s", l.Users["u"].SpentUSD)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestExactLiteLLMExponentRate(t *testing.T) {
	got, err := priceDecimal(json.RawMessage(`1.25e-7`))
	if err != nil || got != "0.000000125" {
		t.Fatalf("%s %v", got, err)
	}
}

func TestCatalogCacheMaximaAndUnsupportedBilling(t *testing.T) {
	row := map[string]json.RawMessage{"input_cost_per_token": json.RawMessage(`0.000001`), "output_cost_per_token": json.RawMessage(`0.000002`), "cache_creation_input_token_cost": json.RawMessage(`0.000003`), "output_cost_per_token_above_200k_tokens": json.RawMessage(`0.000005`)}
	in, out, err := catalogRates(row)
	if err != nil || in != "0.000003" || out != "0.000005" {
		t.Fatalf("%s %s %v", in, out, err)
	}
	row["search_context_cost_per_query"] = json.RawMessage(`{"low":0.01}`)
	if _, _, err = catalogRates(row); err == nil {
		t.Fatal("nested search billing accepted")
	}
}
func TestConfigureHostMetadataAndExactSnapshot(t *testing.T) {
	path, _ := policyFixture(t)
	raw, _ := json.Marshal(map[string]any{"schema_version": 7, "config_yaml": []byte("enabled: true\nrequired: true\npriority: 100\nstate: " + path + "\nmodels:\n  alias:\n    provider: p\n    upstream_model: m\n    input_allowance_tokens: 100\n    output_allowance_tokens: 10\n    input_usd_per_token: '0.001'\n    output_usd_per_token: '0.002'\n")})
	if err := configure(raw); err != nil {
		t.Fatal(err)
	}
	if configSnapshot().Models["alias"].charge != "0.12" {
		t.Fatal("incorrect exact allowance")
	}
}
