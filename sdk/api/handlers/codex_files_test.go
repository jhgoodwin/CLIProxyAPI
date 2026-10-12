package handlers

import (
	"context"
	"strings"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestHTTPFileReferencePreparationPinsOwner(t *testing.T) {
	manager := auth.NewManager(nil, nil, nil)
	if _, err := manager.Register(context.Background(), &auth.Auth{ID: "owner", Provider: "codex", Status: auth.StatusActive, Metadata: map[string]any{"access_token": "fake"}}); err != nil {
		t.Fatal(err)
	}
	h := NewBaseAPIHandlers(&config.SDKConfig{CodexFilesPrototype: config.CodexFilesPrototypeConfig{Enabled: true}}, manager)
	file, err := h.Files.Add(helps.CodexFile{UpstreamID: "upstream", Owner: "owner", Scope: helps.FileCallerScope("caller"), Ready: true}, 10, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"input":[{"role":"user","content":[{"type":"input_image","file_id":"` + file.ID + `"}]}]}`)
	caller := access.WithAuthenticatedPrincipal(context.Background(), "caller")
	ctx, resolved, errMsg := h.prepareFileReferences(caller, "openai-response", body)
	if errMsg != nil {
		t.Fatal(errMsg)
	}
	if pinnedAuthIDFromContext(ctx) != "owner" || !strings.Contains(string(resolved), "upstream") || strings.Contains(string(resolved), file.ID) {
		t.Fatal("reference not resolved/pinned")
	}
	if requestExecutionMetadata(ctx)[exec.PinnedAuthMetadataKey] != "owner" {
		t.Fatal("execution metadata lost owner")
	}
	if validateFileRoute(ctx, "plugin", nil) == nil || validateFileRoute(ctx, "", []string{"claude"}) == nil {
		t.Fatal("unsafe route accepted")
	}
	if _, _, e := h.prepareFileReferences(exec.WithDownstreamWebsocket(caller), "openai-response", body); e == nil {
		t.Fatal("WS accepted file reference")
	}
	if _, _, e := h.prepareFileReferences(access.WithAuthenticatedPrincipal(context.Background(), "other"), "openai-response", body); e == nil {
		t.Fatal("cross-caller")
	}
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	if _, _, e := h.prepareFileReferences(caller, "openai-response", body); e == nil || e.StatusCode != 503 {
		t.Fatal("Home did not fail closed")
	}
	manager.SetConfig(&internalconfig.Config{})
	manager.Remove(context.Background(), "owner")
	if _, _, e := h.prepareFileReferences(caller, "openai-response", body); e == nil {
		t.Fatal("unregistered owner accepted")
	}
}
