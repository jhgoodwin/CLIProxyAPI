//go:build linux && cgo

package main

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func reportedFixture(t *testing.T) (*Ledger, PoolConfig, pluginapi.RequestInterceptRequest, User, *int64) {
	t.Helper()
	now := int64(1000)
	poolClock(t, &now)
	p := PoolConfig{"codex": {Mode: "reported", ReportedMaxAgeSeconds: 60}}
	r := pluginapi.RequestInterceptRequest{Provider: "codex", AuthID: "a", ProviderPoolAuthIDs: []string{"a"}, ProviderPoolQuota: []pluginapi.ProviderQuotaObservation{{AuthID: "a", ObservedAt: time.Unix(now, 0), Signals: map[string]string{"x-codex-primary-used-percent": "0", "x-codex-primary-reset-at": "2000"}}}}
	return emptyLedger(), p, r, User{PoolShares: map[string]string{"codex": "80"}}, &now
}

func TestReportedQuotaShareAndAddedAccount(t *testing.T) {
	l, p, r, u, now := reportedFixture(t)
	if err := reservePool(l, p, r, "user", u); err != nil {
		t.Fatal(err)
	}
	if err := reservePool(l, p, r, "user", u); err != nil {
		t.Fatal("same watermark blocked concurrent attempt")
	}
	*now++
	r.ProviderPoolQuota[0].ObservedAt = time.Unix(*now, 0)
	r.ProviderPoolQuota[0].Signals["x-codex-primary-used-percent"] = "80"
	if err := reservePool(l, p, r, "user", u); err == nil {
		t.Fatal("80 percent share not enforced")
	}
	r.ProviderPoolAuthIDs = append(r.ProviderPoolAuthIDs, "b")
	r.ProviderPoolQuota = append(r.ProviderPoolQuota, pluginapi.ProviderQuotaObservation{AuthID: "b", ObservedAt: time.Unix(*now, 0), Signals: map[string]string{"x-codex-primary-used-percent": "0", "x-codex-primary-reset-at": "2000"}})
	if err := reservePool(l, p, r, "user", u); err != nil {
		t.Fatal("new account must increase denominator:", err)
	}
	if got := l.ReportedPools["codex"].Accounts["a"].Windows["x-codex-primary"].Users["user"]; got != 800000 {
		t.Fatalf("consumption reset on account addition: %d", got)
	}
}

func TestReportedQuotaUnknownAllowsKnownZeroDenies(t *testing.T) {
	for _, kind := range []string{"unknown", "stale", "future", "reset", "invalid", "zero", "missing-window"} {
		t.Run(kind, func(t *testing.T) {
			l, p, r, u, now := reportedFixture(t)
			switch kind {
			case "unknown":
				r.ProviderPoolQuota = nil
			case "stale":
				*now += 61
			case "future":
				r.ProviderPoolQuota[0].ObservedAt = time.Unix(*now+1, 0)
			case "reset":
				r.ProviderPoolQuota[0].Signals["x-codex-primary-reset-at"] = "999"
			case "invalid":
				r.ProviderPoolQuota[0].Signals["x-codex-primary-used-percent"] = "101"
			case "zero":
				r.ProviderPoolQuota[0].Signals["x-codex-primary-used-percent"] = "100"
			case "missing-window":
				r.ProviderPoolQuota[0].Signals["x-codex-secondary-used-percent"] = "0"
				r.ProviderPoolQuota[0].Signals["x-codex-secondary-reset-at"] = "2000"
				if err := reservePool(l, p, r, "user", u); err != nil {
					t.Fatal(err)
				}
				*now++
				r.ProviderPoolQuota[0].ObservedAt = time.Unix(*now, 0)
				delete(r.ProviderPoolQuota[0].Signals, "x-codex-secondary-used-percent")
			}
			err := reservePool(l, p, r, "user", u)
			if (err != nil) != (kind == "zero") {
				t.Fatalf("kind %s: %v", kind, err)
			}
		})
	}
}

