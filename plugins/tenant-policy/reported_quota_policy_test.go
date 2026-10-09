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

func TestReportedSchedulerAdmitsWithoutWaitingForObservations(t *testing.T) {
	now := int64(1000)
	poolClock(t, &now)
	path, r := policyFixture(t)
	if err := withLedger(path, true, func(l *Ledger) error {
		u := l.Users["u"]
		u.PoolShares = map[string]string{"p": "100"}
		l.Users["u"] = u
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	configState.Lock()
	configState.cfg.Pools = PoolConfig{"p": {Mode: "reported", ReportedMaxAgeSeconds: 60}}
	configState.cfg.Models["alias"] = modelPolicy{Provider: "p", UpstreamModel: "m", PoolOnly: true}
	configState.Unlock()
	observations := []pluginapi.ProviderQuotaObservation{}
	for _, id := range []string{"a", "b"} {
		observations = append(observations, pluginapi.ProviderQuotaObservation{AuthID: id, ObservedAt: time.Unix(now, 0), Signals: map[string]string{"x-ratelimit-limit-requests": "100", "x-ratelimit-remaining-requests": "100", "x-ratelimit-reset-requests": "20m"}})
	}
	r.AuthID = "a"
	r.ProviderPoolAuthIDs = []string{"a", "b"}
	r.ProviderPoolQuota = observations
	if got := invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r); got.Terminate {
		t.Fatalf("first account denied: %s", got.ResponseBody)
	}
	request := pluginapi.SchedulerPickRequest{Provider: "p", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "a", Provider: "p"}, {ID: "b", Provider: "p"}}, ProviderPools: map[string][]pluginapi.ProviderQuotaObservation{"p": observations}, Options: pluginapi.SchedulerOptions{Metadata: map[string]any{"caller_scope": session.CallerScope(r.Principal)}}}
	b, _ := json.Marshal(request)
	raw, err := handleMethod(pluginabi.MethodSchedulerPick, b)
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
	if picked.Reject || picked.AuthID != "a" {
		t.Fatalf("selected unavailable account: %#v", picked)
	}
	if err := withLedger(path, false, func(l *Ledger) error {
		if l.ReportedPools["p"].Accounts["b"].PendingUser != "" {
			t.Fatal("scheduler consumed quota")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.AuthID = "b"
	r.AttemptID = "second-attempt"
	if got := invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r); got.Terminate {
		t.Fatalf("second account denied: %s", got.ResponseBody)
	}
	raw, err = handleMethod(pluginabi.MethodSchedulerPick, b)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK {
		t.Fatalf("outstanding attempts must not block scheduler: %#v", envelope)
	}
}
