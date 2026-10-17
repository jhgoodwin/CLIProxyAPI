package executor

import (
	"context"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// prepareCompatibilityFiles recognizes native Files capability by the selected exact origin.
func (e *OpenAICompatExecutor) prepareCompatibilityFiles(ctx context.Context, _ *cliproxyauth.Auth, baseURL, _ string, body []byte, do func(*http.Request) (*http.Response, error)) (context.Context, []byte, error) {
	if _, supported := helps.OpenAIFilesEndpoint(baseURL); supported {
		return helps.PrepareOpenAIFilePayload(ctx, baseURL, body, do)
	}
	return helps.PrepareDeepSeekFilePayload(ctx, baseURL, body, do)
}
