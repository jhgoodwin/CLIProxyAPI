package helps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const NvidiaVilaInferenceURL = "https://ai.api.nvidia.com/v1/vlm/nvidia/vila"
const nvidiaAssetsURL = "https://api.nvcf.nvidia.com/v2/nvcf/assets"
const NvidiaAssetPrefix = "data:image/png;asset_id,"

var nvidiaAssetUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// NvidiaVilaFilesEndpoint opts in only the documented selected model and official endpoint.
// The generic integrate.api.nvidia.com chat endpoint is deliberately not supported.
func NvidiaVilaFilesEndpoint(baseURL, model string) bool {
	return baseURL == NvidiaVilaInferenceURL && model == "nvidia/vila"
}

// NvidiaAssetReference accepts only canonical UUIDs owned by this execution attempt.
func NvidiaAssetReference(value string, allowed map[string]bool) bool {
	if !strings.HasPrefix(value, NvidiaAssetPrefix) {
		return false
	}
	id := strings.TrimPrefix(value, NvidiaAssetPrefix)
	return nvidiaAssetUUID.MatchString(id) && allowed[id]
}

// UploadNvidiaAsset reserves an NVCF asset then PUTs PNG bytes to the signed S3 URL.
// Bearer credentials are used only at the fixed NVCF origin, never on the S3 PUT.
func UploadNvidiaAsset(ctx context.Context, key string, data []byte, client *http.Client) (string, error) {
	if key == "" || len(data) == 0 || len(data) > 20<<20 || http.DetectContentType(data) != "image/png" {
		return "", fmt.Errorf("NVIDIA assets require a selected API key and a normalized PNG of at most 20 MiB")
	}
	client = NativeFilesClient(client)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, nvidiaAssetsURL, bytes.NewBufferString(`{"contentType":"image/png","description":"screenshot"}`))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	var asset struct {
		ID          string `json:"assetId"`
		URL         string `json:"uploadUrl"`
		MIME        string `json:"contentType"`
		Description string `json:"description"`
	}
	if _, err := nativeFileResponse(ctx, client, req, &asset); err != nil {
		return "", err
	}
	u, err := url.Parse(asset.URL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Port() != "" || !signedNvidiaS3Host(u.Hostname()) || u.RawQuery == "" || !nvidiaAssetUUID.MatchString(asset.ID) || (asset.MIME != "" && asset.MIME != "image/png") || (asset.Description != "" && asset.Description != "screenshot") {
		return "", fmt.Errorf("NVCF returned an invalid asset reservation")
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodPut, asset.URL, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("invalid NVIDIA asset upload request")
	}
	req.Header.Set("Content-Type", "image/png")
	req.Header.Set("x-amz-meta-nvcf-asset-description", "screenshot")
	if _, err := nativeFileResponse(ctx, client, req, nil); err != nil {
		return "", err
	}
	return asset.ID, nil
}

func signedNvidiaS3Host(host string) bool {
	if !strings.HasSuffix(host, ".amazonaws.com") {
		return false
	}
	prefix := strings.TrimSuffix(host, ".amazonaws.com")
	// Permit documented virtual-hosted and path-style S3 regional endpoint forms only.
	parts := strings.Split(prefix, ".")
	for i, part := range parts {
		if part == "s3" {
			return i == len(parts)-1 || (i == len(parts)-2 && parts[i+1] != "")
		}
		if strings.HasPrefix(part, "s3-") {
			return i == len(parts)-1 && len(part) > 3
		}
	}
	return false
}

// PrepareNvidiaFilePayload projects uploaded file_id parts to native VILA asset references.
// The caller must apply payload rules next and derive headers from validated final references.
func PrepareNvidiaFilePayload(ctx context.Context, baseURL, model, key string, body []byte, client *http.Client) (context.Context, []byte, error) {
	if f := FileRequestFromContext(ctx); f == nil || !f.Local {
		return ctx, body, nil
	}
	next, body, err := PrepareFilePayload(ctx, body, func(uploadCtx context.Context, _ string, data []byte) (string, error) {
		if !NvidiaVilaFilesEndpoint(baseURL, model) {
			return "", fmt.Errorf("selected NVIDIA model and endpoint do not support native assets")
		}
		return UploadNvidiaAsset(uploadCtx, key, data, client)
	})
	if err != nil {
		return ctx, nil, err
	}
	if f := FileRequestFromContext(next); f != nil {
		f.NativeProvider = "nvidia"
	}
	for _, slot := range ImageSlots(body) {
		id := gjson.GetBytes(body, slot+".file_id").String()
		if !nvidiaAssetUUID.MatchString(id) {
			return ctx, nil, fmt.Errorf("invalid NVIDIA asset identifier")
		}
		part, err := json.Marshal(map[string]any{"type": "image_url", "image_url": map[string]string{"url": NvidiaAssetPrefix + id}})
		if err != nil {
			return ctx, nil, err
		}
		body, err = sjson.SetRawBytes(body, slot, part)
		if err != nil {
			return ctx, nil, err
		}
	}
	return next, body, nil
}

// NvidiaFileAssetHeader validates final image references and returns the exact header value.
// No removed image is restored and no inference/upload retry is performed by this adapter.
func NvidiaFileAssetHeader(ctx context.Context, body []byte) (string, error) {
	f := FileRequestFromContext(ctx)
	if f == nil || !f.Local {
		return "", nil
	}
	if f.NativeProvider != "nvidia" {
		return "", fmt.Errorf("NVIDIA file adapter was not selected")
	}
	if gjson.GetBytes(body, "model").String() != "nvidia/vila" {
		return "", fmt.Errorf("final NVIDIA file payload changed model")
	}
	var value any
	if json.Unmarshal(body, &value) != nil {
		return "", fmt.Errorf("invalid NVIDIA file payload")
	}
	ids := map[string]bool{}
	var walk func(any) error
	walk = func(value any) error {
		switch v := value.(type) {
		case map[string]any:
			for key, child := range v {
				switch key {
				case "image_url":
					ref, ok := child.(map[string]any)
					if !ok || len(ref) != 1 {
						return fmt.Errorf("invalid NVIDIA image reference")
					}
					uri, ok := ref["url"].(string)
					if !ok || !NvidiaAssetReference(uri, f.Allowed) {
						return fmt.Errorf("unowned NVIDIA image reference")
					}
					ids[strings.TrimPrefix(uri, NvidiaAssetPrefix)] = true
					continue
				case "file_id", "inlineData", "inline_data", "image_base64", "video_url":
					return fmt.Errorf("unsupported NVIDIA image encoding")
				}
				if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range v {
				if err := walk(child); err != nil {
					return err
				}
			}
		case string:
			lower := strings.ToLower(v)
			if strings.Contains(lower, "data:image/") || strings.Contains(lower, "<img") || strings.Contains(lower, "data:video/") || strings.Contains(v, PrototypeFilePrefix) {
				return fmt.Errorf("unsupported NVIDIA inline image")
			}
		}
		return nil
	}
	if err := walk(value); err != nil {
		return "", err
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	sort.Strings(list)
	header := strings.Join(list, ",")
	if len(header) > 370 {
		return "", fmt.Errorf("NVIDIA asset references exceed header limit")
	}
	return header, nil
}
