// Package files serves OpenAI and Anthropic file formats from one durable store.
package files

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/anthropicfiles"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
)

type Handler struct {
	mu    sync.RWMutex
	Store *anthropicfiles.Store
	Cfg   *config.Config
}

func NewHandler(store *anthropicfiles.Store, cfg *config.Config) *Handler {
	return &Handler{Store: store, Cfg: cfg}
}
func anthropic(c *gin.Context) bool {
	if _, present := c.Request.Header[http.CanonicalHeaderKey("Anthropic-Version")]; present {
		return true
	}
	for _, beta := range c.Request.Header.Values("Anthropic-Beta") {
		for _, token := range strings.Split(beta, ",") {
			if strings.HasPrefix(strings.TrimSpace(token), "files-api") {
				return true
			}
		}
	}
	return false
}
func failure(c *gin.Context, status int, kind, message string) {
	detail := gin.H{"type": kind, "message": message}
	if anthropic(c) {
		c.AbortWithStatusJSON(status, gin.H{"type": "error", "error": detail})
	} else {
		c.AbortWithStatusJSON(status, gin.H{"error": detail})
	}
}
func fileError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, anthropicfiles.ErrUnauthorized):
		failure(c, 401, "authentication_error", "authenticated principal required")
	case errors.Is(err, anthropicfiles.ErrNotFound):
		failure(c, 404, "not_found_error", "file not found")
	case errors.Is(err, anthropicfiles.ErrInvalid):
		failure(c, 400, "invalid_request_error", "invalid file request")
	case errors.Is(err, anthropicfiles.ErrQuota):
		failure(c, 429, "rate_limit_error", "file storage quota exceeded")
	default:
		failure(c, 500, "api_error", "file storage operation failed")
	}
}
func (h *Handler) principal(c *gin.Context) (string, bool) {
	c.Header("Cache-Control", "no-store")
	p := access.AuthenticatedPrincipal(c.Request.Context())
	if strings.TrimSpace(p) == "" {
		failure(c, 401, "authentication_error", "authenticated principal required")
		return "", false
	}
	if h == nil || h.Cfg == nil || (!h.Cfg.Files.Enabled && !h.Cfg.CodexFilesPrototype.Enabled) {
		failure(c, 404, "not_found_error", "files are disabled")
		return "", false
	}
	if h.Store == nil {
		failure(c, 503, "api_error", "file storage unavailable")
		return "", false
	}
	return p, true
}
func (h *Handler) limits() (int64, time.Duration) {
	if h.Cfg.CodexFilesPrototype.Enabled {
		max, _, ttl := h.Cfg.CodexFilesPrototype.Limits()
		return max, time.Duration(ttl) * time.Second
	}
	return anthropicfiles.MaxUploadBytes, anthropicfiles.DefaultTTL
}
func fileObject(c *gin.Context, m anthropicfiles.Metadata) gin.H {
	if anthropic(c) {
		return gin.H{"id": m.ID, "type": m.Type, "filename": m.Filename, "mime_type": m.MIMEType, "size_bytes": m.SizeBytes, "created_at": m.CreatedAt, "expires_at": m.ExpiresAt, "downloadable": m.Downloadable}
	}
	return gin.H{"id": m.ID, "object": "file", "bytes": m.SizeBytes, "created_at": m.CreatedAt.Unix(), "filename": m.Filename, "purpose": m.Purpose, "status": "processed", "status_details": nil, "expires_at": m.ExpiresAt.Unix()}
}

