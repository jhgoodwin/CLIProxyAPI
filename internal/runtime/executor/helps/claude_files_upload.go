package helps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path/filepath"
	"strings"
)

// UploadClaudeFile uploads a file using standard Anthropic multipart framing.
// It rejects redirects and bounds response reads; upstream rejection never falls
// back to inline image data or a different authentication protocol.
func UploadClaudeFile(ctx context.Context, client *http.Client, endpoint, name string, data []byte, headers func(*http.Request)) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	h := make(textproto.MIMEHeader)
	name = filepath.Base(name)
	h.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": name}))
	h.Set("Content-Type", http.DetectContentType(data))
	part, err := writer.CreatePart(h)
	if err != nil {
		return "", fmt.Errorf("create Claude file part: %w", err)
	}
	if _, err = part.Write(data); err != nil {
		return "", fmt.Errorf("write Claude file part: %w", err)
	}
	if err = writer.Close(); err != nil {
		return "", fmt.Errorf("close Claude file multipart: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return "", fmt.Errorf("create Claude file request: %w", err)
	}
	headers(req)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	// Copy the client so redirect policy does not mutate a shared transport client.
	uploadClient := *client
	uploadClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := uploadClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload Claude file: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	const maxResponse = 64 << 10
	payload, err := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
	if err != nil {
		return "", fmt.Errorf("read Claude file response: %w", err)
	}
	if len(payload) > maxResponse {
		return "", fmt.Errorf("Claude file response exceeds size limit")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("Claude file upload rejected (HTTP %d)", res.StatusCode)
	}
	var result struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(payload, &result); err != nil || strings.TrimSpace(result.ID) == "" {
		return "", fmt.Errorf("Claude file upload returned an invalid file ID")
	}
	if strings.HasPrefix(result.ID, PrototypeFilePrefix) || IsProxyLocalFileID(result.ID) {
		return "", fmt.Errorf("Claude file upload returned a local file ID")
	}
	return result.ID, nil
}
