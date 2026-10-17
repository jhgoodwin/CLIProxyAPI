package api

import (
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/anthropicfiles"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/files"
	log "github.com/sirupsen/logrus"
)

func (s *Server) setupSharedFiles() *files.Handler {
	handler := s.filesHandler
	if handler == nil {
		handler = files.NewHandler(nil, s.cfg)
		s.filesHandler = handler
	}
	if s.cfg == nil || (!s.cfg.Files.Enabled && !s.cfg.CodexFilesPrototype.Enabled) || s.filesStore != nil {
		handler.UpdateConfig(s.cfg, s.filesStore)
		return handler
	}
	handler.UpdateConfig(s.cfg, nil)
	cfg := s.cfg.Files
	root := cfg.StorageDir
	if root == "" {
		root = "files"
	}
	if !filepath.IsAbs(root) {
		root = filepath.Join(filepath.Dir(s.configFilePath), root)
	}
	maxFiles := cfg.MaxFiles
	if !cfg.Enabled && maxFiles <= 0 {
		_, maxFiles, _ = s.cfg.CodexFilesPrototype.Limits()
	}
	store, err := anthropicfiles.Open(root, maxFiles, cfg.MaxStorageBytes)
	if err != nil {
		log.Error("Shared Files storage unavailable: private persistent storage initialization failed")
		return handler
	}
	s.filesStore, s.handlers.SharedFiles = store, store
	handler.UpdateConfig(s.cfg, store)
	s.filesCleanupStop = make(chan struct{})
	interval := cfg.CleanupIntervalSeconds
	if interval <= 0 {
		interval = 300
	}
	stop := s.filesCleanupStop
	go func() {
		ticker := time.NewTicker(time.Duration(interval) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if store.Cleanup() != nil {
					log.Error("Shared Files cleanup failed")
				}
			}
		}
	}()
	return handler
}

// Native Anthropic credentials enter the same frontend authentication pipeline.
// Files authentication errors use the representation negotiated by Files headers.
func canonicalAPIAuthMiddleware(manager *sdkaccess.Manager) gin.HandlerFunc {
	ordinary := AuthMiddleware(manager)
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		isFiles := path == "/v1/files" || strings.HasPrefix(path, "/v1/files/")
		isMessages := path == "/v1/messages" || path == "/v1/messages/count_tokens"
		if (isFiles || isMessages) && c.GetHeader("Authorization") == "" && c.GetHeader("X-Api-Key") != "" {
			c.Request.Header.Set("Authorization", "Bearer "+c.GetHeader("X-Api-Key"))
		}
		if !isFiles {
			ordinary(c)
			return
		}
		if manager != nil {
			result, err := manager.Authenticate(c.Request.Context(), c.Request)
			if err != nil {
				files.AuthenticationError(c, err.HTTPStatusCode(), err.Message)
				return
			}
			if result != nil {
				c.Request = c.Request.WithContext(sdkaccess.WithAuthenticatedPrincipal(c.Request.Context(), result.Principal))
				c.Set("userApiKey", result.Principal)
				c.Set("accessProvider", result.Provider)
				if len(result.Metadata) > 0 {
					c.Set("accessMetadata", result.Metadata)
				}
			}
		}
		if strings.TrimSpace(sdkaccess.AuthenticatedPrincipal(c.Request.Context())) == "" {
			files.AuthenticationError(c, http.StatusUnauthorized, "authenticated principal required")
			return
		}
		c.Next()
	}
}
