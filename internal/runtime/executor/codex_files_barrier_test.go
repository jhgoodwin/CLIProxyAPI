package executor

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestFileValidationAfterFinalBarrier(t *testing.T) {
	for _, tc := range []struct {
		name, final string
		reject      bool
	}{
		{"removed", `{"input":[],"tools":[],"tool_choice":"none","stream":false}`, false},
		{"changed", `{"input":[{"content":[{"type":"input_image","file_id":"changed"}]}]}`, true},
		{"unresolved", `{"input":[{"content":[{"type":"input_image","file_id":"file-cpa-unknown"}]}]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			ctx := helps.WithFileRequest(context.Background(), &helps.FileRequest{Owner: "a", Allowed: map[string]bool{"upstream": true}})
			ctx = helps.WithPayloadFinalizer(ctx, func([]byte) []byte { calls++; return []byte(tc.final) })
			req, body, err := NewCodexExecutor(&config.Config{}).cacheHelper(ctx, translator.FormatOpenAIResponse, "https://example.invalid/responses", exec.Request{}, []byte(`{"input":[{"content":[{"type":"input_image","file_id":"upstream"}]}]}`))
			if (err != nil) != tc.reject {
				t.Fatalf("err=%v", err)
			}
			if calls != 1 {
				t.Fatalf("rules calls=%d", calls)
			}
			if !tc.reject && (req == nil || string(body) != tc.final || len(gjson.GetBytes(body, "input").Array()) != 0) {
				t.Fatal("configured removal restored")
			}
		})
	}
}
