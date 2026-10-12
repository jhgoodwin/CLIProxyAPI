package openai

import (
	"bytes"
	"image"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
)

func fileFailure(c *gin.Context, status int, message string) {
	c.JSON(status, handlers.ErrorResponse{Error: handlers.ErrorDetail{Message: message, Type: "invalid_request_error"}})
}
func (h *OpenAIResponsesAPIHandler) fileCaller(c *gin.Context) (string, bool) {
	if h.Cfg == nil || !h.Cfg.CodexFilesPrototype.Enabled {
		fileFailure(c, 404, "files prototype is disabled")
		return "", false
	}
	p := access.AuthenticatedPrincipal(c.Request.Context())
	if p == "" {
		fileFailure(c, 401, "files require authenticated caller")
		return "", false
	}
	return p, true
}
func fileObject(f helps.CodexFile) gin.H {
	return gin.H{"id": f.ID, "object": "file", "bytes": f.Bytes, "created_at": f.Created.Unix(), "filename": f.Name, "purpose": f.Purpose, "status": "processed", "status_details": nil, "expires_at": f.Expires.Unix()}
}
func (h *OpenAIResponsesAPIHandler) FilesUpload(c *gin.Context) {
	principal, ok := h.fileCaller(c)
	if !ok {
		return
	}
	max, count, ttl := h.Cfg.CodexFilesPrototype.Limits()
	if c.Request.ContentLength > max+65536 {
		fileFailure(c, 413, "multipart request exceeds file limit")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, max+65536)
	reader, err := c.Request.MultipartReader()
	if err != nil {
		fileFailure(c, 400, "expected multipart file and screenshot purpose")
		return
	}
	var data []byte
	name, purpose := "", ""
	seen := map[string]bool{}
	anchor, seconds := "", ""
	for {
		part, e := reader.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			fileFailure(c, 400, "invalid or oversized multipart request")
			return
		}
		key := part.FormName()
		if seen[key] {
			_ = part.Close()
			fileFailure(c, 400, "duplicate multipart field")
			return
		}
		seen[key] = true
		switch key {
		case "file":
			if part.FileName() == "" {
				_ = part.Close()
				fileFailure(c, 400, "exactly one file is required")
				return
			}
			name = filepath.Base(part.FileName())
			data, err = io.ReadAll(io.LimitReader(part, max+1))
			if err != nil || int64(len(data)) > max {
				_ = part.Close()
				fileFailure(c, 413, "file exceeds configured byte limit")
				return
			}
		case "purpose", "expires_after[anchor]", "expires_after[seconds]":
			raw, e := io.ReadAll(io.LimitReader(part, 128))
			if e != nil || len(raw) >= 128 {
				_ = part.Close()
				fileFailure(c, 400, "invalid multipart field")
				return
			}
			switch key {
			case "purpose":
				purpose = string(raw)
			case "expires_after[anchor]":
				anchor = string(raw)
			case "expires_after[seconds]":
				seconds = string(raw)
			}
		default:
			_ = part.Close()
			fileFailure(c, 400, "only file, purpose and expires_after are supported")
			return
		}
		if part.Close() != nil {
			fileFailure(c, 400, "invalid multipart part")
			return
		}
	}
	if !seen["file"] || (purpose != "vision" && purpose != "user_data") {
		fileFailure(c, 400, "one screenshot file and purpose=vision or user_data are required")
		return
	}
	if seen["expires_after[anchor]"] || seen["expires_after[seconds]"] {
		n, e := strconv.Atoi(seconds)
		if anchor != "created_at" || e != nil || n < 3600 || n > ttl {
			fileFailure(c, 400, "expires_after requires created_at anchor and seconds between 3600 and configured TTL")
			return
		}
		ttl = n
	}
	_, format, e := image.DecodeConfig(bytes.NewReader(data))
	if e != nil {
		fileFailure(c, 415, "only valid PNG, JPEG and WebP screenshots are supported")
		return
	}
	mime := map[string]string{"png": "image/png", "jpeg": "image/jpeg", "webp": "image/webp"}[format]
	entry, e := h.Files.Store(helps.CodexFile{Name: name, Purpose: purpose, Scope: helps.FileCallerScope(principal), MIME: mime}, data, count, time.Duration(ttl)*time.Second)
	if e != nil {
		status := 415
		if e.Error() == "file registry is full; wait for local expiry" {
			status = 429
		}
		if e.Error() == "attachment storage unavailable" {
			status = 500
		}
		fileFailure(c, status, e.Error())
		return
	}
	c.Header("X-CPA-Files-Stage", "ready")
	c.Header("X-CPA-Image-Bytes", strconv.Itoa(len(data)))
	c.JSON(200, fileObject(entry))
}
func (h *OpenAIResponsesAPIHandler) FilesRetrieve(c *gin.Context) {
	p, ok := h.fileCaller(c)
	if !ok {
		return
	}
	_, _, ttl := h.Cfg.CodexFilesPrototype.Limits()
	f, e := h.Files.Get(c.Param("file_id"), p, time.Duration(ttl)*time.Second)
	if e != nil {
		fileFailure(c, 404, "file not found")
		return
	}
	c.JSON(200, fileObject(f))
}
func (h *OpenAIResponsesAPIHandler) FilesContent(c *gin.Context) {
	p, ok := h.fileCaller(c)
	if !ok {
		return
	}
	_, _, ttl := h.Cfg.CodexFilesPrototype.Limits()
	f, data, e := h.Files.Content(c.Param("file_id"), p, time.Duration(ttl)*time.Second)
	if e != nil {
		fileFailure(c, 404, "file not found")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Disposition", "attachment")
	c.Data(200, f.MIME, data)
}
func (h *OpenAIResponsesAPIHandler) FilesDelete(c *gin.Context) {
	p, ok := h.fileCaller(c)
	if !ok {
		return
	}
	_, _, ttl := h.Cfg.CodexFilesPrototype.Limits()
	id := c.Param("file_id")
	if h.Files.Delete(id, p, time.Duration(ttl)*time.Second) != nil {
		fileFailure(c, 404, "file not found")
		return
	}
	c.JSON(200, gin.H{"id": id, "object": "file", "deleted": true})
}
func (h *OpenAIResponsesAPIHandler) FilesList(c *gin.Context) {
	p, ok := h.fileCaller(c)
	if !ok {
		return
	}
	_, _, ttl := h.Cfg.CodexFilesPrototype.Limits()
	limit := 100
	if raw := c.Query("limit"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 10000 {
			fileFailure(c, 400, "limit must be between 1 and 10000")
			return
		}
		limit = n
	}
	order := c.DefaultQuery("order", "desc")
	if order != "asc" && order != "desc" {
		fileFailure(c, 400, "order must be asc or desc")
		return
	}
	files := h.Files.List(p, time.Duration(ttl)*time.Second)
	if order == "desc" {
		for i, j := 0, len(files)-1; i < j; i, j = i+1, j-1 {
			files[i], files[j] = files[j], files[i]
		}
	}
	data := []gin.H{}
	after := c.Query("after")
	found := after == ""
	more := false
	for _, f := range files {
		if !found {
			if f.ID == after {
				found = true
			}
			continue
		}
		if purpose := c.Query("purpose"); purpose != "" && purpose != f.Purpose {
			continue
		}
		if len(data) == limit {
			more = true
			break
		}
		data = append(data, fileObject(f))
	}
	var first, last any
	if len(data) > 0 {
		first = data[0]["id"]
		last = data[len(data)-1]["id"]
	}
	c.JSON(200, gin.H{"object": "list", "data": data, "has_more": more, "first_id": first, "last_id": last})
}
