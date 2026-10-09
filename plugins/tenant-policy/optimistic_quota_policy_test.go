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

func TestOptimisticQuotaRejectedFactsSurviveLedgerReload(t *testing.T) {
	for _, gate := range []string{"interceptor", "scheduler"} {
		t.Run(gate, func(t *testing.T) {
			now := int64(1000)
			poolClock(t, &now)
			path, r := policyFixture(t)
			if err := withLedger(path, true, func(l *Ledger) error {
				u := l.Users["u"]
				u.PoolShares = map[string]string{"p": "50"}
				l.Users["u"] = u
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			configState.Lock()
			configState.cfg.Pools = PoolConfig{"p": {Mode: "reported", ReportedMaxAgeSeconds: 60}}
			configState.cfg.Models["alias"] = modelPolicy{Provider: "p", UpstreamModel: "m", PoolOnly: true}
			configState.Unlock()
			r.AuthID = "a"
			r.ProviderPoolAuthIDs = []string{"a"}
			r.ProviderPoolQuota = []pluginapi.ProviderQuotaObservation{{AuthID: "a", ObservedAt: time.Unix(now, 0), Signals: map[string]string{"x-ratelimit-limit-requests": "100", "x-ratelimit-remaining-requests": "100", "x-ratelimit-reset-requests": "2000"}}}
			if got := invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r); got.Terminate {
				t.Fatal("baseline denied")
			}
			now++
			r.ProviderPoolQuota[0].ObservedAt = time.Unix(now, 0)
			r.ProviderPoolQuota[0].Signals["x-ratelimit-remaining-requests"] = "50"
			r.AttemptID = "denied-attempt"
			if gate == "interceptor" {
				if got := invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r); !got.Terminate {
					t.Fatal("share exhaustion admitted")
				}
			} else {
				req := pluginapi.SchedulerPickRequest{Provider: "p", Model: "alias", Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "a", Provider: "p"}}, ProviderPools: map[string][]pluginapi.ProviderQuotaObservation{"p": r.ProviderPoolQuota}, Options: pluginapi.SchedulerOptions{Metadata: map[string]any{"caller_scope": session.CallerScope(r.Principal)}}}
				b, _ := json.Marshal(req)
				out, err := handleMethod(pluginabi.MethodSchedulerPick, b)
				if err != nil {
					t.Fatal(err)
				}
				var env pluginabi.Envelope
				if err := json.Unmarshal(out, &env); err != nil {
					t.Fatal(err)
				}
				if env.OK {
					t.Fatal("scheduler admitted exhausted share")
				}
			}
			if err := withLedger(path, false, func(l *Ledger) error {
				w := l.ReportedPools["p"].Accounts["a"].Windows["ratelimit-requests"]
				if w.Remaining != 500000 || w.Users["u"] != 500000 {
					t.Fatalf("rejection lost facts: %#v", w)
				}
				if _, ok := l.Debits["denied-attempt"]; ok {
					t.Fatal("rejected attempt debited")
				}
				if len(w.Attempts) != 0 {
					t.Fatal("rejected attempt counted")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			r.ProviderPoolQuota = nil
			r.AttemptID = "missing-observation"
			if got := invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r); !got.Terminate {
				t.Fatal("missing reading erased known share exhaustion")
			}
		})
	}
}

func TestOptimisticCreditsExhaustedFlagDurableThroughAdmission(t *testing.T) {
	now := int64(1000)
	poolClock(t, &now)
	path, r := policyFixture(t)
	if err := withLedger(path, true, func(l *Ledger) error {
		u := l.Users["u"]
		u.Providers = []string{"codex"}
		u.CreditShares = map[string]string{"codex": "100"}
		l.Users["u"] = u
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	configState.Lock()
	configState.cfg.Pools = PoolConfig{"codex": {Mode: "reported", ReportedMaxAgeSeconds: 60}}
	configState.cfg.Models["alias"] = modelPolicy{Provider: "codex", UpstreamModel: "m", PoolOnly: true, QuotaResource: "credits"}
	configState.Unlock()
	r.Provider = "codex"
	r.AuthID = "account"
	r.ProviderPoolAuthIDs = []string{"account"}
	r.ProviderPoolQuota = []pluginapi.ProviderQuotaObservation{{AuthID: "account", ObservedAt: time.Unix(now, 0), Signals: map[string]string{"x-codex-credits-balance": "100", "x-codex-credits-has-credits": "true"}}}
	if got := invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r); got.Terminate {
		t.Fatal("positive baseline denied")
	}
	now++
	r.AttemptID = "rejected"
	r.ProviderPoolQuota[0].ObservedAt = time.Unix(now, 0)
	r.ProviderPoolQuota[0].Signals = map[string]string{"x-codex-credits-balance": "0", "x-codex-credits-has-credits": "false"}
	if got := invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r); !got.Terminate {
		t.Fatal("known zero admitted")
	}
	if err := withLedger(path, false, func(l *Ledger) error {
		if l.CreditPools["codex"].Accounts["account"].Remaining != "0" {
			t.Fatal("zero not persisted")
		}
		if _, ok := l.Debits["rejected"]; ok {
			t.Fatal("rejected debit")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.AttemptID = "missing-reading"
	r.ProviderPoolQuota = nil
	if got := invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r); !got.Terminate {
		t.Fatal("missing reading erased known zero")
	}
}
