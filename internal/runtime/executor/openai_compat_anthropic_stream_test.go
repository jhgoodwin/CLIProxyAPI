package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestOpenAICompatAnthropicRawFinishValidation(t *testing.T) {
	for _, reason := range []string{"tool_calls", "stop", "length", "content_filter", "unknown", ""} {
		t.Run(reason, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "data: "+`{"id":"chat","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call","type":"function","function":{"name":"f","arguments":"{"}}]}}]}`+"\n\n")
				if reason != "" {
					_, _ = fmt.Fprintf(w, "data: %s\n\n", `{"id":"chat","choices":[{"index":0,"delta":{},"finish_reason":"`+reason+`"}]}`)
				}
				_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer server.Close()
			exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
			auth := &cliproxyauth.Auth{Provider: "openai-compatibility", Attributes: map[string]string{"base_url": server.URL}}
			req := cliproxyexecutor.Request{Model: "compatible", Payload: []byte(`{"max_tokens":50,"messages":[{"role":"user","content":"hi"}]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: sdktranslator.FormatClaude}
			response, err := exec.ExecuteStream(helps.WithAnthropicPayloadLimits(context.Background(), 100, true), auth, req, opts)
			if err != nil {
				t.Fatal(err)
			}
			var failed bool
			var payload strings.Builder
			for chunk := range response.Chunks {
				if chunk.Err != nil {
					failed = true
				}
				payload.Write(chunk.Payload)
			}
			if reason == "length" {
				if failed || !strings.Contains(payload.String(), `"stop_reason":"max_tokens"`) {
					t.Fatalf("genuine truncation rejected: %s", payload.String())
				}
			} else if !failed || strings.Contains(payload.String(), "event: message_stop") {
				t.Fatalf("invalid finish fabricated success: failed=%v %s", failed, payload.String())
			}
		})
	}
}
