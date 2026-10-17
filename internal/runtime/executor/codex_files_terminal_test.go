package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestFileUpstreamFailuresPreserveDiagnosticsAndRetry(t *testing.T) {
	const errorBody = `{"error":{"type":"usage_limit_reached","code":"usage_limit_reached","message":"synthetic upstream diagnostic","resets_in_seconds":37}}`
	const event = `{"type":"response.failed","response":{"status":"failed","error":{"type":"usage_limit_reached","code":"usage_limit_reached","message":"synthetic upstream diagnostic","resets_in_seconds":37}}}`
	for _, terminal := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, buffering := range []bool{false, true} {
				name := "http"
				if terminal {
					name = "terminal"
				}
				if stream {
					name += "_stream"
				} else {
					name += "_nonstream"
				}
				if buffering {
					name += "_buffered"
				}
				t.Run(name, func(t *testing.T) {
					s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if terminal {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = io.WriteString(w, "data: "+event+"\n\n")
						} else {
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(http.StatusTooManyRequests)
							_, _ = io.WriteString(w, errorBody)
						}
					}))
					defer s.Close()
					cfg := &config.Config{}
					cfg.RequestLog = true
					cfg.Codex.StreamBootstrapBuffering = buffering
					e := NewCodexExecutor(cfg)
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
					ctx := helps.WithFileRequest(context.WithValue(context.Background(), "gin", c), &helps.FileRequest{Owner: "a", Allowed: map[string]bool{"upstream": true}})
					a := &auth.Auth{ID: "a", Provider: "codex", Attributes: map[string]string{"base_url": s.URL}, Metadata: map[string]any{"access_token": "synthetic"}}
					req := exec.Request{Model: "gpt-5", Payload: []byte(`{"input":[{"role":"user","content":[{"type":"input_image","file_id":"upstream"}]}]}`)}
					opts := exec.Options{SourceFormat: translator.FormatOpenAIResponse, OriginalRequest: req.Payload}
					var got error
					if stream {
						result, err := e.ExecuteStream(ctx, a, req, opts)
						got = err
						if err == nil {
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									got = chunk.Err
								}
							}
						}
					} else {
						_, got = e.Execute(ctx, a, req, opts)
					}
					original, ok := got.(statusErr)
					if !ok {
						t.Fatalf("error type=%T, want original statusErr", got)
					}
					want := newCodexStatusErrWithCooling(http.StatusTooManyRequests, []byte(errorBody), false)
					if original.StatusCode() != want.StatusCode() || original.Error() != want.Error() || !original.IsCredentialScoped() {
						t.Fatal("upstream error status, body, or credential scope changed")
					}
					if original.RetryAfter() == nil || *original.RetryAfter() != 37*time.Second {
						t.Fatal("upstream retry interval changed")
					}
					if !strings.Contains(original.Error(), "synthetic upstream diagnostic") || strings.Contains(original.LogDiagnostic(), "synthetic upstream diagnostic") {
						t.Fatal("client diagnostic or private log separation changed")
					}
					for _, key := range []string{"API_REQUEST", "API_RESPONSE", "API_RESPONSE_HEADERS", "API_UPSTREAM_ATTEMPTS"} {
						if _, exists := c.Get(key); exists {
							t.Fatalf("private capture created: %s", key)
						}
					}
				})
			}
		}
	}
}

func TestFileTerminalFailurePreservesClientDiagnosticWithoutLogExposure(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"file not found: PRIVATE_SECRET https://blob?sig=POISON\"}}}\n\n")
	}))
	defer s.Close()
	for _, stream := range []bool{false, true} {
		e := NewCodexExecutor(&config.Config{})
		ctx := helps.WithFileRequest(context.Background(), &helps.FileRequest{Owner: "a", Allowed: map[string]bool{"upstream": true}})
		a := &auth.Auth{ID: "a", Provider: "codex", Attributes: map[string]string{"base_url": s.URL}, Metadata: map[string]any{"access_token": "synthetic"}}
		req := exec.Request{Model: "gpt-5", Payload: []byte(`{"input":[{"role":"user","content":[{"type":"input_image","file_id":"upstream"}]}]}`)}
		opts := exec.Options{SourceFormat: translator.FormatOpenAIResponse, OriginalRequest: req.Payload}
		if stream {
			result, err := e.ExecuteStream(ctx, a, req, opts)
			if err != nil {
				assertPrivateTerminalDiagnostic(t, err)
				continue
			}
			found := false
			for chunk := range result.Chunks {
				if strings.Contains(string(chunk.Payload), "POISON") || strings.Contains(string(chunk.Payload), "PRIVATE") {
					t.Fatal("payload leaked terminal error")
				}
				if chunk.Err != nil {
					found = true
					assertPrivateTerminalDiagnostic(t, chunk.Err)
				}
			}
			if !found {
				t.Fatal("missing failure")
			}
		} else {
			_, err := e.Execute(ctx, a, req, opts)
			assertPrivateTerminalDiagnostic(t, err)
		}
	}
}

func assertPrivateTerminalDiagnostic(t *testing.T, err error) {
	t.Helper()
	original, ok := err.(statusErr)
	if !ok || !strings.Contains(original.Error(), "POISON") || !strings.Contains(original.Error(), "PRIVATE") {
		t.Fatal("original synthetic upstream client diagnostic changed")
	}
	if strings.Contains(original.LogDiagnostic(), "POISON") || strings.Contains(original.LogDiagnostic(), "PRIVATE") {
		t.Fatal("private terminal diagnostic entered logging")
	}
}
