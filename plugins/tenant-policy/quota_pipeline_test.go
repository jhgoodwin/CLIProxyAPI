//go:build linux && cgo

package main

import (
	"errors"
	"math/big"
	"reflect"
	"testing"
	"time"
)

func TestCreditReconciliationOwnsStateAcrossTransitions(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, tc := range []struct {
		name, balance, capacity, alice, bob string
		first                               bool
		attempts                            map[string]int64
	}{
		{name: "first", balance: "62500", capacity: "62500", alice: "4", first: true, attempts: map[string]int64{}},
		{name: "decrease", balance: "70", capacity: "100", alice: "24", bob: "10", attempts: map[string]int64{}},
		{name: "unchanged", balance: "100", capacity: "100", alice: "4", attempts: map[string]int64{"alice": 2, "bob": 1}},
		{name: "increase", balance: "125", capacity: "125", alice: "4", attempts: map[string]int64{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prior := CreditAccountState{Capacity: "100", Remaining: "100", ObservedAt: now.Add(-time.Minute), Users: map[string]string{"alice": "4"}, Attempts: map[string]int64{"alice": 2, "bob": 1}}
			if tc.first {
				prior.Capacity = ""
			}
			before := CreditAccountState{Capacity: prior.Capacity, Remaining: prior.Remaining, ObservedAt: prior.ObservedAt, Users: map[string]string{"alice": "4"}, Attempts: map[string]int64{"alice": 2, "bob": 1}}
			balance, _ := new(big.Rat).SetString(tc.balance)
			balanceBefore := new(big.Rat).Set(balance)
			next, err := reconcileCreditAccount(prior, balance, now)
			if err != nil {
				t.Fatal(err)
			}
			if next.Capacity != tc.capacity || next.Remaining != tc.balance || next.Users["alice"] != tc.alice || next.Users["bob"] != tc.bob || !reflect.DeepEqual(next.Attempts, tc.attempts) || next.PendingUser != "" || !next.ObservedAt.Equal(now) {
				t.Fatalf("bad transition: %#v", next)
			}
			if !reflect.DeepEqual(prior, before) || balance.Cmp(balanceBefore) != 0 {
				t.Fatal("transition changed inputs")
			}
			next.Users["alice"] = "999"
			next.Attempts["alice"] = 999
			balance.SetInt64(999)
			if !reflect.DeepEqual(prior, before) || next.Remaining != tc.balance {
				t.Fatal("returned state aliases input")
			}
		})
	}
}

func TestCreditReconciliationErrorsDoNotMutateInputs(t *testing.T) {
	for _, kind := range []string{"remaining", "capacity", "attempts", "consumption"} {
		t.Run(kind, func(t *testing.T) {
			prior := CreditAccountState{Capacity: "100", Remaining: "100", Users: map[string]string{"alice": "4"}, Attempts: map[string]int64{"alice": 1}}
			switch kind {
			case "remaining":
				prior.Remaining = "broken"
			case "capacity":
				prior.Capacity = "broken"
			case "attempts":
				prior.Attempts["alice"] = -1
			case "consumption":
				prior.Users["alice"] = "broken"
			}
			l := emptyLedger()
			l.CreditPools["codex"] = CreditPoolState{Accounts: map[string]CreditAccountState{"a": prior}}
			before := cloneLedger(l).CreditPools["codex"].Accounts["a"]
			balance := big.NewRat(70, 1)
			if _, err := reconcileCreditAccount(prior, balance, time.Unix(1000, 0)); err == nil {
				t.Fatal("invalid state accepted")
			}
			if !reflect.DeepEqual(prior, before) || balance.Cmp(big.NewRat(70, 1)) != 0 {
				t.Fatal("error mutated input")
			}
		})
	}
}

