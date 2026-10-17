package helps

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// FileInferenceClient clones the resolved client without replacing proxy or TLS selection.
// Validation runs on the serialized final body; credentials are restored only after validation.
func FileInferenceClient(ctx context.Context, client *http.Client, protect func(*http.Request, []byte) error) *http.Client {
	clone := *client
	base := clone.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	clone.Transport = fileInferenceTransport{ctx: ctx, base: base, protect: protect}
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &clone
}

type fileInferenceTransport struct {
	ctx     context.Context
	base    http.RoundTripper
	protect func(*http.Request, []byte) error
}

func (t fileInferenceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.GetBody == nil {
		return nil, fmt.Errorf("hosted file inference requires a replayable JSON body")
	}
	replay, err := req.GetBody()
	if err != nil {
		return nil, fmt.Errorf("final file payload unavailable")
	}
	body, err := io.ReadAll(replay)
	_ = replay.Close()
	if err != nil {
		return nil, fmt.Errorf("final file payload unreadable")
	}
	if strings.Contains(strings.ToLower(string(body)), "cpa-files.invalid") {
		return nil, fmt.Errorf("file translation sentinel reached inference")
	}
	if gjson.GetBytes(body, "__cpa_hosted_files").Exists() {
		return nil, fmt.Errorf("file translation marker reached inference")
	}
	if err = ValidateFilePayload(t.ctx, body); err != nil {
		return nil, err
	}
	outgoing := req.Clone(req.Context())
	if err = t.protect(outgoing, body); err != nil {
		return nil, err
	}
	if f := FileRequestFromContext(t.ctx); f != nil {
		RecordFileSendDiagnostics(t.ctx, body, f.Owner)
	}
	return t.base.RoundTrip(outgoing)
}
