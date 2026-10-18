package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// TestCodexSolFastModelUnderSIWC pins the merged ordering: fast tier, then SIWC
// body rules, then user payload rules exactly once as the final semantic barrier.
func TestCodexSolFastModelUnderSIWC(t *testing.T) {
	for _, mode := range []string{"http", "http-stream", "compact"} {
		for _, tc := range []struct {
			name     string
			model    string
			payload  config.PayloadConfig
			wantTier string
		}{
			{name: "base unchanged", model: "gpt-6.1-sol"},
			{name: "fast", model: "gpt-6.1-sol-fast", wantTier: "priority"},
			{name: "fast reasoning suffix", model: "gpt-6.1-sol-fast(low)", wantTier: "priority"},
			{name: "operator override wins", model: "gpt-6.1-sol-fast", wantTier: "default", payload: config.PayloadConfig{Override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "gpt-6.1-sol-fast"}},
				Params: map[string]any{"service_tier": "default"},
			}}}},
			{name: "operator filter wins", model: "gpt-6.1-sol-fast", payload: config.PayloadConfig{Filter: []config.PayloadFilterRule{{
				Models: []config.PayloadModelRule{{Name: "gpt-6.1-sol-fast"}},
				Params: []string{"service_tier"},
			}}}},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				var mu sync.Mutex
				var gotBody map[string]any
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					raw, _ := io.ReadAll(r.Body)
					mu.Lock()
					gotBody = nil
					_ = json.Unmarshal(raw, &gotBody)
					mu.Unlock()
					if mode == "compact" {
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-6.1-sol\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
				}))
				defer upstream.Close()

				auth := &cliproxyauth.Auth{
					ID:         "codex-siwc-fast",
					Provider:   "codex",
					Attributes: map[string]string{"base_url": upstream.URL + "/v1"},
					Metadata: map[string]any{
						"type":     "codex",
						"use_siwc": true,
						"siwc":     map[string]any{"access_token": "siwc-token", "refresh_token": "siwc-refresh", "client_id": "oaiapp_test"},
					},
				}
				if !codexUseSIWC(auth) {
					t.Fatal("expected SIWC auth")
				}
				cfg := &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}, Payload: tc.payload}
				exec := NewCodexExecutor(cfg)
				req := cliproxyexecutor.Request{Model: tc.model, Payload: []byte(`{"model":"` + tc.model + `","input":[{"role":"user","content":"hi"}]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")}
				switch mode {
				case "http-stream":
					result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				case "compact":
					opts.Alt = "responses/compact"
					fallthrough
				default:
					if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
						t.Fatal(err)
					}
				}

				mu.Lock()
				defer mu.Unlock()
				if gotBody == nil {
					t.Fatal("upstream did not receive a request")
				}
				if gotBody["model"] != "gpt-6.1-sol" {
					t.Fatalf("upstream model = %v, want gpt-6.1-sol", gotBody["model"])
				}
				tier, hasTier := gotBody["service_tier"]
				if tc.wantTier == "" {
					if hasTier {
						t.Fatalf("service_tier = %v, want absent", tier)
					}
				} else if tier != tc.wantTier {
					t.Fatalf("service_tier = %v, want %q", tier, tc.wantTier)
				}
			})
		}
	}
}
