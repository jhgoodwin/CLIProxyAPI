package executor

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexSolFastModel(t *testing.T) {
	for _, mode := range []string{"http", "http-stream", "compact", "websocket", "websocket-stream"} {
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
				cfg := &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}, Payload: tc.payload}
				req := cliproxyexecutor.Request{Model: tc.model, Payload: []byte(`{"model":"` + tc.model + `","input":[{"role":"user","content":"hi"}]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")}
				wantHint := "model=gpt-6.1-sol"
				if tc.wantTier != "" {
					wantHint += ";tier=" + tc.wantTier
				}
				if mode == "websocket" || mode == "websocket-stream" {
					var rec codexWebsocketHintRecorder
					server := newCodexWebsocketHintServer(t, &rec)
					defer server.Close()
					auth := codexOAuthTestAuth(server.URL)
					auth.ID = "sol-fast-test"
					exec := NewCodexWebsocketsExecutor(cfg)
					if mode == "websocket-stream" {
						result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
						if err != nil {
							t.Fatal(err)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					} else if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
						t.Fatal(err)
					}
					rec.mu.Lock()
					defer rec.mu.Unlock()
					if len(rec.frames) != 1 || rec.frames[0].model != "gpt-6.1-sol" || rec.frames[0].tier != tc.wantTier {
						t.Fatalf("frames = %+v, want upstream Sol with tier %q", rec.frames, tc.wantTier)
					}
					if len(rec.handshakes) != 1 || rec.handshakes[0] != wantHint {
						t.Fatalf("hints = %v, want %q", rec.handshakes, wantHint)
					}
					return
				}
				var captured capturedCodexRequest
				server := newCodexRoutingHintServer(t, &captured)
				defer server.Close()
				auth := codexOAuthTestAuth(server.URL)
				exec := NewCodexExecutor(cfg)
				if mode == "http-stream" {
					result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else {
					if mode == "compact" {
						opts.Alt = "responses/compact"
					}
					if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
						t.Fatal(err)
					}
				}
				if captured.model.String() != "gpt-6.1-sol" || captured.serviceTier.String() != tc.wantTier || captured.routingHint != wantHint {
					t.Fatalf("upstream model=%q tier=%q hint=%q; want Sol tier=%q hint=%q", captured.model.String(), captured.serviceTier.String(), captured.routingHint, tc.wantTier, wantHint)
				}
			})
		}
	}
}
