package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

type sharedFilesAccessFixture struct{}

func (sharedFilesAccessFixture) Identifier() string { return "shared-files-fixture" }
func (sharedFilesAccessFixture) Authenticate(_ context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if r.Header.Get("Authorization") != "Bearer frontend-secret" {
		return nil, sdkaccess.NewInvalidCredentialError()
	}
	return &sdkaccess.Result{Principal: "owner", Provider: "fixture"}, nil
}
func sharedFilesServer(t *testing.T, cfg *config.Config, manager *coreauth.Manager) *Server {
	t.Helper()
	accessManager := sdkaccess.NewManager()
	accessManager.SetProviders([]sdkaccess.Provider{sharedFilesAccessFixture{}})
	s := &Server{engine: gin.New(), cfg: cfg, handlers: handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager), accessManager: accessManager}
	s.setupRoutes()
	t.Cleanup(func() {
		if err := s.Stop(t.Context()); err != nil {
			t.Error(err)
		}
	})
	return s
}
func sharedFilesRequest(s *Server, method, path, body string, native bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if native {
		req.Header.Set("X-Api-Key", "frontend-secret")
		req.Header.Set("Anthropic-Version", "arbitrary-version")
		req.Header.Set("Anthropic-Beta", "files-api-2099-01-01, unrecognized-beta")
	} else {
		req.Header.Set("Authorization", "Bearer frontend-secret")
	}
	w := httptest.NewRecorder()
	s.engine.ServeHTTP(w, req)
	return w
}
func TestSharedFilesCanonicalRoutesAndNativeAuthentication(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{Files: config.FilesConfig{Enabled: true, StorageDir: t.TempDir()}}}
	s := sharedFilesServer(t, cfg, nil)
	for _, native := range []bool{false, true} {
		w := sharedFilesRequest(s, "GET", "/v1/files", "", native)
		if w.Code != 200 || !gjson.GetBytes(w.Body.Bytes(), "data").IsArray() {
			t.Fatalf("native=%v: %d %s", native, w.Code, w.Body.String())
		}
	}
	for _, route := range s.engine.Routes() {
		if strings.HasPrefix(route.Path, "/anthropic/v1") {
			t.Fatalf("prefixed route remains: %s", route.Path)
		}
	}
	for _, path := range []string{"/anthropic/v1/files", "/anthropic/v1/messages"} {
		if w := sharedFilesRequest(s, "POST", path, "{}", true); w.Code != 404 {
			t.Fatalf("alias %s: %d", path, w.Code)
		}
	}
	if w := sharedFilesRequest(s, "GET", "/anthropic/callback", "", false); w.Code != 200 {
		t.Fatalf("OAuth callback changed: %d", w.Code)
	}
	req := httptest.NewRequest("GET", "/v1/files", nil)
	req.Header.Set("Anthropic-Version", "arbitrary-version")
	req.Header.Set("X-Api-Key", "wrong")
	w := httptest.NewRecorder()
	s.engine.ServeHTTP(w, req)
	if w.Code != 401 || gjson.GetBytes(w.Body.Bytes(), "type").String() != "error" || gjson.GetBytes(w.Body.Bytes(), "error.type").String() != "authentication_error" {
		t.Fatalf("auth representation: %d %s", w.Code, w.Body.String())
	}
}
func TestSharedFilesOrdinaryMessagesStreamAndNonstream(t *testing.T) {
	captured := make(chan []byte, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("upstream route: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer upstream-secret" {
			t.Error("selected upstream credential missing")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		captured <- body
		if gjson.GetBytes(body, "stream").Bool() {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"id\":\"shared-stream\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"green\"}}]}\n\n")
			fmt.Fprint(w, "data: {\"id\":\"shared-stream\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
		} else {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"shared-nonstream","object":"chat.completion","model":"ordinary-shared-files","choices":[{"index":0,"message":{"role":"assistant","content":"green"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
		}
	}))
	defer upstream.Close()
	model, provider := "ordinary-shared-files", "openai-compatible-shared-files-test"
	cfg := &config.Config{SDKConfig: config.SDKConfig{Files: config.FilesConfig{Enabled: true, StorageDir: t.TempDir()}}}
	cfg.OpenAICompatibility = []config.OpenAICompatibility{{Name: "shared-files-test", BaseURL: upstream.URL + "/v1", Models: []config.OpenAICompatibilityModel{{Name: model, Alias: model, IsCompat: true}}}}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(runtimeexecutor.NewOpenAICompatExecutor(provider, cfg))
	auth := &coreauth.Auth{ID: "shared-files-api-test", Provider: provider, Status: coreauth.StatusActive, Attributes: map[string]string{"base_url": upstream.URL + "/v1", "api_key": "upstream-secret", "compat_name": "shared-files-test", "provider_key": provider}}
	if _, err := manager.Register(t.Context(), auth); err != nil {
		t.Fatal(err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model, IsCompat: true, UserDefined: true}})
	defer reg.UnregisterClient(auth.ID)
	s := sharedFilesServer(t, cfg, manager)
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	meta, err := s.filesStore.Put("owner", "portable.png", "image/png", imageBytes.Bytes(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, stream := range []bool{false, true} {
		body := fmt.Sprintf(`{"model":%q,"stream":%v,"max_tokens":1001,"messages":[{"role":"user","content":[{"type":"text","text":"What color?"},{"type":"image","source":{"type":"file","file_id":%q}}]}]}`, model, stream, meta.ID)
		w := sharedFilesRequest(s, "POST", "/v1/messages", body, true)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "green") {
			t.Fatalf("stream=%v: %d %s", stream, w.Code, w.Body.String())
		}
		var sent []byte
		select {
		case sent = <-captured:
		case <-time.After(5 * time.Second):
			t.Fatal("upstream request not captured")
		}
		url := gjson.GetBytes(sent, "messages.0.content.1.image_url.url").String()
		if url != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(imageBytes.Bytes()) || strings.Contains(string(sent), meta.ID) {
			t.Fatalf("portable image resolution failed: %s", sent)
		}
		if !gjson.GetBytes(sent, "max_tokens").Exists() && !gjson.GetBytes(sent, "max_completion_tokens").Exists() {
			t.Fatalf("output limit lost: %s", sent)
		}
		if stream && !strings.Contains(w.Body.String(), "message_stop") {
			t.Fatalf("stream not finalized: %s", w.Body.String())
		}
		if !stream && gjson.GetBytes(w.Body.Bytes(), "type").String() != "message" {
			t.Fatalf("nonstream representation: %s", w.Body.String())
		}
	}
}

func TestSharedFilesConfigReplacementEnablesAndDisablesHTTP(t *testing.T) {
	root := t.TempDir()
	disabled := &config.Config{SDKConfig: config.SDKConfig{Files: config.FilesConfig{StorageDir: root}}}
	s := sharedFilesServer(t, disabled, nil)
	if w := sharedFilesRequest(s, "GET", "/v1/files", "", true); w.Code != 404 {
		t.Fatalf("disabled: %d", w.Code)
	}
	enabled := &config.Config{SDKConfig: config.SDKConfig{Files: config.FilesConfig{Enabled: true, StorageDir: root}}}
	s.cfg = enabled
	s.handlers.UpdateClients(&enabled.SDKConfig)
	s.setupSharedFiles()
	if w := sharedFilesRequest(s, "GET", "/v1/files", "", true); w.Code != 200 || s.handlers.SharedFiles == nil {
		t.Fatalf("enable reload: %d", w.Code)
	}
	s.cfg = disabled
	s.handlers.UpdateClients(&disabled.SDKConfig)
	s.setupSharedFiles()
	if w := sharedFilesRequest(s, "GET", "/v1/files", "", true); w.Code != 404 {
		t.Fatalf("disable reload: %d", w.Code)
	}
}

type nativeSharedFilesAccessFixture struct{}

func (nativeSharedFilesAccessFixture) Identifier() string { return "native-shared-files-fixture" }
func (nativeSharedFilesAccessFixture) Authenticate(_ context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	if r.Header.Get("X-Api-Key") != "frontend-secret" {
		return nil, sdkaccess.NewInvalidCredentialError()
	}
	return &sdkaccess.Result{Principal: "owner", Provider: "native-fixture"}, nil
}
func TestSharedFilesNativeHeaderSurvivesCredentialAdaptation(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{Files: config.FilesConfig{Enabled: true, StorageDir: t.TempDir()}}}
	s := sharedFilesServer(t, cfg, nil)
	s.accessManager.SetProviders([]sdkaccess.Provider{nativeSharedFilesAccessFixture{}})
	if w := sharedFilesRequest(s, "GET", "/v1/files", "", true); w.Code != 200 {
		t.Fatalf("native-only provider rejected: %d %s", w.Code, w.Body.String())
	}
}
