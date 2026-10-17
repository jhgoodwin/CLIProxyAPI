package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	execution "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/sjson"
)

// filesExecutionDecorator prepares authorized source references before original provider execution.
// The registered executor, credentials, and upload results are never cached or mutated.
type filesExecutionDecorator struct {
	auth.ProviderExecutor
	provider string
	prepare  func(context.Context, *auth.Auth, []byte) (context.Context, []byte, error)
	protect  func(context.Context, *auth.Auth, *http.Request, []byte) error
}

func (e *CodexExecutor) DecorateForAuth(_ *auth.Auth) auth.ProviderExecutor {
	return &filesExecutionDecorator{ProviderExecutor: e, provider: "codex", prepare: func(ctx context.Context, a *auth.Auth, body []byte) (context.Context, []byte, error) {
		if a == nil || codexAuthUsesAPIKey(a) {
			return ctx, nil, fmt.Errorf("hosted screenshot files require selected Codex OAuth")
		}
		return helps.PrepareFilePayload(ctx, body, func(c context.Context, n string, b []byte) (string, error) { return e.UploadVisionPNG(c, a, n, b) })
	}, protect: func(_ context.Context, a *auth.Auth, r *http.Request, _ []byte) error {
		if err := helps.ValidateFileDestination(r, "codex"); err != nil {
			return err
		}
		token, _ := codexCreds(a)
		account, _ := a.Metadata["account_id"].(string)
		restoreFileHeader(r, "Authorization", "Bearer "+token)
		restoreFileHeader(r, "ChatGPT-Account-ID", account)
		restoreFileHeader(r, "Content-Type", "application/json")
		return nil
	}}
}

func (e *CodexAutoExecutor) DecorateForAuth(a *auth.Auth) auth.ProviderExecutor {
	// Local Files always use the original HTTP executor, not WebSocket fallback.
	wrapper := e.httpExec.DecorateForAuth(a).(*filesExecutionDecorator)
	wrapper.ProviderExecutor = e
	return &codexAutoFilesDecorator{filesExecutionDecorator: wrapper, http: e.httpExec}
}

type codexAutoFilesDecorator struct {
	*filesExecutionDecorator
	http *CodexExecutor
}

func (e *codexAutoFilesDecorator) Execute(ctx context.Context, a *auth.Auth, r execution.Request, o execution.Options) (execution.Response, error) {
	if f := helps.FileRequestFromContext(ctx); f != nil && f.Local {
		local := *e.filesExecutionDecorator
		local.ProviderExecutor = e.http
		return local.Execute(ctx, a, r, o)
	}
	return e.filesExecutionDecorator.Execute(ctx, a, r, o)
}
func (e *codexAutoFilesDecorator) ExecuteStream(ctx context.Context, a *auth.Auth, r execution.Request, o execution.Options) (*execution.StreamResult, error) {
	if f := helps.FileRequestFromContext(ctx); f != nil && f.Local {
		local := *e.filesExecutionDecorator
		local.ProviderExecutor = e.http
		return local.ExecuteStream(ctx, a, r, o)
	}
	return e.filesExecutionDecorator.ExecuteStream(ctx, a, r, o)
}

func (e *ClaudeExecutor) DecorateForAuth(_ *auth.Auth) auth.ProviderExecutor {
	return &filesExecutionDecorator{ProviderExecutor: e, provider: "claude", prepare: func(ctx context.Context, a *auth.Auth, b []byte) (context.Context, []byte, error) {
		next, body, err := e.prepareClaudeFiles(ctx, a, b)
		if err != nil {
			return ctx, nil, err
		}
		body, err = sjson.SetBytes(body, "__cpa_hosted_files", map[string]any{"provider": "claude"})
		return next, body, err
	}, protect: func(_ context.Context, a *auth.Auth, r *http.Request, _ []byte) error {
		if err := helps.ValidateFileDestination(r, "claude"); err != nil {
			return err
		}
		key, _ := claudeCreds(a)
		restoreFileHeader(r, "Authorization", "")
		restoreFileHeader(r, "x-api-key", "")
		claudeFileCredential(r, a, key)
		beta := r.Header.Get("Anthropic-Beta")
		if !strings.Contains(beta, "files-api-2025-04-14") {
			if beta != "" {
				beta += ","
			}
			beta += "files-api-2025-04-14"
		}
		restoreFileHeader(r, "Anthropic-Beta", beta)
		restoreFileHeader(r, "Content-Type", "application/json")
		return nil
	}}
}

