package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	claude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/responses"
	gemini "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/openai/responses"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
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
func TestScreenshotProviderNativeMessageAndToolImages(t *testing.T) {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 803, 444)))
	r := helps.NewCodexFileRegistry()
	f, e := r.Store(helps.CodexFile{Scope: helps.FileCallerScope("a")}, b.Bytes(), 2, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Delete(f.ID, "a", time.Hour)
	body := []byte(`{"model":"vision-test","input":[{"type":"message","role":"user","content":[{"type":"input_image","file_id":"` + f.ID + `"}]},{"type":"function_call","call_id":"c","name":"capture","arguments":"{}"},{"type":"function_call_output","call_id":"c","output":[{"type":"input_image","file_id":"` + f.ID + `"}]}]}`)
	_, resolved, e := r.Resolve(context.Background(), body, "a", time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	nativeData := strings.TrimPrefix(gjson.GetBytes(resolved, "input.0.content.0.image_url").String(), "data:image/png;base64,")
	for name, convert := range map[string]func(string, []byte, bool) ([]byte, error){"claude": claude.ConvertOpenAIResponsesRequestToClaude, "gemini": gemini.ConvertOpenAIResponsesRequestToGemini} {
		for _, stream := range []bool{false, true} {
			out, e := convert("vision-test", resolved, stream)
			if e != nil {
				t.Fatalf("%s: %v", name, e)
			}
			if strings.Contains(string(out), f.ID) || strings.Count(string(out), nativeData) != 2 {
				t.Fatalf("%s native image count !=2", name)
			}
			imageBytes, e := base64.StdEncoding.DecodeString(nativeData)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = png.Decode(bytes.NewReader(imageBytes)); e != nil {
				t.Fatal(e)
			}
		}
	}
}
