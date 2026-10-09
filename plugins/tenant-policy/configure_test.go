//go:build linux && cgo

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"gopkg.in/yaml.v3"
)

func configureRequest(t *testing.T, c policyConfig) []byte {
	t.Helper()
	config, err := yaml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"schema_version": pluginabi.SchemaVersion, "config_yaml": config})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func configureFixture(t *testing.T) policyConfig {
	t.Helper()
	previous := configSnapshot()
	t.Cleanup(func() {
		configState.Lock()
		configState.cfg = previous
		configState.Unlock()
	})
	path, _ := policyFixture(t)
	return policyConfig{
		State: path,
		Pools: PoolConfig{"named": {Provider: "p", Mode: "reported", ReportedMaxAgeSeconds: 300, AccountSelectors: []string{"team-*"}}},
		Models: map[string]modelPolicy{"alias": {
			Provider: "p", UpstreamModel: "m", Pool: "named", InputAllowance: 100, OutputAllowance: 10,
			InputUSD: "0.001", OutputUSD: "0.002",
		}},
	}
}

func TestConfigurePricingCases(t *testing.T) {
	for _, tc := range []struct {
		name, input, output, charge string
		poolOnly                    bool
		catalog                     string
		wantRates                   MetricRates
	}{
		{name: "explicit", input: "0.000000125", output: "0.00000025", charge: "0.000015", wantRates: MetricRates{Input: "0.000000125", Output: "0.00000025", Source: "explicit rates"}},
		{name: "catalog maxima", catalog: `{"price":{"input_cost_per_token":1.25e-7,"output_cost_per_token":2.5e-7,"cache_creation_input_token_cost":5e-7,"output_cost_per_token_above_200k_tokens":1e-6}}`, charge: "0.00006", wantRates: MetricRates{Input: "0.000000125", Output: "0.00000025", CacheWrite: "0.0000005", Source: "LiteLLM snapshot", Unsupported: true}},
		{name: "explicit overrides unsupported catalog", input: "0.001", output: "0.002", catalog: `{"price":{"search_context_cost_per_query":{"low":0.01}}}`, charge: "0.12", wantRates: MetricRates{Input: "0.001", Output: "0.002", Source: "explicit rates"}},
		{name: "pool only unpriced", poolOnly: true, wantRates: MetricRates{Source: "no pricing configured"}},
		{name: "pool only estimate", poolOnly: true, catalog: `{"price":{"input_cost_per_token":1.25e-7,"output_cost_per_token":2.5e-7,"search_context_cost_per_query":{"low":0.01}}}`, wantRates: MetricRates{Input: "0.000000125", Output: "0.00000025", Source: "LiteLLM snapshot", Unsupported: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := configureFixture(t)
			m := c.Models["alias"]
			m.InputUSD, m.OutputUSD, m.PoolOnly = tc.input, tc.output, tc.poolOnly
			if tc.poolOnly {
				m.InputAllowance, m.OutputAllowance = 0, 0
			}
			if tc.catalog != "" {
				c.Prices = filepath.Join(filepath.Dir(c.State), "prices.json")
				if err := os.WriteFile(c.Prices, []byte(tc.catalog), 0600); err != nil {
					t.Fatal(err)
				}
				m.PriceKey = "price"
			}
			c.Models["alias"] = m
			if err := configure(configureRequest(t, c)); err != nil {
				t.Fatal(err)
			}
			got := configSnapshot().Models["alias"]
			if got.charge != tc.charge || got.estimateRates != tc.wantRates {
				t.Fatalf("compiled policy: %#v; want charge %q and rates %#v", got, tc.charge, tc.wantRates)
			}
			if tc.catalog != "" {
				if err := os.WriteFile(c.Prices, []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
				if configSnapshot().Models["alias"] != got {
					t.Fatal("active pricing changed with source file")
				}
			}
		})
	}
}

func TestConfigureValidationKeepsActiveSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*policyConfig)
		want string
	}{
		{"relative state", func(c *policyConfig) { c.State = "relative" }, "absolute state"},
		{"relative prices", func(c *policyConfig) { c.Prices = "relative" }, "prices path must be absolute"},
		{"invalid pool", func(c *policyConfig) { c.Pools["bad"] = PoolDefinition{} }, "positive capacity"},
		{"missing mapping", func(c *policyConfig) { m := c.Models["alias"]; m.UpstreamModel = ""; c.Models["alias"] = m }, "invalid model mapping"},
		{"unknown resource", func(c *policyConfig) { m := c.Models["alias"]; m.QuotaResource = "other"; c.Models["alias"] = m }, "invalid quota_resource"},
		{"credits need reported pool", func(c *policyConfig) {
			c.Pools = nil
			m := c.Models["alias"]
			m.QuotaResource = "credits"
			c.Models["alias"] = m
		}, "credit resource requires reported pool"},
		{"wrong provider pool", func(c *policyConfig) { p := c.Pools["named"]; p.Provider = "other"; c.Pools["named"] = p }, "invalid pool"},
		{"partial override", func(c *policyConfig) { m := c.Models["alias"]; m.OutputUSD = ""; c.Models["alias"] = m }, "both override rates required"},
		{"unknown price even with override", func(c *policyConfig) { m := c.Models["alias"]; m.PriceKey = "missing"; c.Models["alias"] = m }, "missing price key"},
		{"zero allowance", func(c *policyConfig) { m := c.Models["alias"]; m.InputAllowance = 0; c.Models["alias"] = m }, "positive token allowances"},
		{"zero charge", func(c *policyConfig) {
			m := c.Models["alias"]
			m.InputUSD, m.OutputUSD = "0", "0"
			c.Models["alias"] = m
		}, "positive charge"},
		{"unpriced tariff", func(c *policyConfig) { m := c.Models["alias"]; m.InputUSD, m.OutputUSD = "", ""; c.Models["alias"] = m }, "missing price key"},
		{"pool only needs pool", func(c *policyConfig) {
			c.Pools = nil
			m := c.Models["alias"]
			m.PoolOnly, m.Pool = true, ""
			c.Models["alias"] = m
		}, "has no provider pool"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := configureFixture(t)
			before := configSnapshot()
			ledgerBefore, err := os.ReadFile(c.State)
			if err != nil {
				t.Fatal(err)
			}
			path := c.State
			// A second valid model must never be published on a failed compilation.
			valid := c.Models["alias"]
			valid.Pool = ""
			c.Models["valid"] = valid
			tc.edit(&c)
			if err := configure(configureRequest(t, c)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
			if !reflect.DeepEqual(configSnapshot(), before) {
				t.Fatal("failed configuration changed active snapshot")
			}
			ledgerAfter, err := os.ReadFile(path)
			if err != nil || string(ledgerAfter) != string(ledgerBefore) {
				t.Fatalf("failed configuration changed ledger: %v", err)
			}
		})
	}
}

