package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/middleware"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
)

// originalPrivateFailure is an upstream failure carrying status, headers, and retry metadata.
type originalPrivateFailure struct {
	body    string
	headers http.Header
	retry   time.Duration
}

func (e *originalPrivateFailure) Error() string              { return e.body }
func (e *originalPrivateFailure) StatusCode() int            { return http.StatusTooManyRequests }
func (e *originalPrivateFailure) Headers() http.Header       { return e.headers }
func (e *originalPrivateFailure) RetryAfter() *time.Duration { return &e.retry }

// This writer sits below the real logging wrapper, holding a keepalive after its
// capture check but before body capture. Channels control both sides of suppression.
type privateKeepAliveGate struct {
	gin.ResponseWriter
	entered chan struct{}
	release chan struct{}
	flushed chan struct{}
}

func (w *privateKeepAliveGate) Write(body []byte) (int, error) {
	if string(body) == "\n" {
		w.entered <- struct{}{}
		<-w.release
	}
	return w.ResponseWriter.Write(body)
}

func (w *privateKeepAliveGate) Flush() {
	w.ResponseWriter.Flush()
	w.flushed <- struct{}{}
}

type privateKeepAliveSuppressionProbe struct {
	owner     interface{ SuppressRequestLogging(*gin.Context) }
	attempted chan struct{}
}

func (p privateKeepAliveSuppressionProbe) SuppressRequestLogging(c *gin.Context) {
	close(p.attempted)
	p.owner.SuppressRequestLogging(c)
}

func TestPrivateFilesKeepAliveSerializesLoggingSuppression(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("full=%v", enabled), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				type response struct {
					status int
					body   string
					retry  string
				}
				var ordinary response
				for _, private := range []bool{false, true} {
					h, _, id, _, _ := sharedFilesFixture(t)
					h.Cfg.RequestLog = true
					h.Cfg.PassthroughHeaders = true
					h.Cfg.NonStreamKeepAliveInterval = 1
					sink := &privateResponseLogSink{enabled: enabled, dir: t.TempDir()}
					gate := &privateKeepAliveGate{entered: make(chan struct{}), release: make(chan struct{}), flushed: make(chan struct{})}
					attempted := make(chan struct{})
					beginResolution := make(chan struct{})
					resolved := make(chan struct{})
					finishExecution := make(chan struct{})
					done := make(chan struct{})
					body := `{"input":"ordinary","stream":false}`
					if private {
						body = fmt.Sprintf(`{"stream":false,"input":[{"content":[{"type":"input_image","file_id":%q}]}]}`, id)
					}
					failure := &originalPrivateFailure{body: `{"error":{"message":"original keepalive upstream diagnostic","type":"rate_limit_error","code":"synthetic_limit"}}`, headers: http.Header{"Retry-After": {"23"}}, retry: 23 * time.Second}
					router := gin.New()
					router.Use(func(c *gin.Context) { gate.ResponseWriter = c.Writer; c.Writer = gate; c.Next() })
					router.Use(middleware.RequestLoggingMiddleware(sink))
					router.POST("/v1/responses", func(c *gin.Context) {
						if c.GetHeader(helps.FileProbePrivacyHeader) != "" {
							t.Error("probe header must be absent")
						}
						input, err := io.ReadAll(c.Request.Body)
						if err != nil {
							t.Error(err)
							return
						}
						c.Header("Content-Type", "application/json")
						ctx := access.WithAuthenticatedPrincipal(context.WithValue(c.Request.Context(), "gin", c), "alice")
						if private {
							owner, _ := c.Get(helps.FilePrivacyLogSuppressorKey)
							c.Set(helps.FilePrivacyLogSuppressorKey, privateKeepAliveSuppressionProbe{owner: owner.(interface{ SuppressRequestLogging(*gin.Context) }), attempted: attempted})
						}
						stopKeepAlive := h.StartNonStreamingKeepAlive(c, ctx)
						defer stopKeepAlive()
						<-beginResolution
						execCtx, _, resolveErr := h.prepareFileReferences(ctx, "openai-response", input)
						if resolveErr != nil {
							t.Errorf("file resolution: %v", resolveErr)
							return
						}
						if private {
							if _, err := helps.FileRequestFromContext(execCtx).Pending[id].Read(execCtx); err != nil {
								t.Error(err)
								return
							}
						}
						close(resolved)
						<-finishExecution
						stopKeepAlive()
						msg := executionErrorMessage(failure)
						if msg.Error != failure || msg.StatusCode != 429 || msg.Addon.Get("Retry-After") != "23" || *failure.RetryAfter() != 23*time.Second {
							t.Error("execution failure lost identity or retry metadata")
						}
						h.LoggingAPIResponseError(execCtx, msg)
						h.WriteErrorResponse(c, msg)
						if private {
							for _, key := range []string{"API_RESPONSE", "API_RESPONSE_ERROR"} {
								if value, _ := c.Get(key); value != nil {
									t.Errorf("private capture remains: %s", key)
								}
							}
						}
					})
					req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
					req.ContentLength = -1
					req.Header.Set("Content-Type", "application/json")
					recorder := httptest.NewRecorder()
					go func() { defer close(done); router.ServeHTTP(recorder, req) }()

					<-gate.entered // Virtual time advances to the actual keepalive timer.
					entries, err := os.ReadDir(sink.dir)
					if err != nil || len(entries) == 0 {
						t.Errorf("expected allocated capture before recognition: %v %v", entries, err)
					}
					close(beginResolution)
					if private {
						<-attempted
					}
					// Suppression has entered while Write is held inside the underlying
					// transport. Only the capture mutex orders cleanup against its return.
					gate.release <- struct{}{}
					<-gate.flushed
					<-resolved
					if private {
						assertPrivateLogTempsRemoved(t, sink)
					}

					// Hold execution through another real keepalive after privacy recognition.
					<-gate.entered
					gate.release <- struct{}{}
					<-gate.flushed
					if private {
						if calls, payload := sink.snapshot(); len(calls) != 0 || payload != "" {
							t.Errorf("private sink before Finalize: %v %q", calls, payload)
						}
					}
					close(finishExecution)
					<-done
					result := recorder.Result()
					got := response{result.StatusCode, recorder.Body.String(), result.Header.Get("Retry-After")}
					_ = result.Body.Close()
					// Keepalive already committed HTTP 200; privacy must preserve that existing
					// behavior and the exact rich failure body, not invent a new status/header.
					if got.status != 200 || got.body != "\n\n"+failure.body || got.retry != "" {
						t.Errorf("client keepalive response changed: %+v", got)
					}
					if private {
						if got != ordinary {
							t.Errorf("private response differs from ordinary: private=%+v ordinary=%+v", got, ordinary)
						}
						if calls, payload := sink.snapshot(); len(calls) != 0 || payload != "" {
							t.Errorf("private sink after Finalize: %v %q", calls, payload)
						}
						assertPrivateLogTempsRemoved(t, sink)
					} else {
						ordinary = got
						if calls, payload := sink.snapshot(); len(calls) == 0 || !strings.Contains(payload, failure.body) {
							t.Errorf("ordinary failure logging missing: %v %q", calls, payload)
						}
					}
				}
			})
		})
	}
}
