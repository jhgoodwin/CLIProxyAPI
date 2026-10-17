package helps

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// PendingFile reads a principal-authorized normalized screenshot at execution time.
// The reader rechecks local deletion, expiry and readiness; names contain no user metadata.
type PendingFile struct {
	Name string
	Read func(context.Context) ([]byte, error)
}

// PrepareFilePayload uploads referenced screenshots with the caller's selected credential
// before final payload rules. IDs and deduplication are confined to one execution attempt.
// Failed uploads never return a body usable for inference. No inline fallback is supported.
func PrepareFilePayload(ctx context.Context, body []byte, upload func(context.Context, string, []byte) (string, error)) (context.Context, []byte, error) {
	f := FileRequestFromContext(ctx)
	if f == nil || !f.Local {
		return ctx, body, nil
	}
	ids, err := ImageFileIDs(body)
	if err != nil {
		return ctx, nil, err
	}
	// Pending contains the unique local IDs authorized from the original request.
	// Check every ID before any upload; final payload rules may remove them later.
	present := make(map[string]bool, len(ids))
	for _, id := range ids {
		present[id] = true
	}
	for id := range f.Pending {
		if !present[id] {
			return ctx, nil, fmt.Errorf("request translation dropped a screenshot reference")
		}
	}
	attempt := &FileRequest{Owner: f.Owner, Local: true, Allowed: map[string]bool{}}
	for id, allowed := range f.Allowed {
		attempt.Allowed[id] = allowed
	}
	replacements := map[string]string{}
	for _, id := range ids {
		if !strings.HasPrefix(id, PrototypeFilePrefix) && !IsProxyLocalFileID(id) {
			if !attempt.Allowed[id] {
				return ctx, nil, fmt.Errorf("unowned upstream file reference")
			}
			continue
		}
		if _, exists := replacements[id]; exists {
			continue
		}
		pending, exists := f.Pending[id]
		if !exists || pending.Read == nil || upload == nil {
			return ctx, nil, fmt.Errorf("file upload unavailable")
		}
		if err := ctx.Err(); err != nil {
			return ctx, nil, err
		}
		data, err := pending.Read(ctx)
		if err != nil {
			return ctx, nil, err
		}
		upstream, err := upload(ctx, pending.Name, data)
		if err != nil {
			return ctx, nil, err
		}
		if err := ctx.Err(); err != nil {
			return ctx, nil, err
		}
		if strings.TrimSpace(upstream) == "" || strings.ContainsAny(upstream, "/?#\r\n") || strings.HasPrefix(upstream, PrototypeFilePrefix) || IsProxyLocalFileID(upstream) {
			return ctx, nil, fmt.Errorf("file upload returned invalid reference")
		}
		replacements[id] = upstream
		attempt.Allowed[upstream] = true
	}
	for _, slot := range ImageSlots(body) {
		for localID, upstream := range replacements {
			// Compare the current slot, not unrelated text occurrences.
			if gjson.GetBytes(body, slot+".file_id").String() == localID {
				body, err = sjson.SetBytes(body, slot+".file_id", upstream)
				if err != nil {
					return ctx, nil, err
				}
				break
			}
		}
	}
	return WithFileRequest(ctx, attempt), body, nil
}

// ValidateHostedFilePrivacy is read-only and rejects alternate image encodings and
// unowned references anywhere in a final hosted-file request, including payload overrides.
func ValidateHostedFilePrivacy(body []byte, allowed map[string]bool) error {
	return validateHostedFilePrivacy(body, allowed, "")
}

func validateHostedFilePrivacy(body []byte, allowed map[string]bool, nativeProvider string) error {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return fmt.Errorf("invalid final file payload")
	}
	var walk func(any) error
	walk = func(value any) error {
		switch value := value.(type) {
		case map[string]any:
			for key, child := range value {
				switch key {
				case "file_id":
					id, ok := child.(string)
					if !ok || !allowed[id] {
						return fmt.Errorf("final payload contains unowned file reference")
					}
				case "image_url":
					ref, ok := child.(map[string]any)
					if nativeProvider == "nvidia" && ok && len(ref) == 1 {
						uri, ok := ref["url"].(string)
						if ok && NvidiaAssetReference(uri, allowed) {
							continue
						}
					}
					return fmt.Errorf("inline or URL images are forbidden in hosted-file requests")
				case "fileData":
					ref, ok := child.(map[string]any)
					if nativeProvider != "gemini" || !ok || len(ref) != 2 || ref["mimeType"] != "image/png" {
						return fmt.Errorf("invalid hosted file data")
					}
					uri, ok := ref["fileUri"].(string)
					if !ok || !allowed[uri] {
						return fmt.Errorf("unowned hosted file URI")
					}
					continue
				case "file_data", "fileUri", "file_uri", "image_base64", "inline_data", "inlineData":
					return fmt.Errorf("inline or URL images are forbidden in hosted-file requests")
				case "source":
					if source, ok := child.(map[string]any); ok && (source["type"] == "base64" || source["type"] == "url") {
						return fmt.Errorf("inline or URL images are forbidden in hosted-file requests")
					}
				}
				if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range value {
				if err := walk(child); err != nil {
					return err
				}
			}
		case string:
			if strings.Contains(strings.ToLower(value), "data:image/") {
				return fmt.Errorf("inline images are forbidden in hosted-file requests")
			}
		}
		return nil
	}
	return walk(value)
}
