package handlers

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"golang.org/x/net/context"
)

func (h *BaseAPIHandler) prepareFileReferences(ctx context.Context, protocol string, body []byte) (context.Context, []byte, *interfaces.ErrorMessage) {
	fail := func(status int, msg string) (context.Context, []byte, *interfaces.ErrorMessage) {
		return ctx, nil, &interfaces.ErrorMessage{StatusCode: status, Error: fmt.Errorf("%s", msg)}
	}
	// A chat/file/MCP payload containing our opaque IDs must never leak to upstream.
	ids, err := helps.ImageFileIDs(body)
	if err != nil {
		return fail(400, err.Error())
	}
	local := false
	for _, id := range ids {
		local = local || strings.HasPrefix(id, helps.PrototypeFilePrefix)
	}
	if !local {
		if helps.ContainsLocalFileID(body) {
			return fail(400, "proxy screenshot references require HTTP Responses input_image in message content or structured tool output")
		}
		return ctx, body, nil
	}
	if h.Cfg == nil || !h.Cfg.CodexFilesPrototype.Enabled {
		return fail(400, "files prototype is disabled")
	}
	if protocol != "openai-response" || exec.DownstreamWebsocket(ctx) {
		return fail(400, "proxy screenshot references require HTTP Responses input_image in message content or structured tool output")
	}
	if h.Files == nil {
		return fail(503, "file registry unavailable")
	}
	_, _, ttl := h.Cfg.CodexFilesPrototype.Limits()
	ctx, body, err = h.Files.Resolve(ctx, body, access.AuthenticatedPrincipal(ctx), time.Duration(ttl)*time.Second)
	if err != nil {
		return fail(400, err.Error())
	}
	// Legacy test fixtures retain their owning account; public uploads never do.
	if f := helps.FileRequestFromContext(ctx); f != nil && f.Owner != "" {
		if h.AuthManager == nil || h.AuthManager.HomeEnabled() {
			return fail(503, "file owner is unavailable")
		}
		owner, ok := h.AuthManager.GetByID(f.Owner)
		if !ok || owner.Disabled || owner.Unavailable || owner.Provider != "codex" || owner.Attributes["api_key"] != "" {
			return fail(503, "file owner is unavailable")
		}
		ctx = WithPinnedAuthID(ctx, f.Owner)
	}
	return ctx, body, nil
}

func privateFileExecutionError(ctx context.Context, e *interfaces.ErrorMessage) *interfaces.ErrorMessage {
	if f := helps.FileRequestFromContext(ctx); f != nil && f.Local && e != nil {
		return &interfaces.ErrorMessage{StatusCode: e.StatusCode, Error: fmt.Errorf("screenshot inference upstream request failed")}
	}
	return e
}

const visionFallbackMessage = "vision-description fallback is not implemented"

func validateFileRoute(ctx context.Context, plugin string, providers []string) *interfaces.ErrorMessage {
	f := helps.FileRequestFromContext(ctx)
	if f == nil {
		return nil
	}
	if f.Local {
		if plugin != "" {
			return &interfaces.ErrorMessage{StatusCode: 501, Error: fmt.Errorf("%s", visionFallbackMessage)}
		}
		for _, p := range providers {
			switch p {
			case "codex", "openai", "openai-compatibility", "claude", "gemini", "gemini-cli":
			default:
				// Named compatibility executors use this reserved runtime family,
				// not the arbitrary display name from configuration.
				if strings.HasPrefix(p, "openai-compatible-") && len(p) > len("openai-compatible-") {
					continue
				}
				return &interfaces.ErrorMessage{StatusCode: 501, Error: fmt.Errorf("%s", visionFallbackMessage)}
			}
		}
		return nil
	}
	if f.Owner != "" && (plugin != "" || (providers != nil && (len(providers) != 1 || providers[0] != "codex"))) {
		return &interfaces.ErrorMessage{StatusCode: 400, Error: fmt.Errorf("file references require native Codex routing")}
	}
	return nil
}
func validateFileModel(ctx context.Context, model string) *interfaces.ErrorMessage {
	f := helps.FileRequestFromContext(ctx)
	if f == nil || !f.Local {
		return nil
	}
	info := registry.GetGlobalRegistry().GetModelInfo(thinking.ParseSuffix(model).ModelName, "")
	if info == nil {
		return nil
	} // Unknown metadata is not evidence of missing vision.
	if len(info.SupportedInputModalities) == 0 {
		return nil
	}
	for _, m := range info.SupportedInputModalities {
		if strings.EqualFold(m, "image") {
			return nil
		}
	}
	return &interfaces.ErrorMessage{StatusCode: http.StatusNotImplemented, Error: fmt.Errorf("%s", visionFallbackMessage)}
}