func (e *GeminiExecutor) DecorateForAuth(_ *auth.Auth) auth.ProviderExecutor {
	return &filesExecutionDecorator{ProviderExecutor: e, provider: "gemini", prepare: func(ctx context.Context, a *auth.Auth, b []byte) (context.Context, []byte, error) {
		if shouldExecuteNativeInteractions(a, execution.Options{}) {
			return ctx, nil, fmt.Errorf("native Gemini Interactions Files are unsupported")
		}
		refs := map[string]any{}
		key := geminiAPIKey(a)
		next, body, err := helps.PrepareFilePayload(ctx, b, func(c context.Context, _ string, data []byte) (string, error) {
			if strings.TrimRight(resolveGeminiBaseURL(a), "/") != "https://generativelanguage.googleapis.com" {
				return "", fmt.Errorf("selected Gemini endpoint does not support native Files")
			}
			file, err := helps.UploadGeminiFile(c, key, data, helps.NewProxyAwareHTTPClient(c, e.cfg, a, 0))
			if err != nil {
				return "", err
			}
			id := strings.TrimPrefix(file.Name, "files/")
			refs[id] = map[string]string{"uri": file.URI, "mime_type": file.MIME}
			return id, nil
		})
		if err != nil {
			return ctx, nil, err
		}
		f := helps.FileRequestFromContext(next)
		f.NativeProvider = "gemini"
		for _, ref := range refs {
			f.Allowed[ref.(map[string]string)["uri"]] = true
		}
		body, err = sjson.SetBytes(body, "__cpa_hosted_files", map[string]any{"provider": "gemini", "refs": refs})
		return next, body, err
	}, protect: func(ctx context.Context, a *auth.Auth, r *http.Request, b []byte) error {
		if err := helps.ValidateFileDestination(r, "gemini"); err != nil {
			return err
		}
		if err := helps.ValidateGeminiFilePlacement(b); err != nil {
			return err
		}
		if err := helps.ValidateGeminiFilePayload(ctx, b); err != nil {
			return err
		}
		restoreFileHeader(r, "Authorization", "")
		restoreFileHeader(r, "x-goog-api-key", geminiAPIKey(a))
		restoreFileHeader(r, "Content-Type", "application/json")
		return nil
	}}
}

func (e *OpenAICompatExecutor) DecorateForAuth(a *auth.Auth) auth.ProviderExecutor {
	base, _ := e.resolveCredentials(a)
	if _, ok := helps.OpenAIFilesEndpoint(base); ok || base == "https://integrate.api.nvidia.com/v1" || base == helps.NvidiaVilaInferenceURL {
		return e.nativeFilesDecorator()
	}
	return &filesExecutionDecorator{ProviderExecutor: e, provider: "deepseek", prepare: func(ctx context.Context, a *auth.Auth, b []byte) (context.Context, []byte, error) {
		base, key := e.resolveCredentials(a)
		if _, ok := helps.DeepSeekFilesEndpoint(base); !ok {
			return ctx, nil, fmt.Errorf("selected compatibility endpoint requires a native Files executor")
		}
		client := helps.FileUploadHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, a, 0))
		next, body, err := helps.PrepareDeepSeekFilePayload(ctx, base, b, func(r *http.Request) (*http.Response, error) {
			restoreFileHeader(r, "Authorization", "Bearer "+key)
			return client.Do(r)
		})
		if err != nil {
			return ctx, nil, err
		}
		body, err = sjson.SetBytes(body, "__cpa_hosted_files", map[string]any{"provider": "deepseek"})
		return next, body, err
	}, protect: func(_ context.Context, a *auth.Auth, r *http.Request, _ []byte) error {
		if err := helps.ValidateFileDestination(r, "deepseek"); err != nil {
			return err
		}
		_, key := e.resolveCredentials(a)
		restoreFileHeader(r, "Authorization", "Bearer "+key)
		restoreFileHeader(r, "Content-Type", "application/json")
		return nil
	}}
}

func restoreFileHeader(r *http.Request, name, value string) {
	for k := range r.Header {
		if strings.EqualFold(k, name) {
			delete(r.Header, k)
		}
	}
	if value != "" {
		r.Header.Set(name, value)
	}
}

