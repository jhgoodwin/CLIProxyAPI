package files

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/anthropicfiles"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
)

func imageBytes(t *testing.T, format string) []byte {
	t.Helper()
	var b bytes.Buffer
	var err error
	if format == "gif" {
		err = gif.Encode(&b, image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.Black}), nil)
	} else {
		err = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func multipartBody(t *testing.T, data []byte, fields [][2]string) (*bytes.Buffer, string) {
	t.Helper()
	b := new(bytes.Buffer)
	w := multipart.NewWriter(b)
	part, err := w.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="file"; filename="folder/original.png"`}, "Content-Type": {"application/octet-stream"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(data); err != nil {
		t.Fatal(err)
	}
	for _, f := range fields {
		if err = w.WriteField(f[0], f[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return b, w.FormDataContentType()
}
func routes(h *Handler) *gin.Engine {
	r := gin.New()
	r.POST("/v1/files", h.Upload)
	r.GET("/v1/files", h.List)
	r.GET("/v1/files/:file_id", h.Get)
	r.GET("/v1/files/:file_id/content", h.Content)
	r.DELETE("/v1/files/:file_id", h.Delete)
	return r
}
func request(r http.Handler, method, path, principal, format string, body *bytes.Buffer, contentType string) *httptest.ResponseRecorder {
	if body == nil {
		body = new(bytes.Buffer)
	}
	req := httptest.NewRequest(method, path, body)
	if principal != "" {
		req = req.WithContext(access.WithAuthenticatedPrincipal(req.Context(), principal))
	}
	if format == "anthropic" {
		req.Header.Set("Anthropic-Version", "future-version")
	}
	if format == "beta" {
		req.Header.Set("Anthropic-Beta", "unrelated-beta, files-api-future")
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}
func decode(t *testing.T, rec *httptest.ResponseRecorder, status int) map[string]any {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, status, rec.Body.String())
	}
	var obj map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &obj); err != nil {
		t.Fatal(err)
	}
	return obj
}
func openHandler(t *testing.T) (*Handler, *gin.Engine) {
	t.Helper()
	s, err := anthropicfiles.Open(t.TempDir(), 2000, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	cfg := new(config.Config)
	cfg.Files.Enabled = true
	h := NewHandler(s, cfg)
	return h, routes(h)
}
func TestCrossFormatLifecycleAndOwnership(t *testing.T) {
	for _, uploadFormat := range []string{"openai", "anthropic", "beta"} {
		t.Run(uploadFormat, func(t *testing.T) {
			_, r := openHandler(t)
			data := imageBytes(t, "png")
			fields := [][2]string{}
			if uploadFormat == "openai" {
				fields = append(fields, [2]string{"purpose", "vision"})
			}
			body, ct := multipartBody(t, data, fields)
			obj := decode(t, request(r, "POST", "/v1/files", "alice", uploadFormat, body, ct), 200)
			id := obj["id"].(string)
			expectedPurpose := "user_data"
			if uploadFormat == "openai" {
				expectedPurpose = "vision"
			}
			for _, format := range []string{"openai", "anthropic"} {
				got := decode(t, request(r, "GET", "/v1/files/"+id, "alice", format, nil, ""), 200)
				if format == "openai" {
					if got["purpose"] != expectedPurpose || got["bytes"] != float64(len(data)) || got["status"] != "processed" {
						t.Fatal(got)
					}
					if _, ok := got["created_at"].(float64); !ok {
						t.Fatal(got)
					}
				} else {
					if _, ok := got["purpose"]; ok {
						t.Fatal("Anthropic purpose leaked")
					}
					if got["mime_type"] != "image/png" || got["size_bytes"] != float64(len(data)) {
						t.Fatal(got)
					}
				}
				content := request(r, "GET", "/v1/files/"+id+"/content", "alice", format, nil, "")
				if content.Code != 200 || !bytes.Equal(content.Body.Bytes(), data) || content.Header().Get("Cache-Control") != "no-store" {
					t.Fatal(content)
				}
				list := decode(t, request(r, "GET", "/v1/files", "alice", format, nil, ""), 200)
				if len(list["data"].([]any)) != 1 {
					t.Fatal(list)
				}
				other := decode(t, request(r, "GET", "/v1/files", "bob", format, nil, ""), 200)
				if len(other["data"].([]any)) != 0 {
					t.Fatal(other)
				}
				for _, endpoint := range []struct{ method, path string }{{"GET", "/v1/files/" + id}, {"GET", "/v1/files/" + id + "/content"}, {"DELETE", "/v1/files/" + id}} {
					decode(t, request(r, endpoint.method, endpoint.path, "bob", format, nil, ""), 404)
				}
			}
			deleted := decode(t, request(r, "DELETE", "/v1/files/"+id, "alice", "anthropic", nil, ""), 200)
			if deleted["type"] != "file_deleted" {
				t.Fatal(deleted)
			}
			decode(t, request(r, "GET", "/v1/files/"+id, "alice", "openai", nil, ""), 404)
		})
	}
}
func TestGIFAndPurposeSurviveRestart(t *testing.T) {
	root := t.TempDir()
	s, err := anthropicfiles.Open(root, 10, 10000)
	if err != nil {
		t.Fatal(err)
	}
	cfg := new(config.Config)
	cfg.Files.Enabled = true
	r := routes(NewHandler(s, cfg))
	data := imageBytes(t, "gif")
	body, ct := multipartBody(t, data, [][2]string{{"purpose", "vision"}})
	obj := decode(t, request(r, "POST", "/v1/files", "alice", "openai", body, ct), 200)
	id := obj["id"].(string)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = anthropicfiles.Open(root, 10, 10000)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r = routes(NewHandler(s, cfg))
	got := decode(t, request(r, "GET", "/v1/files/"+id, "alice", "openai", nil, ""), 200)
	if got["purpose"] != "vision" {
		t.Fatal(got)
	}
	got = decode(t, request(r, "GET", "/v1/files/"+id, "alice", "anthropic", nil, ""), 200)
	if got["mime_type"] != "image/gif" {
		t.Fatal(got)
	}
	rec := request(r, "GET", "/v1/files/"+id+"/content", "alice", "anthropic", nil, "")
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), data) {
		t.Fatal(rec)
	}
}
func TestMultipartValidationAndExpiryBounds(t *testing.T) {
	h, r := openHandler(t)
	data := imageBytes(t, "png")
	cases := []struct {
		name, format string
		fields       [][2]string
		data         []byte
	}{
		{"missing purpose", "openai", nil, data},
		{"invalid purpose", "anthropic", [][2]string{{"purpose", "assistants"}}, data},
		{"duplicate purpose", "openai", [][2]string{{"purpose", "vision"}, {"purpose", "vision"}}, data},
		{"unknown field", "anthropic", [][2]string{{"model", "any-model"}}, data},
		{"expiry no anchor", "anthropic", [][2]string{{"expires_after[seconds]", "3600"}}, data},
		{"expiry low", "anthropic", [][2]string{{"expires_after[anchor]", "created_at"}, {"expires_after[seconds]", "3599"}}, data},
		{"expiry high", "anthropic", [][2]string{{"expires_after[anchor]", "created_at"}, {"expires_after[seconds]", "2592001"}}, data},
		{"expiry overflow", "anthropic", [][2]string{{"expires_after[anchor]", "created_at"}, {"expires_after[seconds]", "9223372036854775807"}}, data},
		{"malformed image", "anthropic", nil, []byte("bad")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, ct := multipartBody(t, tc.data, tc.fields)
			decode(t, request(r, "POST", "/v1/files", "alice", tc.format, body, ct), 400)
		})
	}
	decode(t, request(r, "POST", "/v1/files", "alice", "openai", bytes.NewBufferString("bad"), "text/plain"), 400)
	decode(t, request(r, "POST", "/v1/files", "alice", "anthropic", bytes.NewBufferString("bad"), "multipart/form-data; boundary=x"), 400)
	body, ct := multipartBody(t, data, [][2]string{{"expires_after[anchor]", "created_at"}, {"expires_after[seconds]", "3600"}})
	obj := decode(t, request(r, "POST", "/v1/files", "alice", "beta", body, ct), 200)
	m, err := h.Store.Get("alice", obj["id"].(string))
	if err != nil || m.ExpiresAt.Sub(m.CreatedAt) != time.Hour {
		t.Fatal(m, err)
	}
	h.Cfg.CodexFilesPrototype.Enabled = true
	h.Cfg.CodexFilesPrototype.MaxBytes = 16
	body, ct = multipartBody(t, data, [][2]string{{"purpose", "vision"}})
	decode(t, request(r, "POST", "/v1/files", "alice", "openai", body, ct), 413)
	h.Cfg.CodexFilesPrototype.MaxBytes = 10000
	h.Cfg.CodexFilesPrototype.TTLSeconds = 3600
	body, ct = multipartBody(t, data, [][2]string{{"expires_after[anchor]", "created_at"}, {"expires_after[seconds]", "3601"}})
	decode(t, request(r, "POST", "/v1/files", "alice", "anthropic", body, ct), 400)
}
func TestAuthenticationDisabledAndUnavailable(t *testing.T) {
	cfg := new(config.Config)
	cfg.Files.Enabled = true
	h := NewHandler(nil, cfg)
	r := routes(h)
	for _, format := range []string{"openai", "anthropic"} {
		got := decode(t, request(r, "GET", "/v1/files", "", format, nil, ""), 401)
		if format == "anthropic" && got["type"] != "error" {
			t.Fatal(got)
		}
		decode(t, request(r, "GET", "/v1/files", "alice", format, nil, ""), 503)
		cfg.Files.Enabled = false
		decode(t, request(r, "GET", "/v1/files", "alice", format, nil, ""), 404)
		cfg.CodexFilesPrototype.Enabled = true
		decode(t, request(r, "GET", "/v1/files", "alice", format, nil, ""), 503)
		cfg.CodexFilesPrototype.Enabled = false
		cfg.Files.Enabled = true
	}
}
func TestPaginationFormatsAndPurpose(t *testing.T) {
	h, r := openHandler(t)
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	// Replace the fixture store with an explicit clock to make upload order deterministic.
	s, err := anthropicfiles.Open(t.TempDir(), 2000, 1<<20, anthropicfiles.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h.Store = s
	ids := []string{}
	data := imageBytes(t, "png")
	for i := 0; i < 4; i++ {
		now = now.Add(time.Second)
		purpose := "vision"
		if i%2 == 1 {
			purpose = "user_data"
		}
		m, err := s.PutWithPurpose("alice", fmt.Sprint(i)+".png", "image/png", purpose, data, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	got := decode(t, request(r, "GET", "/v1/files?limit=1&order=asc&purpose=vision", "alice", "openai", nil, ""), 200)
	if got["data"].([]any)[0].(map[string]any)["id"] != ids[0] || got["has_more"] != true {
		t.Fatal(got)
	}
	got = decode(t, request(r, "GET", "/v1/files?limit=1&order=asc&purpose=vision&after="+ids[0], "alice", "openai", nil, ""), 200)
	if got["data"].([]any)[0].(map[string]any)["id"] != ids[2] || got["has_more"] != false {
		t.Fatal(got)
	}
	got = decode(t, request(r, "GET", "/v1/files?limit=1", "alice", "anthropic", nil, ""), 200)
	if got["last_id"] != ids[3] || got["has_more"] != true {
		t.Fatal(got)
	}
	got = decode(t, request(r, "GET", "/v1/files?after_id="+ids[3], "alice", "anthropic", nil, ""), 200)
	if len(got["data"].([]any)) != 3 {
		t.Fatal(got)
	}
	got = decode(t, request(r, "GET", "/v1/files?before_id="+ids[1], "alice", "anthropic", nil, ""), 200)
	if len(got["data"].([]any)) != 2 {
		t.Fatal(got)
	}
	for _, path := range []string{"?limit=0", "?limit=bogus", "?limit=1&limit=2", "?unknown=x", "?order=bad"} {
		decode(t, request(r, "GET", "/v1/files"+path, "alice", "openai", nil, ""), 400)
	}
	for _, path := range []string{"?limit=1001", "?after_id=" + ids[0] + "&before_id=" + ids[1], "?after_id=foreign", "?order=asc"} {
		decode(t, request(r, "GET", "/v1/files"+path, "alice", "anthropic", nil, ""), 400)
	}
	// An unowned OpenAI cursor cannot expose another principal's files.
	got = decode(t, request(r, "GET", "/v1/files?after="+ids[0], "bob", "openai", nil, ""), 200)
	if len(got["data"].([]any)) != 0 {
		t.Fatal(got)
	}
}
func TestMultipartBodyBound(t *testing.T) {
	h, r := openHandler(t)
	h.Cfg.CodexFilesPrototype.Enabled = true
	h.Cfg.CodexFilesPrototype.MaxBytes = 32
	req := httptest.NewRequest("POST", "/v1/files", strings.NewReader(strings.Repeat("x", 65569)))
	req = req.WithContext(access.WithAuthenticatedPrincipal(req.Context(), "alice"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	decode(t, rec, 413)
}

func TestLegacyShortDefaultTTL(t *testing.T) {
	h, r := openHandler(t)
	h.Cfg.CodexFilesPrototype.Enabled = true
	h.Cfg.CodexFilesPrototype.TTLSeconds = 60
	body, ctype := multipartBody(t, imageBytes(t, "png"), [][2]string{{"purpose", "vision"}})
	obj := decode(t, request(r, "POST", "/v1/files", "alice", "openai", body, ctype), 200)
	if obj["expires_at"].(float64)-obj["created_at"].(float64) != 60 {
		t.Fatalf("legacy default changed: %+v", obj)
	}
}
