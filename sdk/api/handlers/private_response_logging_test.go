package handlers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/middleware"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
)

// This sink records calls at their persistence boundary, including stream creation
// and status writes that occur before middleware Finalize.
type privateResponseLogSink struct {
	enabled bool
	dir     string
	mu      sync.Mutex
	calls   []string
	payload bytes.Buffer
	sources []*logging.FileBodySource
	chunks  chan struct{}
}

func (s *privateResponseLogSink) record(call string, payload ...[]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
	for _, p := range payload {
		s.payload.Write(p)
	}
}
func (s *privateResponseLogSink) IsEnabled() bool { return s.enabled }
func (s *privateResponseLogSink) NewFileBodySource(prefix string) (*logging.FileBodySource, error) {
	source, err := logging.NewFileBodySourceInDir(s.dir, prefix)
	if err == nil {
		s.sources = append(s.sources, source)
	}
	return source, err
}
func (s *privateResponseLogSink) LogRequest(_ string, _ string, _ map[string][]string, request []byte, _ int, _ map[string][]string, response, timeline, apiRequest, apiResponse, apiTimeline []byte, failures []*interfaces.ErrorMessage, _ string, _ time.Time, _ time.Time) error {
	s.record("request", request, response, timeline, apiRequest, apiResponse, apiTimeline)
	for _, failure := range failures {
		if failure != nil && failure.Error != nil {
			s.record("error", []byte(failure.Error.Error()))
		}
	}
	return nil
}
func (s *privateResponseLogSink) LogRequestWithOptions(url, method string, headers map[string][]string, request []byte, status int, responseHeaders map[string][]string, response, timeline, apiRequest, apiResponse, apiTimeline []byte, failures []*interfaces.ErrorMessage, force bool, id string, started, responded time.Time) error {
	if !s.enabled && !force {
		return fmt.Errorf("error-only request was not forced")
	}
	return s.LogRequest(url, method, headers, request, status, responseHeaders, response, timeline, apiRequest, apiResponse, apiTimeline, failures, id, started, responded)
}
func (s *privateResponseLogSink) LogStreamingRequest(_ string, _ string, _ map[string][]string, body []byte, _ string) (logging.StreamingLogWriter, error) {
	s.record("stream-open", body)
	return &privateResponseStreamSink{sink: s}, nil
}
func (s *privateResponseLogSink) snapshot() ([]string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...), s.payload.String()
}

type privateResponseStreamSink struct{ sink *privateResponseLogSink }

func (s *privateResponseStreamSink) WriteChunkAsync(body []byte) {
	s.sink.record("stream-chunk", body)
	s.sink.chunks <- struct{}{}
}
func (s *privateResponseStreamSink) WriteStatus(_ int, _ map[string][]string) error {
	s.sink.record("stream-status")
	return nil
}
func (s *privateResponseStreamSink) WriteAPIRequest(body []byte) error {
	s.sink.record("stream-api-request", body)
	return nil
}
func (s *privateResponseStreamSink) WriteAPIResponse(body []byte) error {
	s.sink.record("stream-api-response", body)
	return nil
}
func (s *privateResponseStreamSink) WriteAPIWebsocketTimeline(body []byte) error {
	s.sink.record("stream-api-timeline", body)
	return nil
}
func (s *privateResponseStreamSink) SetFirstChunkTimestamp(time.Time) {}
func (s *privateResponseStreamSink) Close() error                     { s.sink.record("stream-close"); return nil }

func assertPrivateLogTempsRemoved(t *testing.T, sink *privateResponseLogSink) {
	t.Helper()
	entries, err := os.ReadDir(sink.dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary captures remain: entries=%v err=%v", entries, err)
	}
	for _, source := range sink.sources {
		if source.HasPayload() {
			t.Fatal("cleaned source retains payload")
		}
		if _, err := source.CreatePart("after-privacy"); err == nil {
			t.Fatal("cleaned source accepts new image capture")
		}
	}
}

