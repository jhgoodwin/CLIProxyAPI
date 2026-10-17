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

// OpenAIFilesEndpoint recognizes official API-key Files, not Codex subscription endpoints or model aliases.
func OpenAIFilesEndpoint(baseURL string) (string, bool) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "api.openai.com") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	switch strings.TrimSuffix(u.Path, "/") {
	case "", "/v1":
		return "https://api.openai.com/v1/files", true
	default:
		return "", false
	}
}

// PrepareOpenAIFilePayload uploads selected-auth screenshots in native Responses input_image positions.
// Callers must use the Responses inference endpoint, apply final rules afterwards, and validate references read-only.
func PrepareOpenAIFilePayload(ctx context.Context, baseURL string, body []byte, do func(*http.Request) (*http.Response, error)) (context.Context, []byte, error) {
	endpoint, supported := OpenAIFilesEndpoint(baseURL)
	return PrepareFilePayload(ctx, body, func(uploadCtx context.Context, name string, data []byte) (string, error) {
		if !supported {
			return "", fmt.Errorf("selected endpoint does not support official OpenAI Files")
		}
		return UploadOpenAIImageFile(uploadCtx, endpoint, name, data, do)
	})
}

// UploadOpenAIImageFile uploads a common normalized PNG for Responses vision with one-hour retention.
// The selected-auth callback must protect credentials/content type and reject redirects.
func UploadOpenAIImageFile(ctx context.Context, endpoint, name string, data []byte, do func(*http.Request) (*http.Response, error)) (string, error) {
	// The common screenshot store bounds each normalized variant to 20 MiB.
	if len(data) == 0 || len(data) > 20<<20 || http.DetectContentType(data) != "image/png" {
		return "", fmt.Errorf("OpenAI screenshot Files requires a normalized PNG no larger than 20 MiB")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, field := range []struct{ name, value string }{{"purpose", "vision"}, {"expires_after[anchor]", "created_at"}, {"expires_after[seconds]", "3600"}} {
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
		return "", fmt.Errorf("OpenAI Files upload transport failed")
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			LogWithRequestID(ctx).Debug("OpenAI Files response close failed")
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("OpenAI Files upload failed (HTTP %d)", resp.StatusCode)
	}
	const maxResponseBytes = 64 << 10
	responseBody, errRead := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if errRead != nil || len(responseBody) > maxResponseBytes {
		return "", fmt.Errorf("OpenAI Files returned an invalid upload response")
	}
	var file struct {
		ID string `json:"id"`
	}
	if errDecode := json.Unmarshal(responseBody, &file); errDecode != nil || !strings.HasPrefix(file.ID, "file-") || len(file.ID) <= len("file-") || strings.ContainsAny(file.ID, "/?#\r\n\t ") || strings.HasPrefix(file.ID, PrototypeFilePrefix) || IsProxyLocalFileID(file.ID) {
		return "", fmt.Errorf("OpenAI Files returned an invalid file identifier")
	}
	return file.ID, nil
}
