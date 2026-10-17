package executor

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Embed the real executor interface; only transport execution and credential refresh
// are synthetic, leaving actual manager selection/401 retry and HTTP handler intact.
type fileRetryExecutor struct {
	*CodexExecutor
	calls, refreshes int
	failRefresh      bool
	t                *testing.T
}

// DecorateForAuth preserves this fixture's synthetic execution and refresh methods.
func (e *fileRetryExecutor) DecorateForAuth(_ *auth.Auth) auth.ProviderExecutor {
	return e
}

func (e *fileRetryExecutor) Execute(ctx context.Context, a *auth.Auth, r exec.Request, o exec.Options) (exec.Response, error) {
	e.calls++
	if a.ID != "file-retry-owner" || o.Metadata[exec.PinnedAuthMetadataKey] != a.ID {
		e.t.Fatal("owner pin missing before execution")
	}
	if err := helps.ValidateFileOwner(ctx, a.ID, "codex"); err != nil {
		e.t.Fatal(err)
	}
	if err := helps.ValidateFilePayload(ctx, r.Payload); err != nil {
		e.t.Fatal(err)
	}
	if a.Metadata["access_token"] == "stale" {
		return exec.Response{}, &auth.Error{HTTPStatus: http.StatusUnauthorized, Message: "synthetic expired"}
	}
	return exec.Response{Payload: []byte(`{"id":"r","object":"response","output":[]}`)}, nil
}
func (e *fileRetryExecutor) Refresh(_ context.Context, a *auth.Auth) (*auth.Auth, error) {
	e.refreshes++
	if a.ID != "file-retry-owner" {
		e.t.Fatal("wrong account refreshed")
	}
	if e.failRefresh {
		return nil, &auth.Error{HTTPStatus: 401, Message: "synthetic refresh failed"}
	}
	a = a.Clone()
	a.Metadata["access_token"] = "fresh"
	return a, nil
}
func TestFileOwnerPinSurvivesUnauthorizedRefresh(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "refresh_retry", true: "no_owner_fallback"}[fail], func(t *testing.T) {
			cfg := &config.Config{}
			cfg.CodexFilesPrototype.Enabled = true
			m := auth.NewManager(nil, nil, nil)
			m.SetRetryConfig(1, 0, 2)
			e := &fileRetryExecutor{CodexExecutor: NewCodexExecutor(cfg), failRefresh: fail, t: t}
			m.RegisterExecutor(e)
			for _, id := range []string{"file-retry-owner", "file-retry-other"} {
				_, err := m.Register(context.Background(), &auth.Auth{ID: id, Provider: "codex", Status: auth.StatusActive, Metadata: map[string]any{"access_token": "stale", "refresh_token": "synthetic"}})
				if err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "files-retry-model"}})
				defer registry.GetGlobalRegistry().UnregisterClient(id)
			}
			h := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, m)
			f, err := h.Files.Add(helps.CodexFile{UpstreamID: "upstream", Owner: "file-retry-owner", Scope: helps.FileCallerScope("caller"), Ready: true}, 10, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			ctx := access.WithAuthenticatedPrincipal(context.Background(), "caller")
			_, _, errMsg := h.ExecuteWithAuthManager(ctx, "openai-response", "files-retry-model", []byte(`{"model":"files-retry-model","input":[{"role":"user","content":[{"type":"input_image","file_id":"`+f.ID+`"}]}]}`), "")
			if e.refreshes != 1 {
				t.Fatalf("refreshes=%d", e.refreshes)
			}
			if !fail && (errMsg != nil || e.calls != 2) {
				t.Fatalf("calls=%d error=%v", e.calls, errMsg)
			}
			if fail && errMsg == nil {
				t.Fatal("failed refresh silently succeeded")
			}
		})
	}
}
