package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestNvidiaFilesExecutorSelectedRoute(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, mode := range []string{"normal", "direct", "override", "unsupported", "filtered"} {
			t.Run(mode+map[bool]string{false: "execute", true: "stream"}[stream], func(t *testing.T) {
				const id = "87b132b0-08f9-43ea-8fab-f107350a5d00"
				local := "file-cpa-local"
				ctx := helps.WithFileRequest(context.Background(), &helps.FileRequest{Local: true, Allowed: map[string]bool{}, Pending: map[string]helps.PendingFile{local: {Name: "screenshot.png", Read: func(context.Context) ([]byte, error) { return []byte("\x89PNG\r\n\x1a\nimage"), nil }}}})
				events := []string{}
				ctx = context.WithValue(ctx, "cliproxy.roundtripper", deepSeekFilesTransport(func(r *http.Request) (*http.Response, error) {
					reply := func(payload string) *http.Response {
						return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload))}
					}
					switch r.URL.Host {
					case "api.nvcf.nvidia.com":
						if r.Header.Get("Authorization") != "Bearer selected" {
							t.Fatal("wrong reservation key")
						}
						events = append(events, "reserve")
						return reply(`{"assetId":"` + id + `","uploadUrl":"https://bucket.s3.amazonaws.com/image?X-Amz-Signature=private","contentType":"image/png"}`), nil
					case "bucket.s3.amazonaws.com":
						if r.Method != "PUT" || r.Header.Get("Authorization") != "" {
							t.Fatal("signed PUT leaked bearer")
						}
						events = append(events, "put")
						return reply(""), nil
					case "ai.api.nvidia.com":
						events = append(events, "inference")
						if r.URL.String() != helps.NvidiaVilaInferenceURL || r.Header.Get("Authorization") != "Bearer selected" || r.Header.Get("Content-Type") != "application/json" {
							t.Fatal("inference selected route/framing changed")
						}
						body, err := io.ReadAll(r.Body)
						if err != nil {
							t.Fatal(err)
						}
						if mode == "filtered" {
							if r.Header.Get("NVCF-INPUT-ASSET-REFERENCES") != "" || strings.Contains(string(body), helps.NvidiaAssetPrefix) {
								t.Fatal("filtered asset restored")
							}
						} else if r.Header.Get("NVCF-INPUT-ASSET-REFERENCES") != id || !strings.Contains(string(body), helps.NvidiaAssetPrefix+id) {
							t.Fatalf("asset/header mismatch %s", body)
						}
						if strings.Contains(string(body), "base64") || strings.Contains(string(body), local) {
							t.Fatal("local/inline image leaked")
						}
						if stream {
							return reply("data: [DONE]\n\n"), nil
						}
						return reply(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`), nil
					default:
						t.Fatalf("unexpected endpoint %s", r.URL)
						return nil, nil
					}
				}))
				cfg := &config.Config{}
				switch mode {
				case "override":
					cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"messages.0.content.0.image_url.url": "data:image/png;base64,AA=="}}}
				case "filtered":
					cfg.Payload.Filter = []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: []string{"messages.0.content"}}}
				}
				auth := &cliproxyauth.Auth{Provider: "nvidia", Attributes: map[string]string{"base_url": "https://integrate.api.nvidia.com/v1", "api_key": "selected", "header:Authorization": "Bearer wrong", "header:NVCF-INPUT-ASSET-REFERENCES": "foreign", "header:Content-Type": "text/plain"}}
				model := "nvidia/vila"
				if mode == "unsupported" {
					model = "nvidia/neva"
				}
				if mode == "direct" {
					auth.Attributes["base_url"] = helps.NvidiaVilaInferenceURL
				}
				req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"input":[{"role":"user","content":[{"type":"input_image","file_id":"` + local + `"}]}]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAI}
				exec := NewOpenAICompatExecutor("nvidia", cfg).DecorateForAuth(auth)
				var err error
				if stream {
					var result *cliproxyexecutor.StreamResult
					result, err = exec.ExecuteStream(ctx, auth, req, opts)
					if err == nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					}
				} else {
					_, err = exec.Execute(ctx, auth, req, opts)
				}
				if mode == "override" || mode == "unsupported" {
					if err == nil || len(events) > 2 {
						t.Fatalf("unsafe inference events=%v err=%v", events, err)
					}
				} else if err != nil || len(events) != 3 {
					t.Fatalf("events=%v err=%v", events, err)
				}
			})
		}
	}
}
