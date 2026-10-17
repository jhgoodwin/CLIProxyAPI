package helps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
)

// DeepSeekFilesEndpoint recognizes the official endpoint, not provider or model aliases.
func DeepSeekFilesEndpoint(baseURL string) (string, bool) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "api.deepseek.com") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	switch strings.TrimSuffix(u.Path, "/") {
	case "", "/v1":
		return "https://api.deepseek.com/files", true
	default:
		return "", false
	}
}

// PrepareDeepSeekFilePayload uploads pending images under the credential selected for this attempt.
// The caller applies final user payload rules and read-only file validation afterwards.
func PrepareDeepSeekFilePayload(ctx context.Context, baseURL string, body []byte, do func(*http.Request) (*http.Response, error)) (context.Context, []byte, error) {
	endpoint, supported := DeepSeekFilesEndpoint(baseURL)
	return PrepareFilePayload(ctx, body, func(uploadCtx context.Context, name string, data []byte) (string, error) {
		if !supported {
			return "", fmt.Errorf("selected compatibility endpoint does not support Files uploads")
		}
		return UploadDeepSeekFile(uploadCtx, endpoint, name, data, do)
	})
}

// FileUploadHTTPClient clones a selected-auth client and rejects redirects without forwarding credentials or files.
func FileUploadHTTPClient(client *http.Client) *http.Client {
	clone := *client
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &clone
}

// ProtectFileRequestHeaders restores selected credentials and transport content type after custom headers.
func ProtectFileRequestHeaders(req *http.Request, apiKey, contentType string) {
	req.Header.Del("Authorization")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("Content-Type", contentType)
}

// UploadDeepSeekFile sends an image multipart upload without logging image bytes or upstream error bodies.
func UploadDeepSeekFile(ctx context.Context, endpoint, name string, data []byte, do func(*http.Request) (*http.Response, error)) (string, error) {
	if len(data) == 0 || len(data) > 64<<20 {
		return "", fmt.Errorf("DeepSeek image file must contain between 1 byte and 64 MiB")
	}
	switch http.DetectContentType(data) {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return "", fmt.Errorf("DeepSeek Files requires a JPEG, PNG, GIF, or WebP image")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("purpose", "user_data"); err != nil {
		return "", err
	}
	for _, field := range []struct{ name, value string }{
		{"expires_after[anchor]", "created_at"},
		{"expires_after[seconds]", "3600"},
	} {
		if err := writer.WriteField(field.name, field.value); err != nil {
			return "", err
		}
	}
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		return "", err
	}
	if _, errWrite := part.Write(data); errWrite != nil {
		return "", errWrite
	}
	if errClose := writer.Close(); errClose != nil {
		return "", errClose
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := do(req)
	if err != nil {
		return "", fmt.Errorf("DeepSeek Files upload transport failed")
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			LogWithRequestID(ctx).Debug("DeepSeek Files response close failed")
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("DeepSeek Files upload failed (HTTP %d)", resp.StatusCode)
	}
	var file struct {
		ID string `json:"id"`
	}
	const maxResponseBytes = 64 << 10
	responseBody, errRead := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if errRead != nil || len(responseBody) > maxResponseBytes {
		return "", fmt.Errorf("DeepSeek Files returned an invalid upload response")
	}
	if errDecode := json.Unmarshal(responseBody, &file); errDecode != nil || !strings.HasPrefix(file.ID, "file-api-") || len(file.ID) <= len("file-api-") || strings.TrimSpace(file.ID) != file.ID {
		return "", fmt.Errorf("DeepSeek Files returned an invalid file identifier")
	}
	return file.ID, nil
}
