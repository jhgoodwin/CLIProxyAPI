package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func (e *CodexExecutor) prepareSelectedCodexFiles(ctx context.Context, selected *auth.Auth, body []byte, headers http.Header) (context.Context, []byte, error) {
	f := helps.FileRequestFromContext(ctx)
	if f == nil || !f.Local {
		return ctx, body, nil
	}
	if selected == nil || selected.Provider != "codex" || codexAuthUsesAPIKey(selected) {
		return ctx, nil, fmt.Errorf("hosted screenshot files require selected Codex OAuth")
	}
	if err := helps.ValidateFileOwner(ctx, selected.ID, selected.Provider); err != nil {
		return ctx, nil, err
	}
	// Upload and inference must identify the same selected OAuth account.
	resolved := (&http.Request{Header: http.Header{}}).WithContext(ctx)
	token, _ := codexCreds(selected)
	applyCodexHeaders(resolved, selected, token, true, e.cfg, headers)
	account, _ := selected.Metadata["account_id"].(string)
	if token == "" || resolved.Header.Get("Authorization") != "Bearer "+token || resolved.Header.Get("ChatGPT-Account-ID") != account {
		return ctx, nil, fmt.Errorf("file credential headers differ from selected OAuth account")
	}
	return helps.PrepareFilePayload(ctx, body, func(uploadCtx context.Context, name string, data []byte) (string, error) {
		return e.UploadVisionPNG(uploadCtx, selected, name, data)
	})
}

func rejectCodexWebsocketFiles(ctx context.Context, body []byte) error {
	f := helps.FileRequestFromContext(ctx)
	if f != nil || helps.ContainsLocalFileID(body) {
		return fmt.Errorf("hosted screenshot files require HTTP Responses")
	}
	// Non-file malformed requests retain their transport-specific validation path.
	// A file context or discovered local reference has already been rejected above.
	if !json.Valid(body) {
		return nil
	}
	// Native hosted IDs are not authorized by a local principal on websocket transport.
	ids, err := helps.ImageFileIDs(body)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if strings.TrimSpace(id) != "" {
			return fmt.Errorf("hosted screenshot files require HTTP Responses")
		}
	}
	return nil
}

// protectCodexFileInference restores credential and JSON framing headers after
// operator/model header overrides, and isolates redirect policy to this attempt.
func protectCodexFileInference(ctx context.Context, req *http.Request, selected *auth.Auth, client *http.Client) *http.Client {
	f := helps.FileRequestFromContext(ctx)
	if f == nil || !f.Local {
		return client
	}
	token, _ := codexCreds(selected)
	account, _ := selected.Metadata["account_id"].(string)
	for _, name := range []string{"Authorization", "ChatGPT-Account-ID", "Content-Type"} {
		deleteHeaderCaseInsensitive(req.Header, name)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if account != "" {
		req.Header.Set("ChatGPT-Account-ID", account)
	}
	req.Header.Set("Content-Type", "application/json")
	return codexFileNoRedirectClient(client)
}

func codexFileNoRedirectClient(client *http.Client) *http.Client {
	clone := *client
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &clone
}