func TestResolvedPrivateFilesSuppressDownstreamLogs(t *testing.T) {
	for _, protocol := range []string{"openai-response", "openai", "claude"} {
		for _, enabled := range []bool{true, false} {
			for _, streaming := range []bool{false, true} {
				for _, private := range []bool{true, false} {
					t.Run(fmt.Sprintf("%s/full=%v/stream=%v/private=%v", protocol, enabled, streaming, private), func(t *testing.T) {
						h, _, id, _, _ := sharedFilesFixture(t)
						h.Cfg.RequestLog = enabled
						h.Cfg.PassthroughHeaders = true
						sink := &privateResponseLogSink{enabled: enabled, dir: t.TempDir(), chunks: make(chan struct{}, 2)}
						body := `{"model":"synthetic","messages":[{"role":"user","content":"ordinary"}]}`
						if private {
							switch protocol {
							case "openai-response":
								body = fmt.Sprintf(`{"input":[{"content":[{"type":"input_image","file_id":%q}]}]}`, id)
							case "openai":
								body = fmt.Sprintf(`{"messages":[{"content":[{"type":"file","file":{"file_id":%q}}]}]}`, id)
							case "claude":
								body = fmt.Sprintf(`{"messages":[{"content":[{"type":"image","source":{"type":"file","file_id":%q}}]}]}`, id)
							}
						}
						failure := &originalPrivateFailure{body: `{"error":{"message":"original private upstream diagnostic","type":"rate_limit_error","code":"synthetic_limit"}}`, headers: http.Header{"Retry-After": {"23"}}, retry: 23 * time.Second}
						router := gin.New()
						router.Use(middleware.RequestLoggingMiddleware(sink))
						router.POST("/v1/responses", func(c *gin.Context) {
							if c.GetHeader(helps.FileProbePrivacyHeader) != "" {
								t.Fatal("test must not use probe header")
							}
							input, err := io.ReadAll(c.Request.Body)
							if err != nil {
								t.Fatal(err)
							}
							if len(sink.sources) == 0 {
								t.Fatal("expected allocated temporary capture sources")
							}
							for _, source := range sink.sources {
								if err := source.AppendBytes([]byte("pre-resolution capture")); err != nil {
									t.Fatal(err)
								}
							}
							ctx := access.WithAuthenticatedPrincipal(context.WithValue(c.Request.Context(), "gin", c), "alice")
							execCtx, resolved, resolveErr := h.prepareFileReferences(ctx, protocol, input)
							if resolveErr != nil {
								t.Fatal(resolveErr)
							}
							if helps.FilePrivacyContext(execCtx) != private || helps.FilePrivacyRequest(c.Request) != private {
								t.Fatal("resolved privacy did not reach request")
							}
							if private {
								if !sharedFilesContext(execCtx) {
									t.Fatal("request did not resolve local Files")
								}
								if protocol == "openai-response" {
									if _, err := helps.FileRequestFromContext(execCtx).Pending[id].Read(execCtx); err != nil {
										t.Fatal(err)
									}
								} else if !bytes.Contains(resolved, []byte("base64")) {
									t.Fatal("portable file image was not resolved")
								}
								assertPrivateLogTempsRemoved(t, sink)
							}
							msg := executionErrorMessage(failure)
							h.LoggingAPIResponseError(execCtx, msg)
							if streaming {
								c.Header("Content-Type", "text/event-stream")
								c.Header("Retry-After", msg.Addon.Get("Retry-After"))
								c.Writer.WriteHeader(msg.StatusCode)
								_, _ = c.Writer.Write([]byte("event: error\ndata: "))
								_, _ = c.Writer.WriteString(msg.Error.Error() + "\n\n")
								c.Writer.Flush()
								if enabled && !private {
									<-sink.chunks
									<-sink.chunks
								}
							} else {
								h.WriteErrorResponse(c, msg)
							}
							if private {
								if calls, _ := sink.snapshot(); len(calls) != 0 {
									t.Fatalf("persistent writes before Finalize: %v", calls)
								}
								for _, key := range []string{"API_RESPONSE", "API_RESPONSE_ERROR"} {
									if value, _ := c.Get(key); value != nil {
										t.Fatalf("private handler capture remains: %s", key)
									}
								}
							}
						})
						req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
						req.ContentLength = -1 // Exercise error-only disk spooling, not just memory capture.
						req.Header.Set("Content-Type", "application/json")
						recorder := httptest.NewRecorder()
						router.ServeHTTP(recorder, req)
						want := failure.body
						if streaming {
							want = "event: error\ndata: " + want + "\n\n"
						}
						if recorder.Code != 429 || recorder.Header().Get("Retry-After") != "23" || recorder.Body.String() != want {
							t.Fatalf("client failure changed: status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
						}
						calls, payload := sink.snapshot()
						if private {
							if len(calls) != 0 || payload != "" {
								t.Fatalf("private logging persisted: calls=%v payload=%q", calls, payload)
							}
							assertPrivateLogTempsRemoved(t, sink)
						} else if len(calls) == 0 || !strings.Contains(payload, failure.body) || !strings.Contains(payload, body) {
							t.Fatalf("ordinary request logging changed: calls=%v payload=%q", calls, payload)
						}
					})
				}
			}
		}
	}
}
