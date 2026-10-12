package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
)

type unreadFileBody struct{ read bool }

func (b *unreadFileBody) Read([]byte) (int, error) { b.read = true; return 0, io.EOF }
func (*unreadFileBody) Close() error               { return nil }
func TestFileLoggingSkipBeforeBodyCapture(t *testing.T) {
	for _, path := range []string{"/v1/files", "/v1/files/file-cpa-test/content", "/v1/responses"} {
		t.Run(path, func(t *testing.T) {
			spy := &spyRequestLogger{}
			router := gin.New()
			router.Use(RequestLoggingMiddleware(spy))
			router.POST(path, func(c *gin.Context) { c.Status(502) })
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("not consumed"))
			req.Header.Set(helps.FileProbePrivacyHeader, "1")
			body := &unreadFileBody{}
			req.Body = body
			router.ServeHTTP(httptest.NewRecorder(), req)
			if body.read {
				t.Fatal("logger read image before handler")
			}
			if spy.lastLogRequestID != "" || spy.lastStreamingRequestID != "" {
				t.Fatal("logger captured probe")
			}
		})
	}
}
