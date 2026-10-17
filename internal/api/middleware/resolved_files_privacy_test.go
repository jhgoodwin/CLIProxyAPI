package middleware

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
)

type privacyCaptureLogger struct {
	testRequestLogger
	dir string
}

func (l *privacyCaptureLogger) NewFileBodySource(prefix string) (*logging.FileBodySource, error) {
	return logging.NewFileBodySourceInDir(l.dir, prefix)
}

type privacyBody struct {
	io.Reader
	closed bool
}

func (b *privacyBody) Close() error { b.closed = true; return nil }

func TestResolvedPrivacyStopsDeferredCaptureWithoutClosingRequest(t *testing.T) {
	logger := &privacyCaptureLogger{dir: t.TempDir()}
	body := &privacyBody{Reader: strings.NewReader("original-image-body")}
	router := gin.New()
	router.Use(RequestLoggingMiddleware(logger))
	router.POST("/v1/responses", func(c *gin.Context) {
		wrapper := c.Writer.(*ResponseWriterWrapper)
		capture := wrapper.requestInfo.deferredBodyCapture
		if capture == nil {
			t.Fatal("expected deferred request body capture")
		}
		part := make([]byte, 8)
		if _, err := io.ReadFull(c.Request.Body, part); err != nil {
			t.Fatal(err)
		}
		paths := capture.source.Paths()
		if len(paths) != 1 {
			t.Fatalf("capture paths=%v", paths)
		}
		if info, err := os.Stat(paths[0]); err != nil || info.Size() != 8 {
			t.Fatalf("partial request was not spooled: %v %v", info, err)
		}
		ctx := context.WithValue(c.Request.Context(), "gin", c)
		ctx = helps.WithFilePrivacy(ctx)
		if !helps.FilePrivacyContext(ctx) || !helps.FilePrivacyRequest(c.Request) {
			t.Fatal("privacy mark missing")
		}
		if capture.file != nil || capture.source != nil || wrapper.requestInfo.deferredBodyCapture != nil {
			t.Fatal("deferred capture still owned after suppression")
		}
		if !capture.finished || body.closed {
			t.Fatal("capture and HTTP body cleanup ownership changed")
		}
		if _, err := os.Stat(paths[0]); !os.IsNotExist(err) {
			t.Fatalf("spooled request retained: %v", err)
		}
		rest, err := io.ReadAll(c.Request.Body)
		if err != nil || string(part)+string(rest) != "original-image-body" {
			t.Fatalf("request body changed: %q %v", rest, err)
		}
		helps.WithFilePrivacy(ctx) // Suppression and cleanup are idempotent.
		c.Writer.WriteHeader(http.StatusBadGateway)
		_, _ = c.Writer.Write([]byte("original upstream error"))
		_, _ = c.Writer.WriteString(" with detail")
		if wrapper.body.Len() != 0 || len(wrapper.headers) != 0 || len(wrapper.requestInfo.Body) != 0 {
			t.Fatal("private response capture continued")
		}
		if err := c.Request.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if !body.closed {
			t.Fatal("request body was not closed by its owner")
		}
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", body)
	req.ContentLength = -1
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	if recorder.Code != 502 || recorder.Body.String() != "original upstream error with detail" {
		t.Fatalf("client response changed: %d %s", recorder.Code, recorder.Body.String())
	}
	entries, err := os.ReadDir(logger.dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary sources remain after Finalize: %v %v", entries, err)
	}
}
