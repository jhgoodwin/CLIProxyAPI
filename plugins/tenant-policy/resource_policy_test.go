//go:build linux && cgo

package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestNamedCreditSchedulerUsesRequestedAliasAndFinalAdmission(t *testing.T) {
	now := int64(1000)
	poolClock(t, &now)
	path, r := policyFixture(t)
	if err := withLedger(path, true, func(l *Ledger) error {
		u := l.Users["u"]
		u.CreditShares = map[string]string{"shared": "50"}
		l.Users["u"] = u
		return setAccountAlias(l, []string{"sub_1", "p", "b"})
	}); err != nil {
		t.Fatal(err)
	}
	configState.Lock()
	configState.cfg.Pools = PoolConfig{"shared": {Provider: "p", AccountSelectors: []string{"sub_*"}, Mode: "reported", ReportedMaxAgeSeconds: 60, CreditBalanceHeader: "balance"}}
	configState.cfg.Models["alias"] = modelPolicy{Provider: "p", UpstreamModel: "m", Pool: "shared", PoolOnly: true}
	configState.cfg.Models["credit_alias"] = modelPolicy{Provider: "p", UpstreamModel: "m", Pool: "shared", QuotaResource: "credits", PoolOnly: true}
	configState.Unlock()
	observations := []pluginapi.ProviderQuotaObservation{{AuthID: "b", ObservedAt: time.Unix(now, 0), Signals: map[string]string{"balance": "62500"}}, {AuthID: "a", ObservedAt: time.Unix(now, 0), Signals: map[string]string{"balance": "62500"}}}
	req := pluginapi.SchedulerPickRequest{Provider: "p", Model: "m", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "a", Provider: "p"}, {ID: "b", Provider: "p"}}, ProviderPools: map[string][]pluginapi.ProviderQuotaObservation{"p": observations}, Options: pluginapi.SchedulerOptions{Metadata: map[string]any{"caller_scope": session.CallerScope(r.Principal), "requested_model": "credit_alias"}}}
	data, _ := json.Marshal(req)
	raw, err := handleMethod(pluginabi.MethodSchedulerPick, data)
	if err != nil {
		t.Fatal(err)
	}
	var envelope pluginabi.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	var picked pluginapi.SchedulerPickResponse
	if err := json.Unmarshal(envelope.Result, &picked); err != nil {
		t.Fatal(err)
	}
	if picked.AuthID != "b" || picked.Reject {
		t.Fatalf("wrong alias or account selected: %s", raw)
	}
	if err := withLedger(path, false, func(l *Ledger) error {
		for _, pool := range l.CreditPools {
			for _, account := range pool.Accounts {
				if len(account.Attempts) != 0 || len(account.Users) != 0 {
					t.Fatal("scheduler consumed credits")
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.RequestedModel = "credit_alias"
	r.AuthID = "b"
	r.ProviderPoolAuthIDs = []string{"a", "b"}
	r.ProviderPoolQuota = observations
	if got := invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r); got.Terminate {
		t.Fatalf("final credits admission denied: %s", got.ResponseBody)
	}
}
