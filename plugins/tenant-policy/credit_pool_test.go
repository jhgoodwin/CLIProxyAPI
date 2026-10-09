//go:build linux && cgo

package main

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestCreditSharesNativeBalancesAndAddedAccounts(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	old := poolNow
	poolNow = func() time.Time { return now }
	defer func() { poolNow = old }()
	l := emptyLedger()
	p := PoolDefinition{Mode: "reported", ReportedMaxAgeSeconds: 120}
	u := User{CreditShares: map[string]string{"codex": "50"}, PoolShares: map[string]string{"codex": "0"}}
	req := func(account string, balances map[string]string) pluginapi.RequestInterceptRequest {
		r := pluginapi.RequestInterceptRequest{Provider: "codex", AuthID: account}
		for id, b := range balances {
			r.ProviderPoolAuthIDs = append(r.ProviderPoolAuthIDs, id)
			r.ProviderPoolQuota = append(r.ProviderPoolQuota, pluginapi.ProviderQuotaObservation{AuthID: id, ObservedAt: now, Signals: map[string]string{"x-codex-credits-balance": b}})
		}
		return r
	}
	if err := reserveCreditPool(l, p, req("a", map[string]string{"a": "62500"}), "friend", u); err != nil {
		t.Fatal(err)
	}
	if l.CreditPools["codex"].Accounts["a"].Capacity != "62500" || len(l.Debits) != 0 {
		t.Fatal("native credits converted or dollar debit created")
	}
	now = now.Add(time.Second)
	if err := reserveCreditPool(l, p, req("a", map[string]string{"a": "31250"}), "friend", u); err == nil {
		t.Fatal("50% grant did not block")
	}
	// Production transaction rejects mutations on failed admission, so this retry
	// observes the same delta plus a new account without losing consumption.
	if err := reserveCreditPool(l, p, req("b", map[string]string{"a": "31250", "b": "62500"}), "friend", u); err != nil {
		t.Fatal(err)
	}
	if got := l.CreditPools["codex"].Accounts["a"].Users["friend"]; got != "31250" {
		t.Fatalf("consumed %s", got)
	}
	owner := User{CreditShares: map[string]string{"codex": "100"}}
	now = now.Add(time.Second)
	if err := reserveCreditPool(l, p, req("a", map[string]string{"a": "31250", "b": "62499"}), "owner", owner); err != nil {
		t.Fatal(err)
	}
	if err := reserveCreditPool(l, p, req("a", map[string]string{"a": "31250", "b": "62499"}), "friend", u); err != nil {
		t.Fatal("concurrent attempt blocked")
	}
}

func TestCreditUnknownMalformedAndUnlimited(t *testing.T) {
	p := PoolDefinition{}
	for _, h := range []map[string]string{{}, {"x-codex-credits-balance": "-1"}, {"x-codex-credits-balance": "1e6"}, {"x-codex-credits-balance": "62500", "x-codex-credits-unlimited": "true"}, {"x-codex-credits-balance": "62500", "x-codex-credits-has-credits": "false"}} {
		if _, err := creditBalance("codex", p, h); err == nil {
			t.Fatalf("accepted %v", h)
		}
	}
}

