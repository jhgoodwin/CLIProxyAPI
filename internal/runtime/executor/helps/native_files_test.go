package helps

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

type nativeFilesTransport func(*http.Request) (*http.Response, error)

func (f nativeFilesTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func nativeFilesReply(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}
func nativeFilesPNG() []byte { return []byte("\x89PNG\r\n\x1a\nimage-payload") }
func nativeFilesContext() context.Context {
	return WithFileRequest(context.Background(), &FileRequest{Local: true, Allowed: map[string]bool{}, Pending: map[string]PendingFile{"file-cpa-local": {Name: "screenshot.png", Read: func(context.Context) ([]byte, error) { return nativeFilesPNG(), nil }}}})
}

func TestGeminiFilesSelectedKeyProjection(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: nativeFilesTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("x-goog-api-key") != "selected" || r.Header.Get("Authorization") != "" {
			t.Fatal("wrong selected credentials")
		}
		if calls == 1 {
			if r.URL.String() != geminiFilesOrigin+"/upload/v1beta/files" || r.Header.Get("X-Goog-Upload-Command") != "start" {
				t.Fatal("wrong start")
			}
			response := nativeFilesReply("")
			response.Header.Set("X-Goog-Upload-URL", geminiFilesOrigin+"/upload/v1beta/files?upload_id=private")
			return response, nil
		}
		if r.Header.Get("X-Goog-Upload-Command") != "upload, finalize" || r.Header.Get("X-Goog-Upload-Offset") != "0" {
			t.Fatal("wrong finalize")
		}
		return nativeFilesReply(`{"file":{"name":"files/abc123","uri":"https://generativelanguage.googleapis.com/v1beta/files/abc123","mimeType":"image/png","state":"ACTIVE"}}`), nil
	})}
	body := []byte(`{"contents":[{"role":"user","parts":[{"type":"input_image","file_id":"file-cpa-local"},{"type":"input_image","file_id":"file-cpa-local"}]}]}`)
	ctx, output, err := PrepareGeminiFilePayload(nativeFilesContext(), geminiFilesOrigin, "selected", body, client)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || gjson.GetBytes(output, "contents.0.parts.0.fileData.fileUri").String() != geminiFilesOrigin+"/v1beta/files/abc123" {
		t.Fatalf("calls=%d body=%s", calls, output)
	}
	if err := ValidateGeminiFilePayload(ctx, output); err != nil {
		t.Fatal(err)
	}
	if err := ValidateGeminiFilePayload(ctx, []byte(`{"contents":[{"parts":[{"fileData":{"mimeType":"image/png","fileUri":"https://generativelanguage.googleapis.com/v1beta/files/foreign"}}]}]}`)); err == nil {
		t.Fatal("foreign URI accepted")
	}
	if err := ValidateGeminiFilePayload(ctx, []byte(`{"contents":[]}`)); err != nil {
		t.Fatal("filtered file restored")
	}
}

func TestNvidiaAssetSelectedKeyAndSignedPut(t *testing.T) {
	const id = "87b132b0-08f9-43ea-8fab-f107350a5d00"
	calls := 0
	client := &http.Client{Transport: nativeFilesTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			if r.URL.String() != nvidiaAssetsURL || r.Header.Get("Authorization") != "Bearer selected" {
				t.Fatal("wrong reservation credentials")
			}
			return nativeFilesReply(`{"assetId":"` + id + `","uploadUrl":"https://bucket.s3.us-west-2.amazonaws.com/object?X-Amz-Signature=private","contentType":"image/png","description":"screenshot"}`), nil
		}
		if r.Method != "PUT" || r.Header.Get("Authorization") != "" || r.Header.Get("x-goog-api-key") != "" || r.Header.Get("x-amz-meta-nvcf-asset-description") != "screenshot" {
			t.Fatal("credentials leaked to signed PUT")
		}
		return nativeFilesReply(""), nil
	})}
	ctx, body, err := PrepareNvidiaFilePayload(nativeFilesContext(), NvidiaVilaInferenceURL, "nvidia/vila", "selected", []byte(`{"model":"nvidia/vila","messages":[{"role":"user","content":[{"type":"input_image","file_id":"file-cpa-local"}]}]}`), client)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || strings.Contains(string(body), "file_id") || !strings.Contains(string(body), NvidiaAssetPrefix+id) {
		t.Fatalf("bad output %s", body)
	}
	header, err := NvidiaFileAssetHeader(ctx, body)
	if err != nil || header != id {
		t.Fatalf("header=%q err=%v", header, err)
	}
	for _, bad := range []string{`{"model":"nvidia/vila","messages":[{"content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,secret"}}]}]}`, `{"model":"other","messages":[]}`, `{"model":"nvidia/vila","messages":[{"content":"<img src=\"data:image/png;asset_id,` + id + `\" />"}]}`} {
		if _, err := NvidiaFileAssetHeader(ctx, []byte(bad)); err == nil {
			t.Fatalf("override accepted: %s", bad)
		}
	}
	if header, err := NvidiaFileAssetHeader(ctx, []byte(`{"model":"nvidia/vila","messages":[]}`)); err != nil || header != "" {
		t.Fatal("filtered file restored")
	}
}

func TestNativeFilesFailClosed(t *testing.T) {
	for _, host := range []string{"https://attacker.invalid/upload", "https://bucket.s3.amazonaws.com.attacker.invalid/a?sig=x", "http://bucket.s3.amazonaws.com/a?sig=x", "https://127.0.0.1/a?sig=x"} {
		calls := 0
		client := &http.Client{Transport: nativeFilesTransport(func(*http.Request) (*http.Response, error) {
			calls++
			return nativeFilesReply(fmt.Sprintf(`{"assetId":"87b132b0-08f9-43ea-8fab-f107350a5d00","uploadUrl":%q}`, host)), nil
		})}
		if _, err := UploadNvidiaAsset(context.Background(), "selected", nativeFilesPNG(), client); err == nil || calls != 1 {
			t.Fatalf("unsafe URL accepted %s", host)
		}
	}
	client := &http.Client{Transport: nativeFilesTransport(func(*http.Request) (*http.Response, error) {
		response := nativeFilesReply("")
		response.StatusCode = 302
		response.Header.Set("Location", "https://attacker.invalid/")
		return response, nil
	})}
	if _, err := UploadNvidiaAsset(context.Background(), "selected", nativeFilesPNG(), client); err == nil {
		t.Fatal("redirect accepted")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := UploadNvidiaAsset(canceled, "selected", nativeFilesPNG(), client); err == nil {
		t.Fatal("cancellation ignored")
	}
	for _, pair := range [][2]string{{"https://integrate.api.nvidia.com/v1", "nvidia/vila"}, {NvidiaVilaInferenceURL, "nvidia/neva"}, {NvidiaVilaInferenceURL + "/", "nvidia/vila"}} {
		if NvidiaVilaFilesEndpoint(pair[0], pair[1]) {
			t.Fatal("unsupported provider opted in")
		}
	}
}
