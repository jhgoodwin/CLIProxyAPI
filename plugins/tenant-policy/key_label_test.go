//go:build linux && cgo

package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

func keyCLIOutput(t *testing.T, path string, args ...string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = old; _ = f.Close() }()
	if err := runCLI(append([]string{"--state", path}, args...)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestKeyLabelsCLIAndLegacyLedger(t *testing.T) {
	path, _ := policyFixture(t) // Legacy key has no label in its JSON.
	const label = " Website production — note\nsecond line "
	secret := strings.TrimSpace(keyCLIOutput(t, path, "key-create", "u", label))
	unlabeled := strings.TrimSpace(keyCLIOutput(t, path, "key-create", "u"))
	hash := KeyHash(secret)
	check := func(want string, revoked bool) {
		t.Helper()
		if err := withLedger(path, false, func(l *Ledger) error {
			k := l.Keys[hash]
			if k.Label != want || k.User != "u" || k.Revoked != revoked {
				t.Fatalf("key=%+v", k)
			}
			if l.Keys[KeyHash("test-secret")].Label != "" || l.Keys[KeyHash(unlabeled)].Label != "" {
				t.Fatal("legacy or unlabeled key acquired a label")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	check(label, false)
	var rows []struct {
		Hash, User, Label string
		Revoked           bool
	}
	if err := json.Unmarshal([]byte(keyCLIOutput(t, path, "key-list")), &rows); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.Hash == hash {
			found = row.Label == label && row.User == "u"
		}
	}
	if !found {
		t.Fatal("key-list missing label")
	}
	keyCLIOutput(t, path, "key-revoke", hash)
	check(label, true)
	keyCLIOutput(t, path, "key-label", hash, "updated")
	check("updated", true)
	keyCLIOutput(t, path, "key-label", hash, "")
	check("", true)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"key-label", "missing", "note"}, {"key-label", hash}, {"key-create"}, {"key-create", "u", "label", "extra"}, {"key-create", "missing", "label"}} {
		if err := runCLI(append([]string{"--state", path}, args...)); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("invalid command changed ledger")
	}
}

func TestKeyLabelsReportsKeepStableAttribution(t *testing.T) {
	l := emptyLedger()
	d, r := metricFixture()
	label := `<script>alert("note")</script>`
	l.Keys[d.KeyHash] = Key{User: d.User, Label: label, Revoked: true}
	l.Debits[r.AttemptID] = d
	if err := recordUsage(l, r, d); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := renderDashboard(&out, l); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), label) || !strings.Contains(out.String(), "&lt;script&gt;") || !strings.Contains(out.String(), d.KeyHash) {
		t.Fatal("dashboard missing escaped label or stable identifier")
	}
	for _, label := range []string{"=SUM(1,2)", "line one,\"quote\"\nline two", "\n=1+1", ""} {
		l.Keys[d.KeyHash] = Key{User: d.User, Label: label, Revoked: true}
		out.Reset()
		if err := exportMetrics(&out, l, "json"); err != nil {
			t.Fatal(err)
		}
		var rows []struct {
			KeyHash  string `json:"key_hash"`
			KeyLabel string `json:"key_label"`
		}
		if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].KeyLabel != label || rows[0].KeyHash != d.KeyHash {
			t.Fatalf("rows=%+v", rows)
		}
		out.Reset()
		if err := exportMetrics(&out, l, "csv"); err != nil {
			t.Fatal(err)
		}
		csvRows, err := csv.NewReader(&out).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		index := -1
		for i, header := range csvRows[0] {
			if header == "key_label" {
				index = i
			}
		}
		want := label
		if strings.HasPrefix(label, "=") || strings.HasPrefix(label, "\n") {
			want = "'" + label
		}
		if index < 0 || csvRows[1][index] != want {
			t.Fatalf("CSV=%v", csvRows)
		}
		summary := dashboardSummary(l)
		if len(summary.Keys) != 1 || summary.Keys[0].Name != d.KeyHash || summary.Keys[0].Label != label || !summary.Keys[0].Archived || summary.Totals[0].Records != 1 {
			t.Fatal("label changed aggregation or archive metadata")
		}
	}
}