func TestNamedCreditPoolsAccountScopeAndOverlap(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	old := poolNow
	poolNow = func() time.Time { return now }
	defer func() { poolNow = old }()
	l := emptyLedger()
	if err := setAccountAlias(l, []string{"codex_sub_1", "codex", "a"}); err != nil {
		t.Fatal(err)
	}
	if err := setAccountAlias(l, []string{"codex_main", "codex", "b"}); err != nil {
		t.Fatal(err)
	}
	if err := setAccountAlias(l, []string{"codex_sub_1", "codex", "b"}); err == nil {
		t.Fatal("rebound alias")
	}
	pools := PoolConfig{"shared": {Provider: "codex", AccountSelectors: []string{"codex_sub_*"}, Mode: "reported", ReportedMaxAgeSeconds: 120}, "overlap": {Provider: "codex", AccountSelectors: []string{"codex_sub_1"}, Mode: "reported", ReportedMaxAgeSeconds: 120}}
	u := User{CreditShares: map[string]string{"shared": "50", "overlap": "50"}}
	r := pluginapi.RequestInterceptRequest{Provider: "codex", AuthID: "a", ProviderPoolAuthIDs: []string{"a", "b"}, ProviderPoolQuota: []pluginapi.ProviderQuotaObservation{{AuthID: "a", ObservedAt: now, Signals: map[string]string{"x-codex-credits-balance": "100"}}}}
	if err := reserveResource(l, pools, r, "friend", u, "credits", "shared"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	r.ProviderPoolQuota[0].ObservedAt = now
	r.ProviderPoolQuota[0].Signals["x-codex-credits-balance"] = "50"
	if err := reserveResource(l, pools, r, "friend", u, "credits", "overlap"); err == nil {
		t.Fatal("overlapping pool reset user allowance")
	}
	r.AuthID = "b"
	if err := reserveResource(l, pools, r, "friend", u, "credits", "shared"); err == nil {
		t.Fatal("outside-pool credential admitted")
	}
}

func TestAutoOnlyFallsBackAfterRealRenewingExhaustion(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	old := poolNow
	poolNow = func() time.Time { return now }
	defer func() { poolNow = old }()
	pools := PoolConfig{"codex": {Mode: "reported", ReportedMaxAgeSeconds: 120}}
	u := User{PoolShares: map[string]string{"codex": "50"}, CreditShares: map[string]string{"codex": "50"}}
	l := emptyLedger()
	signals := map[string]string{"x-codex-primary-used-percent": "0", "x-codex-primary-reset-at": "1791471600", "x-codex-credits-balance": "100"}
	// Use clock-relative endpoint to keep deterministic fixture readable.
	signals["x-codex-primary-reset-at"] = now.Add(time.Hour).Format(time.RFC3339)
	r := pluginapi.RequestInterceptRequest{Provider: "codex", AuthID: "a", ProviderPoolAuthIDs: []string{"a"}, ProviderPoolQuota: []pluginapi.ProviderQuotaObservation{{AuthID: "a", ObservedAt: now, Signals: signals}}}
	if err := reserveResource(l, pools, r, "friend", u, "auto", ""); err != nil {
		t.Fatal(err)
	}
	// Explicit credits do not wait on renewing observations.
	if err := reserveResource(l, pools, r, "friend", u, "credits", ""); err != nil {
		t.Fatal("cross-resource concurrent attempt blocked")
	}
	now = now.Add(time.Second)
	r.ProviderPoolQuota[0].ObservedAt = now
	signals["x-codex-primary-used-percent"] = "50"
	if err := reserveResource(l, pools, r, "friend", u, "auto", ""); err == nil {
		t.Fatal("user renewing share exhaustion bypassed using credits")
	}
	signals["x-codex-primary-used-percent"] = "100"
	if err := reserveResource(l, pools, r, "friend", u, "auto", ""); err != nil {
		t.Fatal(err)
	}
	if got := l.ReportedPools["codex"].Accounts["a"].Windows["x-codex-primary"].Users["friend"]; got != quotaScale {
		t.Fatalf("renewing consumption lost: %d", got)
	}
	if l.CreditPools["codex"].Accounts["a"].Attempts["friend"] == 0 {
		t.Fatal("no credit admission")
	}
}

func TestNamedPoolsPermitOverprovisionedIndependentGrants(t *testing.T) {
	for _, resource := range []string{"renewing", "credits"} {
		t.Run(resource, func(t *testing.T) {
			now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
			old := poolNow
			poolNow = func() time.Time { return now }
			defer func() { poolNow = old }()
			l := emptyLedger()
			if err := setAccountAlias(l, []string{"codex_sub_1", "codex", "a"}); err != nil {
				t.Fatal(err)
			}
			pools := PoolConfig{"shared": {Provider: "codex", AccountSelectors: []string{"codex_sub_1"}, Mode: "reported", ReportedMaxAgeSeconds: 120}}
			u := User{PoolShares: map[string]string{"shared": "100"}, CreditShares: map[string]string{"shared": "100"}}
			reset := now.Add(time.Hour).Format(time.RFC3339)
			users := []string{"owner", "friend1", "friend2"}
			percentages := []string{"0", "1", "2"}
			balances := []string{"100", "99", "98"}
			// Three independent 100% grants deliberately total 300%, without reserving
			// provider capacity for users who are not consuming it.
			for i, user := range users {
				now = now.Add(time.Second)
				r := pluginapi.RequestInterceptRequest{Provider: "codex", AuthID: "a", ProviderPoolAuthIDs: []string{"a"}, ProviderPoolQuota: []pluginapi.ProviderQuotaObservation{{AuthID: "a", ObservedAt: now, Signals: map[string]string{"x-codex-primary-used-percent": percentages[i], "x-codex-primary-reset-at": reset, "x-codex-credits-balance": balances[i]}}}}
				if err := reserveResource(l, pools, r, user, u, resource, "shared"); err != nil {
					t.Fatalf("grant %d rejected: %v", i+1, err)
				}
			}
		})
	}
}
