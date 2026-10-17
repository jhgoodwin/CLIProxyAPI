package filesoverride

import (
	"bytes"
	"encoding/json"
	clauderesp "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/responses"
	geminiclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/claude"
	geminichat "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/openai/chat-completions"
	geminiresp "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/openai/responses"
	"testing"
)

func marked(t *testing.T, body, provider string) []byte {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		t.Fatal(err)
	}
	root[Marker] = metadata{Provider: provider, Refs: map[string]Ref{"hosted-id": {URI: "https://generativelanguage.googleapis.com/v1beta/files/hosted-id", MIMEType: "image/png"}}}
	out, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestNativeIdentityStripsAndProjects(t *testing.T) {
	for _, tc := range []struct{ format, provider, body, want string }{
		{"claude", "claude", `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"file","file_id":"hosted-id"}}]}]}`, `"source":{"file_id":"hosted-id","type":"file"}`},
		{"openai", "deepseek", `{"messages":[{"role":"user","content":[{"type":"input_image","file_id":"hosted-id"}]}]}`, `"file_id":"hosted-id","type":"file"`},
		{"gemini", "gemini", `{"contents":[{"role":"user","parts":[{"type":"input_image","file_id":"hosted-id"}]}]}`, `"fileData"`},
	} {
		out, err := RestoreNative(tc.format, marked(t, tc.body, tc.provider))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(out, []byte(tc.want)) || bytes.Contains(out, []byte(Marker)) {
			t.Fatalf("native %s: %s", tc.format, out)
		}
	}
	out, err := RestoreNative("claude", []byte(`{"__cpa_hosted_files":{"provider":"claude"},"messages":[{"role":"user","content":[{"type":"input_image","file_id":"hosted-id"}]}]}`))
	if err != nil || !bytes.Contains(out, []byte(`"type":"file"`)) {
		t.Fatalf("provider-only marker: %s %v", out, err)
	}
}

func TestOpenAIResponsesHostedImages(t *testing.T) {
	for _, tc := range []struct {
		from, body string
		fn         converter
	}{
		{"openai-response", `{"input":[{"role":"user","content":[{"type":"input_image","file_id":"hosted-id"}]}]}`, claudeIdentity},
		{"openai", `{"messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"hosted-id"}}]}]}`, claudeIdentity},
		{"claude", `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"file","file_id":"hosted-id"}}]}]}`, claudeIdentity},
		{"gemini", `{"contents":[{"role":"user","parts":[{"type":"input_image","file_id":"hosted-id"}]}]}`, claudeIdentity},
	} {
		out, err := ConvertWithCompat(tc.from, "openai-response", "requested-model", marked(t, tc.body, "openai"), false, tc.fn)
		if tc.from != "openai-response" {
			if err == nil || out != nil {
				t.Fatalf("%s: unsupported source accepted: %s %v", tc.from, out, err)
			}
			want, wantErr := claudeIdentity("requested-model", []byte(tc.body), false)
			got, gotErr := ConvertWithCompat(tc.from, "openai-response", "requested-model", []byte(tc.body), false, claudeIdentity)
			if (wantErr == nil) != (gotErr == nil) || !bytes.Equal(want, got) {
				t.Fatalf("%s: unmarked fallback changed", tc.from)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.from, err)
		}
		if !bytes.Contains(out, []byte(`"file_id":"hosted-id","type":"input_image"`)) || !bytes.Contains(out, []byte(`"model":"requested-model"`)) || bytes.Contains(out, []byte(Marker)) || bytes.Contains(out, []byte(sentinel)) {
			t.Fatalf("%s: %s", tc.from, out)
		}
	}
}

func TestOriginalUnmarkedBehavior(t *testing.T) {
	for _, tc := range []struct {
		from, to, body string
		fn             converter
	}{{"openai-response", "claude", `{"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`, clauderesp.ConvertOpenAIResponsesRequestToClaude}, {"openai-response", "gemini", `{"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`, geminiresp.ConvertOpenAIResponsesRequestToGemini}, {"openai", "gemini", `{"messages":[{"role":"user","content":"hello"}]}`, geminichat.ConvertOpenAIRequestToGemini}} {
		want, e1 := tc.fn("test", []byte(tc.body), false)
		got, e2 := ConvertWithCompat(tc.from, tc.to, "test", []byte(tc.body), false, tc.fn)
		if (e1 == nil) != (e2 == nil) || !bytes.Equal(want, got) {
			t.Fatalf("unmarked %s/%s changed", tc.from, tc.to)
		}
	}
}
func TestHostedImagesUseNativePositions(t *testing.T) {
	cases := []struct {
		name, from, to, provider, body, want string
		fn                                   converter
	}{
		{"claude-user", "openai-response", "claude", "claude", `{"input":[{"role":"user","content":[{"type":"input_image","file_id":"hosted-id"}]}]}`, `"source":{"file_id":"hosted-id","type":"file"}`, clauderesp.ConvertOpenAIResponsesRequestToClaude},
		{"claude-tool", "openai-response", "claude", "claude", `{"input":[{"type":"function_call","call_id":"call","name":"tool","arguments":"{}"},{"type":"function_call_output","call_id":"call","output":[{"type":"input_image","file_id":"hosted-id"}]}]}`, `"type":"tool_result"`, clauderesp.ConvertOpenAIResponsesRequestToClaude},
		{"gemini-responses-user", "openai-response", "gemini", "gemini", `{"input":[{"role":"user","content":[{"type":"input_image","file_id":"hosted-id"}]}]}`, `"fileData"`, geminiresp.ConvertOpenAIResponsesRequestToGemini},
		{"gemini-responses-tool", "openai-response", "gemini", "gemini", `{"input":[{"type":"function_call","call_id":"call","name":"tool","arguments":"{}"},{"type":"function_call_output","call_id":"call","output":[{"type":"input_image","file_id":"hosted-id"}]}]}`, `"parts":[{"fileData"`, geminiresp.ConvertOpenAIResponsesRequestToGemini},
		{"gemini-chat-user", "openai", "gemini", "gemini", `{"messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"hosted-id"}}]}]}`, `"fileData"`, geminichat.ConvertOpenAIRequestToGemini},
		{"gemini-claude-user", "claude", "gemini", "gemini", `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"file","file_id":"hosted-id"}}]}]}`, `"fileData"`, geminiclaude.ConvertClaudeRequestToGemini},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ConvertWithCompat(tc.from, tc.to, "test", marked(t, tc.body, tc.provider), false, tc.fn)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(out, []byte(tc.want)) || bytes.Contains(out, []byte(sentinel)) || bytes.Contains(out, []byte(Marker)) || bytes.Contains(out, []byte("base64")) {
				t.Fatalf("bad native image placement: %s", out)
			}
		})
	}
}
func TestLostAndMalformedImagesFailClosed(t *testing.T) {
	body := marked(t, `{"input":[{"role":"user","content":[{"type":"input_image","file_id":"hosted-id"}]}]}`, "claude")
	out, err := ConvertWithCompat("openai-response", "claude", "test", body, false, func(string, []byte, bool) ([]byte, error) { return []byte(`{"messages":[]}`), nil })
	if err == nil || out != nil {
		t.Fatal("lost image accepted")
	}
	out, err = ConvertWithCompat("openai-response", "gemini", "test", body, false, geminiresp.ConvertOpenAIResponsesRequestToGemini)
	if err == nil || out != nil {
		t.Fatal("provider mismatch accepted")
	}
}
