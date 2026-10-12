package executor

import (
	"context"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestFileSendDiagnosticsCountsFinalRulesPayload(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Request.Header.Set(helps.FileProbePrivacyHeader, "1")
	ctx := context.WithValue(context.Background(), "gin", c)
	final := []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"final configured text"}]}]}`)
	ctx = helps.WithPayloadFinalizer(ctx, func([]byte) []byte { return final })
	_, body, err := NewCodexExecutor(&config.Config{}).cacheHelper(ctx, translator.FormatOpenAIResponse, "https://example.invalid/responses", exec.Request{}, []byte(`{"input":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	helps.RecordFileSendDiagnostics(ctx, body, "")
	if w.Header().Get("X-CPA-Final-JSON-Bytes") != strconv.Itoa(len(final)) || w.Header().Get("X-CPA-Final-Input-Count") != "1" || w.Header().Get("X-CPA-Final-Content-Types") != "input_text:1" {
		t.Fatalf("headers=%v", w.Header())
	}
}
