//go:build linux && cgo

package main

import (
	"errors"
	"math"
	"math/big"
	"sort"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Observations are passive facts, not reservations. Never wait or probe here.
func quotaObservations(l *Ledger, r pluginapi.RequestInterceptRequest) (map[string]pluginapi.ProviderQuotaObservation, error) {
	out := map[string]pluginapi.ProviderQuotaObservation{}
	for _, o := range r.ProviderPoolQuota {
		if _, ok := out[o.AuthID]; ok {
			return nil, errors.New("duplicate trusted quota observation")
		}
		out[o.AuthID] = o
	}
	for id, s := range l.ReportedPools[r.Provider].Seeds {
		if o, ok := out[id]; !ok || s.ObservedAt.After(o.ObservedAt) {
			out[id] = pluginapi.ProviderQuotaObservation{AuthID: id, ObservedAt: s.ObservedAt, Signals: s.Signals}
		}
	}
	return out, nil
}

func updateReportedQuota(l *Ledger, p PoolDefinition, r pluginapi.RequestInterceptRequest) error {
	observations, err := quotaObservations(l, r)
	if err != nil {
		return err
	}
	state := l.ReportedPools[r.Provider]
	if state.Accounts == nil {
		state.Accounts = map[string]ReportedAccountState{}
	}
	now := poolNow()
	for _, id := range r.ProviderPoolAuthIDs {
		o := observations[id]
		a := state.Accounts[id]
		if o.ObservedAt.IsZero() || o.ObservedAt.After(now) || !o.ObservedAt.After(a.ObservedAt) {
			continue
		}
		weight, err := accountWeight(p, id)
		if err != nil {
			return err
		}
		windows, err := parseReportedQuota(r.Provider, o.Signals, o.ObservedAt, weight)
		if err != nil {
			continue
		}
		// Copy windows before reconciliation so failed transitions cannot mutate
		// nested attribution maps owned by the prior account snapshot.
		nextWindows := map[string]ReportedWindowState{}
		for name, w := range a.Windows {
			w.Users = cloneCounts(w.Users)
			w.Attempts = attributionCounts(cloneCounts(w.Attempts), a.PendingUser)
			nextWindows[name] = w
		}
		for name, w := range windows {
			prior, exists := nextWindows[name]
			next, accept, err := reconcileReportedWindow(prior, exists, w, o.ObservedAt.Unix(), now.Unix())
			if err != nil {
				return err
			}
			if accept {
				nextWindows[name] = next
			}
		}
		a.Windows = nextWindows
		a.PendingUser = ""
		a.ObservedAt = o.ObservedAt
		state.Accounts[id] = a
	}
	if l.ReportedPools == nil {
		l.ReportedPools = map[string]ReportedPoolState{}
	}
	l.ReportedPools[r.Provider] = state
	return nil
}

// reconcileReportedWindow is a pure transition: retained windows preserve their
// capacity/reset and attribution, while a rounded unchanged reading retains the
// pending attempts until a visible decrease. Inputs and their maps remain owned
// by the caller, including on errors.
func reconcileReportedWindow(prior ReportedWindowState, exists bool, observed ReportedWindowState, observedAt, now int64) (ReportedWindowState, bool, error) {
	if observed.ResetAt <= now || (exists && prior.ResetAt <= now && observedAt < prior.ResetAt) {
		return ReportedWindowState{}, false, nil
	}
	next := observed
	next.Users = cloneCounts(observed.Users)
	next.Attempts = cloneCounts(observed.Attempts)
	if !exists || prior.ResetAt <= now {
		return next, true, nil
	}
	if prior.Capacity != observed.Capacity {
		return ReportedWindowState{}, false, errors.New("account weight cannot change before quota reset")
	}
	next.ResetAt = prior.ResetAt
	next.Users = cloneCounts(prior.Users)
	if next.Users == nil {
		next.Users = map[string]int64{}
	}
	next.Attempts = cloneCounts(prior.Attempts)
	if prior.Remaining > observed.Remaining {
		allocations, err := quotaAllocations(prior.Remaining-observed.Remaining, next.Attempts)
		if err != nil {
			return ReportedWindowState{}, false, err
		}
		for user, delta := range allocations {
			if next.Users[user] > math.MaxInt64-delta {
				return ReportedWindowState{}, false, errors.New("quota consumption overflow")
			}
			next.Users[user] += delta
		}
	}
	if prior.Remaining != observed.Remaining {
		next.Attempts = map[string]int64{}
	}
	return next, true, nil
}

func updateCreditQuota(l *Ledger, p PoolDefinition, r pluginapi.RequestInterceptRequest) error {
	observations, err := quotaObservations(l, r)
	if err != nil {
		return err
	}
	state := l.CreditPools[r.Provider]
	if state.Accounts == nil {
		state.Accounts = map[string]CreditAccountState{}
	}
	now := poolNow()
	for _, id := range r.ProviderPoolAuthIDs {
		o := observations[id]
		a := state.Accounts[id]
		if o.ObservedAt.IsZero() || o.ObservedAt.After(now) || !o.ObservedAt.After(a.ObservedAt) {
			continue
		}
		balance, err := creditBalance(r.Provider, p, o.Signals)
		if err != nil {
			continue
		}
		next, err := reconcileCreditAccount(a, balance, o.ObservedAt)
		if err != nil {
			return err
		}
		state.Accounts[id] = next
	}
	if l.CreditPools == nil {
		l.CreditPools = map[string]CreditPoolState{}
	}
	l.CreditPools[r.Provider] = state
	return nil
}

// reconcileCreditAccount owns its returned maps and never changes prior or
// balance. Rounded unchanged readings retain attempts for the next depletion;
// top-ups increase native capacity and start a new attribution interval.
func reconcileCreditAccount(prior CreditAccountState, balance *big.Rat, observedAt time.Time) (CreditAccountState, error) {
	next := prior
	next.Users = map[string]string{}
	for user, used := range prior.Users {
		next.Users[user] = used
	}
	next.Attempts = attributionCounts(cloneCounts(prior.Attempts), prior.PendingUser)
	next.PendingUser = ""
	next.Remaining = usdString(balance)
	next.ObservedAt = observedAt
	if prior.Capacity == "" {
		next.Capacity = next.Remaining
		next.Attempts = map[string]int64{}
		return next, nil
	}
	remaining, err := usd(prior.Remaining)
	if err != nil {
		return CreditAccountState{}, err
	}
	capacity, err := usd(prior.Capacity)
	if err != nil {
		return CreditAccountState{}, err
	}
	switch balance.Cmp(remaining) {
	case -1:
		total, err := attemptTotal(next.Attempts)
		if err != nil {
			return CreditAccountState{}, err
		}
		depletion := new(big.Rat).Sub(remaining, balance)
		for user, count := range next.Attempts {
			used := new(big.Rat)
			if value := next.Users[user]; value != "" {
				used, err = usd(value)
				if err != nil {
					return CreditAccountState{}, err
				}
			}
			delta := new(big.Rat).Mul(depletion, big.NewRat(count, total))
			next.Users[user] = usdString(new(big.Rat).Add(used, delta))
		}
		next.Attempts = map[string]int64{}
	case 0:
		// Preserve attempts until the provider exposes a visible decrease.
	case 1:
		capacity.Add(capacity, new(big.Rat).Sub(balance, remaining))
		next.Attempts = map[string]int64{}
	}
	next.Capacity = usdString(capacity)
	return next, nil
}

// Both resources can be charged by the provider; do not assume that selecting a
// local policy resource determines the provider's funding source.
func markQuotaUser(l *Ledger, r pluginapi.RequestInterceptRequest, user string) error {
	renewing := l.ReportedPools[r.Provider]
	if renewing.Accounts == nil {
		renewing.Accounts = map[string]ReportedAccountState{}
	}
	a := renewing.Accounts[r.AuthID]
	for name, w := range a.Windows {
		if w.ResetAt <= poolNow().Unix() {
			continue
		}
		w.Attempts = attributionCounts(w.Attempts, a.PendingUser)
		if w.Attempts[user] == math.MaxInt64 {
			return errors.New("quota attempt count overflow")
		}
		w.Attempts[user]++
		a.Windows[name] = w
	}
	a.PendingUser = ""
	renewing.Accounts[r.AuthID] = a
	if l.ReportedPools == nil {
		l.ReportedPools = map[string]ReportedPoolState{}
	}
	l.ReportedPools[r.Provider] = renewing
	credits := l.CreditPools[r.Provider]
	if credits.Accounts == nil {
		credits.Accounts = map[string]CreditAccountState{}
	}
	c := credits.Accounts[r.AuthID]
	c.Attempts = attributionCounts(c.Attempts, c.PendingUser)
	if c.Attempts[user] == math.MaxInt64 {
		return errors.New("quota attempt count overflow")
	}
	c.Attempts[user]++
	c.PendingUser = ""
	credits.Accounts[r.AuthID] = c
	if l.CreditPools == nil {
		l.CreditPools = map[string]CreditPoolState{}
	}
	l.CreditPools[r.Provider] = credits
	return nil
}
func attributionCounts(counts map[string]int64, legacy string) map[string]int64 {
	if counts != nil {
		return counts
	}
	counts = map[string]int64{}
	if legacy != "" {
		counts[legacy] = 1
	}
	return counts
}
func attemptTotal(counts map[string]int64) (int64, error) {
	var total int64
	for _, count := range counts {
		if count <= 0 || total > math.MaxInt64-count {
			return 0, errors.New("invalid quota attempt count")
		}
		total += count
	}
	return total, nil
}
func quotaAllocations(delta int64, counts map[string]int64) (map[string]int64, error) {
	total, err := attemptTotal(counts)
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	if total == 0 {
		return out, nil
	}
	users := make([]string, 0, len(counts))
	for user := range counts {
		users = append(users, user)
	}
	sort.Strings(users)
	remaining := delta
	for _, user := range users {
		n := new(big.Int).Mul(big.NewInt(delta), big.NewInt(counts[user]))
		n.Quo(n, big.NewInt(total))
		out[user] = n.Int64()
		remaining -= n.Int64()
	}
	for _, user := range users {
		if remaining == 0 {
			break
		}
		out[user]++
		remaining--
	}
	return out, nil
}

// actualRenewingExhausted reads already reconciled facts; it never refreshes or
// spends. Unknown or expired quota is not evidence for automatic credit fallback.
func actualRenewingExhausted(l *Ledger, r pluginapi.RequestInterceptRequest) bool {
	for _, w := range l.ReportedPools[r.Provider].Accounts[r.AuthID].Windows {
		if w.ResetAt > poolNow().Unix() && w.Remaining == 0 {
			return true
		}
	}
	return false
}

// Refresh only local trusted facts. Persist them even when a later admission
// decision rejects; rejection must not erase newly learned exhaustion.
func refreshPoolFacts(l *Ledger, pools PoolConfig, r pluginapi.RequestInterceptRequest, explicit string, u User, resource string) error {
	names := []string{}
	for name, p := range pools {
		if p.Mode == "reported" && poolProvider(name, p) == r.Provider && (explicit == "" || explicit == name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		p := pools[name]
		p.name = name
		renewing, _ := poolPercent(u.PoolShares[name])
		credits, _ := poolPercent(u.CreditShares[name])
		positiveRenewing := renewing != nil && renewing.Sign() > 0
		positiveCredits := credits != nil && credits.Sign() > 0
		if (resource == "credits" && !positiveCredits) || ((resource == "" || resource == "renewing") && !positiveRenewing) || (resource == "auto" && !positiveRenewing && !positiveCredits) {
			continue
		}
		scoped, err := poolRequest(l, p, r)
		if err != nil {
			continue
		}
		if err := updateReportedQuota(l, p, scoped); err != nil {
			return err
		}
		if err := updateCreditQuota(l, p, scoped); err != nil {
			return err
		}
	}
	return nil
}
