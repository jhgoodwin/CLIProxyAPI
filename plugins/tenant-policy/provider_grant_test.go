//go:build linux && cgo

package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func grantAllProviders(t *testing.T, path string) {
	t.Helper()
	if err := runCLI([]string{"--state", path, "user-set", "u", "0", "*"}); err != nil {
		t.Fatal(err)
	}
	if err := withLedger(path, false, func(l *Ledger) error {
		providers := l.Users["u"].Providers
		if len(providers) != 1 || providers[0] != "*" {
			t.Fatalf("wildcard was not persisted: %v", providers)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWildcardProviderGrantAdmitsFutureProviderAndScheduler(t *testing.T) {
	path, r := identityFixture(t)
	r.Provider = "openai-compatible-future-provider"
	if !invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r).Terminate {
		t.Fatal("explicit provider list admitted a future provider")
	}
	grantAllProviders(t, path)
	if invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r).Terminate {
		t.Fatal("wildcard rejected future provider")
	}
	raw, err := json.Marshal(pluginapi.SchedulerPickRequest{Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "future-account", Provider: r.Provider}}, Options: pluginapi.SchedulerOptions{Metadata: map[string]any{"caller_scope": session.CallerScope(r.Principal)}}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := handleMethod(pluginabi.MethodSchedulerPick, raw)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Result pluginapi.SchedulerPickResponse
	}
	if err = json.Unmarshal(out, &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.Result.Handled || envelope.Result.Reject || envelope.Result.AuthID != "future-account" {
		t.Fatalf("wildcard scheduler: %#v", envelope.Result)
	}
	if err := runCLI([]string{"--state", path, "user-set", "u", "0", "p"}); err != nil {
		t.Fatal(err)
	}
	r.AttemptID = "after-removing-wildcard"
	if !invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r).Terminate {
		t.Fatal("replacing wildcard with explicit list retained future access")
	}
}

func TestWildcardProviderGrantPreservesIdentityGuards(t *testing.T) {
	for _, failure := range []string{"disabled", "revoked", "unmapped-model", "selected-model-substitution"} {
		t.Run(failure, func(t *testing.T) {
			path, r := identityFixture(t)
			grantAllProviders(t, path)
			switch failure {
			case "disabled", "revoked":
				args := []string{"user-disable", "u"}
				if failure == "revoked" {
					args = []string{"key-revoke", KeyHash("test-secret")}
				}
				if err := runCLI(append([]string{"--state", path}, args...)); err != nil {
					t.Fatal(err)
				}
			case "unmapped-model", "selected-model-substitution":
				configState.Lock()
				configState.cfg.Models = map[string]modelPolicy{"alias": {Provider: "p", UpstreamModel: "m"}}
				configState.Unlock()
				if failure == "unmapped-model" {
					r.RequestedModel = "unknown"
				} else {
					r.Model = "other-model"
				}
			}
			if !invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r).Terminate {
				t.Fatal("wildcard bypassed identity guard")
			}
		})
	}
}

func TestWildcardProviderGrantPreservesQuotaBudget(t *testing.T) {
	path, r := policyFixture(t)
	grantAllProviders(t, path)
	result := invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r)
	if !result.Terminate || result.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("wildcard bypassed zero quota budget: %#v", result)
	}
	if err := withLedger(path, false, func(l *Ledger) error {
		if len(l.Debits) != 0 || l.Users["u"].SpentUSD != "0" {
			t.Fatal("denied quota request changed spending")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWildcardProviderGrantAllowsRealtimeAndDelegatedIssuer(t *testing.T) {
	path, _ := identityFixture(t)
	grantAllProviders(t, path)
	if !identityAuthenticate(t, http.Header{"Authorization": {"Bearer test-secret"}}, "/v1/realtime").Authenticated {
		t.Fatal("wildcard rejected Codex realtime")
	}
	raw, err := json.Marshal(pluginapi.FrontendAuthRequest{Method: http.MethodGet, Path: "/v1/realtime", VerifiedPrincipal: principal(KeyHash("test-secret"))})
	if err != nil {
		t.Fatal(err)
	}
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
	if !envelope.Result.Authenticated {
		t.Fatal("wildcard rejected verified realtime issuer")
	}
	configState.Lock()
	configState.cfg.Models = map[string]modelPolicy{"alias": {Provider: "codex", UpstreamModel: "m"}}
	configState.Unlock()
	if identityAuthenticate(t, http.Header{"Authorization": {"Bearer test-secret"}}, "/v1/realtime").Authenticated {
		t.Fatal("wildcard bypassed realtime model restriction")
	}
}