func TestConfigureDecodeFailureKeepsActiveSnapshot(t *testing.T) {
	configureFixture(t)
	before := configSnapshot()
	for _, raw := range [][]byte{
		[]byte(`{`),
		[]byte(`{"schema_version":6}`),
		[]byte(`{"schema_version":7,"config_yaml":"dW5rbm93bjogdHJ1ZQ=="}`), // unknown: true
	} {
		if err := configure(raw); err == nil {
			t.Fatalf("accepted invalid wire configuration: %s", raw)
		}
		if !reflect.DeepEqual(configSnapshot(), before) {
			t.Fatal("decode failure changed active snapshot")
		}
	}
}

func TestConfigurePersistenceFailureKeepsActiveSnapshot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	c := configureFixture(t)
	before := configSnapshot()
	ledgerBefore, err := os.ReadFile(c.State)
	if err != nil {
		t.Fatal(err)
	}
	// Reading/locking the existing ledger succeeds; creating its replacement fails.
	if err := withLedger(c.State, false, func(*Ledger) error { return nil }); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(c.State)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0700); err != nil {
			t.Error(err)
		}
	})
	err = configure(configureRequest(t, c))
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || !strings.Contains(pathErr.Path, ".tenant-ledger-") {
		t.Fatalf("expected ledger replacement failure, got %v", err)
	}
	if !reflect.DeepEqual(configSnapshot(), before) {
		t.Fatal("persistence failure changed active snapshot")
	}
	ledgerAfter, err := os.ReadFile(c.State)
	if err != nil || string(ledgerAfter) != string(ledgerBefore) {
		t.Fatalf("persistence failure changed ledger: %v", err)
	}
}

func TestConfigureReloadPublishesCompleteSnapshotsAndPreservesQuota(t *testing.T) {
	c := configureFixture(t)
	if err := withLedger(c.State, true, func(l *Ledger) error {
		l.NamedPools["obsolete"] = NamedPoolSnapshot{Provider: "old", AccountSelectors: []string{"*"}}
		l.PoolMembers["named"] = map[string]bool{"account": true}
		l.Accounts["team-one"] = AccountAlias{Provider: "p", AuthID: "account"}
		l.ResetAudit = []QuotaResetAudit{{Resource: "credits", Provider: "p", Reason: "retained"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := configure(configureRequest(t, c)); err != nil {
		t.Fatal(err)
	}
	old := configSnapshot()
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					cfg := configSnapshot()
					if len(cfg.Models) != 1 || cfg.Models["alias"].charge != "0.12" || cfg.Models["alias"].estimateRates.Input != "0.001" {
						t.Error("reader observed incomplete policy")
						return
					}
				}
			}
		}()
	}
	for i := range 10 {
		c.Priority = i
		if err := configure(configureRequest(t, c)); err != nil {
			t.Error(err)
			break
		}
	}
	close(stop)
	readers.Wait()
	if old.Priority != 0 || old.Models["alias"].charge != "0.12" {
		t.Fatal("reload mutated retained snapshot")
	}
	if err := withLedger(c.State, false, func(l *Ledger) error {
		want := map[string]NamedPoolSnapshot{"named": {Provider: "p", AccountSelectors: []string{"team-*"}}}
		if !reflect.DeepEqual(l.NamedPools, want) || !l.PoolMembers["named"]["account"] || l.Accounts["team-one"].AuthID != "account" || len(l.ResetAudit) != 1 || l.ResetAudit[0].Reason != "retained" {
			t.Fatalf("reload lost pool/reset state: %#v", l)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
