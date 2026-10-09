//go:build linux && cgo

package main

import (
	"testing"
	"time"
)

func TestObserveQuota(t *testing.T) {
	oldNow := poolNow
	poolNow = func() time.Time { return time.Unix(2_000_000_000, 0) }
	t.Cleanup(func() { poolNow = oldNow })
	valid := []string{"codex", "account-a", "2033-05-18T03:33:20Z", `{"x-codex-primary-used-percent":"20","x-codex-primary-reset-after-seconds":"3600"}`}
	l := emptyLedger()
	if err := observeQuota(l, valid); err != nil {
		t.Fatal(err)
	}
	seed := l.ReportedPools["codex"].Seeds["account-a"]
	if seed.ObservedAt.Unix() != 2_000_000_000 || seed.Signals["x-codex-primary-used-percent"] != "20" {
		t.Fatalf("unexpected seed: %#v", seed)
	}
	if err := observeQuota(l, valid); err == nil {
		t.Fatal("equal timestamp replaced existing seed")
	}
}

func TestObserveQuotaRejectsInvalidInput(t *testing.T) {
	oldNow := poolNow
	poolNow = func() time.Time { return time.Unix(2_000_000_000, 0) }
	t.Cleanup(func() { poolNow = oldNow })
	base := []string{"codex", "account-a", "2033-05-18T03:33:20Z", `{"x-codex-primary-used-percent":"20","x-codex-primary-reset-after-seconds":"3600"}`}
	cases := []struct {
		name string
		edit func([]string) []string
	}{
		{"arg count", func(a []string) []string { return a[:3] }},
		{"blank provider", func(a []string) []string { a[0] = " "; return a }},
		{"future timestamp", func(a []string) []string { a[2] = "2033-05-19T03:33:20Z"; return a }},
		{"malformed json", func(a []string) []string { a[3] = "[]"; return a }},
		{"control header", func(a []string) []string {
			a[3] = "{\"x-codex-primary-used-percent\":\"20\\n\",\"x-codex-primary-reset-after-seconds\":\"3600\"}"
			return a
		}},
		{"unsupported headers", func(a []string) []string { a[3] = `{"x":"y"}`; return a }},
		{"expired reset", func(a []string) []string {
			a[3] = `{"x-codex-primary-used-percent":"20","x-codex-primary-reset-at":"1999999999"}`
			return a
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := append([]string(nil), base...)
			if err := observeQuota(emptyLedger(), tc.edit(a)); err == nil {
				t.Fatal("expected invalid input to fail")
			}
		})
	}
}
