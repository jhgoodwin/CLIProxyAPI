package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestCodexFilesUploadThenResponsesHTTP(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			var pngBytes bytes.Buffer
			if errPNG := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 803, 444))); errPNG != nil {
				t.Fatal(errPNG)
			}
			normalized, _, err := helps.NormalizeScreenshot(pngBytes.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			finalBytes := 0
			uploadedBearer := ""
			blob := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if !bytes.Equal(body, normalized) {
					t.Error("image altered")
				}
				if r.Header.Get("Authorization") != "" {
					t.Error("blob bearer")
				}
				w.WriteHeader(201)
			})
			upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/backend-api/files":
					uploadedBearer = r.Header.Get("Authorization")
					_ = json.NewEncoder(w).Encode(map[string]string{"file_id": "upstream-file", "upload_url": "https://fixture.blob.core.windows.net/image?sig=synthetic"})
				case "/backend-api/files/upstream-file/uploaded":
					_, _ = w.Write([]byte(`{"status":"success","download_url":"https://example.invalid/file"}`))
				case "/backend-api/codex/responses":
					calls++
					body, _ := io.ReadAll(r.Body)
					finalBytes = len(body)
					if uploadedBearer == "" || uploadedBearer != r.Header.Get("Authorization") || r.Header.Get("ChatGPT-Account-ID") != "synthetic" {
						t.Error("upload and inference did not use same selected account")
					}
					if gjson.GetBytes(body, "input.0.content.0.file_id").String() != "upstream-file" || strings.Contains(string(body), "file-cpa-") || strings.Contains(string(body), "image_url") || strings.Contains(string(body), "base64") || bytes.Contains(body, normalized) {
						t.Error("inference did not contain upstream reference only")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"object\":\"response\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(404)
				}
			})
			cfg := &config.Config{}
			cfg.CodexFilesPrototype.Enabled = true
			manager := auth.NewManager(nil, nil, nil)
			manager.SetRoundTripperProvider(filesFixtureTransport{"https://chatgpt.com": upstream, "https://fixture.blob.core.windows.net": blob})
			manager.RegisterExecutor(NewCodexAutoExecutor(cfg))
			for _, id := range []string{"files-owner", "files-other"} {
				token := "other-token"
				if id == "files-owner" {
					token = "owner-token"
				}
				_, err := manager.Register(context.Background(), &auth.Auth{ID: id, Provider: "codex", Status: auth.StatusActive, Attributes: map[string]string{"base_url": "https://chatgpt.com/backend-api/codex"}, Metadata: map[string]any{"access_token": token, "account_id": "synthetic"}})
				if err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "files-test-model"}})
				defer registry.GetGlobalRegistry().UnregisterClient(id)
			}
			base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
			h := openai.NewOpenAIResponsesAPIHandler(base)
			r := gin.New()
			r.Use(func(c *gin.Context) {
				c.Request = c.Request.WithContext(access.WithAuthenticatedPrincipal(c.Request.Context(), "caller"))
				c.Set("userApiKey", "caller")
			})
			r.POST("/v1/files", h.FilesUpload)
			r.DELETE("/v1/files/:file_id", h.FilesDelete)
			r.POST("/v1/responses", h.Responses)
			var upload bytes.Buffer
			mw := multipart.NewWriter(&upload)
			p, _ := mw.CreateFormFile("file", "sample.png")
			_, _ = p.Write(pngBytes.Bytes())
			_ = mw.WriteField("purpose", "vision")
			_ = mw.Close()
			req := httptest.NewRequest("POST", "/v1/files", &upload)
			req.Header.Set("Content-Type", mw.FormDataContentType())
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != 200 {
				t.Fatalf("upload status=%d body=%s", w.Code, w.Body.String())
			}
			if uploadedBearer != "" {
				t.Fatal("local Files create used upstream credentials")
			}
			id := gjson.Get(w.Body.String(), "id").String()
			t.Cleanup(func() {
				deleted := httptest.NewRecorder()
				r.ServeHTTP(deleted, httptest.NewRequest("DELETE", "/v1/files/"+id, nil))
				if deleted.Code != http.StatusOK {
					t.Errorf("local file cleanup status=%d", deleted.Code)
				}
			})
			// Local upload is account-neutral; inference may select either synthetic account.
			payload := `{"model":"files-test-model","tools":[],"tool_choice":"none","input":[{"role":"user","content":[{"type":"input_image","file_id":"` + id + `"}]}],"stream":false}`
			if stream {
				payload = strings.Replace(payload, `"stream":false`, `"stream":true`, 1)
			}
			req = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-CPA-Files-Probe", "1")
			w = httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Header().Get("X-CPA-Final-JSON-Bytes") != strconv.Itoa(finalBytes) || w.Header().Get("X-CPA-Final-Content-Types") != "input_image:1" {
				t.Fatalf("diagnostics disagree with actual send: %v", w.Header())
			}
			if w.Code != 200 || calls != 1 {
				t.Fatalf("responses status=%d calls=%d body=%s", w.Code, calls, w.Body.String())
			}
		})
	}
}
