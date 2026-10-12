package openai

import (
	"bytes"
	"image"
	"image/png"
	"mime/multipart"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
)

func TestScreenshotFilesCRUDWithoutUpstream(t *testing.T) {
	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&config.SDKConfig{CodexFilesPrototype: config.CodexFilesPrototypeConfig{Enabled: true}}, nil))
	r := gin.New()
	r.POST("/v1/files", h.FilesUpload)
	r.GET("/v1/files", h.FilesList)
	r.GET("/v1/files/:file_id", h.FilesRetrieve)
	r.GET("/v1/files/:file_id/content", h.FilesContent)
	r.DELETE("/v1/files/:file_id", h.FilesDelete)
	var source bytes.Buffer
	_ = png.Encode(&source, image.NewRGBA(image.Rect(0, 0, 803, 444)))
	call := func(method, path, caller string, body *bytes.Buffer, ctype string) *httptest.ResponseRecorder {
		var req *httptest.ResponseRecorder
		_ = req
		httpReq := httptest.NewRequest(method, path, body)
		httpReq = httpReq.WithContext(access.WithAuthenticatedPrincipal(httpReq.Context(), caller))
		httpReq.Header.Set("Content-Type", ctype)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httpReq)
		return w
	}
	var multipartBody bytes.Buffer
	mw := multipart.NewWriter(&multipartBody)
	part, _ := mw.CreateFormFile("file", "screen.png")
	_, _ = part.Write(source.Bytes())
	_ = mw.WriteField("purpose", "user_data")
	_ = mw.WriteField("expires_after[anchor]", "created_at")
	_ = mw.WriteField("expires_after[seconds]", "3600")
	_ = mw.Close()
	w := call("POST", "/v1/files", "a", &multipartBody, mw.FormDataContentType())
	if w.Code != 200 {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	id := gjson.Get(w.Body.String(), "id").String()
	path := "/v1/files/" + id
	for _, suffix := range []string{"", "/content"} {
		w = call("GET", path+suffix, "b", bytes.NewBuffer(nil), "")
		if w.Code != 404 {
			t.Fatal("caller isolation")
		}
	}
	w = call("GET", path+"/content", "a", bytes.NewBuffer(nil), "")
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), source.Bytes()) || w.Header().Get("Content-Type") != "image/png" {
		t.Fatal("original content")
	}
	w = call("GET", path, "a", bytes.NewBuffer(nil), "")
	if w.Code != 200 || gjson.Get(w.Body.String(), "purpose").String() != "user_data" {
		t.Fatal("retrieve")
	}
	w = call("GET", "/v1/files?purpose=user_data&order=asc&limit=1", "a", bytes.NewBuffer(nil), "")
	if w.Code != 200 || gjson.Get(w.Body.String(), "data.0.id").String() != id {
		t.Fatal("list")
	}
	w = call("DELETE", path, "a", bytes.NewBuffer(nil), "")
	if w.Code != 200 || !gjson.Get(w.Body.String(), "deleted").Bool() {
		t.Fatal("delete")
	}
	w = call("GET", path, "a", bytes.NewBuffer(nil), "")
	if w.Code != 404 {
		t.Fatal("deleted retrieval")
	}
}