func (e *filesExecutionDecorator) prepared(ctx context.Context, a *auth.Auth, r execution.Request, o execution.Options) (context.Context, execution.Request, execution.Options, error) {
	f := helps.FileRequestFromContext(ctx)
	if f == nil || !f.Local {
		if helps.ContainsLocalFileID(r.Payload) || helps.ContainsLocalFileID(o.OriginalRequest) {
			return ctx, r, o, fmt.Errorf("unauthorized local file reference")
		}
		return ctx, r, o, nil
	}
	if err := e.filePreflight(ctx, a, r, o); err != nil {
		return ctx, r, o, err
	}
	source := r.Payload
	if len(o.OriginalRequest) > 0 {
		source = o.OriginalRequest
	}
	next, body, err := e.prepare(ctx, a, source)
	if err != nil {
		return ctx, r, o, err
	}
	r.Payload = body
	o.OriginalRequest = body
	next = helps.WithHTTPClientDecorator(next, func(client *http.Client) *http.Client {
		return helps.FileInferenceClient(next, client, func(req *http.Request, body []byte) error { return e.protect(next, a, req, body) })
	})
	return next, r, o, nil
}

func (e *filesExecutionDecorator) Execute(ctx context.Context, a *auth.Auth, r execution.Request, o execution.Options) (execution.Response, error) {
	next, r, o, err := e.prepared(ctx, a, r, o)
	if err != nil {
		return execution.Response{}, err
	}
	return e.ProviderExecutor.Execute(next, a, r, o)
}
func (e *filesExecutionDecorator) ExecuteStream(ctx context.Context, a *auth.Auth, r execution.Request, o execution.Options) (*execution.StreamResult, error) {
	next, r, o, err := e.prepared(ctx, a, r, o)
	if err != nil {
		return nil, err
	}
	return e.ProviderExecutor.ExecuteStream(next, a, r, o)
}
func (e *filesExecutionDecorator) CountTokens(ctx context.Context, a *auth.Auth, r execution.Request, o execution.Options) (execution.Response, error) {
	if f := helps.FileRequestFromContext(ctx); (f != nil && f.Local) || helps.ContainsLocalFileID(r.Payload) || helps.ContainsLocalFileID(o.OriginalRequest) {
		return execution.Response{}, fmt.Errorf("hosted screenshot files cannot be counted before generation")
	}
	return e.ProviderExecutor.CountTokens(ctx, a, r, o)
}

// filePreflight rejects unsupported routes and origins before reading or uploading screenshots.
func (e *filesExecutionDecorator) filePreflight(ctx context.Context, a *auth.Auth, r execution.Request, o execution.Options) error {
	if o.Alt == "responses/compact" || execution.RequiredUpstreamWebsocket(ctx) || execution.DownstreamWebsocket(ctx) {
		return fmt.Errorf("hosted screenshot files require HTTP generation")
	}
	path := helps.PayloadRequestPath(o)
	if strings.Contains(path, "/images") || strings.Contains(path, "/compact") || strings.Contains(path, "countTokens") {
		return fmt.Errorf("unsupported hosted screenshot operation")
	}
	switch e.provider {
	case "codex":
		_, base := codexCreds(a)
		if base == "" {
			base = "https://chatgpt.com/backend-api/codex"
		}
		if !helps.OfficialFileBase(base, "chatgpt.com", "/backend-api/codex") {
			return fmt.Errorf("selected Codex endpoint does not support native Files")
		}
	case "claude":
		_, base := claudeCreds(a)
		if base == "" {
			base = "https://api.anthropic.com"
		}
		if !helps.OfficialFileBase(base, "api.anthropic.com", "") {
			return fmt.Errorf("selected Claude endpoint does not support native Files")
		}
	case "gemini":
		if shouldExecuteNativeInteractions(a, o) || !helps.OfficialFileBase(resolveGeminiBaseURL(a), "generativelanguage.googleapis.com", "") {
			return fmt.Errorf("selected Gemini endpoint does not support native Files")
		}
		if action, _ := r.Metadata["action"].(string); action == "countTokens" {
			return fmt.Errorf("hosted screenshot files cannot be counted before generation")
		}
	case "deepseek":
		base, _ := e.ProviderExecutor.(*OpenAICompatExecutor).resolveCredentials(a)
		if _, ok := helps.DeepSeekFilesEndpoint(base); !ok {
			return fmt.Errorf("selected compatibility endpoint does not support Files")
		}
	}
	return nil
}
