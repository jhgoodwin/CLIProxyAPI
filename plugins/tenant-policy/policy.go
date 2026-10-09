//go:build linux && cgo

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

type modelPolicy struct {
	Pool            string `yaml:"pool"`
	QuotaResource   string `yaml:"quota_resource"`
	PoolOnly        bool   `yaml:"pool_only"`
	Provider        string `yaml:"provider"`
	UpstreamModel   string `yaml:"upstream_model"`
	PriceKey        string `yaml:"price_key"`
	InputAllowance  int64  `yaml:"input_allowance_tokens"`
	OutputAllowance int64  `yaml:"output_allowance_tokens"`
	InputUSD        string `yaml:"input_usd_per_token"`
	OutputUSD       string `yaml:"output_usd_per_token"`
	charge          string
	estimateRates   MetricRates
}
type policyConfig struct {
	Mode     string                 `yaml:"mode"`
	Enabled  bool                   `yaml:"enabled"`
	Priority int                    `yaml:"priority"`
	Required bool                   `yaml:"required"`
	State    string                 `yaml:"state"`
	Prices   string                 `yaml:"prices"`
	Models   map[string]modelPolicy `yaml:"models"`
	Pools    PoolConfig             `yaml:"pools"`
}

var configState struct {
	sync.RWMutex
	cfg policyConfig
}

func configure(raw []byte) error {
	c, err := decodePolicyConfig(raw)
	if err != nil {
		return err
	}
	catalog, err := loadPolicyDependencies(c)
	if err != nil {
		return err
	}
	models := make(map[string]modelPolicy, len(c.Models))
	for alias, model := range c.Models {
		var compiled modelPolicy
		var err error
		if c.Mode == "identity" {
			compiled, err = compileIdentityModel(alias, model, catalog)
		} else {
			compiled, err = compileModelPolicy(alias, model, c.Pools, catalog)
		}
		if err != nil {
			return err
		}
		models[alias] = compiled
	}
	c.Models = models
	if err := persistNamedPoolSnapshots(c.State, c.Pools); err != nil {
		return err
	}
	// Publish only the complete configuration after its reset bindings are durable.
	configState.Lock()
	configState.cfg = c
	configState.Unlock()
	return nil
}

