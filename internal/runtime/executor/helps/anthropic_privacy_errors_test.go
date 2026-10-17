package helps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestAnthropicPrivacySuppressesUpstreamCaptureAndErrors(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request, _ = http.NewRequest(http.MethodPost, "http://proxy/v1/messages", nil)
	ctx := WithFilePrivacy(context.WithValue(context.Background(), "gin", c))
	secret := []byte(`{"error":{"message":"secret signed-url and image data"}}`)
	if got := SummarizeErrorBodyForRequest(ctx, "application/json", secret); strings.Contains(got, "secret") {
		t.Fatalf("private error leaked: %s", got)
	}
	cfg := &config.Config{}
	cfg.RequestLog = true
	RecordAPIRequest(ctx, cfg, UpstreamRequestLog{Body: secret})
	RecordAPIResponseMetadata(ctx, cfg, 400, http.Header{"X-Secret": []string{"secret"}})
	RecordAPIResponseError(ctx, cfg, context.Canceled)
	AppendAPIResponseChunk(ctx, cfg, secret)
	if _, exists := c.Get(apiAttemptsKey); exists {
		t.Fatal("private upstream capture created")
	}
}
