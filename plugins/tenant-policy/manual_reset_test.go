//go:build linux && cgo

package main

import (
	"reflect"
	"testing"
	"time"
)

func TestResetQuotaCreditsSelectedAccount(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	oldNow := poolNow
	poolNow = func() time.Time { return now }
	t.Cleanup(func() { poolNow = oldNow })

	l := emptyLedger()
	l.CreditPools["codex"] = CreditPoolState{Accounts: map[string]CreditAccountState{
		"a": {ObservedAt: now.Add(-time.Hour), Capacity: "100", Remaining: "60", PendingUser: "alice", Users: map[string]string{"alice": "40"}},
		"b": {ObservedAt: now.Add(-time.Hour), Capacity: "200", Remaining: "150", PendingUser: "bob", Users: map[string]string{"bob": "50"}},
	}}
	if err := resetQuota(l, []string{"credits", "codex", "monthly credit reset", "a"}); err != nil {
		t.Fatal(err)
	}
	a, b := l.CreditPools["codex"].Accounts["a"], l.CreditPools["codex"].Accounts["b"]
	if a.Remaining != "60" || a.Capacity != "60" || a.PendingUser != "" || len(a.Users) != 0 {
		t.Fatalf("selected credit account was not reset correctly: %#v", a)
	}
	if b.PendingUser != "bob" || b.Users["bob"] != "50" || len(l.ResetAudit) != 1 || l.ResetAudit[0].AuthID != "a" {
		t.Fatalf("unselected account or audit changed incorrectly: account=%#v audit=%#v", b, l.ResetAudit)
	}
}

func TestResetQuotaRenewingRetainsObservedWindow(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	oldNow := poolNow
	poolNow = func() time.Time { return now }
	t.Cleanup(func() { poolNow = oldNow })

	l := emptyLedger()
	l.ReportedPools["claude"] = ReportedPoolState{Accounts: map[string]ReportedAccountState{
		"a": {ObservedAt: now.Add(-time.Minute), PendingUser: "alice", Windows: map[string]ReportedWindowState{
			"weekly": {ResetAt: now.Add(time.Hour).Unix(), Capacity: 100, Remaining: 65, Users: map[string]int64{"alice": 35}},
		}},
	}}
	if err := resetQuota(l, []string{"renewing", "claude", "operator reset"}); err != nil {
		t.Fatal(err)
	}
	account := l.ReportedPools["claude"].Accounts["a"]
	window := account.Windows["weekly"]
	if account.PendingUser != "" || len(window.Users) != 0 || window.Remaining != 65 || window.Capacity != 100 || window.ResetAt != now.Add(time.Hour).Unix() {
		t.Fatalf("renewing quota reset changed provider observation: account=%#v window=%#v", account, window)
	}
}

func TestResetQuotaCreditsObserveAliasEstablishesBaseline(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	oldNow := poolNow
	poolNow = func() time.Time { return now }
	t.Cleanup(func() { poolNow = oldNow })

	l := emptyLedger()
	if err := setAccountAlias(l, []string{"main", "codex", "auth-1"}); err != nil {
		t.Fatal(err)
	}
	if err := observeCredits(l, []string{"codex", "auth-1", now.Format(time.RFC3339), "62500"}); err != nil {
		t.Fatal(err)
	}
	if err := resetQuota(l, []string{"credits", "codex", "manual balance baseline", "main"}); err != nil {
		t.Fatal(err)
	}
	a := l.CreditPools["codex"].Accounts["auth-1"]
	if !a.ObservedAt.Equal(now) || a.Capacity != "62500" || a.Remaining != "62500" || a.PendingUser != "" || len(a.Users) != 0 {
		t.Fatalf("latest credits observation was not established as baseline: %#v", a)
	}
	if len(l.ResetAudit) != 1 || l.ResetAudit[0].AuthID != "auth-1" {
		t.Fatalf("reset audit did not resolve account alias: %#v", l.ResetAudit)
	}
}

