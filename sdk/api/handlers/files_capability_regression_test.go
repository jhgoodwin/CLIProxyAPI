package handlers

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
)

func TestHostedFilesSupportedRouteFamilies(t *testing.T) {
	ctx := helps.WithFileRequest(context.Background(), &helps.FileRequest{Local: true})
	for _, provider := range []string{"codex", "claude", "gemini", "openai", "openai-compatibility", "openai-compatible-deepseek"} {
		if err := validateFileRoute(ctx, "", []string{provider}); err != nil {
			t.Fatalf("supported family %q: %v", provider, err)
		}
	}
	for _, providers := range [][]string{{"gemini-cli"}, {"vertex"}, {"codex", "gemini-cli"}} {
		if err := validateFileRoute(ctx, "", providers); err == nil || err.StatusCode != 501 {
			t.Fatalf("unsupported family %v accepted: %v", providers, err)
		}
	}
}

func TestHostedFilesCountRejectsBeforeRouting(t *testing.T) {
	// A nil handler would panic if routing or execution were attempted.
	var h *BaseAPIHandler
	for _, protocol := range []string{"claude", "openai-response", "openai", "gemini"} {
		body := []byte(`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"file","file_id":"file-cpa-test"}}]}]}`)
		if _, _, err := h.ExecuteCountWithAuthManager(context.Background(), protocol, "model", body, ""); err == nil || err.StatusCode != 501 {
			t.Fatalf("%s local ref count was not rejected: %v", protocol, err)
		}
		ctx := helps.WithFileRequest(context.Background(), &helps.FileRequest{Local: true})
		if _, _, err := h.ExecuteCountWithAuthManager(ctx, protocol, "model", []byte(`{}`), ""); err == nil || err.StatusCode != 501 {
			t.Fatalf("%s protected count was not rejected: %v", protocol, err)
		}
	}
}
