package filesoverride

import (
	"bytes"
	"context"
	"testing"

	clauderesp "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/responses"
	sdk "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// TestInstalledWrapperKeepsRequestContext guards the Files wrapper for the
// Responses->Claude pair: an unmarked request must translate exactly as the
// builtin context-aware envelope does, honoring the tool-search value pinned
// in the request context rather than the global setting.
func TestInstalledWrapperKeepsRequestContext(t *testing.T) {
	// Client tool_search plus a named hosted declaration: translation differs by tool-search setting.
	body := []byte(`{"model":"claude-test","input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"create a calendar event"}]},` +
		`{"type":"tool_search_call","call_id":"call_search_1","status":"completed","execution":"client","arguments":{"query":"calendar"}},` +
		`{"type":"tool_search_output","call_id":"call_search_1","status":"completed","execution":"client","tools":[{"type":"namespace","name":"mcp__codex_apps__","description":"Apps.","tools":[{"type":"function","name":"calendar_create_event","description":"Create a calendar event.","strict":false,"defer_loading":true,"parameters":{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]}}]}]}` +
		`],"tools":[` +
		`{"type":"function","name":"exec_command","description":"Run a command.","strict":false,"parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}},` +
		`{"type":"tool_search","execution":"client","description":"Search deferred tools.","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}},` +
		`{"type":"tool_search","name":"HostedSearch"}` +
		`],"stream":false}`)
	from, to := sdk.FormatOpenAIResponse, sdk.FormatClaude
	global := clauderesp.ClientToolSearchEnabled()
	defer clauderesp.SetClientToolSearchEnabled(global)

	// The pinned value must differ from the global one for at least one case to prove ctx is honored.
	clauderesp.SetClientToolSearchEnabled(true)
	on := clauderesp.ConvertOpenAIResponsesRequestEnvelopeToClaude(context.Background(), sdk.RequestEnvelope{Model: "claude-test", Body: bytes.Clone(body)})
	clauderesp.SetClientToolSearchEnabled(false)
	off := clauderesp.ConvertOpenAIResponsesRequestEnvelopeToClaude(context.Background(), sdk.RequestEnvelope{Model: "claude-test", Body: bytes.Clone(body)})
	if bytes.Equal(on.Body, off.Body) {
		t.Fatal("fixture does not depend on tool-search; test would not detect a lost context")
	}
	for _, globalOn := range []bool{false, true} {
		for _, pinned := range []bool{false, true} {
			clauderesp.SetClientToolSearchEnabled(globalOn)
			ctx := clauderesp.WithClientToolSearch(context.Background(), pinned)
			want := clauderesp.ConvertOpenAIResponsesRequestEnvelopeToClaude(ctx, sdk.RequestEnvelope{Model: "claude-test", Body: bytes.Clone(body)})
			Install()
			got := sdk.TranslateRequestEnvelope(ctx, from, to, sdk.RequestEnvelope{Model: "claude-test", Body: bytes.Clone(body)})
			if (want.Err == nil) != (got.Err == nil) {
				t.Fatalf("global=%v pinned=%v: error mismatch want=%v got=%v", globalOn, pinned, want.Err, got.Err)
			}
			if !bytes.Equal(want.Body, got.Body) {
				t.Fatalf("global=%v pinned=%v: wrapper ignored request context\nwant: %s\ngot:  %s", globalOn, pinned, want.Body, got.Body)
			}
		}
	}
}
