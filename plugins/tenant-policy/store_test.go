//go:build linux && cgo

package main

import (
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestUSDExactAndRejectInvalid(t *testing.T) {
	for in, want := range map[string]string{"0.1": "0.1", "1.2300": "1.23", "12": "12", "0.00000001": "0.00000001"} {
		r, err := usd(in)
		if err != nil || usdString(r) != want {
			t.Fatalf("usd(%q) = %v, %v; want %s", in, r, err, want)
		}
	}
	if got := usdString(new(big.Rat).SetFrac(big.NewInt(1), big.NewInt(40))); got != "0.025" {
		t.Fatalf("fraction formatted as %q", got)
	}
	for _, invalid := range []string{"", "-1", "+1", ".5", "1.", "1e2", " 1", "1  ", "01"} {
		if _, err := usd(invalid); err == nil {
			t.Errorf("usd(%q) accepted invalid amount", invalid)
		}
	}
}

func TestLedgerConcurrentTransactionsAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "ledger.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"users":{"u":{"providers":["p"],"limit_usd":"10","spent_usd":"0"}},"keys":{},"debits":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	const n = 24
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- withLedger(path, true, func(l *Ledger) error {
				u := l.Users["u"]
				amount, _ := usd(u.SpentUSD)
				amount.Add(amount, big.NewRat(1, 4))
				u.SpentUSD = usdString(amount)
				l.Users["u"] = u
				return nil
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := withLedger(path, false, func(l *Ledger) error {
		if l.Users["u"].SpentUSD != "6" {
			t.Fatalf("spent=%s", l.Users["u"].SpentUSD)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("ledger mode %o", fi.Mode().Perm())
	}
	if err := withLedger(path, true, func(*Ledger) error { return os.ErrInvalid }); err == nil {
		t.Fatal("callback error lost")
	}
	if err := withLedger(filepath.Join(filepath.Dir(path), "missing.json"), false, func(*Ledger) error { return nil }); err == nil {
		t.Fatal("missing ledger accepted")
	}
}

func privateLedgerPath(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "ledger.json")
}

func TestKeyRevokeAndHash(t *testing.T) {
	path := privateLedgerPath(t)
	if err := os.WriteFile(path, []byte(`{"users":{},"keys":{"`+KeyHash("secret")+`":{"user":"u","revoked":false}},"debits":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runCLI([]string{"--state", path, "key-revoke", KeyHash("secret")}); err != nil {
		t.Fatal(err)
	}
	if err := withLedger(path, false, func(l *Ledger) error {
		if !l.Keys[KeyHash("secret")].Revoked {
			t.Fatal("key remains active")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUserSetPreservesOverageBlockAndExplicitClear(t *testing.T) {
	path := privateLedgerPath(t)
	if err := os.WriteFile(path, []byte(`{"users":{"u":{"providers":["p"],"limit_usd":"1","spent_usd":"2","overage_blocked":true}},"keys":{},"debits":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runCLI([]string{"--state", path, "user-set", "u", "3", "p"}); err != nil {
		t.Fatal(err)
	}
	if err := withLedger(path, false, func(l *Ledger) error {
		if !l.Users["u"].OverageBlocked {
			t.Fatal("user-set cleared the overage block")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := runCLI([]string{"--state", path, "user-clear-overage", "u"}); err != nil {
		t.Fatal(err)
	}
	if err := withLedger(path, false, func(l *Ledger) error {
		if l.Users["u"].OverageBlocked {
			t.Fatal("explicit clear retained the overage block")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPoolShareCLIAndLedgerClone(t *testing.T) {
	path := privateLedgerPath(t)
	if err := os.WriteFile(path, []byte(`{"users":{"u":{"providers":["p"],"limit_usd":"0","spent_usd":"0","pool_shares":{"p":"20"}}},"keys":{},"debits":{},"pools":{"p":{"start":1,"window_seconds":60,"consumed":2,"users":{"u":2}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runCLI([]string{"--state", path, "user-share", "u", "p", "25.5"}); err != nil {
		t.Fatal(err)
	}
	if err := runCLI([]string{"--state", path, "user-set", "u", "0", "p"}); err != nil {
		t.Fatal(err)
	}
	if err := withLedger(path, false, func(l *Ledger) error {
		if l.Users["u"].PoolShares["p"] != "25.5" {
			t.Fatal("user-set discarded existing pool share")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := withLedger(path, true, func(l *Ledger) error {
		if l.Users["u"].PoolShares["p"] != "25.5" {
			t.Fatalf("pool share = %q", l.Users["u"].PoolShares["p"])
		}
		l.Users["u"].PoolShares["p"] = "30"
		l.Pools["p"].Users["u"] = 99
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := withLedger(path, false, func(l *Ledger) error {
		if l.Users["u"].PoolShares["p"] != "30" || l.Pools["p"].Users["u"] != 99 {
			t.Fatalf("changes were not persisted: %#v %#v", l.Users["u"].PoolShares, l.Pools["p"])
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"100.01", "-1", "1e2"} {
		if err := runCLI([]string{"--state", path, "user-share", "u", "p", invalid}); err == nil {
			t.Errorf("accepted pool share %q", invalid)
		}
	}
}
