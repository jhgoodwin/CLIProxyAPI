package handlers

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/anthropicfiles"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// SharedFileStore reads original images with principal ownership, deletion and expiry checks.
// Content must repeat those checks on every read, including execution retries.
type SharedFileStore interface {
	Get(principal, id string) (anthropicfiles.Metadata, error)
	Content(principal, id string) (anthropicfiles.Metadata, []byte, error)
}

type sharedFilesContextKey struct{}

func sharedFilesContext(ctx context.Context) bool {
	marked, _ := ctx.Value(sharedFilesContextKey{}).(bool)
	return marked
}

// prepareSharedFileReferences handles canonical proxy IDs only. Ordinary requests retain
// their original bytes, including opaque upstream IDs and signed thinking blocks.
func (h *BaseAPIHandler) prepareSharedFileReferences(ctx context.Context, protocol string, body []byte) (context.Context, []byte, *interfaces.ErrorMessage, bool) {
	refs := sharedLocalReferences(gjson.ParseBytes(body))
	if len(refs) == 0 {
		return ctx, body, nil, false
	}
	ctx = helps.WithFilePrivacy(ctx)
	fail := func(status int, message string) (context.Context, []byte, *interfaces.ErrorMessage, bool) {
		return ctx, nil, &interfaces.ErrorMessage{StatusCode: status, Error: fmt.Errorf("%s", message)}, true
	}
	if !gjson.ValidBytes(body) {
		return fail(http.StatusBadRequest, "invalid JSON")
	}
	if h.Cfg == nil || (!h.Cfg.Files.Enabled && !h.Cfg.CodexFilesPrototype.Enabled) {
		return fail(http.StatusBadRequest, "files are disabled")
	}
	if h.SharedFiles == nil {
		return fail(http.StatusServiceUnavailable, "file store unavailable")
	}
	principal := access.AuthenticatedPrincipal(ctx)
	if strings.TrimSpace(principal) == "" {
		return fail(http.StatusUnauthorized, "authenticated principal required")
	}
	slots := map[string]string{}
	switch protocol {
	case "claude", "openai":
		for i, message := range gjson.GetBytes(body, "messages").Array() {
			sharedPortableSlots(message.Get("content"), fmt.Sprintf("messages.%d.content", i), protocol, slots)
		}
	case "openai-response":
		for i, item := range gjson.GetBytes(body, "input").Array() {
			path := fmt.Sprintf("input.%d", i)
			switch item.Get("type").String() {
			case "", "message":
				sharedPortableSlots(item.Get("content"), path+".content", protocol, slots)
			case "function_call_output":
				sharedPortableSlots(item.Get("output"), path+".output", protocol, slots)
			}
		}
	default:
		return fail(http.StatusBadRequest, "proxy files require a supported image position")
	}
	for path := range refs {
		if _, ok := slots[path]; !ok {
			return fail(http.StatusBadRequest, "proxy file reference is not in a supported image position")
		}
	}
	native := &helps.FileRequest{Local: true, Pending: map[string]helps.PendingFile{}, Allowed: map[string]bool{}}
	if protocol == "openai-response" {
		ids, err := helps.ImageFileIDs(body)
		if err != nil {
			return fail(http.StatusBadRequest, err.Error())
		}
		for _, id := range ids {
			if strings.HasPrefix(id, helps.PrototypeFilePrefix) {
				return fail(http.StatusBadRequest, "unresolved proxy file reference")
			}
			if !helps.IsProxyLocalFileID(id) {
				native.Allowed[id] = true
			}
		}
	}
	for path, id := range refs {
		partPath := slots[path]
		part := gjson.GetBytes(body, partPath)
		switch protocol {
		case "claude":
			source := part.Get("source")
			if source.Get("type").String() != "file" || len(source.Map()) != 2 || part.Get("image_url").Exists() {
				return fail(http.StatusBadRequest, "invalid file image source")
			}
		case "openai":
			if len(part.Get("file").Map()) != 1 || part.Get("image_url").Exists() || part.Get("file_data").Exists() {
				return fail(http.StatusBadRequest, "invalid file image")
			}
		}
		if _, err := h.SharedFiles.Get(principal, id); err != nil {
			return fail(http.StatusBadRequest, "unknown or unauthorized file reference")
		}
		if protocol == "openai-response" {
			store := h.SharedFiles
			native.Pending[id] = helps.PendingFile{Name: "screenshot.png", Read: func(readCtx context.Context) ([]byte, error) {
				if err := readCtx.Err(); err != nil {
					return nil, err
				}
				_, original, err := store.Content(principal, id)
				if err != nil {
					return nil, fmt.Errorf("unknown or unauthorized file reference")
				}
				png, _, err := helps.NormalizeScreenshot(original)
				if err != nil {
					return nil, fmt.Errorf("invalid file image")
				}
				if err := readCtx.Err(); err != nil {
					return nil, err
				}
				return png, nil
			}}
			continue
		}
		metadata, original, err := h.SharedFiles.Content(principal, id)
		if err != nil {
			return fail(http.StatusBadRequest, "unknown or unauthorized file reference")
		}
		mime, err := anthropicfiles.ValidateImage(original, metadata.MIMEType)
		if err != nil {
			return fail(http.StatusBadRequest, "invalid file image")
		}
		encoded := base64.StdEncoding.EncodeToString(original)
		if protocol == "claude" {
			body, err = sjson.SetBytes(body, partPath+".source", map[string]string{"type": "base64", "media_type": mime, "data": encoded})
		} else {
			body, err = sjson.DeleteBytes(body, partPath+".file")
			if err == nil {
				body, err = sjson.SetBytes(body, partPath+".type", "image_url")
			}
			if err == nil {
				body, err = sjson.SetBytes(body, partPath+".image_url", map[string]string{"url": "data:" + mime + ";base64," + encoded})
			}
		}
		if err != nil {
			return fail(http.StatusBadRequest, "invalid file image")
		}
	}
	// Native IDs remain only in authorized image slots until attempt-local upload.
	for path, id := range sharedLocalReferences(gjson.ParseBytes(body)) {
		if protocol != "openai-response" || slots[path] == "" || native.Pending[id].Read == nil {
			return fail(http.StatusBadRequest, "unresolved proxy file reference")
		}
	}
	if protocol != "openai-response" && helps.ContainsLocalFileID(body) {
		return fail(http.StatusBadRequest, "unresolved proxy file reference")
	}
	ctx = context.WithValue(ctx, sharedFilesContextKey{}, true)
	if protocol == "openai-response" {
		ctx = helps.WithFileRequest(ctx, native)
	}
	return ctx, body, nil, true
}

