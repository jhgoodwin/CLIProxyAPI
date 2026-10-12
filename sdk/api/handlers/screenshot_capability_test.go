package handlers

import (
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

func TestScreenshotNamedCompatibilityRoute(t *testing.T) {
	ctx := helps.WithFileRequest(context.Background(), &helps.FileRequest{Local: true})
	for _, provider := range []string{"openai-compatibility", util.OpenAICompatibleProviderKey("screenshot-compatible")} {
		if e := validateFileRoute(ctx, "", []string{provider}); e != nil {
			t.Fatalf("compatible runtime provider %q: %v", provider, e)
		}
	}
	for _, provider := range []string{"screenshot-compatible", "openai-compatible-", "arbitrary-provider"} {
		if e := validateFileRoute(ctx, "", []string{provider}); e == nil || e.StatusCode != 501 {
			t.Fatalf("unrecognized runtime provider %q accepted", provider)
		}
	}
	if e := validateFileRoute(ctx, "custom-plugin", []string{util.OpenAICompatibleProviderKey("screenshot-compatible")}); e == nil || e.StatusCode != 501 {
		t.Fatal("plugin route accepted")
	}
}

func TestScreenshotCapabilityTriState(t *testing.T) {
	r := registry.GetGlobalRegistry()
	r.RegisterClient("screenshot-capability", "codex", []*registry.ModelInfo{{ID: "screenshot-text", SupportedInputModalities: []string{"TEXT"}}, {ID: "screenshot-vision", SupportedInputModalities: []string{"TEXT", "IMAGE"}}, {ID: "screenshot-unknown"}})
	defer r.UnregisterClient("screenshot-capability")
	ctx := helps.WithFileRequest(context.Background(), &helps.FileRequest{Local: true})
	for _, model := range []string{"screenshot-text", "screenshot-text(high)"} {
		e := validateFileModel(ctx, model)
		if e == nil || e.StatusCode != 501 || e.Error.Error() != visionFallbackMessage {
			t.Fatalf("%s expected explicit501", model)
		}
	}
	for _, model := range []string{"screenshot-vision", "screenshot-unknown", "not-registered"} {
		if e := validateFileModel(ctx, model); e != nil {
			t.Fatalf("%s rejected unknown/vision", model)
		}
	}
	h := NewBaseAPIHandlers(nil, nil)
	body := []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"mention file-cpa-example"},{"type":"input_image","file_id":"external-provider-id"}]}]}`)
	_, out, e := h.prepareFileReferences(context.Background(), "openai-response", body)
	if e != nil || string(out) != string(body) {
		t.Fatal("external or usertext intercepted")
	}
	_, _, e = h.prepareFileReferences(context.Background(), "openai", []byte(`{"messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"file-cpa-example"}}]}]}`))
	if e == nil || !strings.Contains(e.Error.Error(), "HTTP Responses") {
		t.Fatal("unsupportedchat must fail locally")
	}
}
