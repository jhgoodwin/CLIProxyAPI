package executor

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestDirectHostedScreenshotCountFailsBeforeExecution(t *testing.T) {
	claude := NewClaudeExecutor(&config.Config{}).DecorateForAuth(nil)
	codex := NewCodexExecutor(&config.Config{}).DecorateForAuth(nil)
	for name, count := range map[string]func(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error){
		"claude": claude.CountTokens,
		"codex":  codex.CountTokens,
	} {
		body := []byte(`{"input":[{"type":"input_image","file_id":"file-cpa-test"}]}`)
		for _, tc := range []struct {
			ctx  context.Context
			req  cliproxyexecutor.Request
			opts cliproxyexecutor.Options
		}{
			{context.Background(), cliproxyexecutor.Request{Payload: body}, cliproxyexecutor.Options{}},
			{context.Background(), cliproxyexecutor.Request{}, cliproxyexecutor.Options{OriginalRequest: body}},
			{helps.WithFileRequest(context.Background(), &helps.FileRequest{Local: true}), cliproxyexecutor.Request{}, cliproxyexecutor.Options{}},
		} {
			if _, err := count(tc.ctx, nil, tc.req, tc.opts); err == nil {
				t.Fatalf("%s hosted file count accepted", name)
			}
		}
	}
}
