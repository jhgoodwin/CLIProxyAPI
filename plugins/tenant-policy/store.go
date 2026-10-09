//go:build linux && cgo

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
)

type AccountAlias struct {
	Provider string `json:"provider"`
	AuthID   string `json:"auth_id"`
}
type NamedPoolSnapshot struct {
	Provider         string   `json:"provider"`
	AccountSelectors []string `json:"account_selectors"`
}
type Ledger struct {
	NamedPools    map[string]NamedPoolSnapshot `json:"named_pools,omitempty"`
	PoolMembers   map[string]map[string]bool   `json:"pool_members,omitempty"`
	Accounts      map[string]AccountAlias      `json:"accounts,omitempty"`
	CreditPools   map[string]CreditPoolState   `json:"credit_pools,omitempty"`
	ResetAudit    []QuotaResetAudit            `json:"reset_audit,omitempty"`
	Users         map[string]User              `json:"users"`
	Keys          map[string]Key               `json:"keys"`
	Debits        map[string]Debit             `json:"debits"`
	Pools         map[string]PoolWindow        `json:"pools,omitempty"`
	ReportedPools map[string]ReportedPoolState `json:"reported_pools,omitempty"`
	Metrics       map[string]UsageMetric       `json:"metrics,omitempty"`
}
type User struct {
	Providers      []string          `json:"providers"`
	CreditShares   map[string]string `json:"credit_shares,omitempty"`
	PoolShares     map[string]string `json:"pool_shares,omitempty"`
	LimitUSD       string            `json:"limit_usd"`
	SpentUSD       string            `json:"spent_usd"`
	Disabled       bool              `json:"disabled"`
	OverageBlocked bool              `json:"overage_blocked,omitempty"`
}
type Key struct {
	Label   string `json:"label,omitempty"`
	User    string `json:"user"`
	Revoked bool   `json:"revoked"`
}
type Debit struct {
	User          string          `json:"user"`
	AmountUSD     string          `json:"amount_usd"`
	InputUSD      string          `json:"input_usd,omitempty"`
	OutputUSD     string          `json:"output_usd,omitempty"`
	ActualUSD     string          `json:"actual_usd,omitempty"`
	Model         string          `json:"model,omitempty"`
	Provider      string          `json:"provider,omitempty"`
	Settled       bool            `json:"settled,omitempty"`
	Events        map[string]bool `json:"events,omitempty"`
	KeyHash       string          `json:"key_hash,omitempty"`
	EstimateRates MetricRates     `json:"estimate_rates,omitempty"`
}

func emptyLedger() *Ledger {
	return &Ledger{NamedPools: map[string]NamedPoolSnapshot{}, PoolMembers: map[string]map[string]bool{}, Accounts: map[string]AccountAlias{}, CreditPools: map[string]CreditPoolState{}, Users: map[string]User{}, Keys: map[string]Key{}, Debits: map[string]Debit{}, Pools: map[string]PoolWindow{}, ReportedPools: map[string]ReportedPoolState{}, Metrics: map[string]UsageMetric{}}
}

// withLedger serializes all transactions across processes and writes mutations atomically.
func withLedger(path string, mutate bool, fn func(*Ledger) error) error {
	if !filepath.IsAbs(path) {
		return errors.New("state path must be absolute")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
		return errors.New("ledger parent must be a real directory")
	}
	if dirInfo.Mode().Perm()&0077 != 0 {
		return errors.New("ledger parent permissions must be 0700")
	}
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("ledger must not be a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lockPath := path + ".lock"
	if fi, err := os.Lstat(lockPath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("ledger lock must not be a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := lf.Close(); closeErr != nil {
			logrus.Errorf("close ledger lock: %v", closeErr)
		}
	}()
	if err = lf.Chmod(0600); err != nil {
		return err
	}
	if err = unix.Flock(int(lf.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer func() {
		if unlockErr := unix.Flock(int(lf.Fd()), unix.LOCK_UN); unlockErr != nil {
			logrus.Errorf("unlock ledger: %v", unlockErr)
		}
	}()
	fi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("ledger is not initialized; run init")
		}
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return errors.New("ledger must be a regular file")
	}
	if fi.Mode().Perm()&0077 != 0 {
		return errors.New("ledger permissions must be 0600")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var ledger Ledger
	if err = json.Unmarshal(b, &ledger); err != nil {
		return fmt.Errorf("decode ledger: %w", err)
	}
	if ledger.Users == nil {
		ledger.Users = map[string]User{}
	}
	if ledger.Keys == nil {
		ledger.Keys = map[string]Key{}
	}
	if ledger.Debits == nil {
		ledger.Debits = map[string]Debit{}
	}
	if ledger.Pools == nil {
		ledger.Pools = map[string]PoolWindow{}
	}
	if ledger.ReportedPools == nil {
		ledger.ReportedPools = map[string]ReportedPoolState{}
	}
	if ledger.Metrics == nil {
		ledger.Metrics = map[string]UsageMetric{}
	}
	// Work on a decoded private copy and persist only after a successful callback.
	working := cloneLedger(&ledger)
	if err = fn(working); err != nil {
		return err
	}
	if !mutate {
		return nil
	}
	b, err = json.MarshalIndent(working, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(dir, ".tenant-ledger-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if removeErr := os.Remove(tmpName); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			logrus.Errorf("remove temporary ledger: %v", removeErr)
		}
	}()
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(b)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmpName, path); err != nil {
		return err
	}
	df, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := df.Close(); closeErr != nil {
			logrus.Errorf("close ledger directory: %v", closeErr)
		}
	}()
	return df.Sync()
}

