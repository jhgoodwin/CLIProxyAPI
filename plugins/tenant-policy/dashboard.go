//go:build linux && cgo

package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"math/big"
	"os"
	"sort"
	"strconv"
	"time"
)

type metricTotal struct {
	Name                                           string
	Label                                          string
	Records, Failed, UnknownUsage, UnknownEstimate int
	Input, Output, Hit, Miss, Creation             big.Int
	Estimate, Tariff                               big.Rat
	Archived                                       bool
}

func (t *metricTotal) add(m UsageMetric) {
	t.Records++
	if m.Failed {
		t.Failed++
	}
	if m.Input == nil || m.Output == nil || m.CacheHit == nil || m.CacheMiss == nil || m.CacheCreation == nil {
		t.UnknownUsage++
	}
	for _, pair := range []struct {
		dst   *big.Int
		count *int64
	}{{&t.Input, m.Input}, {&t.Output, m.Output}, {&t.Hit, m.CacheHit}, {&t.Miss, m.CacheMiss}, {&t.Creation, m.CacheCreation}} {
		if pair.count != nil {
			pair.dst.Add(pair.dst, big.NewInt(*pair.count))
		}
	}
	if m.EstimatedUSD == nil {
		t.UnknownEstimate++
	} else if v, err := usd(*m.EstimatedUSD); err == nil {
		t.Estimate.Add(&t.Estimate, v)
	}
}

type dashboardData struct {
	Generated                            string
	Totals                               []*metricTotal
	Users, Keys, Providers, Models, Days []*metricTotal
}

func dashboardSummary(l *Ledger) dashboardData {
	d := dashboardData{Generated: time.Now().UTC().Format(time.RFC3339)}
	total := &metricTotal{Name: "All usage"}
	groups := make([]map[string]*metricTotal, 5)
	for i := range groups {
		groups[i] = map[string]*metricTotal{}
	}
	group := func(i int, name string) *metricTotal {
		if name == "" {
			name = "unknown"
		}
		t := groups[i][name]
		if t == nil {
			t = &metricTotal{Name: name}
			groups[i][name] = t
		}
		return t
	}
	for _, m := range l.Metrics {
		total.add(m)
		day := "unknown"
		if !m.RequestedAt.IsZero() {
			day = m.RequestedAt.UTC().Format("2006-01-02")
		}
		for i, name := range []string{m.User, m.KeyHash, m.Provider, m.Model, day} {
			group(i, name).add(m)
		}
		group(1, m.KeyHash).Archived = l.Keys[m.KeyHash].Revoked
	}
	// Tariffs are immutable admission charges, once per attempt rather than per usage event.
	for _, debit := range l.Debits {
		amount, err := usd(debit.AmountUSD)
		if err != nil {
			continue
		}
		total.Tariff.Add(&total.Tariff, amount)
		for i, name := range []string{debit.User, debit.KeyHash, debit.Provider, debit.Model} {
			t := group(i, name)
			t.Tariff.Add(&t.Tariff, amount)
		}
		group(1, debit.KeyHash).Archived = l.Keys[debit.KeyHash].Revoked
	}
	for hash, key := range l.Keys {
		t := group(1, hash)
		t.Archived = key.Revoked
		t.Label = key.Label
	}
	for id := range l.Users {
		group(0, id)
	}
	lists := []*[]*metricTotal{&d.Users, &d.Keys, &d.Providers, &d.Models, &d.Days}
	for i, target := range lists {
		for _, t := range groups[i] {
			*target = append(*target, t)
		}
		sort.Slice(*target, func(a, b int) bool { return (*target)[a].Name < (*target)[b].Name })
	}
	d.Totals = []*metricTotal{total}
	return d
}

