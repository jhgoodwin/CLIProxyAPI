//go:build linux && cgo

package main

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
)

// observeQuota records a manually supplied provider quota watermark.
// args are provider, auth ID, observation time, and a JSON header map.
func observeQuota(l *Ledger, args []string) error {
	if len(args) != 4 {
		return errors.New("usage: quota-observe PROVIDER AUTH_ID RFC3339 HEADERS_JSON")
	}
	provider, authID := args[0], args[1]
	if provider == "" || strings.TrimSpace(provider) != provider || len(provider) > 128 {
		return errors.New("invalid provider")
	}
	if authID == "" || strings.TrimSpace(authID) != authID || len(authID) > 128 {
		return errors.New("invalid auth ID")
	}
	observed, err := time.Parse(time.RFC3339, args[2])
	if err != nil || observed.IsZero() || observed.After(poolNow()) {
		return errors.New("observation time must be a non-future RFC3339 timestamp")
	}
	if len(args[3]) > 32*1024 {
		return errors.New("quota headers JSON exceeds 32KB")
	}
	var signals map[string]string
	if err := json.Unmarshal([]byte(args[3]), &signals); err != nil || signals == nil || len(signals) == 0 || len(signals) > 64 {
		return errors.New("quota headers must be a JSON object with 1 to 64 string entries")
	}
	for name, value := range signals {
		if name == "" || len(name) > 128 || value == "" || len(value) > 512 || strings.TrimSpace(name) != name || strings.TrimSpace(value) != value {
			return errors.New("invalid quota header name or value")
		}
		for _, r := range name + value {
			if unicode.IsControl(r) {
				return errors.New("quota headers cannot contain control characters")
			}
		}
	}
	windows, err := parseReportedQuota(provider, signals, observed, quotaScale)
	if err != nil {
		return err
	}
	now := poolNow().Unix()
	for _, window := range windows {
		if window.ResetAt <= now {
			return errors.New("all quota windows must reset in the future")
		}
	}
	if l.ReportedPools == nil {
		l.ReportedPools = map[string]ReportedPoolState{}
	}
	state := l.ReportedPools[provider]
	if state.Seeds == nil {
		state.Seeds = map[string]ReportedQuotaSeed{}
	}
	if previous, ok := state.Seeds[authID]; ok && !observed.After(previous.ObservedAt) {
		return errors.New("quota observation must be newer than the existing seed")
	}
	state.Seeds[authID] = ReportedQuotaSeed{ObservedAt: observed, Signals: signals}
	l.ReportedPools[provider] = state
	return nil
}
