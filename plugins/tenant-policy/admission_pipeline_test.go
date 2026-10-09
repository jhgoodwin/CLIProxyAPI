//go:build linux && cgo

package main

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestAdmissionUSDDenialPublishesFactsOnly(t *testing.T) {
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
	configState.Unlock()
	r.AuthID = "account"
	r.ProviderPoolAuthIDs = []string{"account"}
	r.ProviderPoolQuota = []pluginapi.ProviderQuotaObservation{{AuthID: "account", ObservedAt: time.Unix(now, 0), Signals: map[string]string{"x-ratelimit-limit-requests": "100", "x-ratelimit-remaining-requests": "100", "x-ratelimit-reset-requests": "2000"}}}
	if got := invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r); got.Terminate {
		t.Fatal("baseline denied")
	}
	if err := withLedger(path, true, func(l *Ledger) error { u := l.Users["u"]; u.LimitUSD = "0.1"; l.Users["u"] = u; return nil }); err != nil {
		t.Fatal(err)
	}
	now++
	r.AttemptID = "denied"
	r.ProviderPoolQuota[0].ObservedAt = time.Unix(now, 0)
	r.ProviderPoolQuota[0].Signals["x-ratelimit-remaining-requests"] = "90"
	if got := invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r); !got.Terminate || got.StatusCode != 429 {
		t.Fatalf("expected USD denial: %#v", got)
	}
	if err := withLedger(path, false, func(l *Ledger) error {
		w := l.ReportedPools["p"].Accounts["account"].Windows["ratelimit-requests"]
		if w.Remaining != 900000 || w.Users["u"] != 100000 || len(w.Attempts) != 0 {
			t.Fatalf("facts or trial spending incorrect: %#v", w)
		}
		if l.Users["u"].SpentUSD != "0.1" || len(l.Debits) != 1 {
			t.Fatalf("denial spent: %#v", l)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAdmissionInternalErrorRollsBackFacts(t *testing.T) {
	now := int64(1000)
	poolClock(t, &now)
	path, r := policyFixture(t)
	if err := withLedger(path, true, func(l *Ledger) error {
		u := l.Users["u"]
		u.PoolShares = map[string]string{"p": "100"}
		u.SpentUSD = "invalid"
		l.Users["u"] = u
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	configState.Lock()
	configState.cfg.Pools = PoolConfig{"p": {Mode: "reported", ReportedMaxAgeSeconds: 60}}
	configState.Unlock()
	r.AuthID = "a"
	r.ProviderPoolAuthIDs = []string{"a"}
	r.ProviderPoolQuota = []pluginapi.ProviderQuotaObservation{{AuthID: "a", ObservedAt: time.Unix(now, 0), Signals: map[string]string{"x-ratelimit-limit-requests": "100", "x-ratelimit-remaining-requests": "100", "x-ratelimit-reset-requests": "2000"}}}
	if got := invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r); !got.Terminate {
		t.Fatal("invalid internal state allowed")
	}
	if err := withLedger(path, false, func(l *Ledger) error {
		if len(l.ReportedPools) != 0 || len(l.Debits) != 0 {
			t.Fatal("internal error published facts or debit")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestResourceFallbackStartsFromSamePreparedFacts(t *testing.T) {
	l, pools, r, u, _ := reportedFixture(t)
	if err := setAccountAlias(l, []string{"sub_1", "codex", "a"}); err != nil {
		t.Fatal(err)
	}
	p := pools["codex"]
	p.Provider = "codex"
	p.AccountSelectors = []string{"sub_1"}
	pools = PoolConfig{"shared": p}
	u.PoolShares = map[string]string{"shared": "100"}
	u.CreditShares = map[string]string{"shared": "100"}
	r.ProviderPoolQuota[0].Signals["x-codex-primary-used-percent"] = "100"
	r.ProviderPoolQuota[0].Signals["x-codex-credits-balance"] = "0"
	before := cloneLedger(l)
	if err := reserveResource(l, pools, r, "user", u, "auto", "shared"); err == nil {
		t.Fatal("exhausted resources admitted")
	}
	if !reflect.DeepEqual(l, before) {
		t.Fatal("failed fallback mutated original ledger")
	}
	r.ProviderPoolQuota[0].Signals["x-codex-credits-balance"] = "100"
	if err := reserveResource(l, pools, r, "user", u, "auto", "shared"); err != nil {
		t.Fatal(err)
	}
	if !l.PoolMembers["shared"]["a"] {
		t.Fatal("fallback omitted named membership")
	}
	if l.CreditPools["codex"].Accounts["a"].Attempts["user"] != 1 || l.ReportedPools["codex"].Accounts["a"].Windows["x-codex-primary"].Attempts["user"] != 1 {
		t.Fatal("fallback counted admission more than once")
	}
}

func TestActualRenewingExhaustedDoesNotApplyObservations(t *testing.T) {
	l, _, r, _, _ := reportedFixture(t)
	r.ProviderPoolQuota[0].Signals["x-codex-primary-used-percent"] = "100"
	before := cloneLedger(l)
	if actualRenewingExhausted(l, r) {
		t.Fatal("predicate applied unprocessed observations")
	}
	if !reflect.DeepEqual(l, before) {
		t.Fatal("predicate mutated ledger")
	}
}

func TestReportedWindowTransitionOwnsNestedMaps(t *testing.T) {
	prior := ReportedWindowState{Capacity: 100, Remaining: 90, ResetAt: 2000, Users: map[string]int64{"u": 10}, Attempts: map[string]int64{"u": 1}}
	observed := ReportedWindowState{Capacity: 100, Remaining: 80, ResetAt: 3000, Users: map[string]int64{}, Attempts: map[string]int64{}}
	before, _ := json.Marshal(prior)
	next, accept, err := reconcileReportedWindow(prior, true, observed, 1001, 1001)
	if err != nil || !accept || next.Users["u"] != 20 || next.ResetAt != 2000 {
		t.Fatalf("transition: %#v %v", next, err)
	}
	next.Users["u"] = 50
	next.Attempts["u"] = 5
	after, _ := json.Marshal(prior)
	if string(before) != string(after) || len(observed.Users) != 0 || len(observed.Attempts) != 0 {
		t.Fatal("transition aliased input maps")
	}
	prior.Users["u"] = math.MaxInt64
	before, _ = json.Marshal(prior)
	if _, _, err := reconcileReportedWindow(prior, true, observed, 1001, 1001); err == nil {
		t.Fatal("overflow accepted")
	}
	after, _ = json.Marshal(prior)
	if string(before) != string(after) {
		t.Fatal("failed transition mutated input")
	}
}

func TestAdmissionAcceptedSnapshotOwnsUserMaps(t *testing.T) {
	l := emptyLedger()
	l.Users["u"] = User{LimitUSD: "1", SpentUSD: "0", PoolShares: map[string]string{"pool": "50"}, CreditShares: map[string]string{"pool": "25"}}
	r := pluginapi.RequestInterceptRequest{AttemptID: "attempt"}
	m := modelPolicy{charge: "0.1"}
	result, err := evaluateAdmission(l, nil, r, m, "u", l.Users["u"])
	if err != nil || result.Next == nil || result.Denial != nil {
		t.Fatalf("admission failed: %#v %v", result, err)
	}
	nextUser := result.Next.Users["u"]
	nextUser.PoolShares["pool"] = "100"
	nextUser.CreditShares["pool"] = "100"
	if l.Users["u"].SpentUSD != "0" || l.Users["u"].PoolShares["pool"] != "50" || l.Users["u"].CreditShares["pool"] != "25" {
		t.Fatal("accepted snapshot aliased facts user")
	}
}

func TestSchedulerInternalErrorRollsBackFacts(t *testing.T) {
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
	configState.Unlock()
	request := pluginapi.SchedulerPickRequest{
		Provider: "p", Model: "alias",
		// The second candidate is malformed internal inventory, rather than
		// provider exhaustion. Fresh facts from the first must roll back.
		Candidates:    []pluginapi.SchedulerAuthCandidate{{ID: "a", Provider: "p"}, {ID: "", Provider: "p"}},
		ProviderPools: map[string][]pluginapi.ProviderQuotaObservation{"p": {{AuthID: "a", ObservedAt: time.Unix(now, 0), Signals: map[string]string{"x-ratelimit-limit-requests": "100", "x-ratelimit-remaining-requests": "100", "x-ratelimit-reset-requests": "2000"}}}},
		Options:       pluginapi.SchedulerOptions{Metadata: map[string]any{"caller_scope": session.CallerScope(r.Principal)}},
	}
	raw, _ := json.Marshal(request)
	out, err := handleMethod(pluginabi.MethodSchedulerPick, raw)
	if err != nil {
		t.Fatal(err)
	}
	var envelope pluginabi.Envelope
	if err := json.Unmarshal(out, &envelope); err != nil {
		t.Fatal(err)
	}
	var response pluginapi.SchedulerPickResponse
	if err := json.Unmarshal(envelope.Result, &response); err != nil || !response.Reject {
		t.Fatalf("invalid inventory did not reject: %#v %v", response, err)
	}
	if err := withLedger(path, false, func(l *Ledger) error {
		if len(l.ReportedPools) != 0 || len(l.Debits) != 0 {
			t.Fatal("scheduler internal error published facts or debit")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
