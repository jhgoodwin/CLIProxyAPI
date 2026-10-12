package api

import (
	"net/http/httptest"
	"testing"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// Construction exercises the real routes without launching a server or loading
// any host config, .env, credentials, or making authenticated provider requests.
func TestScreenshotFilesApplicationConstruction(t *testing.T) {
	cfg := &proxyconfig.Config{SDKConfig: sdkconfig.SDKConfig{APIKeys: []string{"test-key"}, CodexFilesPrototype: sdkconfig.CodexFilesPrototypeConfig{Enabled: true}}}
	s := newTestServerWithConfig(t, cfg)
	for _, path := range []string{"/v1/files", "/v1/files/file-cpa-missing", "/v1/files/file-cpa-missing/content"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer test-key")
		w := httptest.NewRecorder()
		s.engine.ServeHTTP(w, req)
		if w.Code != 200 && w.Code != 404 {
			t.Fatalf("route %s status=%d", path, w.Code)
		}
	}
}
