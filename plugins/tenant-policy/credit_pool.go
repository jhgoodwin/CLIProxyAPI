//go:build linux && cgo

package main

import (
	"errors"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type CreditPoolState struct {
	Accounts map[string]CreditAccountState `json:"accounts"`
}
type CreditAccountState struct {
	Attempts    map[string]int64  `json:"attempts,omitempty"`
	ObservedAt  time.Time         `json:"observed_at"`
	Capacity    string            `json:"capacity"`
	Remaining   string            `json:"remaining"`
	PendingUser string            `json:"pending_user,omitempty"`
	Users       map[string]string `json:"users"`
}

func creditBalance(provider string, p PoolDefinition, signals map[string]string) (*big.Rat, error) {
	header := p.CreditBalanceHeader
	if header == "" && provider == "codex" {
		header = "x-codex-credits-balance"
	}
	if header == "" {
		return nil, errors.New("numeric credit balance header is not configured")
	}
	headers := map[string]string{}
	for k, v := range signals {
		k = strings.ToLower(k)
		if _, ok := headers[k]; ok {
			return nil, errors.New("duplicate credit header")
		}
		headers[k] = strings.TrimSpace(v)
	}
	if provider == "codex" {
		for _, name := range []string{"x-codex-credits-unlimited", "x-codex-credits-has-credits"} {
			if value, ok := headers[name]; ok {
				flag, err := strconv.ParseBool(value)
				if err != nil || (name == "x-codex-credits-unlimited" && flag) {
					return nil, errors.New("finite usable credit balance unavailable")
				}
			}
		}
	}

	if provider == "codex" && headers[strings.ToLower(header)] == "" {
		if hasCredits, err := strconv.ParseBool(headers["x-codex-credits-has-credits"]); err == nil && !hasCredits {
			return new(big.Rat), nil
		}
	}
	balance, err := usd(headers[strings.ToLower(header)])
	if err != nil {
		return nil, errors.New("credit balance must be a finite non-negative decimal")
	}
	if provider == "codex" && headers["x-codex-credits-has-credits"] != "" {
		hasCredits, _ := strconv.ParseBool(headers["x-codex-credits-has-credits"])
		if !hasCredits && balance.Sign() > 0 {
			return nil, errors.New("credit balance contradicts usable-credit flag")
		}
	}
	return balance, nil
}

// Native credits remain native decimal units; missing balances are unknown,
// never zero. We enforce a user share only when the pool denominator is known.
func reserveCreditPool(l *Ledger, p PoolDefinition, r pluginapi.RequestInterceptRequest, userID string, u User) error {
	share, err := poolPercent(u.CreditShares[poolName(p, r.Provider)])
	if err != nil || share.Sign() == 0 {
		return poolExhaustedError("credit share missing or exhausted")
	}
	if err := updateCreditQuota(l, p, r); err != nil {
		return err
	}
	state := l.CreditPools[r.Provider]
	seen := map[string]bool{}
	total := new(big.Rat)
	known := true
	for _, id := range r.ProviderPoolAuthIDs {
		if id == "" || seen[id] {
			return errors.New("invalid trusted credit inventory")
		}
		seen[id] = true
		a := state.Accounts[id]
		if a.Capacity == "" {
			known = false
			continue
		}
		capacity, err := usd(a.Capacity)
		if err != nil {
			return err
		}
		total.Add(total, capacity)
	}
	if !seen[r.AuthID] {
		return errors.New("selected credential absent from credit inventory")
	}
	if value := state.Accounts[r.AuthID].Remaining; value != "" {
		remaining, err := usd(value)
		if err != nil {
			return err
		}
		if remaining.Sign() == 0 {
			return poolExhaustedError("provider credits exhausted")
		}
	}
	used := new(big.Rat)
	for accountID, a := range state.Accounts {
		if !accountInPool(l, p, accountID) {
			continue
		}
		if v := a.Users[poolConsumer(p, userID)]; v != "" {
			n, err := usd(v)
			if err != nil {
				return err
			}
			used.Add(used, n)
		}
	}
	if known {
		allowance := new(big.Rat).Mul(total, share)
		allowance.Quo(allowance, big.NewRat(100, 1))
		if used.Cmp(allowance) >= 0 {
			return poolExhaustedError("user credit share exhausted")
		}
	}
	return markQuotaUser(l, r, poolConsumer(p, userID))
}

// credits-observe PROVIDER AUTH_ID RFC3339 BALANCE [HEADER] seeds an actual
// numeric provider balance. HEADER is required outside Codex and must match config.
func observeCredits(l *Ledger, args []string) error {
	if len(args) != 4 && len(args) != 5 {
		return errors.New("usage: credits-observe PROVIDER AUTH_ID RFC3339 BALANCE [HEADER]")
	}
	provider, id := args[0], args[1]
	if provider == "" || id == "" || strings.TrimSpace(provider) != provider || strings.TrimSpace(id) != id {
		return errors.New("invalid provider or account")
	}
	observed, err := time.Parse(time.RFC3339, args[2])
	if err != nil || observed.IsZero() || observed.After(poolNow()) {
		return errors.New("invalid credit observation time")
	}
	if _, err := usd(args[3]); err != nil {
		return errors.New("credit balance must be a finite non-negative decimal")
	}
	header := "x-codex-credits-balance"
	if len(args) == 5 {
		header = args[4]
	} else if provider != "codex" {
		return errors.New("credit header required outside Codex")
	}
	if header == "" || strings.TrimSpace(header) != header || strings.ContainsAny(header, "\r\n\t ") {
		return errors.New("invalid credit header")
	}
	if l.ReportedPools == nil {
		l.ReportedPools = map[string]ReportedPoolState{}
	}
	state := l.ReportedPools[provider]
	if state.Seeds == nil {
		state.Seeds = map[string]ReportedQuotaSeed{}
	}
	old := state.Seeds[id]
	if !old.ObservedAt.IsZero() && !observed.After(old.ObservedAt) {
		return errors.New("credit observation must be newer")
	}
	// Carry forward only same-time signals; older renewing signals must never be
	// relabeled as fresh by a credit-only observation.
	state.Seeds[id] = ReportedQuotaSeed{ObservedAt: observed, Signals: map[string]string{header: args[3]}}
	l.ReportedPools[provider] = state
	return nil
}