const dashboardTemplate = `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Tenant usage and costs</title><style>body{font:15px system-ui;margin:2rem;color:#19212b;background:#fafafa}table{border-collapse:collapse;width:100%;margin-bottom:2rem}th,td{text-align:left;padding:.6rem;border:1px solid #ddd;overflow-wrap:anywhere}th{background:#e8edf3}h1{font-size:1.8rem}.scroll{overflow:auto}small{color:#465261}</style><h1>Tenant usage and costs</h1><p>Generated {{.Generated}}. Offline administrator report; protect this file as tenant data.</p><p>API-equivalent USD estimates are not provider invoices. Subscription estimates do not represent subscription charges. Internal policy tariffs are admission charges. Billed USD is unknown. Token totals sum classified records; unknown records are counted separately. Cache miss means uncached input; cache creation is separate.</p>{{define "table"}}<div class="scroll"><table><thead><tr><th>Group</th><th>Archived key</th><th>Records</th><th>Failed</th><th>Input</th><th>Output</th><th>Cache hit</th><th>Cache miss</th><th>Cache creation</th><th>Unknown usage</th><th>Estimated API USD (known records)</th><th>Unknown estimate</th><th>Internal tariff USD</th></tr></thead><tbody>{{range .}}<tr><td>{{.Name}}{{if .Label}}<br><small>{{.Label}}</small>{{end}}</td><td>{{.Archived}}</td><td>{{.Records}}</td><td>{{.Failed}}</td><td>{{intText .Input}}</td><td>{{intText .Output}}</td><td>{{intText .Hit}}</td><td>{{intText .Miss}}</td><td>{{intText .Creation}}</td><td>{{.UnknownUsage}}</td><td>{{usdText .Estimate}}</td><td>{{.UnknownEstimate}}</td><td>{{usdText .Tariff}}</td></tr>{{end}}</tbody></table></div>{{end}}<h2>Totals</h2>{{template "table" .Totals}}<h2>By user</h2>{{template "table" .Users}}<h2>By key (SHA-256 identifier)</h2>{{template "table" .Keys}}<h2>By provider</h2>{{template "table" .Providers}}<h2>By model</h2>{{template "table" .Models}}<h2>By UTC day (usage records)</h2>{{template "table" .Days}}</html>`

func renderDashboard(w io.Writer, l *Ledger) error {
	t, err := template.New("dashboard").Funcs(template.FuncMap{"intText": func(v big.Int) string { return v.String() }, "usdText": func(v big.Rat) string { return usdString(&v) }}).Parse(dashboardTemplate)
	if err != nil {
		return err
	}
	return t.Execute(w, dashboardSummary(l))
}

func exportMetrics(w io.Writer, l *Ledger, format string) error {
	type exportRow struct {
		UsageMetric
		KeyLabel string `json:"key_label"`
	}
	rows := make([]exportRow, 0, len(l.Metrics))
	for _, m := range metricRows(l) {
		rows = append(rows, exportRow{UsageMetric: m, KeyLabel: l.Keys[m.KeyHash].Label})
	}
	if format == "json" {
		return json.NewEncoder(w).Encode(rows)
	}
	if format != "csv" {
		return fmt.Errorf("format must be json or csv")
	}
	c := csv.NewWriter(w)
	if err := c.Write([]string{"attempt_id", "record_id", "user", "key_hash", "provider", "model", "requested_at", "failed", "quality", "input_tokens", "output_tokens", "cache_hit_tokens", "cache_miss_tokens", "cache_creation_tokens", "reasoning_tokens", "estimated_api_equivalent_usd", "billed_usd", "price_source", "key_label"}); err != nil {
		return err
	}
	num := func(n *int64) string {
		if n == nil {
			return "unknown"
		}
		return strconv.FormatInt(*n, 10)
	}
	str := func(s *string) string {
		if s == nil {
			return "unknown"
		}
		return *s
	}
	for _, m := range rows {
		// Prevent spreadsheet formula execution in administrator-controlled labels.
		cell := func(s string) string {
			if len(s) > 0 && (s[0] == '=' || s[0] == '+' || s[0] == '-' || s[0] == '@' || s[0] == '\t' || s[0] == '\r' || s[0] == '\n') {
				return "'" + s
			}
			return s
		}
		if err := c.Write([]string{cell(m.AttemptID), cell(m.RecordID), cell(m.User), m.KeyHash, cell(m.Provider), cell(m.Model), m.RequestedAt.Format(time.RFC3339), strconv.FormatBool(m.Failed), m.Quality, num(m.Input), num(m.Output), num(m.CacheHit), num(m.CacheMiss), num(m.CacheCreation), num(m.Reasoning), str(m.EstimatedUSD), str(m.BilledUSD), m.PriceSource, cell(m.KeyLabel)}); err != nil {
			return err
		}
	}
	c.Flush()
	return c.Error()
}

// writeReport never replaces existing reports and creates private administrator artifacts.
func writeReport(path string, write func(io.Writer) error) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	err = write(f)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}
