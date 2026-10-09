//go:build linux && cgo

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func poolClock(t *testing.T, seconds *int64) {
	t.Helper()
	old := poolNow
	poolNow = func() time.Time { return time.Unix(*seconds, 0) }
	t.Cleanup(func() { poolNow = old })
}
func poolFixture() (PoolConfig, pluginapi.RequestInterceptRequest, User) {
	return PoolConfig{"codex": {CapacityPerAccount: 10, WindowSeconds: 100}}, pluginapi.RequestInterceptRequest{Provider: "codex", AuthID: "first", ProviderPoolAuthIDs: []string{"first"}}, User{PoolShares: map[string]string{"codex": "80"}}
}
func TestPoolShareExpandsWithActiveAccountsAndResets(t *testing.T) {
	now := int64(101)
	poolClock(t, &now)
	pools, r, u := poolFixture()
	l := emptyLedger()
	for i := 0; i < 8; i++ {
		if err := reservePool(l, pools, r, "u", u); err != nil {
			t.Fatal(err)
		}
	}
	if reservePool(l, pools, r, "u", u) == nil {
		t.Fatal("80 percent of one account exceeded")
	}
	r.ProviderPoolAuthIDs = append(r.ProviderPoolAuthIDs, "second")
	for i := 0; i < 8; i++ {
		if err := reservePool(l, pools, r, "u", u); err != nil {
			t.Fatal(err)
		}
	}
	if reservePool(l, pools, r, "u", u) == nil {
		t.Fatal("80 percent of two accounts exceeded")
	}
	// Removal reduces capacity without erasing this user's consumption.
	r.ProviderPoolAuthIDs = []string{"first"}
	if reservePool(l, pools, r, "u", u) == nil {
		t.Fatal("removal erased consumption")
	}
	now = 199
	if reservePool(l, pools, r, "u", u) == nil {
		t.Fatal("premature reset")
	}
	now = 200
	if err := reservePool(l, pools, r, "u", u); err != nil {
		t.Fatal(err)
	}
	if l.Pools["codex"].Consumed != 1 {
		t.Fatal("window did not reset")
	}
}
func TestPoolGlobalLimitAndIndependentProviders(t *testing.T) {
	now := int64(100)
	poolClock(t, &now)
	p, r, u := poolFixture()
	l := emptyLedger()
	u.PoolShares["codex"] = "100"
	for i := 0; i < 10; i++ {
		if err := reservePool(l, p, r, fmt.Sprint(i), u); err != nil {
			t.Fatal(err)
		}
	}
	if reservePool(l, p, r, "other", u) == nil {
		t.Fatal("global pool exceeded")
	}
	p["nim"] = PoolDefinition{CapacityPerAccount: 2, WindowSeconds: 100}
	u.PoolShares["nim"] = "50"
	r.Provider = "nim"
	if err := reservePool(l, p, r, "other", u); err != nil {
		t.Fatal(err)
	}
	if reservePool(l, p, r, "other", u) == nil {
		t.Fatal("provider share exceeded")
	}
}
func TestPoolInvalidInventoryShareAndConfiguration(t *testing.T) {
	now := int64(100)
	poolClock(t, &now)
	for _, share := range []string{"", "101", "-1", "NaN", "0"} {
		p, r, u := poolFixture()
		u.PoolShares["codex"] = share
		if reservePool(emptyLedger(), p, r, "u", u) == nil {
			t.Fatalf("share %s accepted", share)
		}
	}
	for _, ids := range [][]string{nil, {"other"}, {"first", "first"}, {"first", ""}} {
		p, r, u := poolFixture()
		r.ProviderPoolAuthIDs = ids
		if reservePool(emptyLedger(), p, r, "u", u) == nil {
			t.Fatalf("inventory %v accepted", ids)
		}
	}
	for _, def := range []PoolDefinition{{CapacityPerAccount: 10, WindowSeconds: 100, Unit: "tokens"}, {CapacityPerAccount: 10}, {WindowSeconds: 100}, {CapacityPerAccount: -1, WindowSeconds: 100}} {
		if validatePools(PoolConfig{"p": def}) == nil {
			t.Fatal("invalid config accepted")
		}
	}
	p, r, u := poolFixture()
	p["codex"] = PoolDefinition{Accounts: map[string]int64{"first": math.MaxInt64, "second": 1}, WindowSeconds: 100}
	r.ProviderPoolAuthIDs = []string{"first", "second"}
	if reservePool(emptyLedger(), p, r, "u", u) == nil {
		t.Fatal("capacity overflow accepted")
	}
}
func TestPoolOverridesFractionalSharesAndClockRegression(t *testing.T) {
	now := int64(100)
	poolClock(t, &now)
	p, r, u := poolFixture()
	p["codex"] = PoolDefinition{CapacityPerAccount: 3, Accounts: map[string]int64{"first": 4}, WindowSeconds: 100}
	u.PoolShares["codex"] = "37.5"
	r.ProviderPoolAuthIDs = []string{"first", "second"}
	l := emptyLedger()
	for i := 0; i < 2; i++ {
		if err := reservePool(l, p, r, "u", u); err != nil {
			t.Fatal(err)
		}
	}
	if reservePool(l, p, r, "u", u) == nil {
		t.Fatal("fractional allowance rounded upward")
	}
	now = 99
	if reservePool(l, p, r, "u", u) == nil {
		t.Fatal("clock regression refreshed allowance")
	}
	def := p["codex"]
	def.WindowSeconds = 50
	p["codex"] = def
	if reservePool(l, p, r, "u", u) == nil {
		t.Fatal("window reconfiguration refreshed allowance")
	}
}
func TestPoolLedgerConcurrencyRollbackAndRestart(t *testing.T) {
	now := int64(100)
	poolClock(t, &now)
	p, r, u := poolFixture()
	l := emptyLedger()
	l.Users["u"] = u
	path := filepath.Join(t.TempDir(), "private", "ledger.json")
	if err := os.Mkdir(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(l)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- withLedger(path, true, func(l *Ledger) error { return reservePool(l, p, r, "u", l.Users["u"]) })
		}()
	}
	wg.Wait()
	close(results)
	admitted := 0
	for err := range results {
		if err == nil {
			admitted++
		}
	}
	if admitted != 8 {
		t.Fatalf("admitted %d, want 8", admitted)
	}
	// Reading afresh simulates restart; failed USD admission rolls back pool debit.
	now = 200
	if err := withLedger(path, true, func(l *Ledger) error {
		if err := reservePool(l, p, r, "u", u); err != nil {
			return err
		}
		return errors.New("USD exhausted")
	}); err == nil {
		t.Fatal("rollback callback unexpectedly succeeded")
	}
	if err := withLedger(path, false, func(l *Ledger) error {
		if l.Pools["codex"].Consumed != 8 {
			t.Fatal("failed transaction persisted pool")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
