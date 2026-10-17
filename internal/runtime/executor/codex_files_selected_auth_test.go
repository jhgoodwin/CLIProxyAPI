package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexSelectedFilesFinalUpstreamReferences(t *testing.T) {
	for _, mode := range []string{"http", "stream", "auto", "auto-stream", "compact"} {
		for _, failure := range []string{"", "reserve", "blob", "finalize", "not-ready", "override-inline", "override-id", "reserve-redirect", "blob-redirect", "finalize-redirect", "inference-redirect"} {
			t.Run(mode+"/"+failure, func(t *testing.T) {
				var imageData bytes.Buffer
				if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
					t.Fatal(err)
				}
				registry := helps.NewCodexFileRegistry()
				file, err := registry.Store(helps.CodexFile{Scope: helps.FileCallerScope("principal")}, imageData.Bytes(), 2, time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				defer registry.Delete(file.ID, "principal", time.Hour)
				body := []byte(`{"model":"gpt-5.5","input":[{"type":"message","role":"user","content":[{"type":"input_image","file_id":"` + file.ID + `"}]},{"type":"function_call","call_id":"c","name":"capture","arguments":"{}"},{"type":"function_call_output","call_id":"c","output":[{"type":"input_image","file_id":"` + file.ID + `"}]}]}`)
				ctx, resolved, err := registry.Resolve(context.Background(), body, "principal", time.Hour)
				if err != nil || !bytes.Equal(body, resolved) {
					t.Fatalf("resolve expanded reference: %v", err)
				}
				normalized, err := helps.FileRequestFromContext(ctx).Pending[file.ID].Read(ctx)
				if err != nil {
					t.Fatal(err)
				}
				var redirectHits atomic.Int32
				redirected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirectHits.Add(1); w.WriteHeader(200) })
				var reservations, inferences atomic.Int32
				blob := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "" || r.Header.Get("ChatGPT-Account-ID") != "" {
						t.Error("blob leaked OAuth")
					}
					data, _ := io.ReadAll(r.Body)
					if !bytes.Equal(data, normalized) {
						t.Error("blob bytes changed")
					}
					if failure == "blob-redirect" {
						http.Redirect(w, r, "https://redirect.invalid/", http.StatusTemporaryRedirect)
						return
					}
					if failure == "blob" {
						w.WriteHeader(403)
						return
					}
					w.WriteHeader(201)
				})
				backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer selected-token" || r.Header.Get("ChatGPT-Account-ID") != "selected-account" {
						t.Error("wrong selected OAuth/account")
					}
					switch r.URL.Path {
					case "/backend-api/files":
						reservations.Add(1)
						if failure == "reserve-redirect" {
							http.Redirect(w, r, "https://redirect.invalid/", http.StatusTemporaryRedirect)
							return
						}
						if failure == "reserve" {
							w.WriteHeader(403)
							return
						}
						_ = json.NewEncoder(w).Encode(map[string]any{"file_id": "upstream-selected", "upload_url": "https://fixture.blob.core.windows.net/blob?sig=synthetic"})
					case "/backend-api/files/upstream-selected/uploaded":
						if failure == "finalize-redirect" {
							http.Redirect(w, r, "https://redirect.invalid/", http.StatusTemporaryRedirect)
							return
						}
						if failure == "finalize" {
							w.WriteHeader(400)
							return
						}
						if failure == "not-ready" {
							_, _ = w.Write([]byte(`{"status":"pending"}`))
							return
						}
						_, _ = w.Write([]byte(`{"status":"success","download_url":"https://example.invalid/download"}`))
					case "/backend-api/codex/responses", "/backend-api/codex/responses/compact":
						inferences.Add(1)
						if r.Header.Get("Content-Type") != "application/json" {
							t.Error("custom Content-Type changed JSON framing")
						}
						if failure == "inference-redirect" {
							http.Redirect(w, r, "https://redirect.invalid/", http.StatusTemporaryRedirect)
							return
						}
						data, _ := io.ReadAll(r.Body)
						if strings.Contains(string(data), file.ID) || strings.Contains(string(data), "image_url") || strings.Contains(string(data), "base64") || bytes.Contains(data, imageData.Bytes()) {
							t.Error("inference contains local ID or image bytes")
						}
						if gjson.GetBytes(data, "input.0.content.0.file_id").String() != "upstream-selected" || gjson.GetBytes(data, "input.2.output.0.file_id").String() != "upstream-selected" {
							t.Errorf("missing upstream references: %s", data)
						}
						if mode == "compact" {
							_, _ = w.Write([]byte(`{"id":"r","object":"response","output":[]}`))
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-5.5\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n")
					default:
						t.Errorf("unexpected path %s", r.URL.Path)
						w.WriteHeader(404)
					}
				})
				ctx = context.WithValue(ctx, "cliproxy.roundtripper", http.RoundTripper(filesFixtureTransport{"https://chatgpt.com": backend, "https://fixture.blob.core.windows.net": blob, "https://redirect.invalid": redirected}))
				selected := &auth.Auth{ID: "selected", Provider: "codex", Attributes: map[string]string{"base_url": "https://chatgpt.com/backend-api/codex"}, Metadata: map[string]any{"access_token": "selected-token", "account_id": "selected-account"}}
				selected.Attributes["header:Content-Type"] = "text/plain"
				cfg := &config.Config{}
				if strings.HasPrefix(failure, "override-") {
					part := map[string]any{"type": "input_image", "file_id": "changed"}
					if failure == "override-inline" {
						part = map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AAAA"}
					}
					protocol := "codex"
					if mode == "compact" {
						protocol = "openai-response"
					}
					cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "gpt-5.5", Protocol: protocol}}, Params: map[string]any{"input.0.content.0": part}}}
				}
				request := exec.Request{Model: "gpt-5.5", Payload: resolved}
				options := exec.Options{SourceFormat: translator.FormatOpenAIResponse}
				if mode == "compact" {
					options.Alt = "responses/compact"
				}
				var callErr error
				switch mode {
				case "http", "compact":
					_, callErr = NewCodexExecutor(cfg).DecorateForAuth(selected).Execute(ctx, selected, request, options)
				case "auto":
					_, callErr = NewCodexAutoExecutor(cfg).DecorateForAuth(selected).Execute(ctx, selected, request, options)
				case "stream", "auto-stream":
					var result *exec.StreamResult
					if mode == "stream" {
						result, callErr = NewCodexExecutor(cfg).DecorateForAuth(selected).ExecuteStream(ctx, selected, request, options)
					} else {
						result, callErr = NewCodexAutoExecutor(cfg).DecorateForAuth(selected).ExecuteStream(ctx, selected, request, options)
					}
					if callErr == nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								callErr = chunk.Err
							}
						}
					}
				}
				if mode == "compact" {
					if callErr == nil || reservations.Load() != 0 || inferences.Load() != 0 {
						t.Fatalf("compact accepted hosted Files: err=%v inference=%d reserve=%d", callErr, inferences.Load(), reservations.Load())
					}
				} else if failure == "" {
					if callErr != nil || inferences.Load() != 1 || reservations.Load() != 1 {
						t.Fatalf("error=%v inference=%d reserve=%d", callErr, inferences.Load(), reservations.Load())
					}
				} else if failure == "inference-redirect" {
					if callErr == nil || inferences.Load() != 1 {
						t.Fatalf("redirect not rejected: err=%v inference=%d", callErr, inferences.Load())
					}
				} else if callErr == nil || inferences.Load() != 0 {
					t.Fatalf("failed open error=%v inference=%d", callErr, inferences.Load())
				}
				if redirectHits.Load() != 0 {
					t.Fatal("redirect followed with protected request")
				}
			})
		}
	}
}

