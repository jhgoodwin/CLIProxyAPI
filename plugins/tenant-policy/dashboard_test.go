//go:build linux && cgo

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDashboardEscapingAndArchiveTotals(t *testing.T) {
	l := emptyLedger()
	d, r := metricFixture()
	d.User = `<script>alert("owned")</script>`
	l.Keys[d.KeyHash] = Key{User: d.User, Revoked: true}
	l.Debits[r.AttemptID] = d
	if err := recordUsage(l, r, d); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := renderDashboard(&out, l); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "<script>") || !strings.Contains(out.String(), "&lt;script&gt;") || !strings.Contains(out.String(), "0.133") || !strings.Contains(out.String(), "Billed USD is unknown") {
		t.Fatalf("unsafe/missing dashboard %s", out.String())
	}
	summary := dashboardSummary(l)
	if len(summary.Keys) != 1 || !summary.Keys[0].Archived || summary.Totals[0].Records != 1 {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestDashboardCLIAndExportPrivacy(t *testing.T) {
	path, _ := policyFixture(t)
	d, r := metricFixture()
	if err := withLedger(path, true, func(l *Ledger) error { return recordUsage(l, r, d) }); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range [][]string{{"dashboard", filepath.Join(t.TempDir(), "report.html")}, {"metrics-export", "json", filepath.Join(t.TempDir(), "report.json")}, {"metrics-export", "csv", filepath.Join(t.TempDir(), "report.csv")}} {
		args := append([]string{"--state", path}, cmd...)
		if err := runCLI(args); err != nil {
			t.Fatal(err)
		}
		file := cmd[len(cmd)-1]
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatal("public report")
		}
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "test-secret") || strings.Contains(string(b), `"APIKey"`) {
			t.Fatal("secret leaked")
		}
		if cmd[0] == "metrics-export" && cmd[1] == "json" {
			var rows []UsageMetric
			if err = json.Unmarshal(b, &rows); err != nil || len(rows) != 1 {
				t.Fatalf("export %s: %v", b, err)
			}
		}
		if err := runCLI(args); err == nil {
			t.Fatal("existing report overwritten")
		}
	}
}

func TestCSVFormulaLabelsEscaped(t *testing.T) {
	l := emptyLedger()
	d, r := metricFixture()
	d.User = "=SUM(1,2)"
	if err := recordUsage(l, r, d); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := exportMetrics(&out, l, "csv"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "'=SUM") {
		t.Fatal("spreadsheet formula injection")
	}
}
