package executor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	execution "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type filesDelegateSpy struct {
	auth.ProviderExecutor
	call func(context.Context, *auth.Auth, execution.Request, execution.Options) error
}

func (s *filesDelegateSpy) Execute(c context.Context, a *auth.Auth, r execution.Request, o execution.Options) (execution.Response, error) {
	return execution.Response{}, s.call(c, a, r, o)
}
func (s *filesDelegateSpy) ExecuteStream(c context.Context, a *auth.Auth, r execution.Request, o execution.Options) (*execution.StreamResult, error) {
	return nil, s.call(c, a, r, o)
}
func (s *filesDelegateSpy) CountTokens(c context.Context, a *auth.Auth, r execution.Request, o execution.Options) (execution.Response, error) {
	return execution.Response{}, s.call(c, a, r, o)
}

func TestFilesDecoratorUnprotectedDelegatesUnchanged(t *testing.T) {
	ctx := context.WithValue(context.Background(), struct{}{}, "original")
	a := &auth.Auth{}
	body := []byte(`{"model":"text","input":"hello"}`)
	opts := execution.Options{OriginalRequest: []byte("original")}
	spy := &filesDelegateSpy{call: func(c context.Context, selected *auth.Auth, r execution.Request, o execution.Options) error {
		if c != ctx || selected != a || !bytes.Equal(r.Payload, body) || !bytes.Equal(o.OriginalRequest, opts.OriginalRequest) {
			t.Fatal("changed unprotected execution")
		}
		return nil
	}}
	wrapper := &filesExecutionDecorator{ProviderExecutor: spy, prepare: func(context.Context, *auth.Auth, []byte) (context.Context, []byte, error) {
		t.Fatal("unexpected upload")
		return nil, nil, nil
	}}
	_, err := wrapper.Execute(ctx, a, execution.Request{Payload: body}, opts)
	if err != nil {
		t.Fatal(err)
	}
	_, err = wrapper.ExecuteStream(ctx, a, execution.Request{Payload: body}, opts)
	if err != nil {
		t.Fatal(err)
	}
	_, err = wrapper.CountTokens(ctx, a, execution.Request{Payload: body}, opts)
	if err != nil {
		t.Fatal(err)
	}
}

func TestFilesDecoratorPreparesBothSourcesPerAttempt(t *testing.T) {
	local := helps.PrototypeFilePrefix + "one"
	source := []byte(fmt.Sprintf(`{"input":[{"role":"user","content":[{"type":"input_image","file_id":%q}]}]}`, local))
	pending := &helps.FileRequest{Local: true, Pending: map[string]helps.PendingFile{local: {Name: "screenshot.png", Read: func(context.Context) ([]byte, error) { return []byte("PNG"), nil }}}}
	ctx := helps.WithFileRequest(context.Background(), pending)
	uploads := 0
	selected := &auth.Auth{ID: "exact"}
	spy := &filesDelegateSpy{call: func(c context.Context, a *auth.Auth, r execution.Request, o execution.Options) error {
		if a != selected || !bytes.Equal(r.Payload, o.OriginalRequest) || helps.ContainsLocalFileID(r.Payload) {
			t.Fatal("delegated wrong auth or source")
		}
		f := helps.FileRequestFromContext(c)
		if f == pending || !f.Allowed[fmt.Sprintf("uploaded-%d", uploads)] {
			t.Fatal("shared or missing attempt references")
		}
		return nil
	}}
	wrapper := &filesExecutionDecorator{ProviderExecutor: spy, prepare: func(c context.Context, a *auth.Auth, b []byte) (context.Context, []byte, error) {
		if a != selected || !bytes.Equal(b, source) {
			t.Fatal("not original source")
		}
		return helps.PrepareFilePayload(c, b, func(context.Context, string, []byte) (string, error) {
			uploads++
			return fmt.Sprintf("uploaded-%d", uploads), nil
		})
	}}
	for _, stream := range []bool{false, true} {
		r := execution.Request{Payload: []byte("different")}
		o := execution.Options{OriginalRequest: source}
		var err error
		if stream {
			_, err = wrapper.ExecuteStream(ctx, selected, r, o)
		} else {
			_, err = wrapper.Execute(ctx, selected, r, o)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if uploads != 2 || len(pending.Allowed) != 0 {
		t.Fatal("attempt cache leaked")
	}
}

func TestFilesDecoratorCountRejectsWithoutUpload(t *testing.T) {
	calls := 0
	wrapper := &filesExecutionDecorator{ProviderExecutor: &filesDelegateSpy{call: func(context.Context, *auth.Auth, execution.Request, execution.Options) error { calls++; return nil }}, prepare: func(context.Context, *auth.Auth, []byte) (context.Context, []byte, error) {
		t.Fatal("count uploaded")
		return nil, nil, nil
	}}
	for _, ctx := range []context.Context{context.Background(), helps.WithFileRequest(context.Background(), &helps.FileRequest{Local: true})} {
		body := []byte(fmt.Sprintf(`{"file_id":%q}`, helps.PrototypeFilePrefix+"one"))
		if _, err := wrapper.CountTokens(ctx, nil, execution.Request{Payload: body}, execution.Options{}); err == nil {
			t.Fatal("count accepted local reference")
		}
	}
	if calls != 0 {
		t.Fatal("count delegated")
	}
}

func TestFilesDecoratorTransportRejectsInlineAndRedirect(t *testing.T) {
	ctx := helps.WithFileRequest(context.Background(), &helps.FileRequest{Local: true, Allowed: map[string]bool{"uploaded": true}})
	hits := 0
	base := &http.Client{Transport: filesTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		hits++
		if r.Header.Get("Authorization") != "Bearer selected" {
			t.Fatal("credentials overwritten")
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://redirect.invalid"}}, Body: http.NoBody, Request: r}, nil
	})}
	client := helps.FileInferenceClient(ctx, base, func(r *http.Request, _ []byte) error {
		restoreFileHeader(r, "Authorization", "Bearer selected")
		return nil
	})
	for _, body := range []string{`{"input":[{"type":"input_image","file_id":"uploaded"}]}`, `{"input":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}`} {
		req, _ := http.NewRequest("POST", "https://inference.invalid", bytes.NewBufferString(body))
		req.Header.Set("authorization", "wrong")
		resp, err := client.Do(req)
		if bytes.Contains([]byte(body), []byte("base64")) {
			if err == nil {
				t.Fatal("inline image accepted")
			}
		} else {
			if err != nil || resp.StatusCode != 302 {
				t.Fatalf("redirect not isolated: %v", err)
			}
		}
	}
	if hits != 1 || base.CheckRedirect != nil {
		t.Fatal("redirect followed or shared client mutated")
	}
}

type filesTestRoundTripper func(*http.Request) (*http.Response, error)

func (f filesTestRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
