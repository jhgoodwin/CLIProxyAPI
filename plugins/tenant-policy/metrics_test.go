//go:build linux && cgo

package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func metricFixture() (Debit, pluginapi.UsageRecord) {
	return Debit{User: "u", KeyHash: KeyHash("secret"), Provider: "p", Model: "m", AmountUSD: "0", EstimateRates: MetricRates{Input: "0.001", Output: "0.002", CacheRead: "0.0001", CacheWrite: "0.003", Source: "LiteLLM snapshot"}}, pluginapi.UsageRecord{AttemptID: "attempt", RequestID: "record", Provider: "p", Model: "m", RequestedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Detail: pluginapi.UsageDetail{TokenBreakdown: usage.NewSubsetTokenBreakdown(100, 30, 10, 20, 5, 120)}}
}

func TestMetricsCacheBucketsAndExactEstimate(t *testing.T) {
	l := emptyLedger()
	d, r := metricFixture()
	if err := recordUsage(l, r, d); err != nil {
		t.Fatal(err)
	}
	m := metricRows(l)[0]
	if *m.Input != 100 || *m.Output != 20 || *m.CacheHit != 30 || *m.CacheMiss != 60 || *m.CacheCreation != 10 || *m.Reasoning != 5 {
		t.Fatalf("overlapping buckets: %+v", m)
	}
	if m.EstimatedUSD == nil || *m.EstimatedUSD != "0.133" || m.BilledUSD != nil {
		t.Fatalf("cost=%+v", m)
	}
	if err := recordUsage(l, r, d); err != nil {
		t.Fatal(err)
	}
	if len(l.Metrics) != 1 {
		t.Fatal("replay counted twice")
	}
	r.RequestID = "retry"
	r.Failed = true
	if err := recordUsage(l, r, d); err != nil {
		t.Fatal(err)
	}
	if len(l.Metrics) != 2 {
		t.Fatal("distinct failed execution lost")
	}
}

func TestMetricsUnknownUsageAndMissingCachePrices(t *testing.T) {
	for _, quality := range []usage.TokenBreakdown{{}, usage.NewUnclassifiedTokenBreakdown(120), usage.NewSubsetTokenBreakdown(1, 2, 0, 0, 0, 1)} {
		l := emptyLedger()
		d, r := metricFixture()
		r.Detail.TokenBreakdown = quality
		if err := recordUsage(l, r, d); err != nil {
			t.Fatal(err)
		}
		m := metricRows(l)[0]
		if m.Input != nil || m.Output != nil || m.CacheMiss != nil || m.EstimatedUSD != nil || m.BilledUSD != nil {
			t.Fatalf("invented known zero: %+v", m)
		}
	}
	l := emptyLedger()
	d, r := metricFixture()
	d.EstimateRates.CacheRead = ""
	if err := recordUsage(l, r, d); err != nil {
		t.Fatal(err)
	}
	if metricRows(l)[0].EstimatedUSD != nil {
		t.Fatal("invented cache price")
	}
	r.RequestID = "mismatch"
	r.ResponseModel = "other"
	if err := recordUsage(l, r, d); err != nil {
		t.Fatal(err)
	}
	for _, m := range metricRows(l) {
		if m.RecordID == "mismatch" && (m.EstimatedUSD != nil || m.Input == nil || *m.Input != 100) {
			t.Fatal("mismatch priced or reported counters lost")
		}
	}
}

func TestMetricsPartialAndUnsupportedPrices(t *testing.T) {
	l := emptyLedger()
	d, r := metricFixture()
	r.Detail.TokenBreakdown = usage.NewPartialSubsetTokenBreakdown(10, 4, 0, 0, 0, 15)
	if err := recordUsage(l, r, d); err != nil {
		t.Fatal(err)
	}
	m := metricRows(l)[0]
	if m.Input == nil || *m.Input != 10 || m.CacheHit == nil || *m.CacheHit != 4 || m.CacheMiss == nil || *m.CacheMiss != 6 || m.Output != nil || m.EstimatedUSD != nil || m.Unclassified == nil || *m.Unclassified != 5 {
		t.Fatalf("partial=%+v", m)
	}
	var entry map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"input_cost_per_token":0.001,"output_cost_per_token":0.002,"output_cost_per_token_above_200k_tokens":0.003}`), &entry); err != nil {
		t.Fatal(err)
	}
	rates, err := metricRates(entry, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !rates.Unsupported {
		t.Fatal("tier rates ignored")
	}
	for _, mode := range []string{"tier", "dimensions"} {
		l = emptyLedger()
		d, r = metricFixture()
		if mode == "tier" {
			r.ResponseServiceTier = "priority"
		} else {
			d.EstimateRates = rates
		}
		if err := recordUsage(l, r, d); err != nil {
			t.Fatal(err)
		}
		if metricRows(l)[0].EstimatedUSD != nil {
			t.Fatal("unsupported price claimed")
		}
	}
}

func TestMetricsRevokedAttributionAndReplayPersist(t *testing.T) {
	path, req := policyFixture(t)
	configState.Lock()
	c := configState.cfg
	m := c.Models["alias"]
	m.estimateRates = MetricRates{Input: "0.001", Output: "0.002"}
	c.Models["alias"] = m
	configState.cfg = c
	configState.Unlock()
	if invokePolicy(t, pluginabi.MethodRequestInterceptAfter, req).Terminate {
		t.Fatal("admission failed")
	}
	if err := runCLI([]string{"--state", path, "key-revoke", KeyHash("test-secret")}); err != nil {
		t.Fatal(err)
	}
	r := pluginapi.UsageRecord{AttemptID: req.AttemptID, RequestID: "event", Provider: "p", Model: "m", Detail: pluginapi.UsageDetail{InputTokens: 10, OutputTokens: 2, TokenBreakdown: usage.NewSubsetTokenBreakdown(10, 0, 0, 2, 0, 12)}}
	for i := 0; i < 2; i++ {
		if err := settle(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := withLedger(path, false, func(l *Ledger) error {
		rows := metricRows(l)
		if len(rows) != 1 || rows[0].KeyHash != KeyHash("test-secret") || rows[0].User != "u" {
			t.Fatalf("lost attribution %+v", rows)
		}
		if !l.Keys[rows[0].KeyHash].Revoked {
			t.Fatal("archive lost")
		}
		if *rows[0].EstimatedUSD != "0.014" {
			t.Fatal("price snapshot lost")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMetricRatesCacheAndDecimalSnapshot(t *testing.T) {
	var entry map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"input_cost_per_token":1e-6,"output_cost_per_token":2e-6,"cache_read_input_token_cost":1e-7,"cache_creation_input_token_cost":3e-6}`), &entry); err != nil {
		t.Fatal(err)
	}
	r, err := metricRates(entry, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Input != "0.000001" || r.Output != "0.000002" || r.CacheRead != "0.0000001" || r.CacheWrite != "0.000003" {
		t.Fatalf("rates=%+v", r)
	}
}
