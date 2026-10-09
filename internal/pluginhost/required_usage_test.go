package pluginhost

import (
	"context"
	"testing"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type policyUsageCapture struct {
	got      pluginapi.UsageRecord
	canceled bool
}

func (p *policyUsageCapture) HandleUsage(ctx context.Context, record pluginapi.UsageRecord) {
	p.got = record
	p.canceled = ctx.Err() != nil
}

func TestRequiredPolicyUsageSettlementCarriesAttemptAfterCancellation(t *testing.T) {
	capture := &policyUsageCapture{}
	h := newHostWithRecords(capabilityRecord{id: "tenant", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{UsagePlugin: capture}}})
	adapter := &usageAdapter{host: h, pluginID: "tenant"}
	ctx, cancel := context.WithCancel(coreexecutor.WithAttemptID(context.Background(), "authorized-attempt"))
	cancel()
	breakdown := coreusage.NewSubsetTokenBreakdown(100, 20, 0, 50, 10, 150)
	adapter.HandleUsage(ctx, coreusage.Record{Detail: coreusage.Detail{TokenBreakdown: breakdown}, RequestID: "usage-record", Provider: "codex", Model: "model"})
	if capture.got.AttemptID != "authorized-attempt" || capture.got.RequestID != "usage-record" {
		t.Fatalf("usage correlation lost: %#v", capture.got)
	}
	if capture.got.Detail.TokenBreakdown != breakdown || !capture.got.Detail.TokenBreakdown.Valid() {
		t.Fatalf("canonical accounting lost: %#v", capture.got.Detail.TokenBreakdown)
	}
	if capture.canceled {
		t.Fatal("usage settlement inherited canceled request")
	}
}