func TestCreditReconciliationMigratesLegacyAttribution(t *testing.T) {
	prior := CreditAccountState{Capacity: "100", Remaining: "100", PendingUser: "alice", Users: map[string]string{}}
	next, err := reconcileCreditAccount(prior, big.NewRat(100, 1), time.Unix(1000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if next.PendingUser != "" || next.Attempts["alice"] != 1 || prior.Attempts != nil || prior.PendingUser != "alice" {
		t.Fatalf("bad legacy transition: %#v", next)
	}
}

func TestReportedSnapshotUsesCurrentCapacityAndHistoricalConsumption(t *testing.T) {
	l, _, r, _, now := reportedFixture(t)
	r.ProviderPoolQuota = nil
	r.ProviderPoolAuthIDs = []string{"a", "unknown"}
	p := PoolDefinition{Provider: "codex", name: "shared", AccountWeights: map[string]string{"a": "2"}}
	l.PoolMembers["shared"] = map[string]bool{"a": true, "removed": true}
	u := User{PoolShares: map[string]string{"shared": "50"}}
	window := func(used int64) ReportedWindowState {
		return ReportedWindowState{Capacity: 2 * quotaScale, Remaining: quotaScale, ResetAt: 2000, Users: map[string]int64{"alice": used}}
	}
	l.ReportedPools["codex"] = ReportedPoolState{Accounts: map[string]ReportedAccountState{
		"a":       {Windows: map[string]ReportedWindowState{"primary": window(500000), "weekly": window(400000)}},
		"removed": {Windows: map[string]ReportedWindowState{"primary": window(1000000), "weekly": window(1000000)}},
	}}
	before := cloneLedger(l)
	snapshot, err := summarizeReportedQuota(l, p, r, "alice", *now)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.capacity != 3*quotaScale || len(snapshot.windows) != 2 || snapshot.windows[0].consumed != 1500000 || snapshot.windows[1].consumed != 1400000 {
		t.Fatalf("wrong snapshot: %#v", snapshot)
	}
	if !reflect.DeepEqual(l, before) {
		t.Fatal("summarizing mutated ledger")
	}
	var exhausted poolExhaustedError
	if err := reserveReportedPool(l, p, r, "alice", u); !errors.As(err, &exhausted) {
		t.Fatalf("historical consumption not enforced: %v", err)
	}
	*now = 2000
	if err := reserveReportedPool(l, p, r, "alice", u); err != nil {
		t.Fatal("expired historical consumption must not block:", err)
	}
}

func TestReportedAdmissionEvaluatesEveryWindowAndMarksOnce(t *testing.T) {
	l, pools, r, u, _ := reportedFixture(t)
	r.ProviderPoolQuota = nil
	l.ReportedPools["codex"] = ReportedPoolState{Accounts: map[string]ReportedAccountState{"a": {Windows: map[string]ReportedWindowState{
		"primary": {Capacity: quotaScale, Remaining: quotaScale, ResetAt: 2000, Users: map[string]int64{"alice": 100000}},
		"weekly":  {Capacity: quotaScale, Remaining: quotaScale, ResetAt: 2000, Users: map[string]int64{"alice": 800000}},
	}}}}
	if err := reserveReportedPool(l, pools["codex"], r, "alice", u); err == nil {
		t.Fatal("second window's limit ignored")
	}
	if l.CreditPools["codex"].Accounts["a"].Attempts["alice"] != 0 {
		t.Fatal("denial marked credit attempt")
	}
	state := l.ReportedPools["codex"]
	account := state.Accounts["a"]
	w := account.Windows["weekly"]
	w.Users["alice"] = 700000
	account.Windows["weekly"] = w
	if err := reserveReportedPool(l, pools["codex"], r, "alice", u); err != nil {
		t.Fatal(err)
	}
	for name, w := range l.ReportedPools["codex"].Accounts["a"].Windows {
		if w.Attempts["alice"] != 1 {
			t.Fatalf("window %s marked %d times", name, w.Attempts["alice"])
		}
	}
	if l.CreditPools["codex"].Accounts["a"].Attempts["alice"] != 1 {
		t.Fatal("credit attempt not marked exactly once")
	}
}

func TestQuotaResetSelectionAndValidationAreAtomic(t *testing.T) {
	now := int64(1000)
	poolClock(t, &now)
	for _, resource := range []string{"credits", "renewing"} {
		t.Run(resource, func(t *testing.T) {
			for _, selection := range []string{"sub_a", "auth-a", ""} {
				t.Run(selection, func(t *testing.T) {
					l := emptyLedger()
					l.NamedPools["shared"] = NamedPoolSnapshot{Provider: "codex", AccountSelectors: []string{"sub_*"}}
					l.Accounts["sub_a"] = AccountAlias{Provider: "codex", AuthID: "auth-a"}
					l.Accounts["sub_b"] = AccountAlias{Provider: "codex", AuthID: "auth-b"}
					l.Accounts["private"] = AccountAlias{Provider: "codex", AuthID: "auth-private"}
					l.CreditPools["codex"] = CreditPoolState{Accounts: map[string]CreditAccountState{
						"auth-a":       {ObservedAt: time.Unix(now, 0), Capacity: "100", Remaining: "60", Users: map[string]string{"alice": "40"}, Attempts: map[string]int64{"alice": 2}},
						"auth-b":       {ObservedAt: time.Unix(now, 0), Capacity: "invalid", Remaining: "70", Users: map[string]string{"bob": "30"}},
						"auth-private": {ObservedAt: time.Unix(now, 0), Capacity: "invalid", Remaining: "90", Users: map[string]string{}},
					}}
					window := func(reset int64) map[string]ReportedWindowState {
						return map[string]ReportedWindowState{"weekly": {ResetAt: reset, Capacity: quotaScale, Remaining: 600000, Users: map[string]int64{"alice": 400000}, Attempts: map[string]int64{"alice": 2}}}
					}
					l.ReportedPools["codex"] = ReportedPoolState{Accounts: map[string]ReportedAccountState{
						"auth-a":       {ObservedAt: time.Unix(now, 0), Windows: window(2000)},
						"auth-b":       {ObservedAt: time.Unix(now, 0), Windows: window(900)},
						"auth-private": {ObservedAt: time.Unix(now, 0), Windows: window(900)},
					}}
					before := cloneLedger(l)
					args := []string{resource, "shared", "operator reset"}
					if selection != "" {
						args = append(args, selection)
					}
					err := resetQuota(l, args)
					if selection == "" {
						if err == nil {
							t.Fatal("invalid selected account accepted")
						}
						if !reflect.DeepEqual(l, before) {
							t.Fatal("failed reset changed state or audit")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if len(l.ResetAudit) != 1 || l.ResetAudit[0].AuthID != "auth-a" || l.ResetAudit[0].Pool != "shared" {
						t.Fatalf("wrong audit: %#v", l.ResetAudit)
					}
					if !reflect.DeepEqual(l.CreditPools["codex"].Accounts["auth-b"], before.CreditPools["codex"].Accounts["auth-b"]) || !reflect.DeepEqual(l.ReportedPools["codex"].Accounts["auth-private"], before.ReportedPools["codex"].Accounts["auth-private"]) {
						t.Fatal("unselected state changed")
					}
					if resource == "credits" {
						a := l.CreditPools["codex"].Accounts["auth-a"]
						if a.Remaining != "60" || a.Capacity != "60" || len(a.Users) != 0 || len(a.Attempts) != 0 {
							t.Fatalf("bad reset: %#v", a)
						}
					} else {
						w := l.ReportedPools["codex"].Accounts["auth-a"].Windows["weekly"]
						if w.Remaining != 600000 || w.Capacity != quotaScale || w.ResetAt != 2000 || len(w.Users) != 0 || len(w.Attempts) != 0 {
							t.Fatalf("bad reset: %#v", w)
						}
					}
				})
			}
		})
	}
}

func TestQuotaResetRefreshRollsBackWithInvalidSelectedRecord(t *testing.T) {
	now := int64(1000)
	poolClock(t, &now)
	l := emptyLedger()
	l.CreditPools["codex"] = CreditPoolState{Accounts: map[string]CreditAccountState{
		"a": {ObservedAt: time.Unix(900, 0), Capacity: "100", Remaining: "60", Users: map[string]string{"alice": "40"}},
		"b": {ObservedAt: time.Unix(900, 0), Capacity: "invalid", Remaining: "70", Users: map[string]string{}},
	}}
	l.ReportedPools["codex"] = ReportedPoolState{Seeds: map[string]ReportedQuotaSeed{"a": {ObservedAt: time.Unix(now, 0), Signals: map[string]string{"x-codex-credits-balance": "80"}}}}
	before := cloneLedger(l)
	if err := resetQuota(l, []string{"credits", "codex", "reset"}); err == nil {
		t.Fatal("invalid record accepted")
	}
	if !reflect.DeepEqual(l, before) {
		t.Fatal("failed reset published refreshed balance or audit")
	}
}

func TestReportedInventoryErrorsRemainInternal(t *testing.T) {
	l, pools, r, u, _ := reportedFixture(t)
	r.ProviderPoolQuota = nil
	for _, ids := range [][]string{{"a", "a"}, {""}, {"b"}} {
		r.ProviderPoolAuthIDs = ids
		err := reserveReportedPool(l, pools["codex"], r, "alice", u)
		var exhausted poolExhaustedError
		if err == nil || errors.As(err, &exhausted) {
			t.Fatalf("inventory error became policy denial: %v", err)
		}
	}
}
