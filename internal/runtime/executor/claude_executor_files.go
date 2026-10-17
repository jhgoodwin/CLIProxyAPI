package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// claudeFileHeaders uses the selected inference credential without a CLI identity
// or message-body signature. Protected headers cannot be replaced by attributes.
func claudeFileHeaders(req *http.Request, auth *cliproxyauth.Auth, apiKey string) {
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(req, auth.Attributes)
	}
	claudeFileCredential(req, auth, apiKey)
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("Anthropic-Beta", "files-api-2025-04-14")
}

func claudeFileCredential(req *http.Request, auth *cliproxyauth.Auth, apiKey string) {
	req.Header.Del("Authorization")
	req.Header.Del("x-api-key")
	if apiKey != "" {
		if isAnthropicUpstreamURL(req.URL) && !claudeCredentialUsesOAuth(auth, apiKey) {
			req.Header.Set("x-api-key", apiKey)
		} else {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}
}

func (e *ClaudeExecutor) prepareClaudeFiles(ctx context.Context, auth *cliproxyauth.Auth, body []byte) (context.Context, []byte, error) {
	apiKey, baseURL := claudeCreds(auth)
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	return helps.PrepareFilePayload(ctx, body, func(uploadCtx context.Context, name string, data []byte) (string, error) {
		if strings.TrimSpace(apiKey) == "" {
			return "", fmt.Errorf("Claude Files requires a selected credential")
		}
		return helps.UploadClaudeFile(uploadCtx, helps.NewUtlsHTTPClient(uploadCtx, e.cfg, auth, 0), strings.TrimRight(baseURL, "/")+"/v1/files", name, data, func(r *http.Request) { claudeFileHeaders(r, auth, apiKey) })
	})
}
