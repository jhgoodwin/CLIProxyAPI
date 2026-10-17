package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestClaudeFilesUploadBeforeInference(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, reject := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v/reject=%v", stream, reject), func(t *testing.T) {
				var uploaded, inferred atomic.Int32
				server := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("x-api-key") != "selected" || r.Header.Get("Authorization") != "" {
						t.Errorf("credential overridden: %q", r.Header.Get("x-api-key"))
					}
					switch r.URL.Path {
					case "/v1/files":
						uploaded.Add(1)
						if !strings.Contains(r.Header.Get("Anthropic-Beta"), "files-api-2025-04-14") {
							t.Error("missing upload beta")
						}
						if err := r.ParseMultipartForm(1024); err != nil {
							t.Error(err)
						}
						f, _, err := r.FormFile("file")
						if err != nil {
							t.Error(err)
						} else {
							data, _ := io.ReadAll(f)
							f.Close()
							if string(data) != "pixel" {
								t.Errorf("data=%q", data)
							}
						}
						if reject {
							w.WriteHeader(403)
							return
						}
						fmt.Fprint(w, `{"id":"file_remote"}`)
					case "/v1/messages":
						inferred.Add(1)
						if r.Header.Get("Content-Type") != "application/json" {
							t.Errorf("inference framing overridden: %q", r.Header.Get("Content-Type"))
						}
						if uploaded.Load() != 1 {
							t.Error("inference preceded upload")
						}
						body, _ := io.ReadAll(r.Body)
						if gjson.GetBytes(body, "messages.0.content.0.source.file_id").String() != "file_remote" || strings.Contains(string(body), "base64") || strings.Contains(string(body), "file-cpa-") {
							t.Errorf("payload=%s", body)
						}
						if !strings.Contains(r.Header.Get("Anthropic-Beta"), "files-api-2025-04-14") {
							t.Error("missing inference beta")
						}
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-6\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
						} else {
							fmt.Fprint(w, `{"id":"msg","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
						}
					default:
						t.Errorf("unexpected path %s", r.URL.Path)
						w.WriteHeader(404)
					}
				})
				auth := &cliproxyauth.Auth{ID: "owner", Provider: "claude", Attributes: map[string]string{"api_key": "selected", "base_url": "https://api.anthropic.com", "header:Authorization": "Bearer wrong", "header:Content-Type": "application/octet-stream"}}
				ctx := helps.WithFileRequest(context.Background(), &helps.FileRequest{Local: true, Pending: map[string]helps.PendingFile{"file-cpa-local": {Name: "pixel.png", Read: func(context.Context) ([]byte, error) { return []byte("pixel"), nil }}}})
				ctx = context.WithValue(ctx, "cliproxy.roundtripper", http.RoundTripper(filesFixtureTransport{"https://api.anthropic.com": server}))
				req := cliproxyexecutor.Request{Model: "claude-sonnet-4-6", Payload: []byte(`{"model":"claude-sonnet-4-6","max_tokens":32,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"file","file_id":"file-cpa-local"}}]}]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")}
				e := NewClaudeExecutor(&config.Config{}).DecorateForAuth(auth)
				var err error
				if stream {
					var result *cliproxyexecutor.StreamResult
					result, err = e.ExecuteStream(ctx, auth, req, opts)
					if err == nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								err = chunk.Err
							}
						}
					}
				} else {
					_, err = e.Execute(ctx, auth, req, opts)
				}
				if reject {
					if err == nil || inferred.Load() != 0 {
						t.Fatalf("rejection err=%v inference=%d", err, inferred.Load())
					}
				} else if err != nil || inferred.Load() != 1 {
					t.Fatalf("err=%v inference=%d", err, inferred.Load())
				}
			})
		}
	}
}