func TestReportedProviderWindows(t *testing.T) {
	cases := []struct {
		provider  string
		signals   map[string]string
		name      string
		remaining int64
	}{
		{"claude", map[string]string{"anthropic-ratelimit-unified-5h-utilization": "0.25", "anthropic-ratelimit-unified-5h-reset": "2000"}, "anthropic-ratelimit-unified-5h", 750000},
		{"groq", map[string]string{"x-ratelimit-limit-requests": "100", "x-ratelimit-remaining-requests": "20", "x-ratelimit-reset-requests": "2m59.56s"}, "ratelimit-requests", 200000},
		{"nim", map[string]string{"x-ratelimit-limit": "10", "x-ratelimit-remaining": "7", "x-ratelimit-reset": "1970-01-01T00:33:20Z"}, "ratelimit", 700000},
	}
	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			w, err := parseReportedQuota(c.provider, c.signals, time.Unix(1000, 0), quotaScale)
			if err != nil {
				t.Fatal(err)
			}
			if w[c.name].Remaining != c.remaining {
				t.Fatalf("quota=%#v", w)
			}
		})
	}
}

func TestReportedQuotaUsesAdminSeed(t *testing.T) {
	l, p, r, u, _ := reportedFixture(t)
	l.ReportedPools = map[string]ReportedPoolState{"codex": {Seeds: map[string]ReportedQuotaSeed{"a": {ObservedAt: r.ProviderPoolQuota[0].ObservedAt, Signals: r.ProviderPoolQuota[0].Signals}}}}
	r.ProviderPoolQuota = nil
	if err := reservePool(l, p, r, "user", u); err != nil {
		t.Fatal(err)
	}
}

func TestReportedQuotaRoundedObservationDoesNotBlock(t *testing.T) {
	l, p, r, u, now := reportedFixture(t)
	if err := reservePool(l, p, r, "first", u); err != nil {
		t.Fatal(err)
	}
	*now++
	r.ProviderPoolQuota[0].ObservedAt = time.Unix(*now, 0)
	if err := reservePool(l, p, r, "next", u); err != nil {
		t.Fatal("unchanged newer observation blocked admission")
	}
	if l.ReportedPools["codex"].Accounts["a"].ObservedAt.Unix() != *now {
		t.Fatal("observation was not recorded")
	}
}

func TestReportedQuotaExpiredWindowDoesNotRequireRefresh(t *testing.T) {
	l, p, r, u, now := reportedFixture(t)
	r.ProviderPoolQuota[0].Signals["x-codex-primary-reset-at"] = "1010"
	if err := reservePool(l, p, r, "user", u); err != nil {
		t.Fatal(err)
	}
	*now = 1011
	r.ProviderPoolQuota[0].ObservedAt = time.Unix(1009, 0)
	r.ProviderPoolQuota[0].Signals["x-codex-primary-reset-at"] = "1020"
	if err := reservePool(l, p, r, "user", u); err != nil {
		t.Fatal("expired prior period blocked admission")
	}
	r.ProviderPoolQuota[0].ObservedAt = time.Unix(1011, 0)
	if err := reservePool(l, p, r, "user", u); err != nil {
		t.Fatal(err)
	}
}

func TestReportedPoolConfigurationAndWeight(t *testing.T) {
	if err := validatePools(PoolConfig{"p": {Mode: "reported", ReportedMaxAgeSeconds: 60, AccountWeights: map[string]string{"a": "2"}}}); err != nil {
		t.Fatal(err)
	}
	if err := validatePools(PoolConfig{"p": {Mode: "reported", ReportedMaxAgeSeconds: 60, WindowSeconds: 100}}); err == nil {
		t.Fatal("reported accepted synthetic window")
	}
	n, err := accountWeight(PoolDefinition{AccountWeights: map[string]string{"a": "2"}}, "a")
	if err != nil || n != 2000000 {
		t.Fatalf("weight %d %v", n, err)
	}
}