func TestCodexFilesAttemptsDoNotReuseAccountIDs(t *testing.T) {
	var uploads atomic.Int32
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/backend-api/files":
			uploads.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"file_id": "remote-" + r.Header.Get("ChatGPT-Account-ID"), "upload_url": "https://fixture.blob.core.windows.net/blob?sig=synthetic"})
		case r.URL.Path == "/blob":
			w.WriteHeader(201)
		default:
			_, _ = w.Write([]byte(`{"status":"success","download_url":"https://example.invalid/download"}`))
		}
	})
	id := "file-cpa-local"
	ctx := helps.WithFileRequest(context.Background(), &helps.FileRequest{Local: true, Pending: map[string]helps.PendingFile{id: {Name: "screenshot.png", Read: func(context.Context) ([]byte, error) { return []byte("PNG"), nil }}}})
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", http.RoundTripper(filesFixtureTransport{"https://chatgpt.com": backend, "https://fixture.blob.core.windows.net": backend}))
	body := []byte(`{"input":[{"content":[{"type":"input_image","file_id":"` + id + `"}]}]}`)
	e := NewCodexExecutor(&config.Config{})
	for _, account := range []string{"first", "second", "first"} {
		selected := &auth.Auth{ID: account, Provider: "codex", Attributes: map[string]string{"base_url": "https://chatgpt.com/backend-api/codex"}, Metadata: map[string]any{"access_token": "token-" + account, "account_id": account}}
		attempt, out, err := e.prepareSelectedCodexFiles(ctx, selected, body, nil)
		if err != nil || gjson.GetBytes(out, "input.0.content.0.file_id").String() != "remote-"+account {
			t.Fatalf("account=%s err=%v out=%s", account, err, out)
		}
		if len(helps.FileRequestFromContext(attempt).Allowed) != 1 || len(helps.FileRequestFromContext(ctx).Allowed) != 0 {
			t.Fatal("attempt mutated shared context")
		}
	}
	if uploads.Load() != 3 {
		t.Fatal("IDs reused between attempts")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := e.prepareSelectedCodexFiles(canceled, &auth.Auth{Provider: "codex", Metadata: map[string]any{"access_token": "token"}}, body, nil); err == nil {
		t.Fatal("cancellation ignored")
	}
	if uploads.Load() != 3 {
		t.Fatal("canceled attempt uploaded")
	}
}

