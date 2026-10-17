package executor

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
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

func TestOpenAIFilesPublicResponsesSelectedAuth(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			var pixels bytes.Buffer
			if err := png.Encode(&pixels, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
				t.Fatal(err)
			}
			normalized, _, err := helps.NormalizeScreenshot(pixels.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			uploads, inferences := 0, 0
			selectedBearer := ""
			fixture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Scheme != "https" || r.URL.Host != "api.openai.com" || r.Method != http.MethodPost {
					t.Errorf("unexpected destination %s %s", r.Method, r.URL)
				}
				bearer := r.Header.Get("Authorization")
				if bearer != "Bearer owner-token" && bearer != "Bearer other-token" {
					t.Errorf("wrong selected credential: %q", bearer)
				}
				switch r.URL.Path {
				case "/v1/files":
					uploads++
					selectedBearer = bearer
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						t.Fatal(err)
					}
					defer r.MultipartForm.RemoveAll()
					if r.FormValue("purpose") != "vision" || r.FormValue("expires_after[seconds]") != "3600" {
						t.Error("upload retention/purpose missing")
					}
					file, _, err := r.FormFile("file")
					if err != nil {
						t.Fatal(err)
					}
					data, err := io.ReadAll(file)
					file.Close()
					if err != nil || !bytes.Equal(data, normalized) {
						t.Error("upload did not contain normalized screenshot")
					}
					fmt.Fprint(w, `{"id":"file-api-selected"}`)
				case "/v1/responses":
					inferences++
					if uploads != 1 || bearer != selectedBearer {
						t.Error("upload/inference credential selection diverged")
					}
					if r.Header.Get("Content-Type") != "application/json" {
						t.Error("custom header overrode JSON framing")
					}
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Fatal(err)
					}
					if gjson.GetBytes(body, "input.0.content.0.file_id").String() != "file-api-selected" {
						t.Errorf("upstream reference missing: %s", body)
					}
					for _, forbidden := range []string{"file-cpa-", "base64", "image_url", "__cpa", "cpa-hosted"} {
						if bytes.Contains(body, []byte(forbidden)) {
							t.Errorf("private or inline carrier escaped: %s", forbidden)
						}
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"object\":\"response\",\"status\":\"in_progress\",\"output\":[]}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"object\":\"response\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
				default:
					t.Errorf("unexpected official endpoint %s", r.URL)
					w.WriteHeader(http.StatusNotFound)
				}
			})
			cfg := &config.Config{}
			cfg.CodexFilesPrototype.Enabled = true
			manager := auth.NewManager(nil, nil, nil)
			manager.SetRoundTripperProvider(filesFixtureTransport{"https://api.openai.com": fixture})
			manager.RegisterExecutor(NewOpenAICompatExecutor("openai", cfg))
			for _, label := range []string{"owner", "other"} {
				id := t.Name() + "-" + label
				_, err := manager.Register(context.Background(), &auth.Auth{ID: id, Provider: "openai", Status: auth.StatusActive, Attributes: map[string]string{"api_key": label + "-token", "base_url": "https://api.openai.com/v1", "header:Authorization": "Bearer wrong", "header:Content-Type": "text/plain"}})
				if err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(id, "openai", []*registry.ModelInfo{{ID: "openai-files-test", SupportedInputModalities: []string{"image"}}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			}
			base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
			responses := openai.NewOpenAIResponsesAPIHandler(base)
			chat := openai.NewOpenAIAPIHandler(base)
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Request = c.Request.WithContext(access.WithAuthenticatedPrincipal(c.Request.Context(), "caller"))
				c.Set("userApiKey", "caller")
			})
			router.POST("/v1/files", responses.FilesUpload)
			router.DELETE("/v1/files/:file_id", responses.FilesDelete)
			router.POST("/v1/responses", responses.Responses)
			router.POST("/v1/chat/completions", chat.ChatCompletions)
			var upload bytes.Buffer
			writer := multipart.NewWriter(&upload)
			part, err := writer.CreateFormFile("file", "screenshot.png")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write(pixels.Bytes()); err != nil {
				t.Fatal(err)
			}
			if err := writer.WriteField("purpose", "vision"); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/files", &upload)
			request.Header.Set("Content-Type", writer.FormDataContentType())
			result := httptest.NewRecorder()
			router.ServeHTTP(result, request)
			if result.Code != http.StatusOK || uploads != 0 {
				t.Fatalf("local upload status=%d upstream uploads=%d body=%s", result.Code, uploads, result.Body)
			}
			id := gjson.Get(result.Body.String(), "id").String()
			if !strings.HasPrefix(id, "file-cpa-") {
				t.Fatalf("local upload id=%q", id)
			}
			t.Cleanup(func() {
				deleted := httptest.NewRecorder()
				router.ServeHTTP(deleted, httptest.NewRequest(http.MethodDelete, "/v1/files/"+id, nil))
				if deleted.Code != http.StatusOK {
					t.Errorf("file cleanup status=%d", deleted.Code)
				}
			})
			// Public hosted screenshots are Responses-only; Chat rejection performs no upload.
			chatBody := fmt.Sprintf(`{"model":"openai-files-test","messages":[{"role":"user","content":[{"type":"file","file_id":%q}]}]}`, id)
			rejected := httptest.NewRecorder()
			router.ServeHTTP(rejected, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody)))
			if rejected.Code != http.StatusBadRequest || uploads != 0 || inferences != 0 {
				t.Fatalf("Chat accepted local file: status=%d uploads=%d inference=%d", rejected.Code, uploads, inferences)
			}
			payload := fmt.Sprintf(`{"model":"openai-files-test","input":[{"role":"user","content":[{"type":"input_image","file_id":%q}]}],"stream":%v}`, id, stream)
			request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(payload))
			request.Header.Set("Content-Type", "application/json")
			result = httptest.NewRecorder()
			router.ServeHTTP(result, request)
			if result.Code != http.StatusOK || uploads != 1 || inferences != 1 || !strings.Contains(result.Body.String(), "OK") {
				t.Fatalf("status=%d uploads=%d inference=%d body=%s", result.Code, uploads, inferences, result.Body)
			}
			for _, forbidden := range []string{"file-cpa-", "file-api-selected", "__cpa", "cpa-hosted", "base64"} {
				if strings.Contains(result.Body.String(), forbidden) {
					t.Errorf("downstream exposed %s", forbidden)
				}
			}
		})
	}
}
