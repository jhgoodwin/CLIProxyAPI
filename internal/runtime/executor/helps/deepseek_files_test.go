package helps

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDeepSeekFilesEndpoint(t *testing.T) {
	for _, base := range []string{"https://api.deepseek.com", "https://api.deepseek.com/", "https://api.deepseek.com/v1/"} {
		if endpoint, ok := DeepSeekFilesEndpoint(base); !ok || endpoint != "https://api.deepseek.com/files" {
			t.Fatalf("endpoint %q: %q %v", base, endpoint, ok)
		}
	}
	for _, base := range []string{"http://api.deepseek.com", "https://api.deepseek.com.evil.test", "https://api.deepseek.com:443", "https://user@api.deepseek.com", "https://api.deepseek.com/anthropic", "https://api.deepseek.com?key=x", "https://other.test/deepseek-flash"} {
		if _, ok := DeepSeekFilesEndpoint(base); ok {
			t.Fatalf("unexpected Files support: %q", base)
		}
	}
}

func TestUploadDeepSeekFileMultipart(t *testing.T) {
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	marker := &struct{}{}
	ctx := context.WithValue(context.Background(), marker, "selected")
	id, err := UploadDeepSeekFile(ctx, "https://api.deepseek.com/files", "screen.png", data, func(req *http.Request) (*http.Response, error) {
		if req.Context().Value(marker) != "selected" || req.Method != http.MethodPost || req.URL.String() != "https://api.deepseek.com/files" {
			t.Fatal("upload request lost selected attempt context")
		}
		if errParse := req.ParseMultipartForm(1 << 20); errParse != nil {
			t.Fatal(errParse)
		}
		if req.FormValue("purpose") != "user_data" {
			t.Fatal("wrong purpose")
		}
		if req.FormValue("expires_after[anchor]") != "created_at" || req.FormValue("expires_after[seconds]") != "3600" {
			t.Fatal("transient upload expiration missing")
		}
		file, header, errOpen := req.FormFile("file")
		if errOpen != nil {
			t.Fatal(errOpen)
		}
		got, errRead := io.ReadAll(file)
		if errClose := file.Close(); errClose != nil {
			t.Fatal(errClose)
		}
		if errRead != nil || header.Filename != "screen.png" || !bytes.Equal(data, got) {
			t.Fatal("multipart image mismatch")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"file-api-selected"}`))}, nil
	})
	if err != nil || id != "file-api-selected" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	for _, response := range []struct {
		status int
		body   string
	}{{400, "secret"}, {200, `{"id":"file-other"}`}, {200, `{`}, {200, `{"id":"file-api-selected"}` + strings.Repeat(" ", 64<<10)}, {200, `{"id":"file-api-selected"}{}`}} {
		_, err = UploadDeepSeekFile(ctx, "https://api.deepseek.com/files", "screen.png", data, func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: response.status, Body: io.NopCloser(strings.NewReader(response.body))}, nil
		})
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("upload response not rejected safely: %v", err)
		}
	}
	called := false
	_, err = UploadDeepSeekFile(ctx, "https://api.deepseek.com/files", "not.png", []byte("not an image"), func(*http.Request) (*http.Response, error) { called = true; return nil, nil })
	if err == nil || called {
		t.Fatal("non-image upload was sent")
	}
}
