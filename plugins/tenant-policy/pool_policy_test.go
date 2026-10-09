//go:build linux && cgo

package main

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

func TestPoolOnlyLiveAdmissionAcrossKeysAndAttempts(t *testing.T) {
	now := int64(100)
	poolClock(t, &now)
	path, r := policyFixture(t)
	if err := withLedger(path, true, func(l *Ledger) error {
		u := l.Users["u"]
		u.LimitUSD = "0"
		u.PoolShares = map[string]string{"p": "80"}
		l.Users["u"] = u
		l.Keys[KeyHash("second-key")] = Key{User: "u"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	configState.Lock()
	cfg := configState.cfg
	cfg.Pools = PoolConfig{"p": {CapacityPerAccount: 10, WindowSeconds: 100}}
	cfg.Models["alias"] = modelPolicy{Provider: "p", UpstreamModel: "m", PoolOnly: true}
	configState.cfg = cfg
	configState.Unlock()
	r.AuthID = "account"
	r.ProviderPoolAuthIDs = []string{"account"}
	for i := 0; i < 8; i++ {
		r.AttemptID = fmt.Sprint("attempt-", i)
		if i%2 == 0 {
			r.Principal = principal(KeyHash("test-secret"))
		} else {
			r.Principal = principal(KeyHash("second-key"))
		}
		if got := invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r); got.Terminate {
			t.Fatalf("attempt %d denied: %s", i, got.ResponseBody)
		}
	}
	r.AttemptID = "retry-with-same-model"
	if got := invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r); !got.Terminate || got.StatusCode != 429 {
		t.Fatalf("pool exhaustion response %#v", got)
	}
	r.ProviderPoolAuthIDs = append(r.ProviderPoolAuthIDs, "new-account")
	if got := invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r); got.Terminate {
		t.Fatalf("account addition not recognized: %s", got.ResponseBody)
	}
	// Duplicate attempt rolls back pool mutation in the same ledger transaction.
	if got := invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r); !got.Terminate {
		t.Fatal("duplicate attempt accepted")
	}
	if err := withLedger(path, false, func(l *Ledger) error {
		if l.Pools["p"].Consumed != 9 || l.Users["u"].SpentUSD != "0" {
			t.Fatalf("incorrect usage state %#v", l)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPoolAndUSDDenialAreAtomic(t *testing.T) {
	now := int64(100)
	poolClock(t, &now)
	path, r := policyFixture(t)
	if err := withLedger(path, true, func(l *Ledger) error {
		u := l.Users["u"]
		u.LimitUSD = "0"
		u.PoolShares = map[string]string{"p": "100"}
		l.Users["u"] = u
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	configState.Lock()
	configState.cfg.Pools = PoolConfig{"p": {CapacityPerAccount: 10, WindowSeconds: 100}}
	configState.Unlock()
	r.AuthID = "account"
	r.ProviderPoolAuthIDs = []string{"account"}
	if got := invokePolicy(t, pluginabi.MethodRequestInterceptAfter, r); !got.Terminate {
		t.Fatal("USD denial bypassed")
	}
	if err := withLedger(path, false, func(l *Ledger) error {
		if l.Pools["p"].Consumed != 0 {
			t.Fatal("USD denial committed pool charge")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPoolOnlyConfigurationRequiresPoolNotPrices(t *testing.T) {
	path, _ := policyFixture(t)
	config := fmt.Sprintf("state: %s\npools:\n  p:\n    capacity_per_account: 10\n    window_seconds: 100\nmodels:\n  alias:\n    provider: p\n    upstream_model: m\n    pool_only: true\n", path)
	raw, _ := json.Marshal(map[string]any{"schema_version": pluginabi.SchemaVersion, "config_yaml": []byte(config)})
	if err := configure(raw); err != nil {
		t.Fatalf("pool-only configuration required USD pricing: %v", err)
	}
	invalid := fmt.Sprintf("state: %s\nmodels:\n  alias:\n    provider: p\n    upstream_model: m\n    pool_only: true\n", path)
	raw, _ = json.Marshal(map[string]any{"schema_version": pluginabi.SchemaVersion, "config_yaml": []byte(invalid)})
	if err := configure(raw); err == nil {
		t.Fatal("pool-only mapping without pool accepted")
	}
}
