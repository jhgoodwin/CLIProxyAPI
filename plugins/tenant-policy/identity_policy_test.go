//go:build linux && cgo

package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func identityFixture(t *testing.T) (string, pluginapi.RequestInterceptRequest) {
	t.Helper()
	path, r := policyFixture(t)
	configState.Lock()
	configState.cfg.Mode = "identity"
	configState.cfg.Models = nil
	configState.Unlock()
	// Existing quota state must not disable identity-only attribution.
	if err := withLedger(path, true, func(l *Ledger) error {
		u := l.Users["u"]
		u.LimitUSD = "0"
		u.SpentUSD = "99"
		u.OverageBlocked = true
		l.Users["u"] = u
		k := l.Keys[KeyHash("test-secret")]
		k.Label = "OpenWebUI"
		l.Keys[KeyHash("test-secret")] = k
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return path, r
}

func TestIdentityModeMediaCountAndWebsocketTurnAttribution(t *testing.T) {
	for _, endpoint := range []string{"/v1/chat/completions", "/v1/images/generations", "/v1/images/edits", "/v1/audio/speech", "/v1/tts", "/v1/messages/count_tokens", "/v1/responses", "/v1/responses/compact", "/v1beta/models/*action", "/v1/videos"} {
		t.Run(endpoint, func(t *testing.T) {
			path, r := identityFixture(t)
			r.Metadata["request_path"] = endpoint
			r.Body = []byte(`{"modalities":["audio"],"messages":[{"content":[{"type":"image_url","image_url":{"url":"https://example.test/image"}}]}]}`)
			before := r
			before.Provider = ""
			before.AttemptID = ""
			if invokePolicy(t, pluginabi.MethodRequestInterceptBefore, before).Terminate {
				t.Fatal("before auth rejected")
			}
			if invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r).Terminate {
				t.Fatal("after auth rejected")
			}
			event := pluginapi.UsageRecord{AttemptID: r.AttemptID, RequestID: "event", Provider: r.Provider, Model: r.Model, Detail: pluginapi.UsageDetail{InputTokens: 100000}}
			if err := settle(event); err != nil {
				t.Fatal(err)
			}
			if err := settle(event); err != nil {
				t.Fatal(err)
			}
			if err := withLedger(path, false, func(l *Ledger) error {
				d := l.Debits[r.AttemptID]
				if !d.IdentityOnly || d.User != "u" || d.KeyHash != KeyHash("test-secret") || d.Provider != "p" || d.Model != "m" {
					t.Fatalf("attribution: %#v", d)
				}
				if l.Users["u"].SpentUSD != "99" || len(l.Pools) != 0 {
					t.Fatal("identity mode changed quotas")
				}
				rows := metricRows(l)
				if len(rows) != 1 || rows[0].User != "u" || rows[0].EstimatedUSD != nil || rows[0].BilledUSD != nil {
					t.Fatalf("unknown usage cost: %#v", rows)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIdentityModeRejectsRevokedDisabledUnauthorizedAndMissingAttempt(t *testing.T) {
	for _, failure := range []string{"revoked", "disabled", "provider", "missing-attempt", "missing-model", "replayed-attempt", "mapped-substitution"} {
		t.Run(failure, func(t *testing.T) {
			path, r := identityFixture(t)
			switch failure {
			case "revoked":
				_ = withLedger(path, true, func(l *Ledger) error {
					k := l.Keys[KeyHash("test-secret")]
					k.Revoked = true
					l.Keys[KeyHash("test-secret")] = k
					return nil
				})
			case "disabled":
				_ = withLedger(path, true, func(l *Ledger) error { u := l.Users["u"]; u.Disabled = true; l.Users["u"] = u; return nil })
			case "provider":
				r.Provider = "other"
			case "missing-attempt":
				r.AttemptID = ""
			case "missing-model":
				r.Model = ""
			case "replayed-attempt":
				if invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r).Terminate {
					t.Fatal("first rejected")
				}
			case "mapped-substitution":
				configState.Lock()
				configState.cfg.Models = map[string]modelPolicy{"alias": {Provider: "p", UpstreamModel: "expected"}}
				configState.Unlock()
			}
			if !invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r).Terminate {
				t.Fatal("unauthorized attempt accepted")
			}
		})
	}
}

func TestIdentityModeConfigurationWithoutModelsOrPrices(t *testing.T) {
	path, _ := identityFixture(t)
	c := policyConfig{Mode: "identity", State: path}
	if err := configure(configureRequest(t, c)); err != nil {
		t.Fatal(err)
	}
	c.Mode = "quota"
	if err := configure(configureRequest(t, c)); err == nil {
		t.Fatal("quota mode accepted no models")
	}
	c.Mode = "unexpected"
	if err := configure(configureRequest(t, c)); err == nil {
		t.Fatal("invalid mode accepted")
	}
	c.Mode = "identity"
	c.Models = map[string]modelPolicy{"alias": {Provider: "p", UpstreamModel: "m", InputAllowance: 1}}
	if err := configure(configureRequest(t, c)); err == nil {
		t.Fatal("identity allowance accepted")
	}
}

func identityAuthenticate(t *testing.T, headers http.Header, path string) pluginapi.FrontendAuthResponse {
	t.Helper()
	raw, _ := json.Marshal(pluginapi.FrontendAuthRequest{Method: http.MethodGet, Path: path, Headers: headers})
	out, err := handleMethod(pluginabi.MethodFrontendAuthAuthenticate, raw)
	if err != nil {
		t.Fatal(err)
	}
	var e pluginabi.Envelope
	if err = json.Unmarshal(out, &e); err != nil {
		t.Fatal(err)
	}
	var result pluginapi.FrontendAuthResponse
	if err = json.Unmarshal(e.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestIdentityRealtimeRequiresTenantBearerAndCodexAccess(t *testing.T) {
	path, _ := identityFixture(t)
	valid := http.Header{"Authorization": {"Bearer test-secret"}}
	if identityAuthenticate(t, valid, "/v1/realtime").Authenticated {
		t.Fatal("non-Codex user entered realtime")
	}
	if err := withLedger(path, true, func(l *Ledger) error {
		u := l.Users["u"]
		u.Providers = append(u.Providers, "codex")
		l.Users["u"] = u
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !identityAuthenticate(t, valid, "/v1/realtime").Authenticated {
		t.Fatal("Codex user rejected")
	}
	for _, headers := range []http.Header{{}, {"Authorization": {"Bearer wrong"}}, {"Authorization": {"Bearer test-secret", "Bearer test-secret"}}, {"X-Api-Key": {"test-secret"}}} {
		if identityAuthenticate(t, headers, "/v1/realtime").Authenticated {
			t.Fatal("invalid auth accepted")
		}
	}
}

func TestIdentityFilesRequireActiveTenantBearer(t *testing.T) {
	path, _ := identityFixture(t)
	valid := http.Header{"Authorization": {"Bearer test-secret"}}
	routes := []string{"/v1/files", "/v1/files/file-123", "/v1/files/file-123/content"}
	for _, route := range routes {
		if !identityAuthenticate(t, valid, route).Authenticated {
			t.Fatalf("tenant frontend authentication separately blocked %s", route)
		}
		for _, headers := range []http.Header{{}, {"Authorization": {"Bearer wrong"}}, {"Authorization": {"Bearer test-secret", "Bearer test-secret"}}, {"X-Api-Key": {"test-secret"}}} {
			if identityAuthenticate(t, headers, route).Authenticated {
				t.Fatalf("invalid credentials accepted for %s", route)
			}
		}
	}
	for _, state := range []string{"disabled", "revoked"} {
		if err := withLedger(path, true, func(l *Ledger) error {
			u := l.Users["u"]
			u.Disabled = state == "disabled"
			l.Users["u"] = u
			k := l.Keys[KeyHash("test-secret")]
			k.Revoked = state == "revoked"
			l.Keys[KeyHash("test-secret")] = k
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		for _, route := range routes {
			if identityAuthenticate(t, valid, route).Authenticated {
				t.Fatalf("%s tenant authenticated for %s", state, route)
			}
		}
	}
}

func TestIdentityMediaNeverEstimatesTextOnlyCost(t *testing.T) {
	path, r := identityFixture(t)
	configState.Lock()
	configState.cfg.Models = map[string]modelPolicy{"alias": {Provider: "p", UpstreamModel: "m", estimateRates: MetricRates{Input: "0.1", Output: "0.2"}}}
	configState.Unlock()
	r.Metadata["request_path"] = "/v1/images/generations"
	if invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r).Terminate {
		t.Fatal("image rejected")
	}
	b := usage.NewSubsetTokenBreakdown(10, 0, 0, 10, 0, 20)
	if err := settle(pluginapi.UsageRecord{AttemptID: r.AttemptID, RequestID: "event", Provider: "p", Model: "m", Detail: pluginapi.UsageDetail{InputTokens: 10, OutputTokens: 10, TokenBreakdown: b}}); err != nil {
		t.Fatal(err)
	}
	if err := withLedger(path, false, func(l *Ledger) error {
		if metricRows(l)[0].EstimatedUSD != nil {
			t.Fatal("media token price pretended accurate")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityThinkingSuffixAndSchedulerProviderFilter(t *testing.T) {
	_, r := identityFixture(t)
	r.RequestedModel = "m(high)"
	if invokePolicy(t, pluginabi.MethodRequestInterceptBefore, r).Terminate || invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r).Terminate {
		t.Fatal("thinking suffix rejected")
	}
	raw, _ := json.Marshal(pluginapi.SchedulerPickRequest{Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "denied", Provider: "other"}, {ID: "allowed", Provider: "p"}}, Options: pluginapi.SchedulerOptions{Metadata: map[string]any{"caller_scope": session.CallerScope(r.Principal)}}})
	out, err := handleMethod(pluginabi.MethodSchedulerPick, raw)
	if err != nil {
		t.Fatal(err)
	}
	var e pluginabi.Envelope
	_ = json.Unmarshal(out, &e)
	var picked pluginapi.SchedulerPickResponse
	_ = json.Unmarshal(e.Result, &picked)
	if !picked.Handled || picked.Reject || picked.AuthID != "allowed" {
		t.Fatalf("provider filter: %#v", picked)
	}
}

func TestIdentityExplicitModelsDenyUninterceptedCodexRoutes(t *testing.T) {
	path, _ := identityFixture(t)
	if err := withLedger(path, true, func(l *Ledger) error {
		u := l.Users["u"]
		u.Providers = append(u.Providers, "codex")
		l.Users["u"] = u
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	valid := http.Header{"Authorization": {"Bearer test-secret"}}
	routes := []string{"/v1/realtime", "/v1/realtime/client_secrets", "/v1/realtime/sessions", "/v1/realtime/transcription_sessions", "/v1/realtime/calls/call", "/v1/realtime/calls/call/hangup", "/v1/live", "/v1/live/call", "/v1/alpha/search", "/backend-api/codex/alpha/search"}
	for _, route := range routes {
		if !identityAuthenticate(t, valid, route).Authenticated {
			t.Fatalf("unrestricted identity rejected %s", route)
		}
	}
	configState.Lock()
	configState.cfg.Models = map[string]modelPolicy{"known-model": {Provider: "codex", UpstreamModel: "known-model"}}
	configState.Unlock()
	for _, route := range routes {
		if identityAuthenticate(t, valid, route).Authenticated {
			t.Fatalf("restricted identity allowed %s", route)
		}
	}
	if !identityAuthenticate(t, valid, "/v1/responses").Authenticated {
		t.Fatal("mapped identity blocked intercepted Responses route")
	}
}

func TestDelegatedRealtimeIssuerRequiresIdentityModeAndCodexGrant(t *testing.T) {
	path, _ := identityFixture(t)
	if err := withLedger(path, true, func(l *Ledger) error {
		u := l.Users["u"]
		u.Providers = []string{"codex"}
		l.Users["u"] = u
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	req := pluginapi.FrontendAuthRequest{Method: http.MethodGet, Path: "/v1/realtime", VerifiedPrincipal: principal(KeyHash("test-secret"))}
	authenticate := func() bool {
		t.Helper()
		raw, _ := json.Marshal(req)
		out, err := handleMethod(pluginabi.MethodFrontendAuthAuthenticate, raw)
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Result pluginapi.FrontendAuthResponse
		}
		if err = json.Unmarshal(out, &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Result.Authenticated
	}
	if !authenticate() {
		t.Fatal("verified issuer rejected")
	}
	configState.Lock()
	configState.cfg.Mode = "quota"
	configState.Unlock()
	if authenticate() {
		t.Fatal("quota mode accepted delegation")
	}
	configState.Lock()
	configState.cfg.Mode = "identity"
	configState.Unlock()
	req.Path = "/v1/chat/completions"
	if authenticate() {
		t.Fatal("delegation escaped realtime scope")
	}
}
