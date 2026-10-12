package openai

import (
	"bytes"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestFilesUploadBoundedValidation(t *testing.T) {
	var valid bytes.Buffer
	_ = png.Encode(&valid, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	cases := []struct {
		name                   string
		enabled, authenticated bool
		purpose                string
		data                   []byte
		max                    int64
		want                   int
	}{
		{"disabled", false, true, "vision", valid.Bytes(), 1024, 404},
		{"anonymous", true, false, "vision", valid.Bytes(), 1024, 401},
		{"purpose", true, true, "assistants", valid.Bytes(), 1024, 400},
		{"format", true, true, "vision", []byte("not PNG"), 1024, 415},
		{"oversize", true, true, "vision", valid.Bytes(), 10, 413},
		{"no oauth required", true, true, "vision", valid.Bytes(), 1024, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.SDKConfig{CodexFilesPrototype: config.CodexFilesPrototypeConfig{Enabled: tc.enabled, MaxBytes: tc.max}}
			h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(cfg, auth.NewManager(nil, nil, nil)))
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			part, _ := writer.CreateFormFile("file", "sample.png")
			_, _ = part.Write(tc.data)
			_ = writer.WriteField("purpose", tc.purpose)
			_ = writer.Close()
			req := httptest.NewRequest(http.MethodPost, "/v1/files", &body)
			req.Header.Set("Content-Type", writer.FormDataContentType())
			if tc.authenticated {
				req = req.WithContext(access.WithAuthenticatedPrincipal(req.Context(), "alice"))
			}
			r := gin.New()
			r.POST("/v1/files", h.FilesUpload)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.want, w.Body.String())
			}
			// net/http.MultipartReader sets a nonnil empty sentinel; only
			// populated Value/File maps indicate multipart form buffering.
			if req.MultipartForm != nil && (len(req.MultipartForm.File) != 0 || len(req.MultipartForm.Value) != 0) {
				t.Fatal("parser buffered multipart form")
			}
		})
	}
}
