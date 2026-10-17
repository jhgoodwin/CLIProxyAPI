package executor

import (
	"context"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// nvidiaFilesRoute opts in only local images on the documented exact selected route.
func nvidiaFilesRoute(ctx context.Context, baseURL, model string) string {
	f := helps.FileRequestFromContext(ctx)
	if f == nil || !f.Local || model != "nvidia/vila" {
		return ""
	}
	if baseURL == "https://integrate.api.nvidia.com/v1" || baseURL == helps.NvidiaVilaInferenceURL {
		return helps.NvidiaVilaInferenceURL
	}
	return ""
}

// prepareNvidiaFiles uses only the selected compatibility credential and proxy transport.
// Invoke before payload finalization, only for the exact explicit VILA native endpoint.
func (e *OpenAICompatExecutor) prepareNvidiaFiles(ctx context.Context, auth *cliproxyauth.Auth, baseURL, model, apiKey string, body []byte) (context.Context, []byte, error) {
	return helps.PrepareNvidiaFilePayload(ctx, baseURL, model, apiKey, body, helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
}

// applyNvidiaFileHeaders derives owned asset IDs after read-only final payload validation.
// Custom inference headers must be applied first so they cannot override these asset IDs.
func applyNvidiaFileHeaders(ctx context.Context, req *http.Request, body []byte) error {
	header, err := helps.NvidiaFileAssetHeader(ctx, body)
	if err != nil {
		return err
	}
	req.Header.Del("NVCF-INPUT-ASSET-REFERENCES")
	if header != "" {
		req.Header.Set("NVCF-INPUT-ASSET-REFERENCES", header)
	}
	return nil
}
