//go:build linux && cgo

package main

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// PoolConfig chooses explicit request capacity or provider-reported quota shares.
// No provider quota is inferred from a subscription name or scheduler availability.
type PoolConfig map[string]PoolDefinition

type PoolDefinition struct {
	Provider              string   `yaml:"provider"`
	AccountSelectors      []string `yaml:"account_selectors"`
	name                  string
	CreditBalanceHeader   string            `yaml:"credit_balance_header"`
	Mode                  string            `yaml:"mode"`
	ReportedMaxAgeSeconds int64             `yaml:"reported_max_age_seconds"`
	AccountWeights        map[string]string `yaml:"account_weights"`
	Unit                  string            `yaml:"unit"`
	CapacityPerAccount    int64             `yaml:"capacity_per_account"`
	Accounts              map[string]int64  `yaml:"accounts"`
	WindowSeconds         int64             `yaml:"window_seconds"`
}

type PoolWindow struct {
	Start         int64            `json:"start"`
	WindowSeconds int64            `json:"window_seconds"`
	Consumed      int64            `json:"consumed"`
	Users         map[string]int64 `json:"users"`
}

type poolExhaustedError string

func (e poolExhaustedError) Error() string { return string(e) }
func (poolExhaustedError) StatusCode() int { return 429 }

var poolNow = time.Now

func validatePools(pools PoolConfig) error {
	for provider, p := range pools {
		if p.Provider != "" {
			if p.Mode != "reported" {
				return fmt.Errorf("named pool %s requires reported mode", provider)
			}
			if strings.TrimSpace(p.Provider) != p.Provider || len(p.AccountSelectors) == 0 {
				return fmt.Errorf("named pool %s requires provider and account_selectors", provider)
			}
			for _, selector := range p.AccountSelectors {
				if _, err := filepath.Match(selector, "probe"); err != nil || selector == "" {
					return fmt.Errorf("invalid account selector in %s", provider)
				}
			}
		}
		if provider == "" || strings.TrimSpace(provider) != provider {
			return errors.New("invalid pool provider")
		}
		if p.Mode == "reported" {
			if err := validateReportedPool(p); err != nil {
				return fmt.Errorf("pool %s: %w", provider, err)
			}
			continue
		}
		if p.Mode != "" && p.Mode != "configured" {
			return fmt.Errorf("pool %s: invalid mode", provider)
		}
		if p.Unit != "" && p.Unit != "requests" {
			return fmt.Errorf("pool %s: only request units are supported", provider)
		}
		if p.WindowSeconds <= 0 || p.CapacityPerAccount < 0 || (p.CapacityPerAccount == 0 && len(p.Accounts) == 0) {
			return fmt.Errorf("pool %s requires positive capacity and window_seconds", provider)
		}
		for id, capacity := range p.Accounts {
			if id == "" || strings.TrimSpace(id) != id || capacity <= 0 {
				return fmt.Errorf("pool %s has invalid account capacity", provider)
			}
		}
	}
	return nil
}

func poolPercent(s string) (*big.Rat, error) {
	p, err := usd(s)
	if err != nil {
		return nil, err
	}
	if p.Cmp(big.NewRat(100, 1)) > 0 {
		return nil, errors.New("pool share must not exceed 100 percent")
	}
	return p, nil
}

// reservePool runs inside the same ledger transaction as USD admission. Each
// real upstream attempt consumes one request, even if its result is unsuccessful.
func reservePool(l *Ledger, pools PoolConfig, r pluginapi.RequestInterceptRequest, userID string, u User) error {
	p, ok := pools[r.Provider]
	if !ok {
		return nil
	}
	if err := validatePools(PoolConfig{r.Provider: p}); err != nil {
		return err
	}
	if p.Mode == "reported" {
		return reserveReportedPool(l, p, r, userID, u)
	}
	share, err := poolPercent(u.PoolShares[poolName(p, r.Provider)])
	if err != nil {
		return errors.New("provider pool share is missing or invalid")
	}
	if share.Sign() == 0 {
		return poolExhaustedError("provider pool share exhausted")
	}
	seen := map[string]bool{}
	var total int64
	for _, id := range r.ProviderPoolAuthIDs {
		if id == "" || seen[id] {
			return errors.New("invalid trusted provider pool inventory")
		}
		seen[id] = true
		capacity, exists := p.Accounts[id]
		if !exists {
			capacity = p.CapacityPerAccount
		}
		if capacity <= 0 {
			return errors.New("enabled provider account has no configured capacity")
		}
		if total > math.MaxInt64-capacity {
			return errors.New("provider pool capacity overflow")
		}
		total += capacity
	}
	if total == 0 || !seen[r.AuthID] {
		return errors.New("selected credential is absent from enabled provider pool")
	}
	now := poolNow().Unix()
	if now < 0 {
		return errors.New("invalid pool clock")
	}
	w := l.Pools[poolName(p, r.Provider)]
	// Changing the window never refreshes an existing allowance early. Clock
	// regression also retains existing usage until its original window expires.
	if w.WindowSeconds != 0 {
		if w.Start < 0 || w.WindowSeconds <= 0 || w.Start > math.MaxInt64-w.WindowSeconds || w.Consumed < 0 {
			return errors.New("invalid persisted pool window")
		}
		if now < w.Start+w.WindowSeconds && w.WindowSeconds != p.WindowSeconds {
			return errors.New("pool reset window cannot change before expiry")
		}
	}
	if w.WindowSeconds == 0 || now >= w.Start+w.WindowSeconds {
		w = PoolWindow{Start: now - now%p.WindowSeconds, WindowSeconds: p.WindowSeconds, Users: map[string]int64{}}
	}
	if w.Users == nil {
		w.Users = map[string]int64{}
	}
	consumer := poolConsumer(p, userID)
	consumed := w.Users[consumer]
	if consumed < 0 {
		return errors.New("invalid persisted user pool consumption")
	}
	if w.Consumed >= total {
		return poolExhaustedError("provider capacity pool exhausted")
	}
	if consumed == math.MaxInt64 {
		return errors.New("user pool consumption overflow")
	}
	next := new(big.Rat).SetInt64(consumed + 1)
	allowance := new(big.Rat).Mul(share, new(big.Rat).SetInt64(total))
	allowance.Quo(allowance, big.NewRat(100, 1))
	if next.Cmp(allowance) > 0 {
		return poolExhaustedError("user provider pool share exhausted")
	}
	w.Consumed++
	w.Users[consumer] = consumed + 1
	if l.Pools == nil {
		l.Pools = map[string]PoolWindow{}
	}
	l.Pools[poolName(p, r.Provider)] = w
	return nil
}
