package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"mime/multipart"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type expiryFileExecutor struct {
	auth.ProviderExecutor
	t         *testing.T
	refreshed bool
}

func (*expiryFileExecutor) Identifier() string { return "codex" }
func (e *expiryFileExecutor) Refresh(_ context.Context, a *auth.Auth) (*auth.Auth, error) {
	if a.ID != "expiry-owner" {
		e.t.Fatal("wrong owner refreshed")
	}
	e.refreshed = true
	a = a.Clone()
	a.Metadata["access_token"] = "fresh"
	return a, nil
}
func (e *expiryFileExecutor) UploadVisionPNG(_ context.Context, a *auth.Auth, _ string, _ []byte) (string, error) {
	if !e.refreshed || a.ID != "expiry-owner" || a.Metadata["access_token"] != "fresh" {
		e.t.Fatal("near-expiry credential not refreshed with same owner")
	}
	return "upstream", nil
}
func TestFilesUploadDoesNotRefreshUpstreamCredentials(t *testing.T) {
	m := auth.NewManager(nil, nil, nil)
	e := &expiryFileExecutor{t: t}
	m.RegisterExecutor(e)
	token := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(30*time.Second).Unix()))) + ".synthetic"
	_, err := m.Register(context.Background(), &auth.Auth{ID: "expiry-owner", Provider: "codex", Status: auth.StatusActive, Metadata: map[string]any{"access_token": token, "refresh_token": "synthetic"}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.SDKConfig{CodexFilesPrototype: config.CodexFilesPrototypeConfig{Enabled: true}}
	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(cfg, m))
	var data, body bytes.Buffer
	_ = png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	mw := multipart.NewWriter(&body)
	p, _ := mw.CreateFormFile("file", "sample.png")
	_, _ = p.Write(data.Bytes())
	_ = mw.WriteField("purpose", "vision")
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/v1/files", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req = req.WithContext(access.WithAuthenticatedPrincipal(req.Context(), "caller"))
	r := gin.New()
	r.POST("/v1/files", h.FilesUpload)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if e.refreshed {
		t.Fatal("local upload accessed upstream credentials")
	}
}
