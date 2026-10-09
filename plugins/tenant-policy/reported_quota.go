//go:build linux && cgo

package main

import (
	"errors"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Account quota is normalized to millionths of an explicitly weighted account.
// This is a share of observed provider capacity, never an inferred token budget.
const quotaScale int64 = 1000000

type ReportedPoolState struct {
	Accounts map[string]ReportedAccountState `json:"accounts"`
	Seeds    map[string]ReportedQuotaSeed    `json:"seeds,omitempty"`
}
type ReportedQuotaSeed struct {
	ObservedAt time.Time         `json:"observed_at"`
	Signals    map[string]string `json:"signals"`
}

type ReportedAccountState struct {
	ObservedAt  time.Time                      `json:"observed_at"`
	PendingUser string                         `json:"pending_user,omitempty"`
	Windows     map[string]ReportedWindowState `json:"windows"`
}
type ReportedWindowState struct {
	Attempts  map[string]int64 `json:"attempts,omitempty"`
	ResetAt   int64            `json:"reset_at"`
	Capacity  int64            `json:"capacity"`
	Remaining int64            `json:"remaining"`
	Users     map[string]int64 `json:"users"`
}

func accountWeight(p PoolDefinition, id string) (int64, error) {
	s := p.AccountWeights[id]
	if s == "" {
		return quotaScale, nil
	}
	r, err := usd(s)
	if err != nil || r.Sign() <= 0 {
		return 0, errors.New("account weight must be a positive decimal")
	}
	r.Mul(r, big.NewRat(quotaScale, 1))
	if !r.IsInt() || !r.Num().IsInt64() {
		return 0, errors.New("account weight must fit millionths")
	}
	return r.Num().Int64(), nil
}

func validateReportedPool(p PoolDefinition) error {
	if p.ReportedMaxAgeSeconds <= 0 || p.ReportedMaxAgeSeconds > 86400 {
		return errors.New("reported_max_age_seconds must be between 1 and 86400")
	}
	if p.CapacityPerAccount != 0 || len(p.Accounts) != 0 || p.WindowSeconds != 0 || p.Unit != "" {
		return errors.New("reported pools cannot use configured request capacity or windows")
	}
	for id := range p.AccountWeights {
		if id == "" || strings.TrimSpace(id) != id {
			return errors.New("invalid account weight ID")
		}
		if _, err := accountWeight(p, id); err != nil {
			return err
		}
	}
	return nil
}

func quotaReset(s string, observed time.Time) (int64, error) {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
		return n, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Unix(), nil
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 && d <= 365*24*time.Hour {
		return observed.Add(d).Unix(), nil
	}
	return 0, errors.New("invalid reported quota reset")
}

func quotaFraction(s string, denominator *big.Rat, capacity int64) (int64, error) {
	n, err := usd(s)
	if err != nil || denominator.Sign() <= 0 || n.Cmp(denominator) > 0 {
		return 0, errors.New("invalid reported quota fraction")
	}
	n.Quo(n, denominator).Mul(n, big.NewRat(capacity, 1))
	return new(big.Int).Quo(n.Num(), n.Denom()).Int64(), nil
}

func parseReportedQuota(provider string, signals map[string]string, observed time.Time, capacity int64) (map[string]ReportedWindowState, error) {
	h := map[string]string{}
	for k, v := range signals {
		name := strings.ToLower(k)
		if _, exists := h[name]; exists {
			return nil, errors.New("duplicate quota header name")
		}
		h[name] = strings.TrimSpace(v)
	}
	out := map[string]ReportedWindowState{}
	add := func(name, value, reset string, denom *big.Rat, used bool) error {
		if value == "" {
			return nil
		}
		n, err := quotaFraction(value, denom, capacity)
		if err != nil {
			return err
		}
		if used {
			n = capacity - n
		}
		t, err := quotaReset(reset, observed)
		if err != nil {
			return err
		}
		out[name] = ReportedWindowState{ResetAt: t, Capacity: capacity, Remaining: n, Users: map[string]int64{}}
		return nil
	}
	for k, v := range h {
		if provider == "codex" && strings.HasPrefix(k, "x-codex-") && strings.HasSuffix(k, "-used-percent") {
			prefix := strings.TrimSuffix(k, "-used-percent")
			reset := h[prefix+"-reset-at"]
			if reset == "" {
				if n, err := strconv.ParseInt(h[prefix+"-reset-after-seconds"], 10, 64); err == nil && n > 0 && n <= 365*86400 {
					reset = strconv.FormatInt(observed.Unix()+n, 10)
				}
			}
			if err := add(prefix, v, reset, big.NewRat(100, 1), true); err != nil {
				return nil, err
			}
		}
		if provider == "claude" && strings.HasPrefix(k, "anthropic-ratelimit-unified-") && strings.HasSuffix(k, "-utilization") {
			prefix := strings.TrimSuffix(k, "-utilization")
			if strings.Contains(prefix, "overage") {
				continue
			}
			if err := add(prefix, v, h[prefix+"-reset"], big.NewRat(1, 1), true); err != nil {
				return nil, err
			}
		}
	}
	for _, suffix := range []string{"", "-requests", "-tokens"} {
		prefix := "x-ratelimit-"
		if h[prefix+"remaining"+suffix] == "" {
			continue
		}
		limit, err := usd(h[prefix+"limit"+suffix])
		if err != nil {
			return nil, err
		}
		if err := add("ratelimit"+suffix, h[prefix+"remaining"+suffix], h[prefix+"reset"+suffix], limit, false); err != nil {
			return nil, err
		}
	}
	if len(out) == 0 {
		return nil, errors.New("provider quota has no supported capacity observation")
	}
	return out, nil
}

// reserveReportedPool uses facts already available locally. Observation freshness
// is never an admission prerequisite; unexpired known limits still apply.
func reserveReportedPool(l *Ledger, p PoolDefinition, r pluginapi.RequestInterceptRequest, userID string, u User) error {
	share, err := poolPercent(u.PoolShares[poolName(p, r.Provider)])
	if err != nil || share.Sign() == 0 {
		return poolExhaustedError("provider pool share is missing or exhausted")
	}
	if err := updateReportedQuota(l, p, r); err != nil {
		return err
	}
	if err := validateQuotaInventory(r); err != nil {
		return err
	}
	snapshot, err := summarizeReportedQuota(l, p, r, poolConsumer(p, userID), poolNow().Unix())
	if err != nil {
		return err
	}
	allowance := new(big.Rat).Mul(share, big.NewRat(snapshot.capacity, 100))
	for _, window := range snapshot.windows {
		if window.selectedExhausted {
			return poolExhaustedError("provider remaining quota exhausted")
		}
		if new(big.Rat).SetInt64(window.consumed).Cmp(allowance) >= 0 {
			return poolExhaustedError("user provider quota share exhausted")
		}
	}
	return markQuotaUser(l, r, poolConsumer(p, userID))
}

func validateQuotaInventory(r pluginapi.RequestInterceptRequest) error {
	seen := map[string]bool{}
	for _, id := range r.ProviderPoolAuthIDs {
		if id == "" || seen[id] {
			return errors.New("invalid trusted quota inventory")
		}
		seen[id] = true
	}
	if !seen[r.AuthID] {
		return errors.New("selected credential absent from provider quota inventory")
	}
	return nil
}

type reportedQuotaSnapshot struct {
	capacity int64
	windows  []reportedQuotaLimit
}
type reportedQuotaLimit struct {
	consumed          int64
	selectedExhausted bool
}

// Capacity comes from current inventory; consumption includes historical pool
// members until their windows reset. Unknown remaining balances are not zero.
// The snapshot owns only scalar values and cannot mutate ledger observations.
func summarizeReportedQuota(l *Ledger, p PoolDefinition, r pluginapi.RequestInterceptRequest, user string, now int64) (reportedQuotaSnapshot, error) {
	state := l.ReportedPools[r.Provider]
	byWindow := map[string]reportedQuotaLimit{}
	for id, account := range state.Accounts {
		if !accountInPool(l, p, id) {
			continue
		}
		for name, window := range account.Windows {
			if window.ResetAt <= now {
				continue
			}
			summary := byWindow[name]
			used := window.Users[user]
			if used < 0 || summary.consumed > math.MaxInt64-used {
				return reportedQuotaSnapshot{}, errors.New("invalid quota consumption")
			}
			summary.consumed += used
			byWindow[name] = summary
		}
	}
	snapshot := reportedQuotaSnapshot{}
	if len(byWindow) == 0 {
		return snapshot, nil
	}
	for _, id := range r.ProviderPoolAuthIDs {
		weight, err := accountWeight(p, id)
		if err != nil {
			return reportedQuotaSnapshot{}, err
		}
		if snapshot.capacity > math.MaxInt64-weight {
			return reportedQuotaSnapshot{}, errors.New("quota pool capacity overflow")
		}
		snapshot.capacity += weight
	}
	names := make([]string, 0, len(byWindow))
	for name := range byWindow {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		summary := byWindow[name]
		selected, known := state.Accounts[r.AuthID].Windows[name]
		summary.selectedExhausted = known && selected.ResetAt > now && selected.Remaining <= 0
		snapshot.windows = append(snapshot.windows, summary)
	}
	return snapshot, nil
}
