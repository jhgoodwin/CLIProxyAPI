//go:build linux && cgo

package main

import (
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// QuotaResetAudit records an operator initiated quota baseline reset.
type QuotaResetAudit struct {
	Timestamp time.Time `json:"timestamp"`
	Resource  string    `json:"resource"`
	Pool      string    `json:"pool,omitempty"`
	Provider  string    `json:"provider"`
	AuthID    string    `json:"auth_id,omitempty"`
	Reason    string    `json:"reason"`
}

// resetQuota clears attributed usage while retaining the provider's observed
// remaining capacity. Omitting authID resets all observed accounts.
func resetQuota(l *Ledger, args []string) error {
	if len(args) < 3 || len(args) > 4 {
		return errors.New("usage: quota-reset renewing|credits POOL_OR_PROVIDER REASON [ACCOUNT]")
	}
	working := cloneLedger(l)
	if err := resetQuotaWorking(working, args); err != nil {
		return err
	}
	*l = *working
	return nil
}

type quotaResetTarget struct {
	resource string
	provider string
	reason   string
	pool     string
	authID   string
	members  map[string]bool // nil selects provider-wide records
}

func resetQuotaWorking(l *Ledger, args []string) error {
	target, err := resolveQuotaResetTarget(l, args)
	if err != nil {
		return err
	}
	// Refresh selected local observations before selecting existing records.
	if target.members == nil {
		if err := refreshResetObservations(l, target.resource, target.provider, target.authID); err != nil {
			return err
		}
	} else {
		for id := range target.members {
			if target.authID == "" || id == target.authID {
				if err := refreshResetObservations(l, target.resource, target.provider, id); err != nil {
					return err
				}
			}
		}
	}
	ids, err := quotaResetRecordIDs(l, target)
	if err != nil {
		return err
	}
	now := poolNow()
	if err := validateQuotaResetRecords(l, target, ids, now); err != nil {
		return err
	}
	applyQuotaReset(l, target, ids)
	l.ResetAudit = append(l.ResetAudit, QuotaResetAudit{Timestamp: now, Resource: target.resource, Provider: target.provider, Pool: target.pool, AuthID: target.authID, Reason: target.reason})
	return nil
}

func resolveQuotaResetTarget(l *Ledger, args []string) (quotaResetTarget, error) {
	target := quotaResetTarget{resource: args[0], provider: args[1], reason: args[2]}
	if definition, ok := l.NamedPools[target.provider]; ok {
		target.pool = target.provider
		target.provider = definition.Provider
		target.members = map[string]bool{}
		for name, account := range l.Accounts {
			if account.Provider != target.provider {
				continue
			}
			for _, pattern := range definition.AccountSelectors {
				match, err := filepath.Match(pattern, name)
				if err != nil {
					return quotaResetTarget{}, err
				}
				if match {
					target.members[account.AuthID] = true
					break
				}
			}
		}
		if len(target.members) == 0 {
			return quotaResetTarget{}, errors.New("named pool has no registered matching accounts")
		}
	}
	if target.resource != "renewing" && target.resource != "credits" {
		return quotaResetTarget{}, errors.New("resource must be renewing or credits")
	}
	if target.provider == "" || strings.TrimSpace(target.provider) != target.provider {
		return quotaResetTarget{}, errors.New("invalid provider")
	}
	if strings.TrimSpace(target.reason) != target.reason || target.reason == "" {
		return quotaResetTarget{}, errors.New("reset reason is required")
	}
	if len(args) == 4 {
		target.authID = args[3]
		if target.authID == "" || strings.TrimSpace(target.authID) != target.authID {
			return quotaResetTarget{}, errors.New("invalid auth ID")
		}
	}
	if account, ok := l.Accounts[target.authID]; ok {
		if account.Provider != target.provider {
			return quotaResetTarget{}, errors.New("account belongs to another provider")
		}
		target.authID = account.AuthID
	}
	if target.members != nil && target.authID != "" && !target.members[target.authID] {
		return quotaResetTarget{}, errors.New("account is outside selected pool")
	}
	return target, nil
}

// Resolve record IDs once after refresh; validation and mutation use exactly
// this selection rather than reinterpreting aliases or pool membership.
func quotaResetRecordIDs(l *Ledger, target quotaResetTarget) ([]string, error) {
	ids := []string{}
	add := func(id string) {
		if (target.authID == "" || id == target.authID) && (target.members == nil || target.members[id]) {
			ids = append(ids, id)
		}
	}
	if target.resource == "credits" {
		pool, ok := l.CreditPools[target.provider]
		if !ok {
			return nil, errors.New("unknown credit pool")
		}
		for id := range pool.Accounts {
			add(id)
		}
		if len(ids) == 0 {
			return nil, errors.New("no matching observed credit accounts")
		}
	} else {
		pool, ok := l.ReportedPools[target.provider]
		if !ok {
			return nil, errors.New("unknown renewing quota pool")
		}
		for id := range pool.Accounts {
			add(id)
		}
		if len(ids) == 0 {
			return nil, errors.New("no matching observed renewing quota accounts")
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func validateQuotaResetRecords(l *Ledger, target quotaResetTarget, ids []string, now time.Time) error {
	const maxObservationAge = 24 * time.Hour
	validObservation := func(observed time.Time) bool {
		return !observed.IsZero() && !observed.After(now) && now.Sub(observed) <= maxObservationAge
	}
	for _, id := range ids {
		if target.resource == "credits" {
			account := l.CreditPools[target.provider].Accounts[id]
			if !validObservation(account.ObservedAt) {
				return errors.New("credit account observation is missing, future, or stale")
			}
			capacity, err := usd(account.Capacity)
			if err != nil {
				return errors.New("credit capacity must be a non-negative decimal")
			}
			remaining, err := usd(account.Remaining)
			if err != nil || remaining.Cmp(capacity) > 0 {
				return errors.New("credit remaining must be a non-negative decimal no greater than capacity")
			}
		} else {
			account := l.ReportedPools[target.provider].Accounts[id]
			if !validObservation(account.ObservedAt) {
				return errors.New("renewing quota observation is missing, future, or stale")
			}
			if len(account.Windows) == 0 {
				return errors.New("renewing quota account has no observed windows")
			}
			for _, window := range account.Windows {
				if window.ResetAt <= now.Unix() {
					return errors.New("renewing quota window has expired; observe provider quota first")
				}
			}
		}
	}
	return nil
}

func applyQuotaReset(l *Ledger, target quotaResetTarget, ids []string) {
	for _, id := range ids {
		if target.resource == "credits" {
			pool := l.CreditPools[target.provider]
			account := pool.Accounts[id]
			account.Capacity = account.Remaining
			account.Users = map[string]string{}
			account.Attempts = map[string]int64{}
			account.PendingUser = ""
			pool.Accounts[id] = account
			l.CreditPools[target.provider] = pool
		} else {
			pool := l.ReportedPools[target.provider]
			account := pool.Accounts[id]
			account.PendingUser = ""
			for name, window := range account.Windows {
				window.Users = map[string]int64{}
				window.Attempts = map[string]int64{}
				account.Windows[name] = window
			}
			pool.Accounts[id] = account
			l.ReportedPools[target.provider] = pool
		}
	}
}