func TestCodexFilesWebsocketAndCredentialOverridesFailClosed(t *testing.T) {
	ctx := helps.WithFileRequest(context.Background(), &helps.FileRequest{Local: true})
	body := []byte(`{"input":[]}`)
	request := exec.Request{Model: "gpt-5.5", Payload: body}
	opts := exec.Options{SourceFormat: translator.FormatOpenAIResponse}
	selected := &auth.Auth{ID: "selected", Provider: "codex", Metadata: map[string]any{"access_token": "token", "account_id": "account"}}
	ws := NewCodexWebsocketsExecutor(&config.Config{})
	if _, err := ws.Execute(ctx, selected, request, opts); err == nil {
		t.Fatal("websocket accepted file context")
	}
	if _, err := ws.prepareCodexWebsocketStream(ctx, selected, request, opts); err == nil {
		t.Fatal("stream/duplex accepted file context")
	}
	for _, key := range []string{"Authorization", "ChatGPT-Account-ID"} {
		a := selected.Clone()
		a.Attributes = map[string]string{"header:" + key: "other"}
		if _, _, err := NewCodexExecutor(&config.Config{}).prepareSelectedCodexFiles(ctx, a, body, nil); err == nil {
			t.Fatal("credential override accepted")
		}
	}
}

func TestCodexFilesWebsocketGuardLeavesMalformedNonFileValidationToTransport(t *testing.T) {
	malformed := []byte("not-json")
	if err := rejectCodexWebsocketFiles(context.Background(), malformed); err != nil {
		t.Fatalf("non-file validation intercepted: %v", err)
	}
	protected := helps.WithFileRequest(context.Background(), &helps.FileRequest{Local: true})
	if err := rejectCodexWebsocketFiles(protected, malformed); err == nil {
		t.Fatal("malformed payload bypassed protected file context")
	}
	for _, body := range []string{
		`{"input":[{"content":[{"type":"input_image","file_id":"remote-id"}]}]}`,
		`{"input":[{"content":[{"type":"input_image","file_id":""}]}]}`,
	} {
		if err := rejectCodexWebsocketFiles(context.Background(), []byte(body)); err == nil {
			t.Fatal("hosted or malformed reference bypassed WebSocket guard")
		}
	}
}