func decodePolicyConfig(raw []byte) (policyConfig, error) {
	var r struct {
		ConfigYAML    []byte `json:"config_yaml"`
		SchemaVersion uint32 `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return policyConfig{}, err
	}
	if r.SchemaVersion < 7 {
		return policyConfig{}, errors.New("tenant policy requires host schema version 7 or newer")
	}
	var c policyConfig
	d := yaml.NewDecoder(strings.NewReader(string(r.ConfigYAML)))
	d.KnownFields(true)
	if err := d.Decode(&c); err != nil {
		return policyConfig{}, err
	}
	if c.Mode != "" && c.Mode != "quota" && c.Mode != "identity" {
		return policyConfig{}, errors.New("mode must be quota or identity")
	}
	if c.Mode == "identity" && len(c.Pools) != 0 {
		return policyConfig{}, errors.New("identity mode does not enforce quota pools")
	}
	if !filepath.IsAbs(c.State) || (c.Mode != "identity" && len(c.Models) == 0) {
		return policyConfig{}, errors.New("absolute state and explicit models are required")
	}
	return c, nil
}

func loadPolicyDependencies(c policyConfig) (map[string]map[string]json.RawMessage, error) {
	if err := withLedger(c.State, false, func(*Ledger) error { return nil }); err != nil {
		return nil, err
	}
	var catalog map[string]map[string]json.RawMessage
	if c.Prices != "" {
		if !filepath.IsAbs(c.Prices) {
			return nil, errors.New("prices path must be absolute")
		}
		b, err := os.ReadFile(c.Prices)
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &catalog); err != nil {
			return nil, err
		}
	}
	if err := validatePools(c.Pools); err != nil {
		return nil, err
	}
	return catalog, nil
}

// Identity mappings authorize routing and optionally estimate text usage. They
// deliberately carry no reservation tariff or provider quota enforcement.
func compileIdentityModel(alias string, m modelPolicy, catalog map[string]map[string]json.RawMessage) (modelPolicy, error) {
	if alias == "" || m.Provider == "" || m.UpstreamModel == "" {
		return modelPolicy{}, fmt.Errorf("invalid model mapping %q", alias)
	}
	if m.Pool != "" || m.PoolOnly || m.QuotaResource != "" || m.InputAllowance != 0 || m.OutputAllowance != 0 {
		return modelPolicy{}, fmt.Errorf("identity model %s cannot configure quota allowances", alias)
	}
	if (m.InputUSD == "") != (m.OutputUSD == "") {
		return modelPolicy{}, fmt.Errorf("both override rates required: %s", alias)
	}
	entry := catalog[m.PriceKey]
	if m.PriceKey != "" && entry == nil {
		return modelPolicy{}, fmt.Errorf("missing price key: %s", m.PriceKey)
	}
	var err error
	if m.InputUSD != "" {
		m.estimateRates, err = metricRates(nil, m.InputUSD, m.OutputUSD)
	} else {
		m.estimateRates, err = metricRates(entry, "", "")
	}
	if err != nil {
		return modelPolicy{}, fmt.Errorf("metrics price %s: %w", alias, err)
	}
	return m, nil
}

// compileModelPolicy returns a complete policy without changing shared state.
// Pool-only policies need estimates when available, but do not require a USD tariff.
func compileModelPolicy(alias string, m modelPolicy, pools PoolConfig, catalog map[string]map[string]json.RawMessage) (modelPolicy, error) {
	if m.QuotaResource != "" && m.QuotaResource != "renewing" && m.QuotaResource != "credits" && m.QuotaResource != "auto" {
		return modelPolicy{}, fmt.Errorf("invalid quota_resource for %s", alias)
	}
	if m.QuotaResource != "" && m.QuotaResource != "renewing" {
		if !providerHasReportedPool(pools, m.Provider) {
			return modelPolicy{}, fmt.Errorf("credit resource requires reported pool for %s", alias)
		}
	}
	if m.Pool != "" {
		p, ok := pools[m.Pool]
		if !ok || poolProvider(m.Pool, p) != m.Provider || ((m.QuotaResource == "credits" || m.QuotaResource == "auto") && p.Mode != "reported") {
			return modelPolicy{}, fmt.Errorf("invalid pool for %s", alias)
		}
	}
	if alias == "" || m.Provider == "" || m.UpstreamModel == "" {
		return modelPolicy{}, fmt.Errorf("invalid model mapping %q", alias)
	}
	if (m.InputUSD == "") != (m.OutputUSD == "") {
		return modelPolicy{}, fmt.Errorf("both override rates required: %s", alias)
	}
	entry := catalog[m.PriceKey]
	if m.PriceKey != "" && entry == nil {
		return modelPolicy{}, fmt.Errorf("missing price key: %s", m.PriceKey)
	}
	var rateErr error
	// Dashboard estimates use ordinary LiteLLM rates, independent of conservative tariffs.
	if m.InputUSD != "" {
		m.estimateRates, rateErr = metricRates(nil, m.InputUSD, m.OutputUSD)
	} else {
		m.estimateRates, rateErr = metricRates(entry, "", "")
	}
	if rateErr != nil {
		return modelPolicy{}, fmt.Errorf("metrics price %s: %w", alias, rateErr)
	}
	if m.PoolOnly {
		if !providerHasPool(pools, m.Provider) {
			return modelPolicy{}, fmt.Errorf("pool_only model %s has no provider pool", alias)
		}
		return m, nil
	}
	if m.InputAllowance <= 0 || m.OutputAllowance <= 0 {
		return modelPolicy{}, fmt.Errorf("positive token allowances required for %s", alias)
	}
	if m.InputUSD == "" {
		if entry == nil {
			return modelPolicy{}, fmt.Errorf("missing price key: %s", m.PriceKey)
		}
		var err error
		m.InputUSD, m.OutputUSD, err = catalogRates(entry)
		if err != nil {
			return modelPolicy{}, fmt.Errorf("price %s: %w", alias, err)
		}
	}
	in, err := usd(m.InputUSD)
	if err != nil {
		return modelPolicy{}, err
	}
	out, err := usd(m.OutputUSD)
	if err != nil {
		return modelPolicy{}, err
	}
	charge := new(big.Rat).Add(new(big.Rat).Mul(in, new(big.Rat).SetInt64(m.InputAllowance)), new(big.Rat).Mul(out, new(big.Rat).SetInt64(m.OutputAllowance)))
	if charge.Sign() <= 0 {
		return modelPolicy{}, fmt.Errorf("positive charge required for %s", alias)
	}
	m.charge = usdString(charge)
	return m, nil
}

func persistNamedPoolSnapshots(state string, pools PoolConfig) error {
	return withLedger(state, true, func(l *Ledger) error {
		snapshots := map[string]NamedPoolSnapshot{}
		for name, p := range pools {
			if p.Provider != "" {
				snapshots[name] = NamedPoolSnapshot{Provider: p.Provider, AccountSelectors: append([]string(nil), p.AccountSelectors...)}
			}
		}
		l.NamedPools = snapshots
		return nil
	})
}

func priceDecimal(raw json.RawMessage) (string, error) {
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return "", err
	}
	r, ok := new(big.Rat).SetString(string(n))
	if !ok || r.Sign() < 0 {
		return "", errors.New("invalid rate")
	}
	return usdString(r), nil
}
func configSnapshot() policyConfig {
	configState.RLock()
	defer configState.RUnlock()
	return configState.cfg
}
func principal(hash string) string { return "tenant-policy:" + hash }
func userForScope(l *Ledger, scope string) (string, User, error) {
	if scope != "" {
		for hash, k := range l.Keys {
			if session.CallerScope(principal(hash)) == scope && !k.Revoked {
				if u, ok := l.Users[k.User]; ok && !u.Disabled {
					return k.User, u, nil
				}
			}
		}
	}
	return "", User{}, errors.New("invalid or revoked tenant key")
}
func allowed(u User, p string) bool {
	for _, a := range u.Providers {
		if a == p {
			return true
		}
	}
	return false
}
func reject(status int, msg string) ([]byte, error) {
	body, _ := json.Marshal(map[string]any{"error": map[string]string{"type": "tenant_policy", "message": msg}})
	return okEnvelope(pluginapi.RequestInterceptResponse{Terminate: true, StatusCode: status, ResponseHeaders: http.Header{"Content-Type": {"application/json"}}, ResponseBody: body})
}
func handleMethod(method string, raw []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if err := configure(raw); err != nil {
			return nil, err
		}
		return okEnvelope(map[string]any{"schema_version": pluginabi.SchemaVersion, "metadata": pluginapi.Metadata{Name: "tenant-policy", Version: "0.1.0", Author: "jhgoodwin", GitHubRepository: "https://github.com/jhgoodwin/CLIProxyAPI"}, "capabilities": map[string]bool{"frontend_auth_provider": true, "frontend_auth_provider_exclusive": true, "request_interceptor": true, "request_interceptor_required": true, "scheduler": true, "scheduler_across_priorities": true, "usage_plugin": true}})
	case pluginabi.MethodFrontendAuthIdentifier:
		return okEnvelope(map[string]string{"Identifier": "tenant-policy"})
	case pluginabi.MethodFrontendAuthAuthenticate:
		var r pluginapi.FrontendAuthRequest
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		var hash string
		if r.VerifiedPrincipal != "" {
			// This field is host-only, from a locally validated realtime secret.
			// Recheck the issuer's ledger entry; never accept a hash as a Bearer key.
			if configSnapshot().Mode != "identity" || !identityCodexPath(r.Path) || !strings.HasPrefix(r.VerifiedPrincipal, "tenant-policy:") {
				return okEnvelope(pluginapi.FrontendAuthResponse{})
			}
			hash = strings.TrimPrefix(r.VerifiedPrincipal, "tenant-policy:")
		} else {
			// Never accept query keys, alternate headers, or multiple credentials.
			if len(r.Headers.Values("Authorization")) != 1 {
				return okEnvelope(pluginapi.FrontendAuthResponse{})
			}
			parts := strings.Fields(r.Headers.Get("Authorization"))
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				return okEnvelope(pluginapi.FrontendAuthResponse{})
			}
			hash = KeyHash(parts[1])
		}
		accepted := false
		err := withLedger(configSnapshot().State, false, func(l *Ledger) error {
			k, ok := l.Keys[hash]
			if ok && !k.Revoked {
				u, exists := l.Users[k.User]
				accepted = exists && !u.Disabled
				if accepted && identityCodexPath(r.Path) {
					// These routes select Codex outside request interception and
					// cannot enforce an optional per-model identity restriction.
					c := configSnapshot()
					accepted = c.Mode == "identity" && len(c.Models) == 0 && allowed(u, "codex")
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		response := pluginapi.FrontendAuthResponse{Authenticated: accepted}
		if accepted {
			response.Principal = principal(hash)
		}
		return okEnvelope(response)
	case pluginabi.MethodSchedulerPick:
		var r pluginapi.SchedulerPickRequest
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		c := configSnapshot()

		selected := ""
		var quotaErr error
		scope, _ := r.Options.Metadata["caller_scope"].(string)
		err := withLedger(c.State, true, func(l *Ledger) error {
			id, u, err := userForScope(l, scope)
			if err != nil {
				return err
			}
			if u.OverageBlocked && c.Mode != "identity" {
				return errors.New("tenant observed usage exceeded allowance; administrator review required")
			}

			for _, a := range r.Candidates {
				if !allowed(u, a.Provider) {
					continue
				}
				if c.Mode != "identity" && providerHasPool(c.Pools, a.Provider) {
					observations := r.ProviderPools[a.Provider]
					ids := make([]string, 0, len(observations))
					seen := map[string]bool{}
					for _, observation := range observations {
						if !seen[observation.AuthID] {
							ids = append(ids, observation.AuthID)
							seen[observation.AuthID] = true
						}
					}
					for _, candidate := range r.Candidates {
						if candidate.Provider == a.Provider && !seen[candidate.ID] {
							ids = append(ids, candidate.ID)
							seen[candidate.ID] = true
						}
					}
					requested, _ := r.Options.Metadata["requested_model"].(string)
					if requested == "" {
						requested = r.Model
					}
					resource := resolveModel(c, requested, a.Provider).QuotaResource
					probe := pluginapi.RequestInterceptRequest{Provider: a.Provider, AuthID: a.ID, ProviderPoolAuthIDs: ids, ProviderPoolQuota: observations}
					if err := refreshPoolFacts(l, c.Pools, probe, modelPool(c, requested, a.Provider), u, resource); err != nil {
						return err
					}
					// Selection never spends capacity. Actual admission repeats
					// this check under the ledger lock after auth selection.
					if err := reserveResource(cloneLedger(l), c.Pools, probe, id, u, resource, modelPool(c, requested, a.Provider)); err != nil {
						var exhausted poolExhaustedError
						if !errors.As(err, &exhausted) {
							return err
						}
						quotaErr = err
						continue
					}
				}
				selected = a.ID
				break
			}
			return nil
		})
		if err == nil && selected == "" && quotaErr != nil {
			status := 503
			var coded interface{ StatusCode() int }
			if errors.As(quotaErr, &coded) {
				status = coded.StatusCode()
			}
			return pluginabi.NewErrorEnvelope("provider_quota_exhausted", quotaErr.Error(), status)
		}
		if err != nil || selected == "" {
			return okEnvelope(pluginapi.SchedulerPickResponse{Handled: true, Reject: true, RejectReason: "tenant provider access denied or unavailable"})
		}
		return okEnvelope(pluginapi.SchedulerPickResponse{Handled: true, AuthID: selected})
	case pluginabi.MethodRequestInterceptBefore, pluginabi.MethodRequestInterceptAfter:
		return interceptRequest(method, raw)
	case pluginabi.MethodUsageHandle:
		var r pluginapi.UsageRecord
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		if err := settle(r); err != nil {
			return nil, err
		}
		return okEnvelope(struct{}{})
	default:
		return errorEnvelope("unknown_method", "unsupported method"), nil
	}
}

// admissionResult separates durable quota facts from speculative spending. A
// denial publishes no speculative state; an internal error aborts the transaction.
type admissionResult struct {
	Next   *Ledger
	Denial error
}

func interceptRequest(method string, raw []byte) ([]byte, error) {
	var r pluginapi.RequestInterceptRequest
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	c := configSnapshot()
	if c.Mode != "identity" {
		if err := validateTextPayload(r.Body); err != nil {
			return reject(403, err.Error())
		}
	}
	path, _ := r.Metadata["request_path"].(string)
	if c.Mode != "identity" && !quotaRequestPath(path) {
		return reject(403, "unsupported tenant request path")
	}
	name := r.RequestedModel
	if name == "" {
		name = r.Model
	}
	m, ok := c.Models[name]
	if !ok && (c.Mode != "identity" || len(c.Models) != 0) {
		return reject(403, "model lacks explicit tenant mapping")
	}
	if !ok && method == pluginabi.MethodRequestInterceptAfter {
		// Actual provider and normalized model are trusted host selection metadata.
		m = modelPolicy{Provider: r.Provider, UpstreamModel: r.Model, estimateRates: MetricRates{Source: "no pricing configured"}}
	}
	var result admissionResult
	err := withLedger(c.State, method == pluginabi.MethodRequestInterceptAfter, func(l *Ledger) error {
		id, u, err := authorizeAdmission(l, r, m, c.Mode == "identity")
		if err != nil || method == pluginabi.MethodRequestInterceptBefore {
			return err
		}
		if err := validateAttempt(l, r, m); err != nil {
			return err
		}
		if c.Mode == "identity" {
			rates := m.estimateRates
			if !quotaRequestPath(path) || validateTextPayload(r.Body) != nil {
				rates.Unsupported = true
				rates.Source += " (media or non-generation cost unknown)"
			}
			l.Debits[r.AttemptID] = Debit{IdentityOnly: true, User: id, KeyHash: strings.TrimPrefix(r.Principal, "tenant-policy:"), EstimateRates: rates, Provider: m.Provider, Model: m.UpstreamModel, AmountUSD: "0", Settled: true}
			return nil
		}
		if err := refreshPoolFacts(l, c.Pools, r, m.Pool, u, m.QuotaResource); err != nil {
			return err
		}
		result, err = evaluateAdmission(l, c.Pools, r, m, id, u)
		if err != nil {
			return err
		}
		if result.Next != nil {
			*l = *result.Next
		}
		return nil
	})
	if err == nil {
		err = result.Denial
	}
	if err != nil {
		status := 403
		var coded interface{ StatusCode() int }
		if errors.As(err, &coded) {
			status = coded.StatusCode()
		}
		return reject(status, err.Error())
	}
	return okEnvelope(pluginapi.RequestInterceptResponse{})
}

func authorizeAdmission(l *Ledger, r pluginapi.RequestInterceptRequest, m modelPolicy, identity bool) (string, User, error) {
	id, u, err := userForScope(l, session.CallerScope(r.Principal))
	if err != nil {
		return id, u, err
	}
	if u.OverageBlocked && !identity {
		return id, u, errors.New("tenant observed usage exceeded allowance; administrator review required")
	}
	if (m.Provider != "" || !identity) && !allowed(u, m.Provider) {
		return id, u, errors.New("provider access denied")
	}
	return id, u, nil
}

func validateAttempt(l *Ledger, r pluginapi.RequestInterceptRequest, m modelPolicy) error {
	if r.Provider == "" || r.Model == "" || r.Provider != m.Provider || r.Model != m.UpstreamModel {
		return errors.New("selected provider or upstream model differs from policy")
	}
	if r.AttemptID == "" {
		return errors.New("missing trusted execution attempt ID")
	}
	if _, exists := l.Debits[r.AttemptID]; exists {
		return errors.New("duplicate execution attempt")
	}
	return nil
}

// evaluateAdmission owns a private trial. The caller keeps its facts-only ledger
// on denial, and publishes Next only after every resource and USD check succeeds.
func evaluateAdmission(facts *Ledger, pools PoolConfig, r pluginapi.RequestInterceptRequest, m modelPolicy, id string, u User) (admissionResult, error) {
	next := cloneLedger(facts)
	if err := reserveResource(next, pools, r, id, u, m.QuotaResource, m.Pool); err != nil {
		var exhausted poolExhaustedError
		if errors.As(err, &exhausted) {
			return admissionResult{Denial: err}, nil
		}
		return admissionResult{}, err
	}
	debit := Debit{User: id, KeyHash: strings.TrimPrefix(r.Principal, "tenant-policy:"), EstimateRates: m.estimateRates, Provider: m.Provider, Model: m.UpstreamModel, AmountUSD: "0", Settled: true}
	if !m.PoolOnly {
		spent, err := usd(u.SpentUSD)
		if err != nil {
			return admissionResult{}, err
		}
		limit, err := usd(u.LimitUSD)
		if err != nil {
			return admissionResult{}, err
		}
		charge, err := usd(m.charge)
		if err != nil {
			return admissionResult{}, err
		}
		amount := new(big.Rat).Add(spent, charge)
		if amount.Cmp(limit) > 0 {
			return admissionResult{Denial: pluginabi.NewError("budget_exhausted", "tenant USD allowance exhausted", 429)}, nil
		}
		chargedUser := next.Users[id]
		chargedUser.SpentUSD = usdString(amount)
		next.Users[id] = chargedUser
		debit.AmountUSD = m.charge
		debit.Settled = false
		debit.InputUSD = m.InputUSD
		debit.OutputUSD = m.OutputUSD
		debit.Events = map[string]bool{}
	}
	next.Debits[r.AttemptID] = debit
	return admissionResult{Next: next}, nil
}

// settle aggregates idempotent usage events for an execution attempt. Unknown or failed
// usage never releases its reservation; delayed usage remains safe across restart.
func settle(r pluginapi.UsageRecord) error {
	if r.AttemptID == "" || r.RequestID == "" {
		return nil
	}
	d := r.Detail
	if d.InputTokens < 0 || d.OutputTokens < 0 || d.ReasoningTokens < 0 || d.CachedTokens < 0 || d.CacheReadTokens < 0 || d.CacheCreationTokens < 0 {
		return errors.New("invalid usage counters")
	}
	return withLedger(configSnapshot().State, true, func(l *Ledger) error {
		debit, ok := l.Debits[r.AttemptID]
		if !ok {
			return nil
		}
		if err := recordUsage(l, r, debit); err != nil {
			return err
		}
		if debit.IdentityOnly || r.Failed || debit.InputUSD == "" || debit.OutputUSD == "" || (d.InputTokens == 0 && d.OutputTokens == 0 && d.ReasoningTokens == 0 && d.CachedTokens == 0 && d.CacheReadTokens == 0 && d.CacheCreationTokens == 0) {
			return nil
		}
		if debit.Events[r.RequestID] {
			return nil
		}
		// Pricing snapshot belongs to the reservation, never to hot-reloaded config.
		if r.Provider != debit.Provider || r.Model != debit.Model || helps.IsModelSubstituted(debit.Model, r.ResponseModel) {
			u, exists := l.Users[debit.User]
			if !exists {
				return errors.New("reserved user missing")
			}
			u.OverageBlocked = true
			l.Users[debit.User] = u
			return nil
		}
		in, err := usd(debit.InputUSD)
		if err != nil {
			return err
		}
		out, err := usd(debit.OutputUSD)
		if err != nil {
			return err
		}
		// Cache and reasoning counters vary by protocol. Counting separately is conservative.
		input := new(big.Int).Add(big.NewInt(d.InputTokens), big.NewInt(d.CachedTokens))
		input.Add(input, big.NewInt(d.CacheReadTokens))
		input.Add(input, big.NewInt(d.CacheCreationTokens))
		output := new(big.Int).Add(big.NewInt(d.OutputTokens), big.NewInt(d.ReasoningTokens))
		amount := new(big.Rat).Add(new(big.Rat).Mul(in, new(big.Rat).SetInt(input)), new(big.Rat).Mul(out, new(big.Rat).SetInt(output)))
		prior := new(big.Rat)
		if debit.ActualUSD != "" {
			prior, err = usd(debit.ActualUSD)
			if err != nil {
				return err
			}
		}
		actual := new(big.Rat).Add(prior, amount)
		charged, err := usd(debit.AmountUSD)
		if err != nil {
			return err
		}
		u, ok := l.Users[debit.User]
		if !ok {
			return errors.New("reserved user missing")
		}
		// The admission tariff is immutable. Excess observed usage freezes future
		// requests, while diagnostics retain actual cost without rewriting the cap.
		if actual.Cmp(charged) > 0 {
			u.OverageBlocked = true
			l.Users[debit.User] = u
		}
		debit.ActualUSD = usdString(actual)
		debit.Settled = true
		if debit.Events == nil {
			debit.Events = map[string]bool{}
		}
		debit.Events[r.RequestID] = true
		l.Debits[r.AttemptID] = debit
		return nil
	})
}

func catalogRates(entry map[string]json.RawMessage) (string, string, error) {
	inText, err := priceDecimal(entry["input_cost_per_token"])
	if err != nil {
		return "", "", err
	}
	outText, err := priceDecimal(entry["output_cost_per_token"])
	if err != nil {
		return "", "", err
	}
	in, _ := usd(inText)
	out, _ := usd(outText)
	for k, v := range entry {
		if !strings.Contains(k, "cost") || k == "input_cost_per_token" || k == "output_cost_per_token" {
			continue
		}
		var n json.Number
		if json.Unmarshal(v, &n) != nil {
			return "", "", fmt.Errorf("invalid billing dimension %s", k)
		}
		rate, ok := new(big.Rat).SetString(string(n))
		if !ok || rate.Sign() < 0 {
			return "", "", fmt.Errorf("invalid billing dimension %s", k)
		}
		if rate.Sign() == 0 {
			continue
		}
		switch {
		case strings.HasPrefix(k, "input_cost_per_token_") || k == "cache_read_input_token_cost" || k == "cache_creation_input_token_cost":
			if rate.Cmp(in) > 0 {
				in.Set(rate)
			}
		case strings.HasPrefix(k, "output_cost_per_token_"):
			if rate.Cmp(out) > 0 {
				out.Set(rate)
			}
		default:
			return "", "", fmt.Errorf("unsupported billing dimension %s; explicit conservative rates required", k)
		}
	}
	return usdString(in), usdString(out), nil
}

// Text-only billing excludes hosted tools and media whose costs cannot be
// represented by the supported token-rate policy.
func validateTextPayload(body []byte) error {
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return errors.New("tenant requests require a JSON text payload")
	}
	var walk func(any) error
	walk = func(value any) error {
		switch v := value.(type) {
		case []any:
			for _, item := range v {
				if err := walk(item); err != nil {
					return err
				}
			}
		case map[string]any:
			for key, item := range v {
				switch key {
				case "parameters", "input_schema", "schema":
					continue
				}
				if key == "modalities" {
					a, ok := item.([]any)
					if !ok {
						return errors.New("invalid modalities")
					}
					for _, m := range a {
						if m != "text" {
							return errors.New("only text modality is supported")
						}
					}
				}
				if key == "tools" {
					a, ok := item.([]any)
					if !ok {
						return errors.New("invalid tools")
					}
					for _, tool := range a {
						m, ok := tool.(map[string]any)
						if !ok {
							return errors.New("invalid tool")
						}
						if kind, exists := m["type"]; exists && kind != "function" {
							return errors.New("hosted tools are outside tenant text billing")
						}
					}
				}
				if key == "type" {
					if kind, ok := item.(string); ok {
						switch kind {
						case "image", "image_url", "input_image", "input_audio", "audio", "video", "document", "file", "input_file":
							return errors.New("media payloads are outside tenant text billing")
						}
					}
				}
				if err := walk(item); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(payload)
}

// Strict quota mode remains limited to the audited token-billed text endpoints.
func quotaRequestPath(path string) bool {
	switch strings.TrimSuffix(path, "/") {
	case "/v1/chat/completions", "/v1/completions", "/v1/messages", "/v1/responses":
		return true
	}
	return false
}

func identityCodexPath(path string) bool {
	return path == "/v1/live" || strings.HasPrefix(path, "/v1/live/") || path == "/v1/realtime" || strings.HasPrefix(path, "/v1/realtime/") || path == "/v1/alpha/search" || path == "/backend-api/codex/alpha/search"
}
