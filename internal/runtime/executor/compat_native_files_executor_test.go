package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	execution "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type nativeFilesTestTransport func(*http.Request) (*http.Response, error)

func (f nativeFilesTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNativeFilesOriginalResponsesRunnerCompletesWithoutChatDone(t *testing.T) {
	e := NewOpenAICompatExecutor("official", &config.Config{})
	a := &auth.Auth{Attributes: map[string]string{"base_url": "https://api.openai.com/v1", "api_key": "selected"}}
	for _, stream := range []bool{false, true} {
		calls := 0
		ctx := context.WithValue(context.Background(), nativeResponsesKey{}, true)
		ctx = context.WithValue(ctx, "cliproxy.roundtripper", http.RoundTripper(nativeFilesTestTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			body, _ := io.ReadAll(r.Body)
			if r.URL.Path != "/v1/responses" || strings.Contains(string(body), "stream_options") {
				t.Fatalf("wrong native request %s: %s", r.URL, body)
			}
			result := `{"id":"resp_test","object":"response","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`
			if stream {
				result = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + result + "}\n\n"
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(result))}, nil
		})))
		r := execution.Request{Model: "gpt-4.1", Payload: []byte(`{"model":"gpt-4.1","input":[]}`)}
		o := execution.Options{SourceFormat: translator.FormatOpenAIResponse}
		if stream {
			result, err := e.ExecuteStream(ctx, a, r, o)
			if err != nil {
				t.Fatal(err)
			}
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
			}
		} else {
			if _, err := e.Execute(ctx, a, r, o); err != nil {
				t.Fatal(err)
			}
		}
		if calls != 1 {
			t.Fatal("unexpected retry", calls)
		}
	}
}

func TestNativeFilesTargetRequiresAttemptOptIn(t *testing.T) {
	ctx := context.Background()
	format, path := compatibilityFilesTarget(ctx)
	if format != translator.FormatOpenAI || path != "/chat/completions" {
		t.Fatal("ordinary request changed")
	}
	ctx = context.WithValue(ctx, nativeResponsesKey{}, true)
	format, path = compatibilityFilesTarget(ctx)
	if format != translator.FormatOpenAIResponse || path != "/responses" {
		t.Fatal("native Responses not selected")
	}
	for _, body := range []string{`{"type":"response.completed"}`, `{"type":"response.failed","response":{"status":"failed"}}`, `{"type":"response.output_text.delta"}`} {
		if nativeFilesCompleted(ctx, []byte(body)) {
			t.Fatal("incomplete event accepted")
		}
	}
	if !nativeFilesCompleted(ctx, []byte(`{"type":"response.completed","response":{"status":"completed"}}`)) {
		t.Fatal("completion rejected")
	}
}

func TestNativeFilesRejectsOAuthBeforeReadingScreenshots(t *testing.T) {
	e := NewOpenAICompatExecutor("official", &config.Config{})
	a := &auth.Auth{Attributes: map[string]string{"base_url": "https://api.openai.com/v1"}, Metadata: map[string]any{"access_token": "oauth"}}
	wrapper := e.nativeFilesDecorator().(*nativeCompatibilityFilesExecutor)
	local := helps.PrototypeFilePrefix + "one"
	ctx := helps.WithFileRequest(context.Background(), &helps.FileRequest{Local: true, Pending: map[string]helps.PendingFile{local: {Read: func(context.Context) ([]byte, error) { t.Fatal("OAuth request read screenshot"); return nil, nil }}}})
	_, _, err := wrapper.prepare(ctx, a, []byte(`{"input":[{"role":"user","content":[{"type":"input_image","file_id":"`+local+`"}]}]}`))
	if err == nil {
		t.Fatal("OAuth accepted")
	}
	if _, err := wrapper.sourceContext(ctx, execution.Request{}, execution.Options{Alt: "responses/compact"}); err == nil {
		t.Fatal("compact accepted")
	}
}

func TestNativeFilesProtectsCredentialRouteAndOwnedAssets(t *testing.T) {
	e := NewOpenAICompatExecutor("official", &config.Config{})
	wrapper := e.nativeFilesDecorator().(*nativeCompatibilityFilesExecutor)
	a := &auth.Auth{Attributes: map[string]string{"base_url": "https://api.openai.com", "api_key": "selected"}}
	ctx := helps.WithFileRequest(context.Background(), &helps.FileRequest{Local: true, NativeProvider: "openai", Allowed: map[string]bool{"file-owned": true}})
	req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/responses", nil)
	req.Header["authorization"] = []string{"spoof"}
	if err := wrapper.protect(ctx, a, req, []byte(`{"input":[{"role":"user","content":[{"type":"input_image","file_id":"file-owned"}]}]}`)); err != nil {
		t.Fatal(err)
	}
	if req.URL.String() != "https://api.openai.com/v1/responses" || req.Header.Get("Authorization") != "Bearer selected" || req.Header.Get("Content-Type") != "application/json" {
		t.Fatal("native route or credential changed")
	}
	req, _ = http.NewRequest(http.MethodPost, "https://evil.example/responses", nil)
	if err := wrapper.protect(ctx, a, req, nil); err == nil {
		t.Fatal("destination changed")
	}
	id := "12345678-1234-1234-1234-123456789abc"
	a.Attributes["base_url"] = "https://integrate.api.nvidia.com/v1"
	ctx = helps.WithFileRequest(context.Background(), &helps.FileRequest{Local: true, NativeProvider: "nvidia", Allowed: map[string]bool{id: true}})
	ctx = context.WithValue(ctx, nativeFilesModelKey{}, "nvidia/vila")
	req, _ = http.NewRequest(http.MethodPost, "https://integrate.api.nvidia.com/v1/chat/completions", nil)
	req.Header.Set("NVCF-INPUT-ASSET-REFERENCES", "spoof")
	body := []byte(`{"model":"nvidia/vila","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;asset_id,` + id + `"}}]}]}`)
	if err := wrapper.protect(ctx, a, req, body); err != nil {
		t.Fatal(err)
	}
	if req.URL.String() != helps.NvidiaVilaInferenceURL || req.Header.Get("NVCF-INPUT-ASSET-REFERENCES") != id {
		t.Fatal("wrong VILA route or asset header")
	}
	req, _ = http.NewRequest(http.MethodPost, "https://integrate.api.nvidia.com/v1/chat/completions", nil)
	if err := wrapper.protect(ctx, a, req, []byte(`{"model":"other"}`)); err == nil {
		t.Fatal("final model override accepted")
	}
}
