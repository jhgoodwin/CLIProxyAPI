package helps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const geminiFilesOrigin = "https://generativelanguage.googleapis.com"

var geminiFileName = regexp.MustCompile(`^files/[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$`)

// GeminiUploadedFile describes an ACTIVE image uploaded with this attempt's API key.
type GeminiUploadedFile struct {
	Name  string `json:"name"`
	URI   string `json:"uri"`
	MIME  string `json:"mimeType"`
	State string `json:"state"`
}

// UploadGeminiFile performs the official resumable start and upload/finalize protocol.
// Only ACTIVE PNG files are returned; processing or failed images fail closed without inference.
func UploadGeminiFile(ctx context.Context, apiKey string, data []byte, client *http.Client) (GeminiUploadedFile, error) {
	var file GeminiUploadedFile
	if apiKey == "" || len(data) == 0 || len(data) > 20<<20 || http.DetectContentType(data) != "image/png" {
		return file, fmt.Errorf("Gemini Files requires a selected API key and a normalized PNG of at most 20 MiB")
	}
	client = NativeFilesClient(client)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiFilesOrigin+"/upload/v1beta/files", bytes.NewBufferString(`{"file":{"display_name":"screenshot.png"}}`))
	if err != nil {
		return file, err
	}
	req.Header.Set("x-goog-api-key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Upload-Protocol", "resumable")
	req.Header.Set("X-Goog-Upload-Command", "start")
	req.Header.Set("X-Goog-Upload-Header-Content-Length", strconv.Itoa(len(data)))
	req.Header.Set("X-Goog-Upload-Header-Content-Type", "image/png")
	headers, err := nativeFileResponse(ctx, client, req, nil)
	if err != nil {
		return file, err
	}
	uploadURL := headers.Get("X-Goog-Upload-URL")
	u, err := url.Parse(uploadURL)
	if err != nil || u.Scheme != "https" || u.Host != "generativelanguage.googleapis.com" || u.User != nil || u.Fragment != "" || !strings.HasPrefix(u.Path, "/upload/v1beta/files") {
		return file, fmt.Errorf("Gemini Files returned an unsafe upload URL")
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, bytes.NewReader(data))
	if err != nil {
		return file, fmt.Errorf("invalid Gemini upload request")
	}
	req.Header.Set("x-goog-api-key", apiKey)
	req.Header.Set("Content-Type", "image/png")
	req.Header.Set("X-Goog-Upload-Offset", "0")
	req.Header.Set("X-Goog-Upload-Command", "upload, finalize")
	var response struct {
		File GeminiUploadedFile `json:"file"`
	}
	if _, err = nativeFileResponse(ctx, client, req, &response); err != nil {
		return file, err
	}
	file = response.File
	if !geminiFileName.MatchString(file.Name) || file.URI != geminiFilesOrigin+"/v1beta/"+file.Name || file.MIME != "image/png" || file.State != "ACTIVE" {
		return GeminiUploadedFile{}, fmt.Errorf("Gemini Files did not return a valid ACTIVE PNG")
	}
	return file, nil
}

// PrepareGeminiFilePayload replaces preserved input_image/file_id parts with native fileData.
// Uploads are selected-key scoped and deduplicated only inside this attempt, before payload rules.
func PrepareGeminiFilePayload(ctx context.Context, baseURL, apiKey string, body []byte, client *http.Client) (context.Context, []byte, error) {
	if f := FileRequestFromContext(ctx); f == nil || !f.Local {
		return ctx, body, nil
	}
	files := map[string]GeminiUploadedFile{}
	next, body, err := PrepareFilePayload(ctx, body, func(uploadCtx context.Context, _ string, data []byte) (string, error) {
		if strings.TrimSuffix(baseURL, "/") != geminiFilesOrigin {
			return "", fmt.Errorf("selected Gemini endpoint does not support native Files")
		}
		file, err := UploadGeminiFile(uploadCtx, apiKey, data, client)
		if err != nil {
			return "", err
		}
		id := strings.TrimPrefix(file.Name, "files/")
		files[id] = file
		return id, nil
	})
	if err != nil {
		return ctx, nil, err
	}
	f := FileRequestFromContext(next)
	if f != nil {
		f.NativeProvider = "gemini"
	}
	for _, slot := range ImageSlots(body) {
		id := gjson.GetBytes(body, slot+".file_id").String()
		file, ok := files[id]
		if !ok {
			return ctx, nil, fmt.Errorf("Gemini file reference was not uploaded by this attempt")
		}
		part, err := json.Marshal(map[string]any{"fileData": map[string]string{"mimeType": file.MIME, "fileUri": file.URI}})
		if err != nil {
			return ctx, nil, err
		}
		body, err = sjson.SetRawBytes(body, slot, part)
		if err != nil {
			return ctx, nil, err
		}
		if f != nil {
			f.Allowed[file.URI] = true
		}
	}
	return next, body, nil
}

// ValidateGeminiFilePayload checks native URI references after final payload overrides.
func ValidateGeminiFilePayload(ctx context.Context, body []byte) error {
	if err := ValidateFilePayload(ctx, body); err != nil {
		return err
	}
	f := FileRequestFromContext(ctx)
	if f == nil || !f.Local {
		return nil
	}
	var value any
	if json.Unmarshal(body, &value) != nil {
		return fmt.Errorf("invalid Gemini file payload")
	}
	var walk func(any) error
	walk = func(value any) error {
		switch v := value.(type) {
		case map[string]any:
			for key, child := range v {
				if key == "fileData" || key == "file_data" {
					file, ok := child.(map[string]any)
					if !ok || len(file) != 2 || file["mimeType"] != "image/png" {
						return fmt.Errorf("invalid Gemini file data")
					}
					uri, ok := file["fileUri"].(string)
					if !ok || !f.Allowed[uri] {
						return fmt.Errorf("unowned Gemini file URI")
					}
				} else if key == "fileUri" || key == "file_uri" || key == "uri" {
					return fmt.Errorf("unexpected Gemini file URI")
				}
				if key != "fileData" && key != "file_data" {
					if err := walk(child); err != nil {
						return err
					}
				}
			}
		case []any:
			for _, child := range v {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(value)
}