func TestResetQuotaCreditsNamedPoolSelectsOnlyCurrentAliases(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	oldNow := poolNow
	poolNow = func() time.Time { return now }
	t.Cleanup(func() { poolNow = oldNow })

	l := emptyLedger()
	l.NamedPools["shared"] = NamedPoolSnapshot{Provider: "codex", AccountSelectors: []string{"codex_sub_*"}}
	for name, authID := range map[string]string{"codex_sub_a": "auth-a", "codex_sub_b": "auth-b", "codex_private_c": "auth-c"} {
		if err := setAccountAlias(l, []string{name, "codex", authID}); err != nil {
			t.Fatal(err)
		}
	}
	l.CreditPools["codex"] = CreditPoolState{Accounts: map[string]CreditAccountState{
		"auth-a": {ObservedAt: now.Add(-time.Hour), Capacity: "100", Remaining: "70", PendingUser: "alice", Users: map[string]string{"alice": "30"}},
		"auth-b": {ObservedAt: now.Add(-time.Hour), Capacity: "200", Remaining: "120", PendingUser: "bob", Users: map[string]string{"bob": "80"}},
		"auth-c": {ObservedAt: now.Add(-time.Hour), Capacity: "300", Remaining: "250", PendingUser: "carol", Users: map[string]string{"carol": "50"}},
	}}
	if err := resetQuota(l, []string{"credits", "shared", "shared pool reset"}); err != nil {
		t.Fatal(err)
	}
	accounts := l.CreditPools["codex"].Accounts
	for id, wantRemaining := range map[string]string{"auth-a": "70", "auth-b": "120"} {
		a := accounts[id]
		if a.Capacity != wantRemaining || a.Remaining != wantRemaining || a.PendingUser != "" || len(a.Users) != 0 {
			t.Errorf("shared account %s was not reset to its observed balance: %#v", id, a)
		}
	}
	private := accounts["auth-c"]
	if private.Capacity != "300" || private.Remaining != "250" || private.PendingUser != "carol" || private.Users["carol"] != "50" {
		t.Fatalf("private account was changed by shared pool reset: %#v", private)
	}
	if len(l.ResetAudit) != 1 || l.ResetAudit[0].Pool != "shared" || l.ResetAudit[0].Provider != "codex" {
		t.Fatalf("named pool reset audit is incorrect: %#v", l.ResetAudit)
	}

	before := cloneLedger(l)
	if err := resetQuota(l, []string{"credits", "shared", "private reset", "codex_private_c"}); err == nil {
		t.Fatal("expected reset of account outside named pool to fail")
	}
	if !reflect.DeepEqual(l.CreditPools, before.CreditPools) || !reflect.DeepEqual(l.ResetAudit, before.ResetAudit) {
		t.Fatal("rejected outside-pool reset partially changed quota or audit")
	}
}

func TestResetQuotaRenewingUsesLatestSeedAndPreservesAccounting(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	oldNow := poolNow
	poolNow = func() time.Time { return now }
	t.Cleanup(func() { poolNow = oldNow })

	l := emptyLedger()
	if err := setAccountAlias(l, []string{"main", "codex", "auth-1"}); err != nil {
		t.Fatal(err)
	}
	resetAt := now.Add(3 * time.Hour).Unix()
	l.ReportedPools["codex"] = ReportedPoolState{
		Accounts: map[string]ReportedAccountState{"auth-1": {
			ObservedAt: now.Add(-time.Hour), PendingUser: "alice",
			Windows: map[string]ReportedWindowState{"x-codex-primary": {
				ResetAt: now.Add(time.Hour).Unix(), Capacity: 100, Remaining: 65, Users: map[string]int64{"alice": 35},
			}},
		}},
		Seeds: map[string]ReportedQuotaSeed{"auth-1": {
			ObservedAt: now,
			Signals: map[string]string{
				"x-codex-primary-used-percent": "20",
				"x-codex-primary-reset-at":     time.Unix(resetAt, 0).Format(time.RFC3339),
			},
		}},
	}
	l.Users["alice"] = User{LimitUSD: "20", SpentUSD: "3.5"}
	l.Keys["key-hash"] = Key{User: "alice", Revoked: false}
	l.Debits["debit-1"] = Debit{User: "alice", AmountUSD: "1.25", Provider: "codex"}
	l.Metrics["metric-1"] = UsageMetric{RecordID: "metric-1", User: "alice", Provider: "codex"}
	usersBefore, keysBefore := cloneLedger(l).Users, cloneLedger(l).Keys
	debitsBefore, metricsBefore := cloneLedger(l).Debits, cloneLedger(l).Metrics

	if err := resetQuota(l, []string{"renewing", "codex", "manual early reset", "main"}); err != nil {
		t.Fatal(err)
	}
	a := l.ReportedPools["codex"].Accounts["auth-1"]
	w := a.Windows["x-codex-primary"]
	if a.ObservedAt != now || a.PendingUser != "" || w.Remaining != 80 || w.Capacity != 100 || w.ResetAt != resetAt || len(w.Users) != 0 {
		t.Fatalf("latest renewing observation was not retained as reset baseline: account=%#v window=%#v", a, w)
	}
	if !reflect.DeepEqual(l.Users, usersBefore) || !reflect.DeepEqual(l.Keys, keysBefore) || !reflect.DeepEqual(l.Debits, debitsBefore) || !reflect.DeepEqual(l.Metrics, metricsBefore) {
		t.Fatal("quota reset modified USD accounting, keys, debits, or metrics")
	}
}

func TestResetQuotaRejectsUnknownAndStaleWithoutMutation(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	oldNow := poolNow
	poolNow = func() time.Time { return now }
	t.Cleanup(func() { poolNow = oldNow })

	l := emptyLedger()
	l.CreditPools["codex"] = CreditPoolState{Accounts: map[string]CreditAccountState{
		"stale": {ObservedAt: now.Add(-25 * time.Hour), Capacity: "100", Remaining: "40", PendingUser: "alice", Users: map[string]string{"alice": "60"}},
	}}
	if err := resetQuota(l, []string{"credits", "missing", "reason"}); err == nil {
		t.Fatal("expected unknown provider error")
	}
	if err := resetQuota(l, []string{"credits", "codex", "reason"}); err == nil {
		t.Fatal("expected stale observation error")
	}
	a := l.CreditPools["codex"].Accounts["stale"]
	if a.PendingUser != "alice" || a.Users["alice"] != "60" || len(l.ResetAudit) != 0 {
		t.Fatalf("failed reset partially mutated state: account=%#v audit=%#v", a, l.ResetAudit)
	}
}
