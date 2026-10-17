package executor

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

type deepSeekFilesTransport func(*http.Request) (*http.Response, error)

func (f deepSeekFilesTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestOpenAICompatDeepSeekFilesSelectedAuth(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, mode := range []string{"normal", "filtered", "unknown", "inline", "unsupported", "upload-error"} {
			t.Run(map[bool]string{false: "execute", true: "stream"}[stream]+"/"+mode, func(t *testing.T) {
				data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
				if err != nil {
					t.Fatal(err)
				}
				localID := helps.PrototypeFilePrefix + "screen"
				original := &helps.FileRequest{Local: true, Allowed: map[string]bool{}, Pending: map[string]helps.PendingFile{localID: {Name: "screenshot.png", Read: func(context.Context) ([]byte, error) { return data, nil }}}}
				ctx := helps.WithFileRequest(context.Background(), original)
				events := []string{}
				ctx = context.WithValue(ctx, "cliproxy.roundtripper", deepSeekFilesTransport(func(r *http.Request) (*http.Response, error) {
					if r.Header.Get("Authorization") != "Bearer selected-key" {
						t.Fatal("wrong selected credential")
					}
					if r.URL.Path == "/files" {
						events = append(events, "upload")
						if errParse := r.ParseMultipartForm(1 << 20); errParse != nil {
							t.Fatal(errParse)
						}
						if r.FormValue("purpose") != "user_data" {
							t.Fatal("wrong purpose")
						}
						if r.FormValue("expires_after[anchor]") != "created_at" || r.FormValue("expires_after[seconds]") != "3600" {
							t.Fatal("transient upload expiration missing")
						}
						f, h, errOpen := r.FormFile("file")
						if errOpen != nil {
							t.Fatal(errOpen)
						}
						got, errRead := io.ReadAll(f)
						if errClose := f.Close(); errClose != nil {
							t.Fatal(errClose)
						}
						if errRead != nil || string(got) != string(data) || h.Filename != "screenshot.png" {
							t.Fatal("upload mismatch")
						}
						if mode == "upload-error" {
							return &http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("private"))}, nil
						}
						return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":"file-api-selected"}`))}, nil
					}
					if r.Header.Get("Content-Type") != "application/json" {
						t.Fatal("custom header replaced inference content type")
					}
					events = append(events, "inference")
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Fatal(errRead)
					}
					if len(events) != 2 || events[0] != "upload" {
						t.Fatal("inference preceded upload")
					}
					if strings.Contains(string(body), "data:image/") || strings.Contains(string(body), localID) {
						t.Fatal("inline/local bytes reached inference")
					}
					part := gjson.GetBytes(body, "messages.0.content.0")
					if mode == "filtered" {
						if part.Get("file_id").Exists() {
							t.Fatal("filtered file restored")
						}
					} else if part.Get("type").String() != "file" || part.Get("file_id").String() != "file-api-selected" {
						t.Fatalf("native file lost: %s", body)
					}
					payload := `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`
					if stream {
						payload = "data: [DONE]\n\n"
					}
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload))}, nil
				}))
				cfg := &config.Config{}
				models := []config.PayloadModelRule{{Name: "*"}}
				switch mode {
				case "filtered":
					cfg.Payload.Filter = []config.PayloadFilterRule{{Models: models, Params: []string{"messages.0.content.0.file_id"}}}
				case "unknown":
					cfg.Payload.Override = []config.PayloadRule{{Models: models, Params: map[string]any{"messages.0.content.0.file_id": "file-api-unowned"}}}
				case "inline":
					cfg.Payload.Override = []config.PayloadRule{{Models: models, Params: map[string]any{"messages.0.content.0.image_url": "data:image/png;base64,AA=="}}}
				}
				auth := &cliproxyauth.Auth{Provider: "dsh-deepseek", Attributes: map[string]string{"base_url": "https://api.deepseek.com", "api_key": "selected-key", "header:Authorization": "Bearer wrong-key", "header:Content-Type": "text/plain"}}
				if mode == "unsupported" {
					auth.Attributes["base_url"] = "https://arbitrary.test"
				}
				req := cliproxyexecutor.Request{Model: "dsh-deepseek-flash", Payload: []byte(`{"input":[{"role":"user","content":[{"type":"input_image","file_id":"` + localID + `"}]}]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAI}
				exec := NewOpenAICompatExecutor("dsh-deepseek", cfg).DecorateForAuth(auth)
				if stream {
					var response *cliproxyexecutor.StreamResult
					response, err = exec.ExecuteStream(ctx, auth, req, opts)
					if err == nil {
						for chunk := range response.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					}
				} else {
					_, err = exec.Execute(ctx, auth, req, opts)
				}
				success := mode == "normal" || mode == "filtered"
				if success && (err != nil || len(events) != 2) {
					t.Fatalf("events=%v err=%v", events, err)
				}
				if !success && (err == nil || len(events) > 1) {
					t.Fatalf("unsafe inference events=%v err=%v", events, err)
				}
				if mode == "unsupported" && len(events) != 0 {
					t.Fatal("unsupported compat uploaded")
				}
				if len(original.Allowed) != 0 {
					t.Fatal("selected attempt mutated shared Allowed")
				}
			})
		}
	}
}
