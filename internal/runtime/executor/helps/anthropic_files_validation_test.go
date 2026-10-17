package helps

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestAnthropicFilesFinalValidation(t *testing.T) {
	id := "file_" + strings.Repeat("a", 64) + "_" + strings.Repeat("b", 16) + "_" + strings.Repeat("c", 32)
	for _, body := range []string{
		`{"messages":[{"content":[{"type":"image","source":{"type":"file","file_id":"` + id + `"}}]}]}`,
		`{"messages":[{"content":[{"type":"tool_result","content":[{"type":"image","source":{"file_id":"` + id + `"}}]}]}]}`,
		`{"configured":{"file_id":"` + id + `"}}`,
	} {
		if err := ValidateFilePayload(context.Background(), []byte(body)); err == nil {
			t.Fatal("unresolved local file accepted")
		}
	}
	for _, body := range []string{`{"messages":[]}`, `{"file_id":"file_upstream-id"}`, `{"text":"` + id + `"}`} {
		if err := ValidateFilePayload(context.Background(), []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCanonicalFilesOperationsUseFilePrivacy(t *testing.T) {
	for _, path := range []string{"/v1/files", "/v1/files/id", "/v1/files/id/content"} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
			request, _ := http.NewRequest(method, "http://proxy"+path, nil)
			if !FilePrivacyRequest(request) {
				t.Fatalf("privacy missing for %s %s", method, path)
			}
		}
	}
	for _, path := range []string{"/v1/chat/completions", "/anthropic/v10/messages"} {
		request, _ := http.NewRequest(http.MethodPost, "http://proxy"+path, nil)
		if FilePrivacyRequest(request) {
			t.Fatalf("privacy scope broadened to %s", path)
		}
	}
	if FilePrivacyRequest(&http.Request{}) {
		t.Fatal("request without URL accepted")
	}
}
