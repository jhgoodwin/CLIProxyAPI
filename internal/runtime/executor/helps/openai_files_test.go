package helps

import (
	"bytes"
	"context"
	"encoding/base64"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOpenAIFilesEndpoint(t *testing.T) {
	for _, base := range []string{"https://api.openai.com", "https://api.openai.com/", "https://api.openai.com/v1/"} {
		if endpoint, ok := OpenAIFilesEndpoint(base); !ok || endpoint != "https://api.openai.com/v1/files" {
			t.Fatalf("endpoint %q=%q supported=%v", base, endpoint, ok)
		}
	}
	for _, base := range []string{"http://api.openai.com", "https://api.openai.com.evil.test/v1", "https://api.openai.com:443/v1", "https://user@api.openai.com/v1", "https://api.openai.com/v1?key=x", "https://chatgpt.com/backend-api/codex", "https://api.other.test/gpt-6"} {
		if _, ok := OpenAIFilesEndpoint(base); ok {
			t.Fatalf("unrecognized endpoint supported: %s", base)
		}
	}
}

func TestPrepareOpenAIFilePayloadNativeResponses(t *testing.T) {
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	localID := PrototypeFilePrefix + "openai-screen"
	original := &FileRequest{Local: true, Allowed: map[string]bool{}, Pending: map[string]PendingFile{localID: {Name: "screenshot.png", Read: func(context.Context) ([]byte, error) { return data, nil }}}}
	ctx := WithFileRequest(context.Background(), original)
	body := []byte(`{"input":[{"role":"user","content":[{"type":"input_image","file_id":"` + localID + `"},{"type":"input_image","file_id":"` + localID + `"}]}]}`)
	uploads := 0
	attempt, out, err := PrepareOpenAIFilePayload(ctx, "https://api.openai.com/v1", body, func(req *http.Request) (*http.Response, error) {
		uploads++
		if req.Method != http.MethodPost || req.URL.String() != "https://api.openai.com/v1/files" {
			t.Fatal("wrong upload endpoint")
		}
		if errParse := req.ParseMultipartForm(1 << 20); errParse != nil {
			t.Fatal(errParse)
		}
		if req.FormValue("purpose") != "vision" || req.FormValue("expires_after[anchor]") != "created_at" || req.FormValue("expires_after[seconds]") != "3600" {
			t.Fatal("incorrect vision purpose/retention")
		}
		f, h, errOpen := req.FormFile("file")
		if errOpen != nil {
			t.Fatal(errOpen)
		}
		got, errRead := io.ReadAll(f)
		if errClose := f.Close(); errClose != nil {
			t.Fatal(errClose)
		}
		if errRead != nil || !bytes.Equal(data, got) || h.Filename != "screenshot.png" {
			t.Fatal("file multipart mismatch")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"file-selected"}`))}, nil
	})
	if err != nil || uploads != 1 || len(original.Allowed) != 0 {
		t.Fatalf("attempt upload isolation/dedup failed: uploads=%d err=%v", uploads, err)
	}
	for i := range 2 {
		part := gjson.GetBytes(out, "input.0.content").Array()[i]
		if part.Get("type").String() != "input_image" || part.Get("file_id").String() != "file-selected" || part.Get("image_url").Exists() {
			t.Fatalf("native Responses image changed: %s", out)
		}
	}
	if errValidate := ValidateFilePayload(attempt, out); errValidate != nil {
		t.Fatal(errValidate)
	}
	called := false
	_, _, err = PrepareOpenAIFilePayload(ctx, "https://unofficial.test/v1", body, func(*http.Request) (*http.Response, error) { called = true; return nil, nil })
	if err == nil || called {
		t.Fatal("unofficial endpoint did not fail closed")
	}
	for _, response := range []struct {
		status int
		body   string
	}{{403, "private"}, {200, `{"id":"file-cpa-local"}`}, {200, `{"id":"file-selected"}{}`}, {200, `{"id":"file-selected"}` + strings.Repeat(" ", 64<<10)}} {
		_, err = UploadOpenAIImageFile(ctx, "https://api.openai.com/v1/files", "screenshot.png", data, func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: response.status, Body: io.NopCloser(strings.NewReader(response.body))}, nil
		})
		if err == nil || strings.Contains(err.Error(), "private") {
			t.Fatalf("unsafe upload response: %v", err)
		}
	}
	called = false
	_, err = UploadOpenAIImageFile(ctx, "https://api.openai.com/v1/files", "image.jpg", []byte("not png"), func(*http.Request) (*http.Response, error) { called = true; return nil, nil })
	if err == nil || called {
		t.Fatal("non-normalized image uploaded")
	}
}
