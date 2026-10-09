//go:build linux && cgo

package main

import "errors"

// Operator-supplied newer observations become the reset baseline. No new
// capacity is invented and no older watermark can replace a live observation.
func refreshResetObservations(l *Ledger, resource, provider, authID string) error {
	reported := l.ReportedPools[provider]
	for id, seed := range reported.Seeds {
		if authID != "" && authID != id {
			continue
		}
		if resource == "credits" {
			pool := l.CreditPools[provider]
			a := pool.Accounts[id]
			if !seed.ObservedAt.After(a.ObservedAt) {
				continue
			}
			definition := PoolDefinition{}
			if provider != "codex" {
				if len(seed.Signals) != 1 {
					return errors.New("reset requires an unambiguous actual credit balance observation")
				}
				for header := range seed.Signals {
					definition.CreditBalanceHeader = header
				}
			}
			balance, err := creditBalance(provider, definition, seed.Signals)
			if err != nil {
				continue
			}
			if pool.Accounts == nil {
				pool.Accounts = map[string]CreditAccountState{}
			}
			a.ObservedAt = seed.ObservedAt
			a.Remaining = usdString(balance)
			a.Capacity = a.Remaining
			pool.Accounts[id] = a
			if l.CreditPools == nil {
				l.CreditPools = map[string]CreditPoolState{}
			}
			l.CreditPools[provider] = pool
		} else {
			a, exists := reported.Accounts[id]
			if !exists || !seed.ObservedAt.After(a.ObservedAt) {
				continue
			}
			weight := quotaScale
			for _, w := range a.Windows {
				weight = w.Capacity
				break
			}
			windows, err := parseReportedQuota(provider, seed.Signals, seed.ObservedAt, weight)
			if err != nil {
				continue
			}
			a.ObservedAt = seed.ObservedAt
			a.Windows = windows
			reported.Accounts[id] = a
		}
	}
	l.ReportedPools[provider] = reported
	return nil
}