func cloneLedger(in *Ledger) *Ledger {
	out := emptyLedger()
	for name, p := range in.NamedPools {
		p.AccountSelectors = append([]string(nil), p.AccountSelectors...)
		out.NamedPools[name] = p
	}
	for pool, members := range in.PoolMembers {
		copy := map[string]bool{}
		for id, yes := range members {
			copy[id] = yes
		}
		out.PoolMembers[pool] = copy
	}
	for name, a := range in.Accounts {
		out.Accounts[name] = a
	}
	out.ResetAudit = append([]QuotaResetAudit(nil), in.ResetAudit...)
	for provider, pool := range in.CreditPools {
		accounts := map[string]CreditAccountState{}
		for id, a := range pool.Accounts {
			a.Attempts = cloneCounts(a.Attempts)
			users := map[string]string{}
			for user, used := range a.Users {
				users[user] = used
			}
			a.Users = users
			accounts[id] = a
		}
		pool.Accounts = accounts
		out.CreditPools[provider] = pool
	}
	for k, v := range in.Users {
		if v.CreditShares != nil {
			shares := map[string]string{}
			for p, s := range v.CreditShares {
				shares[p] = s
			}
			v.CreditShares = shares
		}
		v.Providers = append([]string(nil), v.Providers...)
		if v.PoolShares != nil {
			shares := make(map[string]string, len(v.PoolShares))
			for provider, share := range v.PoolShares {
				shares[provider] = share
			}
			v.PoolShares = shares
		}
		out.Users[k] = v
	}
	for k, v := range in.Keys {
		out.Keys[k] = v
	}
	for k, v := range in.Debits {
		if v.Events != nil {
			events := make(map[string]bool, len(v.Events))
			for event, seen := range v.Events {
				events[event] = seen
			}
			v.Events = events
		}
		out.Debits[k] = v
	}
	for k, v := range in.Pools {
		if v.Users != nil {
			users := make(map[string]int64, len(v.Users))
			for user, consumed := range v.Users {
				users[user] = consumed
			}
			v.Users = users
		}
		out.Pools[k] = v
	}
	for provider, pool := range in.ReportedPools {
		if pool.Seeds != nil {
			seeds := make(map[string]ReportedQuotaSeed, len(pool.Seeds))
			for id, seed := range pool.Seeds {
				signals := make(map[string]string, len(seed.Signals))
				for key, value := range seed.Signals {
					signals[key] = value
				}
				seed.Signals = signals
				seeds[id] = seed
			}
			pool.Seeds = seeds
		}
		if pool.Accounts != nil {
			accounts := make(map[string]ReportedAccountState, len(pool.Accounts))
			for id, account := range pool.Accounts {
				if account.Windows != nil {
					windows := make(map[string]ReportedWindowState, len(account.Windows))
					for key, window := range account.Windows {
						window.Attempts = cloneCounts(window.Attempts)
						if window.Users != nil {
							users := make(map[string]int64, len(window.Users))
							for user, consumed := range window.Users {
								users[user] = consumed
							}
							window.Users = users
						}
						windows[key] = window
					}
					account.Windows = windows
				}
				accounts[id] = account
			}
			pool.Accounts = accounts
		}
		out.ReportedPools[provider] = pool
	}
	for id, metric := range in.Metrics {
		out.Metrics[id] = metric
	}
	return out
}

func KeyHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// usd accepts canonical, non-negative base-10 USD values without exponent notation.
func usd(s string) (*big.Rat, error) {
	if s == "" || strings.TrimSpace(s) != s || strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-") || strings.ContainsAny(s, "eE") {
		return nil, errors.New("USD amount must be a non-negative decimal")
	}
	parts := strings.Split(s, ".")
	if len(parts) > 2 || parts[0] == "" {
		return nil, errors.New("invalid USD decimal")
	}
	for _, p := range parts {
		if p == "" {
			return nil, errors.New("invalid USD decimal")
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return nil, errors.New("invalid USD decimal")
			}
		}
	}
	if len(parts[0]) > 1 && parts[0][0] == '0' {
		return nil, errors.New("USD decimal is not canonical")
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, errors.New("invalid USD decimal")
	}
	return r, nil
}

// usdString emits an exact finite base-10 representation, trimming insignificant zeroes.
func usdString(r *big.Rat) string {
	if r == nil || r.Sign() == 0 {
		return "0"
	}
	n, d := new(big.Int).Set(r.Num()), new(big.Int).Set(r.Denom())
	twos, fives := 0, 0
	for new(big.Int).Mod(d, big.NewInt(2)).Sign() == 0 {
		d.Div(d, big.NewInt(2))
		twos++
	}
	for new(big.Int).Mod(d, big.NewInt(5)).Sign() == 0 {
		d.Div(d, big.NewInt(5))
		fives++
	}
	if d.Cmp(big.NewInt(1)) != 0 {
		return r.FloatString(18)
	}
	scale := twos
	if fives > scale {
		scale = fives
	}
	if twos < scale {
		n.Mul(n, new(big.Int).Exp(big.NewInt(2), big.NewInt(int64(scale-twos)), nil))
	}
	if fives < scale {
		n.Mul(n, new(big.Int).Exp(big.NewInt(5), big.NewInt(int64(scale-fives)), nil))
	}
	neg := n.Sign() < 0
	n.Abs(n)
	digits := n.String()
	if scale > 0 {
		for len(digits) <= scale {
			digits = "0" + digits
		}
		digits = digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
		digits = strings.TrimRight(strings.TrimRight(digits, "0"), ".")
	}
	if neg {
		return "-" + digits
	}
	return digits
}

func cloneCounts(in map[string]int64) map[string]int64 {
	if in == nil {
		return nil
	}
	out := map[string]int64{}
	for k, v := range in {
		out[k] = v
	}
	return out
}
