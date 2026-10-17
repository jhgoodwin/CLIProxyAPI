package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	execution "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type nativeFilesModelKey struct{}
type nativeResponsesKey struct{}

// compatibilityFilesTarget opts in only after the selected-auth native upload succeeds.
func compatibilityFilesTarget(ctx context.Context) (translator.Format, string) {
	if active, _ := ctx.Value(nativeResponsesKey{}).(bool); active {
		return translator.FormatOpenAIResponse, "/responses"
	}
	return translator.FormatOpenAI, "/chat/completions"
}

// nativeFilesCompleted recognizes the official Responses terminal event, not arbitrary EOF.
func nativeFilesCompleted(ctx context.Context, data []byte) bool {
	active, _ := ctx.Value(nativeResponsesKey{}).(bool)
	return active && gjson.GetBytes(data, "type").String() == "response.completed" && gjson.GetBytes(data, "response.status").String() == "completed"
}

// nativeCompatibilityFilesExecutor retains the original HTTP and translation runners.
type nativeCompatibilityFilesExecutor struct{ *filesExecutionDecorator }

func (e *nativeCompatibilityFilesExecutor) sourceContext(ctx context.Context, r execution.Request, o execution.Options) (context.Context, error) {
	if f := helps.FileRequestFromContext(ctx); f != nil && f.Local {
		if o.Alt != "" || openAICompatImageEndpointPath(o) != "" {
			return ctx, fmt.Errorf("native screenshot Files require ordinary generation")
		}
		ctx = context.WithValue(ctx, nativeFilesModelKey{}, thinking.ParseSuffix(r.Model).ModelName)
	}
	return ctx, nil
}
func (e *nativeCompatibilityFilesExecutor) Execute(ctx context.Context, a *auth.Auth, r execution.Request, o execution.Options) (execution.Response, error) {
	ctx, err := e.sourceContext(ctx, r, o)
	if err != nil {
		return execution.Response{}, err
	}
	return e.filesExecutionDecorator.Execute(ctx, a, r, o)
}
func (e *nativeCompatibilityFilesExecutor) ExecuteStream(ctx context.Context, a *auth.Auth, r execution.Request, o execution.Options) (*execution.StreamResult, error) {
	ctx, err := e.sourceContext(ctx, r, o)
	if err != nil {
		return nil, err
	}
	return e.filesExecutionDecorator.ExecuteStream(ctx, a, r, o)
}

func (e *OpenAICompatExecutor) nativeFilesDecorator() auth.ProviderExecutor {
	wrapper := &filesExecutionDecorator{ProviderExecutor: e}
	wrapper.prepare = func(ctx context.Context, a *auth.Auth, body []byte) (context.Context, []byte, error) {
		base, key := e.resolveCredentials(a)
		if a == nil || a.AuthKind() != auth.AuthKindAPIKey || strings.TrimSpace(key) == "" {
			return ctx, nil, fmt.Errorf("native compatibility Files require selected API-key authentication")
		}
		client := helps.FileUploadHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, a, 0))
		if _, ok := helps.OpenAIFilesEndpoint(base); ok {
			next, prepared, err := helps.PrepareOpenAIFilePayload(ctx, base, body, func(r *http.Request) (*http.Response, error) {
				restoreFileHeader(r, "Authorization", "Bearer "+key)
				return client.Do(r)
			})
			if err != nil {
				return ctx, nil, err
			}
			helps.FileRequestFromContext(next).NativeProvider = "openai"
			prepared, err = sjson.SetBytes(prepared, "__cpa_hosted_files", map[string]any{"provider": "openai"})
			return context.WithValue(next, nativeResponsesKey{}, true), prepared, err
		}
		model, _ := ctx.Value(nativeFilesModelKey{}).(string)
		route := nvidiaFilesRoute(ctx, base, model)
		if route == "" {
			return ctx, nil, fmt.Errorf("selected compatibility endpoint does not support native Files")
		}
		refs := map[string]any{}
		next, prepared, err := helps.PrepareFilePayload(ctx, body, func(c context.Context, _ string, data []byte) (string, error) {
			id, err := helps.UploadNvidiaAsset(c, key, data, client)
			if err == nil {
				refs[id] = map[string]string{"uri": helps.NvidiaAssetPrefix + id, "mime_type": "image/png"}
			}
			return id, err
		})
		if err != nil {
			return ctx, nil, err
		}
		helps.FileRequestFromContext(next).NativeProvider = "nvidia"
		prepared, err = sjson.SetBytes(prepared, "__cpa_hosted_files", map[string]any{"provider": "nvidia", "refs": refs})
		return next, prepared, err
	}
	wrapper.protect = func(ctx context.Context, a *auth.Auth, r *http.Request, body []byte) error {
		base, key := e.resolveCredentials(a)
		f := helps.FileRequestFromContext(ctx)
		if a == nil || a.AuthKind() != auth.AuthKindAPIKey || key == "" || f == nil {
			return fmt.Errorf("native Files selected authentication unavailable")
		}
		target := ""
		switch f.NativeProvider {
		case "openai":
			if err := helps.ValidateOpenAIFilePlacement(body); err != nil {
				return err
			}
			if _, ok := helps.OpenAIFilesEndpoint(base); !ok {
				return fmt.Errorf("official OpenAI Files endpoint changed")
			}
			if r.URL.String() != strings.TrimSuffix(base, "/")+"/responses" {
				return fmt.Errorf("official OpenAI Files inference route changed")
			}
			target = "https://api.openai.com/v1/responses"
		case "nvidia":
			model, _ := ctx.Value(nativeFilesModelKey{}).(string)
			if nvidiaFilesRoute(ctx, base, model) == "" || r.URL.String() != strings.TrimSuffix(base, "/")+"/chat/completions" {
				return fmt.Errorf("NVIDIA Files inference route changed")
			}
			header, err := helps.NvidiaFileAssetHeader(ctx, body)
			if err != nil {
				return err
			}
			restoreFileHeader(r, "NVCF-INPUT-ASSET-REFERENCES", header)
			target = helps.NvidiaVilaInferenceURL
		default:
			return fmt.Errorf("native Files provider changed")
		}
		u, err := url.Parse(target)
		if err != nil {
			return err
		}
		r.URL = u
		r.Host = u.Host
		restoreFileHeader(r, "Authorization", "Bearer "+key)
		restoreFileHeader(r, "Content-Type", "application/json")
		return nil
	}
	return &nativeCompatibilityFilesExecutor{wrapper}
}
