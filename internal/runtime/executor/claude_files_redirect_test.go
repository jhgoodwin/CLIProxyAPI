package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestClaudeHostedFileInferenceRejectsRedirects(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, sameOrigin := range []bool{false, true} {
			for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
				t.Run(fmt.Sprintf("stream=%v/sameOrigin=%v/status=%d", stream, sameOrigin, status), func(t *testing.T) {
					var leaked atomic.Int32
					target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1); fmt.Fprint(w, `{}`) }))
					defer target.Close()
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						switch r.URL.Path {
						case "/v1/files":
							fmt.Fprint(w, `{"id":"file_remote"}`)
						case "/v1/messages":
							destination := target.URL + "/leak"
							if sameOrigin {
								destination = "/leak"
							}
							http.Redirect(w, r, destination, status)
						case "/leak":
							leaked.Add(1)
							fmt.Fprint(w, `{}`)
						default:
							w.WriteHeader(404)
						}
					}))
					defer server.Close()
					auth := &cliproxyauth.Auth{ID: "owner", Provider: "claude", Attributes: map[string]string{"api_key": "selected", "base_url": server.URL}}
					ctx := helps.WithFileRequest(context.Background(), &helps.FileRequest{Local: true, Pending: map[string]helps.PendingFile{"file-cpa-local": {Name: "pixel.png", Read: func(context.Context) ([]byte, error) { return []byte("pixel"), nil }}}})
					req := cliproxyexecutor.Request{Model: "claude-sonnet-4-6", Payload: []byte(`{"model":"claude-sonnet-4-6","max_tokens":32,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"file","file_id":"file-cpa-local"}}]}]}`)}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")}
					e := NewClaudeExecutor(&config.Config{})
					var err error
					if stream {
						_, err = e.ExecuteStream(ctx, auth, req, opts)
					} else {
						_, err = e.Execute(ctx, auth, req, opts)
					}
					if err == nil {
						t.Error("expected redirect rejection")
					}
					if leaked.Load() != 0 {
						t.Fatalf("redirect forwarded %d requests", leaked.Load())
					}
				})
			}
		}
	}
}