// Upload accepts one image plus purpose and optional expires_after fields.
// Anthropic requests may omit purpose; the durable default is user_data.
func (h *Handler) Upload(c *gin.Context) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	p, ok := h.principal(c)
	if !ok {
		return
	}
	max, ttl := h.limits()
	maxTTL := anthropicfiles.MaxTTL
	if h.Cfg.CodexFilesPrototype.Enabled {
		maxTTL = ttl
	}
	invalid := func(message string) { failure(c, 400, "invalid_request_error", message) }
	oversized := func() { failure(c, 413, "request_too_large", "upload exceeds configured byte limit") }
	if c.Request.ContentLength > max+65536 {
		oversized()
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, max+65536)
	reader, err := c.Request.MultipartReader()
	if err != nil {
		invalid("expected multipart form data")
		return
	}
	fields := map[string]string{}
	seen := map[string]bool{}
	var data []byte
	filename, declaredMIME := "", ""
	for {
		part, e := reader.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			var large *http.MaxBytesError
			if errors.As(e, &large) {
				oversized()
			} else {
				invalid("malformed multipart upload")
			}
			return
		}
		key := part.FormName()
		disposition, params, e := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if e != nil || disposition != "form-data" || seen[key] {
			_ = part.Close()
			invalid("invalid or duplicate multipart field")
			return
		}
		seen[key] = true
		if key == "file" {
			filename = params["filename"]
			if filename == "" {
				_ = part.Close()
				invalid("file requires a filename")
				return
			}
			if raw := part.Header.Get("Content-Type"); raw != "" {
				declaredMIME, _, e = mime.ParseMediaType(raw)
				if e != nil {
					_ = part.Close()
					invalid("invalid file MIME type")
					return
				}
			}
			data, e = io.ReadAll(io.LimitReader(part, max+1))
			if e != nil || int64(len(data)) > max {
				_ = part.Close()
				var large *http.MaxBytesError
				if int64(len(data)) > max || errors.As(e, &large) {
					oversized()
				} else {
					invalid("malformed file body")
				}
				return
			}
		} else if key == "purpose" || key == "expires_after[anchor]" || key == "expires_after[seconds]" {
			if _, exists := params["filename"]; exists {
				_ = part.Close()
				invalid("multipart fields must be text")
				return
			}
			raw, e := io.ReadAll(io.LimitReader(part, 129))
			if e != nil || len(raw) > 128 {
				_ = part.Close()
				invalid("invalid multipart field")
				return
			}
			fields[key] = string(raw)
		} else {
			_ = part.Close()
			invalid("unknown multipart field")
			return
		}
		if e = part.Close(); e != nil {
			invalid("malformed multipart part")
			return
		}
	}
	purpose := fields["purpose"]
	if !seen["purpose"] && anthropic(c) {
		purpose = "user_data"
	}
	if !seen["file"] || (purpose != "vision" && purpose != "user_data") {
		invalid("one file and purpose=vision or user_data are required")
		return
	}
	if seen["expires_after[anchor]"] || seen["expires_after[seconds]"] {
		seconds, e := strconv.ParseInt(fields["expires_after[seconds]"], 10, 64)
		if fields["expires_after[anchor]"] != "created_at" || e != nil || seconds < int64(anthropicfiles.MinTTL/time.Second) || seconds > int64(maxTTL/time.Second) {
			invalid("expiry requires created_at anchor and seconds within configured TTL bounds")
			return
		}
		ttl = time.Duration(seconds) * time.Second
	}
	actual, err := anthropicfiles.ValidateImage(data)
	if err != nil {
		fileError(c, err)
		return
	}
	// Multipart SDKs may use a generic MIME type; detect the image from its bytes.
	if declaredMIME != "" && declaredMIME != "application/octet-stream" && declaredMIME != actual {
		invalid("MIME does not match image")
		return
	}
	m, err := h.Store.PutWithPurpose(p, filename, actual, purpose, data, ttl)
	if err != nil {
		fileError(c, err)
		return
	}
	c.JSON(200, fileObject(c, m))
}
func (h *Handler) Get(c *gin.Context) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	p, ok := h.principal(c)
	if !ok {
		return
	}
	m, err := h.Store.Get(p, c.Param("file_id"))
	if err != nil {
		fileError(c, err)
		return
	}
	c.JSON(200, fileObject(c, m))
}
func (h *Handler) Content(c *gin.Context) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	p, ok := h.principal(c)
	if !ok {
		return
	}
	m, data, err := h.Store.Content(p, c.Param("file_id"))
	if err != nil {
		fileError(c, err)
		return
	}
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": m.Filename}))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(200, m.MIMEType, data)
}
func (h *Handler) Delete(c *gin.Context) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	p, ok := h.principal(c)
	if !ok {
		return
	}
	id := c.Param("file_id")
	if err := h.Store.Delete(p, id); err != nil {
		fileError(c, err)
		return
	}
	if anthropic(c) {
		c.JSON(200, gin.H{"id": id, "type": "file_deleted"})
	} else {
		c.JSON(200, gin.H{"id": id, "object": "file", "deleted": true})
	}
}
func (h *Handler) List(c *gin.Context) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	p, ok := h.principal(c)
	if !ok {
		return
	}
	isAnthropic := anthropic(c)
	allowed := map[string]bool{"limit": true, "order": true, "after": true, "purpose": true}
	limit, maxLimit := 100, 10000
	if isAnthropic {
		allowed = map[string]bool{"limit": true, "after_id": true, "before_id": true}
		limit, maxLimit = 20, 1000
	}
	query := c.Request.URL.Query()
	for key, values := range query {
		if !allowed[key] || len(values) != 1 {
			failure(c, 400, "invalid_request_error", "unsupported or duplicate pagination parameter")
			return
		}
	}
	if raw, exists := query["limit"]; exists {
		n, err := strconv.Atoi(raw[0])
		if err != nil || n < 1 || n > maxLimit {
			failure(c, 400, "invalid_request_error", "limit outside supported range")
			return
		}
		limit = n
	}
	if isAnthropic {
		page, err := h.Store.List(p, anthropicfiles.ListOptions{Limit: limit, AfterID: c.Query("after_id"), BeforeID: c.Query("before_id")})
		if err != nil {
			fileError(c, err)
			return
		}
		data := []gin.H{}
		for _, m := range page.Data {
			data = append(data, fileObject(c, m))
		}
		result := gin.H{"data": data, "has_more": page.HasMore}
		if page.FirstID != "" {
			result["first_id"] = page.FirstID
			result["last_id"] = page.LastID
		}
		c.JSON(200, result)
		return
	}
	order := c.DefaultQuery("order", "desc")
	if order != "asc" && order != "desc" {
		failure(c, 400, "invalid_request_error", "order must be asc or desc")
		return
	}
	all := []anthropicfiles.Metadata{}
	cursor := ""
	for {
		page, err := h.Store.List(p, anthropicfiles.ListOptions{Limit: 1000, AfterID: cursor})
		if err != nil {
			fileError(c, err)
			return
		}
		all = append(all, page.Data...)
		if !page.HasMore {
			break
		}
		cursor = page.LastID
	}
	sort.Slice(all, func(i, j int) bool {
		less := all[i].CreatedAt.Before(all[j].CreatedAt)
		if all[i].CreatedAt.Equal(all[j].CreatedAt) {
			less = all[i].ID < all[j].ID
		}
		if order == "asc" {
			return less
		}
		return !less
	})
	after, purpose := c.Query("after"), c.Query("purpose")
	found := after == ""
	data := []gin.H{}
	more := false
	for _, m := range all {
		if !found {
			if m.ID == after {
				found = true
			}
			continue
		}
		if purpose != "" && purpose != m.Purpose {
			continue
		}
		if len(data) == limit {
			more = true
			break
		}
		data = append(data, fileObject(c, m))
	}
	var first, last any
	if len(data) > 0 {
		first = data[0]["id"]
		last = data[len(data)-1]["id"]
	}
	c.JSON(200, gin.H{"object": "list", "data": data, "has_more": more, "first_id": first, "last_id": last})
}

// UpdateConfig publishes an immutable config snapshot with its current store.
func (h *Handler) UpdateConfig(cfg *config.Config, store *anthropicfiles.Store) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Cfg, h.Store = cfg, store
}
