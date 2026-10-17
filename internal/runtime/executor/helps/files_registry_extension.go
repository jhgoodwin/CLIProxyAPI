package helps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// discoverHostedImageSlots discovers Responses input images and native Claude file image sources,
// including nested tool result content. Paths are reused for discovery and rewriting.
func discoverHostedImageSlots(body []byte) []string {
	var out []string
	var walk func(gjson.Result, string)
	walk = func(value gjson.Result, path string) {
		if value.IsArray() {
			for i, child := range value.Array() {
				childPath := fmt.Sprintf("%s.%d", path, i)
				if path == "" {
					childPath = fmt.Sprintf("%d", i)
				}
				walk(child, childPath)
			}
			return
		}
		if !value.IsObject() {
			return
		}
		switch value.Get("type").String() {
		case "input_image":
			out = append(out, path)
		case "file":
			if value.Get("file_id").Exists() {
				out = append(out, path)
			}
		case "image":
			if value.Get("source.type").String() == "file" {
				out = append(out, path+".source")
			}
		}
		value.ForEach(func(key, child gjson.Result) bool {
			// JSON object keys are escaped for sjson/gjson paths.
			escaped := strings.NewReplacer("\\", "\\\\", ".", "\\.", "*", "\\*", "?", "\\?").Replace(key.String())
			childPath := escaped
			if path != "" {
				childPath = path + "." + escaped
			}
			walk(child, childPath)
			return true
		})
	}
	walk(gjson.ParseBytes(body), "")
	return out
}

func (r *CodexFileRegistry) resolveHostedFileReferences(ctx context.Context, body []byte, principal string, ttl time.Duration) (context.Context, []byte, error) {
	ids, err := ImageFileIDs(body)
	if err != nil {
		return ctx, nil, err
	}
	if len(ids) == 0 {
		return ctx, body, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune(ttl)
	fReq := &FileRequest{Allowed: map[string]bool{}, Pending: map[string]PendingFile{}}
	replacements := map[string]string{}
	for _, id := range ids {
		if !strings.HasPrefix(id, PrototypeFilePrefix) {
			continue
		}
		f, ok := r.files[id]
		if principal == "" || !ok || f.Scope != FileCallerScope(principal) {
			return ctx, nil, fmt.Errorf("unknown or unauthorized file reference")
		}
		if !f.Ready {
			return ctx, nil, fmt.Errorf("file is not ready")
		}
		if f.VariantPath != "" {
			localID := id
			fReq.Pending[id] = PendingFile{Name: "screenshot.png", Read: func(readCtx context.Context) ([]byte, error) {
				if err := readCtx.Err(); err != nil {
					return nil, err
				}
				r.mu.Lock()
				defer r.mu.Unlock()
				r.prune(ttl)
				current, exists := r.files[localID]
				if !exists || current.Scope != FileCallerScope(principal) || !current.Ready {
					return nil, fmt.Errorf("unknown or unauthorized file reference")
				}
				data, err := os.ReadFile(current.VariantPath)
				if err != nil {
					return nil, fmt.Errorf("file content unavailable")
				}
				if len(data) == 0 || len(data) > 20*1024*1024 {
					return nil, fmt.Errorf("file content invalid")
				}
				if err := readCtx.Err(); err != nil {
					return nil, err
				}
				return data, nil
			}}
			fReq.Local = true
		} else {
			if fReq.Owner != "" && fReq.Owner != f.Owner {
				return ctx, nil, fmt.Errorf("all file references must have one owner")
			}
			fReq.Owner = f.Owner
			fReq.Allowed[f.UpstreamID] = true
			replacements[id] = f.UpstreamID
		}
	}
	if len(replacements) == 0 && !fReq.Local {
		return ctx, body, nil
	}
	for _, p := range ImageSlots(body) {
		id := gjson.GetBytes(body, p+".file_id").String()
		if value, ok := replacements[id]; ok {
			body, err = sjson.SetBytes(body, p+".file_id", value)
			if err != nil {
				return ctx, nil, err
			}
		}
	}
	return WithFileRequest(ctx, fReq), body, nil
}

func ContainsLocalFileID(body []byte) bool {
	var value any
	if json.Unmarshal(body, &value) != nil {
		return false
	}
	var walk func(any) bool
	walk = func(v any) bool {
		switch t := v.(type) {
		case map[string]any:
			for k, child := range t {
				if k == "file_id" {
					if s, ok := child.(string); ok && (strings.HasPrefix(s, PrototypeFilePrefix) || IsProxyLocalFileID(s)) {
						return true
					}
				}
				if walk(child) {
					return true
				}
			}
		case []any:
			for _, child := range t {
				if walk(child) {
					return true
				}
			}
		}
		return false
	}
	return walk(value)
}

func validateHostedFilePayload(ctx context.Context, body []byte) error {
	if ContainsLocalFileID(body) {
		return fmt.Errorf("final payload contains unresolved proxy file reference")
	}
	ids, err := ImageFileIDs(body)
	if err != nil {
		return err
	}
	f := FileRequestFromContext(ctx)
	if f != nil && f.Local {
		if err := validateHostedFilePrivacy(body, f.Allowed, f.NativeProvider); err != nil {
			return err
		}
	}
	for _, id := range ids {
		if strings.HasPrefix(id, PrototypeFilePrefix) || (f != nil && !f.Allowed[id]) {
			return fmt.Errorf("final payload contains unresolved or changed file reference")
		}
	}
	return nil
}
