package helps

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

const FileProbePrivacyHeader = "X-CPA-Files-Probe"

func FilePrivacyRequest(r *http.Request) bool {
	return r != nil && (r.URL.Path == "/v1/files" || strings.HasPrefix(r.URL.Path, "/v1/files/") || (r.Method == http.MethodPost && r.URL.Path == "/v1/responses" && r.Header.Get(FileProbePrivacyHeader) == "1"))
}
func FilePrivacyContext(ctx context.Context) bool {
	if f := FileRequestFromContext(ctx); f != nil && (f.Owner != "" || f.Local) {
		return true
	}
	c := ginContextFrom(ctx)
	return c != nil && FilePrivacyRequest(c.Request)
}
func ValidateFileOwner(ctx context.Context, id, provider string) error {
	f := FileRequestFromContext(ctx)
	if f != nil && f.Owner != "" && (f.Owner != id || provider != "codex") {
		return fmt.Errorf("file owner routing invariant failed")
	}
	return nil
}
