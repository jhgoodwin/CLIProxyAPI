package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestFileTerminalFailureDoesNotExposePoisonMessage(t *testing.T) {
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
				if strings.Contains(err.Error(), "POISON") || strings.Contains(err.Error(), "PRIVATE") {
					t.Fatal(err)
				}
				continue
			}
			found := false
			for chunk := range result.Chunks {
				if strings.Contains(string(chunk.Payload), "POISON") || strings.Contains(string(chunk.Payload), "PRIVATE") {
					t.Fatal("payload leaked terminal error")
				}
				if chunk.Err != nil {
					found = true
					if strings.Contains(chunk.Err.Error(), "POISON") || strings.Contains(chunk.Err.Error(), "PRIVATE") {
						t.Fatal(chunk.Err)
					}
				}
			}
			if !found {
				t.Fatal("missing failure")
			}
		} else {
			_, err := e.Execute(ctx, a, req, opts)
			if err == nil || strings.Contains(err.Error(), "POISON") || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatalf("error=%v", err)
			}
		}
	}
}
