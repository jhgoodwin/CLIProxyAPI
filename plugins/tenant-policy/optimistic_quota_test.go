//go:build linux && cgo

package main

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestOptimisticQuotaKnownExhaustionSurvivesStaleMissing(t *testing.T) {
	l, p, r, u, now := reportedFixture(t)
	r.ProviderPoolQuota[0].Signals["x-codex-primary-used-percent"] = "100"
	if reservePool(l, p, r, "user", u) == nil {
		t.Fatal("exhaustion admitted")
	}
	*now += 61
	r.ProviderPoolQuota = nil
	if reservePool(l, p, r, "user", u) == nil {
		t.Fatal("missing observations erased known exhaustion")
	}
	*now = 2001
	if err := reservePool(l, p, r, "user", u); err != nil {
		t.Fatal("expired quota must allow:", err)
	}
}
func TestOptimisticQuotaMixedUsersEstimatedByAttemptCounts(t *testing.T) {
	for _, resource := range []string{"renewing", "credits"} {
		t.Run(resource, func(t *testing.T) {
			l, pools, r, _, now := reportedFixture(t)
			p := pools["codex"]
			u := User{PoolShares: map[string]string{"codex": "100"}, CreditShares: map[string]string{"codex": "100"}}
			r.ProviderPoolQuota[0].Signals["x-codex-credits-balance"] = "100"
			reserve := func(user string) error {
				if resource == "credits" {
					return reserveCreditPool(l, p, r, user, u)
				}
				return reserveReportedPool(l, p, r, user, u)
			}
			for _, user := range []string{"alice", "alice", "bob"} {
				if err := reserve(user); err != nil {
					t.Fatal(err)
				}
			}
			// A rounded unchanged response leaves all three counts available.
			*now++
			r.ProviderPoolQuota[0].ObservedAt = time.Unix(*now, 0)
			if resource == "credits" {
				if err := updateCreditQuota(l, p, r); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := updateReportedQuota(l, p, r); err != nil {
					t.Fatal(err)
				}
			}
			*now++
			r.ProviderPoolQuota[0].ObservedAt = time.Unix(*now, 0)
			r.ProviderPoolQuota[0].Signals["x-codex-primary-used-percent"] = "30"
			r.ProviderPoolQuota[0].Signals["x-codex-credits-balance"] = "70"
			if err := reserve("bob"); err != nil {
				t.Fatal(err)
			}
			if resource == "credits" {
				a := l.CreditPools["codex"].Accounts["a"]
				if a.Users["alice"] != "20" || a.Users["bob"] != "10" || a.Attempts["bob"] != 1 {
					t.Fatalf("bad allocation: %#v", a)
				}
			} else {
				w := l.ReportedPools["codex"].Accounts["a"].Windows["x-codex-primary"]
				if w.Users["alice"] != 200000 || w.Users["bob"] != 100000 || w.Attempts["bob"] != 1 {
					t.Fatalf("bad allocation: %#v", w)
				}
			}
		})
	}
}
func TestOptimisticCreditsUnknownMemberAndKnownZero(t *testing.T) {
	l, pools, r, _, now := reportedFixture(t)
	p := pools["codex"]
	u := User{CreditShares: map[string]string{"codex": "50"}}
	r.ProviderPoolQuota[0].Signals = map[string]string{"x-codex-credits-balance": "100"}
	if err := reserveCreditPool(l, p, r, "alice", u); err != nil {
		t.Fatal(err)
	}
	*now++
	r.ProviderPoolQuota[0].ObservedAt = time.Unix(*now, 0)
	r.ProviderPoolQuota[0].Signals["x-codex-credits-balance"] = "50"
	if reserveCreditPool(l, p, r, "alice", u) == nil {
		t.Fatal("known share exhausted")
	}
	r.ProviderPoolAuthIDs = append(r.ProviderPoolAuthIDs, "unknown")
	if err := reserveCreditPool(l, p, r, "alice", u); err != nil {
		t.Fatal("unknown member must allow uncertain share:", err)
	}
	*now++
	r.ProviderPoolQuota[0].ObservedAt = time.Unix(*now, 0)
	r.ProviderPoolQuota[0].Signals["x-codex-credits-balance"] = "0"
	if reserveCreditPool(l, p, r, "alice", u) == nil {
		t.Fatal("known selected zero must block")
	}
	*now += 1000
	r.ProviderPoolQuota = nil
	if reserveCreditPool(l, p, r, "alice", u) == nil {
		t.Fatal("stale zero must block")
	}
	*now++
	r.ProviderPoolQuota = []pluginapi.ProviderQuotaObservation{{AuthID: "a", ObservedAt: time.Unix(*now, 0), Signals: map[string]string{"x-codex-credits-balance": "100"}}}
	if err := reserveCreditPool(l, p, r, "alice", u); err != nil {
		t.Fatal("topup must resume:", err)
	}
}
func TestOptimisticQuotaNoObservationRequiresPositiveGrant(t *testing.T) {
	for _, resource := range []string{"renewing", "credits"} {
		l, pools, r, u, _ := reportedFixture(t)
		r.ProviderPoolQuota = nil
		u.CreditShares = map[string]string{"codex": "50"}
		if err := reserveResource(l, pools, r, "user", u, resource, ""); err != nil {
			t.Fatal(resource, err)
		}
		u.PoolShares["codex"] = "0"
		u.CreditShares["codex"] = "0"
		if reserveResource(l, pools, r, "user", u, resource, "") == nil {
			t.Fatal("zero grant admitted", resource)
		}
	}
}
func TestOptimisticQuotaProbesDoNotMutateAttemptCounts(t *testing.T) {
	l, pools, r, u, _ := reportedFixture(t)
	if err := reservePool(l, pools, r, "user", u); err != nil {
		t.Fatal(err)
	}
	c := cloneLedger(l)
	if err := reservePool(c, pools, r, "user", u); err != nil {
		t.Fatal(err)
	}
	if l.ReportedPools["codex"].Accounts["a"].Windows["x-codex-primary"].Attempts["user"] != 1 {
		t.Fatal("probe mutated source counts")
	}
	if l.CreditPools["codex"].Accounts["a"].Attempts["user"] != 1 {
		t.Fatal("probe mutated source credit counts")
	}
}

