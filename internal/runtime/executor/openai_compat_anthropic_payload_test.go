package executor

import (
	"context"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAICompatAnthropicFinalPayloadLimits(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
		filter []string
		valid  bool
	}{
		{name: "valid override", params: map[string]any{"max_tokens": 80, "reasoning_effort": "max"}, valid: true},
		{name: "over cap", params: map[string]any{"max_tokens": 101}},
		{name: "fraction", params: map[string]any{"max_tokens": 1.5}},
		{name: "deletion", filter: []string{"max_tokens"}},
		{name: "conflicting limits", params: map[string]any{"max_completion_tokens": 80}},
		{name: "invalid effort", params: map[string]any{"reasoning_effort": "xhigh"}},
	}
	for _, stream := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(tc.name+map[bool]string{true: " stream", false: " execute"}[stream], func(t *testing.T) {
				called := false
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					called = true
					body, _ := io.ReadAll(r.Body)
					if gjson.GetBytes(body, "max_tokens").Int() != 80 || gjson.GetBytes(body, "reasoning_effort").String() != "max" {
						t.Errorf("final override lost: %s", body)
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = w.Write([]byte("data: [DONE]\n\n"))
					} else {
						_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
					}
				}))
				defer server.Close()
				models := []config.PayloadModelRule{{Name: "*"}}
				cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: models, Params: tc.params}}, Filter: []config.PayloadFilterRule{{Models: models, Params: tc.filter}}}}
				exec := NewOpenAICompatExecutor("openai-compatibility", cfg)
				auth := &cliproxyauth.Auth{Provider: "openai-compatibility", Attributes: map[string]string{"base_url": server.URL}}
				req := cliproxyexecutor.Request{Model: "compatible", Payload: []byte(`{"max_tokens":50,"messages":[{"role":"user","content":"hi"}]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: sdktranslator.FormatOpenAI}
				ctx := helps.WithAnthropicPayloadLimits(context.Background(), 100, true)
				var err error
				if stream {
					response, e := exec.ExecuteStream(ctx, auth, req, opts)
					err = e
					if err == nil {
						for range response.Chunks {
						}
					}
				} else {
					_, err = exec.Execute(ctx, auth, req, opts)
				}
				if tc.valid {
					if err != nil || !called {
						t.Fatalf("valid override rejected: %v", err)
					}
				} else if err == nil || called {
					t.Fatalf("invalid final payload sent called=%v err=%v", called, err)
				}
			})
		}
	}
}
