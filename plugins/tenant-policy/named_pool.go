//go:build linux && cgo

package main

import (
	"errors"
	"path/filepath"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func poolName(p PoolDefinition, provider string) string {
	if p.name != "" {
		return p.name
	}
	return provider
}
func poolConsumer(p PoolDefinition, user string) string { return user }
func accountInPool(l *Ledger, p PoolDefinition, id string) bool {
	return p.Provider == "" || l.PoolMembers[p.name][id]
}
func poolProvider(name string, p PoolDefinition) string {
	if p.Provider != "" {
		return p.Provider
	}
	return name
}
func providerHasPool(pools PoolConfig, provider string) bool {
	for name, p := range pools {
		if poolProvider(name, p) == provider {
			return true
		}
	}
	return false
}
func providerHasReportedPool(pools PoolConfig, provider string) bool {
	for name, p := range pools {
		if poolProvider(name, p) == provider && p.Mode == "reported" {
			return true
		}
	}
	return false
}
func modelPool(c policyConfig, model, provider string) string {
	m := resolveModel(c, model, provider)
	return m.Pool
}
func resolveModel(c policyConfig, model, provider string) modelPolicy {
	if m, ok := c.Models[model]; ok && m.Provider == provider {
		return m
	}
	var found modelPolicy
	count := 0
	for _, m := range c.Models {
		if m.UpstreamModel == model && m.Provider == provider {
			found = m
			count++
		}
	}
	if count == 1 {
		return found
	}

	return modelPolicy{}
}

// Names bind permanently to provider credentials. Changing a pool's selectors
// is the explicit operation that changes membership, without rewriting history.
func setAccountAlias(l *Ledger, args []string) error {
	if len(args) != 3 {
		return errors.New("usage: account-set NAME PROVIDER AUTH_ID")
	}
	for _, s := range args {
		if s == "" || strings.TrimSpace(s) != s || strings.ContainsAny(s, "\x00\r\n") {
			return errors.New("invalid account identifier")
		}
	}
	if strings.ContainsAny(args[0], "*?[]") {
		return errors.New("account names cannot contain glob characters")
	}
	a := AccountAlias{Provider: args[1], AuthID: args[2]}
	if old, ok := l.Accounts[args[0]]; ok && old != a {
		return errors.New("account name is already bound to another credential")
	}
	for name, old := range l.Accounts {
		if name != args[0] && old == a {
			return errors.New("credential already has an account name")
		}
	}
	if l.Accounts == nil {
		l.Accounts = map[string]AccountAlias{}
	}
	l.Accounts[args[0]] = a
	return nil
}

func poolRequest(l *Ledger, p PoolDefinition, r pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptRequest, error) {
	if p.Provider == "" {
		return r, nil
	}
	matched := map[string]bool{}
	for name, a := range l.Accounts {
		if a.Provider != r.Provider {
			continue
		}
		for _, pattern := range p.AccountSelectors {
			ok, err := filepath.Match(pattern, name)
			if err != nil {
				return r, err
			}
			if ok {
				matched[a.AuthID] = true
				break
			}
		}
	}
	ids := make([]string, 0, len(r.ProviderPoolAuthIDs))
	for _, id := range r.ProviderPoolAuthIDs {
		if matched[id] {
			ids = append(ids, id)
		}
	}
	selected := false
	for _, id := range ids {
		if id == r.AuthID {
			selected = true
		}
	}
	if !selected {
		return r, poolExhaustedError("selected account is outside granted pool")
	}
	r.ProviderPoolAuthIDs = ids
	return r, nil
}

func reserveResource(l *Ledger, pools PoolConfig, r pluginapi.RequestInterceptRequest, id string, u User, resource, explicitPool string) error {
	names := []string{}
	for name, p := range pools {
		if poolProvider(name, p) == r.Provider && (explicitPool == "" || name == explicitPool) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		if explicitPool != "" {
			return errors.New("model pool unavailable")
		}
		return nil
	}
	sort.Strings(names)
	var last error
	for _, name := range names {
		p := pools[name]
		p.name = name
		scoped, err := poolRequest(l, p, r)
		if err != nil {
			last = err
			continue
		}
		prepared, err := prepareResourceTrial(l, p, scoped)
		if err != nil {
			return err
		}
		var probe *Ledger
		switch resource {
		case "", "renewing":
			probe, err = tryResource(prepared, p, scoped, id, u, false)
		case "credits":
			probe, err = tryResource(prepared, p, scoped, id, u, true)
		case "auto":
			probe, err = tryResource(prepared, p, scoped, id, u, false)
			var exhausted poolExhaustedError
			if errors.As(err, &exhausted) && p.Mode == "reported" && actualRenewingExhausted(prepared, scoped) {
				probe, err = tryResource(prepared, p, scoped, id, u, true)
			}
		default:
			return errors.New("invalid quota resource")
		}
		if err == nil {
			*l = *probe
			return nil
		}
		var exhausted poolExhaustedError
		if !errors.As(err, &exhausted) {
			return err
		}
		last = err
	}
	return last
}

// Prepare membership and observations once, before trying either resource.
// Each resource starts from this same baseline; failed trials cannot leak spending.
func prepareResourceTrial(l *Ledger, p PoolDefinition, r pluginapi.RequestInterceptRequest) (*Ledger, error) {
	prepared := cloneLedger(l)
	if p.Provider != "" {
		if prepared.PoolMembers[p.name] == nil {
			prepared.PoolMembers[p.name] = map[string]bool{}
		}
		for _, account := range r.ProviderPoolAuthIDs {
			prepared.PoolMembers[p.name][account] = true
		}
	}
	if p.Mode == "reported" {
		if err := updateReportedQuota(prepared, p, r); err != nil {
			return nil, err
		}
		if err := updateCreditQuota(prepared, p, r); err != nil {
			return nil, err
		}
	}
	return prepared, nil
}

func tryResource(prepared *Ledger, p PoolDefinition, r pluginapi.RequestInterceptRequest, id string, u User, credits bool) (*Ledger, error) {
	trial := cloneLedger(prepared)
	var err error
	if credits {
		if p.Mode != "reported" {
			return nil, errors.New("credits require reported pool")
		}
		err = reserveCreditPool(trial, p, r, id, u)
	} else {
		err = reservePool(trial, PoolConfig{r.Provider: p}, r, id, u)
	}
	if err != nil {
		return nil, err
	}
	return trial, nil
}