func TestOptimisticQuotaTopUpStartsNewAttributionInterval(t *testing.T) {
	l, pools, r, _, now := reportedFixture(t)
	p := pools["codex"]
	u := User{CreditShares: map[string]string{"codex": "100"}}
	r.ProviderPoolQuota[0].Signals = map[string]string{"x-codex-credits-balance": "100"}
	if err := reserveCreditPool(l, p, r, "alice", u); err != nil {
		t.Fatal(err)
	}
	*now++
	r.ProviderPoolQuota[0].ObservedAt = time.Unix(*now, 0)
	r.ProviderPoolQuota[0].Signals["x-codex-credits-balance"] = "200"
	if err := reserveCreditPool(l, p, r, "bob", u); err != nil {
		t.Fatal(err)
	}
	*now++
	r.ProviderPoolQuota[0].ObservedAt = time.Unix(*now, 0)
	r.ProviderPoolQuota[0].Signals["x-codex-credits-balance"] = "190"
	if err := reserveCreditPool(l, p, r, "bob", u); err != nil {
		t.Fatal(err)
	}
	a := l.CreditPools["codex"].Accounts["a"]
	if a.Users["alice"] != "" || a.Users["bob"] != "10" {
		t.Fatalf("pretopup attempts charged: %#v", a)
	}
}

func TestOptimisticCreditsConsistentExhaustedFlagBlocks(t *testing.T) {
	l, pools, r, _, now := reportedFixture(t)
	p := pools["codex"]
	u := User{CreditShares: map[string]string{"codex": "100"}}
	r.ProviderPoolQuota[0].Signals = map[string]string{"x-codex-credits-balance": "100", "x-codex-credits-has-credits": "true"}
	if err := reserveCreditPool(l, p, r, "alice", u); err != nil {
		t.Fatal(err)
	}
	*now++
	r.ProviderPoolQuota[0].ObservedAt = time.Unix(*now, 0)
	r.ProviderPoolQuota[0].Signals["x-codex-credits-balance"] = "0"
	r.ProviderPoolQuota[0].Signals["x-codex-credits-has-credits"] = "false"
	if reserveCreditPool(l, p, r, "alice", u) == nil {
		t.Fatal("consistent known zero was ignored")
	}
	if l.CreditPools["codex"].Accounts["a"].Remaining != "0" {
		t.Fatal("known zero lost")
	}
}

func TestOptimisticCreditsExplicitUnavailableWithoutNumericBalance(t *testing.T) {
	l, pools, r, _, _ := reportedFixture(t)
	p := pools["codex"]
	u := User{CreditShares: map[string]string{"codex": "100"}}
	r.ProviderPoolQuota[0].Signals = map[string]string{"x-codex-credits-has-credits": "false"}
	if reserveCreditPool(l, p, r, "alice", u) == nil {
		t.Fatal("explicit unavailable ignored")
	}
	r.ProviderPoolQuota = nil
	if reserveCreditPool(l, p, r, "alice", u) == nil {
		t.Fatal("explicit unavailable forgotten")
	}
	unknown := emptyLedger()
	r.ProviderPoolQuota = []pluginapi.ProviderQuotaObservation{{AuthID: "a", ObservedAt: poolNow(), Signals: map[string]string{"x-codex-credits-has-credits": "true"}}}
	if err := reserveCreditPool(unknown, p, r, "alice", u); err != nil {
		t.Fatal("available flag without numeric capacity must stay unknown", err)
	}
}
