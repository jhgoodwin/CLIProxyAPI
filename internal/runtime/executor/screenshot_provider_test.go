package executor

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestScreenshotFinalRulesDoNotRestoreImage(t *testing.T) {
	for _, field := range []string{"content", "output"} {
		calls := 0
		ctx := helps.WithFileRequest(context.Background(), &helps.FileRequest{Local: true})
		ctx = helps.WithPayloadFinalizer(ctx, func([]byte) []byte {
			calls++
			return []byte(`{"input":[],"tools":[],"tool_choice":"none","stream":false}`)
		})
		req, body, e := NewCodexExecutor(&config.Config{}).cacheHelper(ctx, translator.FormatOpenAIResponse, "https://example.invalid/responses", exec.Request{}, []byte(`{"input":[{"`+field+`":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`))
		if e != nil || req == nil || calls != 1 || strings.Contains(string(body), "data:image") {
			t.Fatal("final removal restored")
		}
	}
}
func TestScreenshotReferencesRemainAccountNeutralUntilUpload(t *testing.T) {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 803, 444)))
	r := helps.NewCodexFileRegistry()
	f, err := r.Store(helps.CodexFile{Scope: helps.FileCallerScope("a")}, b.Bytes(), 2, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Delete(f.ID, "a", time.Hour)
	body := []byte(`{"input":[{"content":[{"type":"input_image","file_id":"` + f.ID + `"}]}]}`)
	ctx, resolved, err := r.Resolve(context.Background(), body, "a", time.Hour)
	if err != nil || !bytes.Equal(body, resolved) || helps.FileRequestFromContext(ctx).Owner != "" {
		t.Fatal("reference changed before selection")
	}
}
