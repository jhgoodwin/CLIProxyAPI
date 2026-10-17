package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatAnthropicFinalLocalFileValidation(t *testing.T) {
	id := "file_" + strings.Repeat("a", 64) + "_" + strings.Repeat("b", 16) + "_" + strings.Repeat("c", 32)
	for _, stream := range []bool{false, true} {
		for _, remove := range []bool{false, true} {
			t.Run(map[bool]string{true: "stream", false: "execute"}[stream]+map[bool]string{true: "-removed", false: "-injected"}[remove], func(t *testing.T) {
				called := false
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					called = true
					body, _ := io.ReadAll(r.Body)
					if gjson.GetBytes(body, "configured.file_id").Exists() {
						t.Error("filtered file field restored")
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = w.Write([]byte("data: [DONE]\n\n"))
					} else {
						_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
					}
				}))
				defer server.Close()
				models := []config.PayloadModelRule{{Name: "*"}}
				cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: models, Params: map[string]any{"configured.file_id": id}}}}}
				if remove {
					cfg.Payload.Filter = []config.PayloadFilterRule{{Models: models, Params: []string{"configured"}}}
				}
				exec := NewOpenAICompatExecutor("openai-compatibility", cfg)
				auth := &cliproxyauth.Auth{Provider: "openai-compatibility", Attributes: map[string]string{"base_url": server.URL, "api_key": "test"}}
				req := cliproxyexecutor.Request{Model: "compatible", Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: sdktranslator.FormatOpenAI}
				var err error
				if stream {
					var response *cliproxyexecutor.StreamResult
					response, err = exec.ExecuteStream(context.Background(), auth, req, opts)
					if err == nil {
						for range response.Chunks {
						}
					}
				} else {
					_, err = exec.Execute(context.Background(), auth, req, opts)
				}
				if remove {
					if err != nil || !called {
						t.Fatalf("removal not respected: called=%v err=%v", called, err)
					}
				} else if err == nil || called {
					t.Fatalf("final local ID not rejected: called=%v err=%v", called, err)
				}
			})
		}
	}
}

func TestOpenAICompatAnthropicThinkingLevels(t *testing.T) {
	for _, effort := range []string{"auto", "low", "high", "max"} {
		t.Run(effort, func(t *testing.T) {
			var body []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ = io.ReadAll(r.Body)
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
			}))
			defer server.Close()
			payload := `{"thinking":{"type":"enabled"},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"prior","signature":""},{"type":"text","text":"done"}]},{"role":"user","content":"next"}]}`
			if effort != "auto" {
				payload = strings.Replace(payload, `"thinking":`, `"output_config":{"effort":"`+effort+`"},"thinking":`, 1)
			}
			req := cliproxyexecutor.Request{Model: "compatible", Payload: []byte(payload), Metadata: map[string]any{"cliproxy.resolved_api_key_model_info": &registry.ModelInfo{ID: "compatible", IsCompat: true, UserDefined: true}}}
			exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
			auth := &cliproxyauth.Auth{Provider: "openai-compatibility", Attributes: map[string]string{"base_url": server.URL, "api_key": "test"}}
			_, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: sdktranslator.FormatOpenAI})
			if err != nil {
				t.Fatal(err)
			}
			if gjson.GetBytes(body, "reasoning_effort").String() != effort || gjson.GetBytes(body, "messages.0.reasoning_content").String() != "prior" {
				t.Fatalf("thinking lost: %s", body)
			}
		})
	}
}
