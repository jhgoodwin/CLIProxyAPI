package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// UploadVisionPNG reserves, uploads unchanged bytes, and requires final readiness.
func (e *CodexExecutor) UploadVisionPNG(ctx context.Context, a *auth.Auth, name string, data []byte) (string, error) {
	token, base := codexCreds(a)
	if a == nil || a.Attributes["api_key"] != "" || token == "" {
		return "", fmt.Errorf("files require Codex OAuth")
	}
	if base == "" {
		base = "https://chatgpt.com/backend-api/codex"
	}
	base = strings.TrimSuffix(strings.TrimRight(base, "/"), "/codex")
	client := codexFileNoRedirectClient(helps.NewUtlsHTTPClient(ctx, e.cfg, a, 0))
	backend := func(path string, payload []byte) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("files backend request invalid")
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		if account, ok := a.Metadata["account_id"].(string); ok && account != "" {
			req.Header.Set("ChatGPT-Account-ID", account)
		}
		response, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("files backend transport failed")
		}
		defer func() { _ = response.Body.Close() }()
		body, err := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
		if err != nil || len(body) > 64*1024 {
			return nil, fmt.Errorf("files backend response invalid")
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			if message := helps.SafeFileRejection(body); message != "" {
				return nil, fmt.Errorf("files backend rejected request (HTTP %d): %s", response.StatusCode, message)
			}
			return nil, fmt.Errorf("files backend rejected request (HTTP %d)", response.StatusCode)
		}
		return body, nil
	}
	reservation, _ := json.Marshal(map[string]any{"file_name": name, "file_size": len(data), "use_case": "codex"})
	body, err := backend("/files", reservation)
	if err != nil {
		return "", err
	}
	var created struct {
		ID             string `json:"file_id"`
		AltID          string `json:"id"`
		URL            string `json:"upload_url"`
		PDFReservation bool   `json:"pdf_c2pa_reservation"`
	}
	if json.Unmarshal(body, &created) != nil {
		return "", fmt.Errorf("files create response invalid")
	}
	if created.ID == "" {
		created.ID = created.AltID
	}
	signed, err := url.Parse(created.URL)
	localTestBlob := signed != nil && signed.Scheme == "http" && (signed.Hostname() == "127.0.0.1" || signed.Hostname() == "localhost") && strings.HasPrefix(base, "http://127.0.0.1:")
	if err != nil || signed.Host == "" || signed.User != nil || (signed.Scheme != "https" && !localTestBlob) || created.ID == "" || strings.ContainsAny(created.ID, "/?#") {
		return "", fmt.Errorf("files create response missing reservation")
	}
	put, err := http.NewRequestWithContext(ctx, http.MethodPut, created.URL, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("files blob request invalid")
	}
	put.Header.Set("Content-Type", "image/png")
	put.Header.Set("x-ms-blob-type", "BlockBlob")
	put.Header.Set("x-ms-client-request-id", uuid.NewString())
	// This request never passes through PrepareRequest/HttpRequest or custom headers.
	response, err := client.Do(put)
	if err != nil {
		return "", fmt.Errorf("files blob transport failed")
	}
	_, errRead := io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
	_ = response.Body.Close()
	if errRead != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("files blob upload failed (HTTP %d)", response.StatusCode)
	}
	finalize := []byte(`{}`)
	if created.PDFReservation {
		finalize, _ = json.Marshal(map[string]any{"pdf_c2pa_create_request": json.RawMessage(reservation)})
	}
	body, err = backend("/files/"+url.PathEscape(created.ID)+"/uploaded", finalize)
	if err != nil {
		return "", err
	}
	var ready struct {
		Status      string `json:"status"`
		DownloadURL string `json:"download_url"`
	}
	if json.Unmarshal(body, &ready) != nil || (ready.Status != "success" || ready.DownloadURL == "") {
		return "", fmt.Errorf("files finalize did not confirm readiness")
	}
	return created.ID, nil
}
func (e *CodexAutoExecutor) UploadVisionPNG(ctx context.Context, a *auth.Auth, name string, data []byte) (string, error) {
	return e.httpExec.UploadVisionPNG(ctx, a, name, data)
}