func sharedPortableSlots(content gjson.Result, path, protocol string, slots map[string]string) {
	if !content.IsArray() {
		return
	}
	for i, part := range content.Array() {
		partPath := fmt.Sprintf("%s.%d", path, i)
		if protocol == "claude" {
			switch part.Get("type").String() {
			case "image":
				slots[partPath+".source.file_id"] = partPath
			case "tool_result":
				sharedPortableSlots(part.Get("content"), partPath+".content", protocol, slots)
			}
		} else if protocol == "openai" && part.Get("type").String() == "file" {
			slots[partPath+".file.file_id"] = partPath
		} else if protocol == "openai-response" && part.Get("type").String() == "input_image" {
			slots[partPath+".file_id"] = partPath
		}
	}
}

// sharedLocalReferences inspects file_id fields rather than text or thinking content.
func sharedLocalReferences(value gjson.Result) map[string]string {
	refs := map[string]string{}
	var walk func(gjson.Result, string, bool)
	walk = func(value gjson.Result, path string, fileField bool) {
		if fileField && value.Type == gjson.String && helps.IsProxyLocalFileID(value.String()) {
			refs[path] = value.String()
		}
		if value.IsArray() {
			for i, child := range value.Array() {
				walk(child, fmt.Sprintf("%s.%d", path, i), fileField)
			}
		} else if value.IsObject() {
			value.ForEach(func(key, child gjson.Result) bool {
				escaped := strings.NewReplacer("\\", "\\\\", ".", "\\.", "*", "\\*", "?", "\\?").Replace(key.String())
				childPath := escaped
				if path != "" {
					childPath = path + "." + escaped
				}
				walk(child, childPath, fileField || key.String() == "file_id")
				return true
			})
		}
	}
	walk(value, "", false)
	return refs
}
