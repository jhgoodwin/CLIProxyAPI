//go:build linux && cgo

package main

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// MetricRates is a snapshot of API-equivalent prices, separate from admission tariffs.
type MetricRates struct {
	Input       string `json:"input,omitempty"`
	Output      string `json:"output,omitempty"`
	CacheRead   string `json:"cache_read,omitempty"`
	CacheWrite  string `json:"cache_write,omitempty"`
	Source      string `json:"source,omitempty"`
	Unsupported bool   `json:"unsupported_dimensions,omitempty"`
}

type UsageMetric struct {
	AttemptID     string                `json:"attempt_id"`
	RecordID      string                `json:"record_id"`
	User          string                `json:"user"`
	KeyHash       string                `json:"key_hash"`
	Provider      string                `json:"provider"`
	Model         string                `json:"model"`
	RequestedAt   time.Time             `json:"requested_at"`
	Failed        bool                  `json:"failed"`
	Quality       string                `json:"quality"`
	Input         *int64                `json:"input_tokens"`
	Output        *int64                `json:"output_tokens"`
	CacheHit      *int64                `json:"cache_hit_tokens"`
	CacheMiss     *int64                `json:"cache_miss_tokens"`
	CacheCreation *int64                `json:"cache_creation_tokens"`
	Reasoning     *int64                `json:"reasoning_tokens"`
	EstimatedUSD  *string               `json:"estimated_api_equivalent_usd"`
	BilledUSD     *string               `json:"billed_usd"`
	PriceSource   string                `json:"price_source,omitempty"`
	Unclassified  *int64                `json:"unclassified_tokens"`
	Reported      pluginapi.UsageDetail `json:"reported_usage"`
}

func metricRates(entry map[string]json.RawMessage, in, out string) (MetricRates, error) {
	r := MetricRates{Input: in, Output: out, Source: "explicit rates"}
	if in == "" && out == "" && entry == nil {
		r.Source = "no pricing configured"
	}
	if entry != nil {
		r.Source = "LiteLLM snapshot"
		for name, dst := range map[string]*string{"input_cost_per_token": &r.Input, "output_cost_per_token": &r.Output, "cache_read_input_token_cost": &r.CacheRead, "cache_creation_input_token_cost": &r.CacheWrite} {
			if raw, ok := entry[name]; ok {
				v, err := priceDecimal(raw)
				if err != nil {
					return r, fmt.Errorf("metric price %s: %w", name, err)
				}
				*dst = v
			}
		}
		for name, raw := range entry {
			if !strings.Contains(name, "cost") || name == "input_cost_per_token" || name == "output_cost_per_token" || name == "cache_read_input_token_cost" || name == "cache_creation_input_token_cost" {
				continue
			}
			value, err := priceDecimal(raw)
			if err != nil || value != "0" {
				r.Unsupported = true
			}
		}
	}
	for _, text := range []string{r.Input, r.Output, r.CacheRead, r.CacheWrite} {
		if text != "" {
			if _, err := usd(text); err != nil {
				return r, err
			}
		}
	}
	return r, nil
}

func tokenValue(n int64) *int64 { return &n }

// recordUsage retains only trusted reservation attribution, never APIKey or upstream secrets.
// A distinct executor record is counted once even if delivery is replayed after restart.
func recordUsage(l *Ledger, r pluginapi.UsageRecord, debit Debit) error {
	if r.AttemptID == "" || r.RequestID == "" {
		return nil
	}
	idBytes, _ := json.Marshal([]string{r.AttemptID, r.RequestID})
	id := string(idBytes)
	if _, exists := l.Metrics[id]; exists {
		return nil
	}
	m := UsageMetric{AttemptID: r.AttemptID, RecordID: r.RequestID, User: debit.User, KeyHash: debit.KeyHash, Provider: debit.Provider, Model: debit.Model, RequestedAt: r.RequestedAt, Failed: r.Failed, Quality: "not reported", PriceSource: debit.EstimateRates.Source}
	m.Reported = r.Detail
	b := r.Detail.TokenBreakdown
	if b.Valid() {
		m.Quality = string(b.Quality)
		if b.Quality == usage.TokenAccountingQualityComplete && b.TotalTokens > 0 {
			m.Input, m.Output = tokenValue(b.Input.TotalTokens), tokenValue(b.Output.TotalTokens)
			m.CacheHit, m.CacheMiss = tokenValue(b.Input.CacheReadTokens), tokenValue(b.Input.UncachedTokens)
			m.CacheCreation, m.Reasoning = tokenValue(b.Input.CacheWriteTokens), tokenValue(b.Output.ReasoningTokens)
		} else if b.Quality == usage.TokenAccountingQualityUnclassified && b.TotalTokens > 0 {
			// Positive classified buckets remain trustworthy when an unknown remainder exists.
			positive := func(n int64) *int64 {
				if n > 0 {
					return tokenValue(n)
				}
				return nil
			}
			m.Input, m.Output = positive(b.Input.TotalTokens), positive(b.Output.TotalTokens)
			m.CacheHit, m.CacheMiss = positive(b.Input.CacheReadTokens), positive(b.Input.UncachedTokens)
			m.CacheCreation, m.Reasoning = positive(b.Input.CacheWriteTokens), positive(b.Output.ReasoningTokens)
			m.Unclassified = positive(b.UnclassifiedTokens)
		}
	}
	modelMatches := r.Provider == debit.Provider && r.Model == debit.Model && (r.ResponseModel == "" || r.ResponseModel == debit.Model)
	standardTier := func(t string) bool { return t == "" || t == "default" || t == "standard" }
	if !modelMatches {
		m.PriceSource += " (model mismatch; estimate unknown)"
	}
	if m.Quality == string(usage.TokenAccountingQualityComplete) && m.Input != nil && modelMatches && !debit.EstimateRates.Unsupported && standardTier(r.ServiceTier) && standardTier(r.ResponseServiceTier) {
		total := new(big.Rat)
		known := true
		for _, part := range []struct {
			count int64
			rate  string
		}{{*m.CacheMiss, debit.EstimateRates.Input}, {*m.Output, debit.EstimateRates.Output}, {*m.CacheHit, debit.EstimateRates.CacheRead}, {*m.CacheCreation, debit.EstimateRates.CacheWrite}} {
			if part.count == 0 {
				continue
			}
			if part.rate == "" {
				known = false
				break
			}
			rate, err := usd(part.rate)
			if err != nil {
				return err
			}
			total.Add(total, new(big.Rat).Mul(rate, new(big.Rat).SetInt64(part.count)))
		}
		if known {
			text := usdString(total)
			m.EstimatedUSD = &text
		}
	}
	if l.Metrics == nil {
		l.Metrics = map[string]UsageMetric{}
	}
	l.Metrics[id] = m
	return nil
}

func metricRows(l *Ledger) []UsageMetric {
	rows := make([]UsageMetric, 0, len(l.Metrics))
	for _, m := range l.Metrics {
		rows = append(rows, m)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].RequestedAt.Equal(rows[j].RequestedAt) {
			if rows[i].AttemptID == rows[j].AttemptID {
				return rows[i].RecordID < rows[j].RecordID
			}
			return rows[i].AttemptID < rows[j].AttemptID
		}
		return rows[i].RequestedAt.Before(rows[j].RequestedAt)
	})
	return rows
}
