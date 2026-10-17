package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/anthropicfiles"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/tidwall/gjson"
)

func sharedFilesFixture(t *testing.T) (*BaseAPIHandler, *anthropicfiles.Store, string, []byte, *time.Time) {
	t.Helper()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store, err := anthropicfiles.Open(t.TempDir(), 10, 1<<20, anthropicfiles.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	var original bytes.Buffer
	if err := jpeg.Encode(&original, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatal(err)
	}
	m, err := store.Put("alice", "original.jpg", "image/jpeg", original.Bytes(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.SDKConfig{}
	cfg.Files.Enabled = true
	return &BaseAPIHandler{Cfg: cfg, SharedFiles: store}, store, m.ID, original.Bytes(), &now
}

func TestSharedFilesSameIDAcrossProtocols(t *testing.T) {
	h, _, id, original, _ := sharedFilesFixture(t)
	caller := access.WithAuthenticatedPrincipal(context.Background(), "alice")
	cases := []struct{ protocol, body, dataPath, mimePath string }{
		{"claude", `{"model":"arbitrary-text-model","stream":false,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"file","file_id":"%s"}}]}]}`, "messages.0.content.0.source.data", "messages.0.content.0.source.media_type"},
		{"claude", `{"model":"arbitrary-text-model","stream":true,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool","content":[{"type":"image","source":{"type":"file","file_id":"%s"}}]}]}]}`, "messages.0.content.0.content.0.source.data", "messages.0.content.0.content.0.source.media_type"},
		{"openai", `{"model":"arbitrary-text-model","stream":false,"messages":[{"role":"tool","content":[{"type":"file","file":{"file_id":"%s"}}]}]}`, "messages.0.content.0.image_url.url", ""},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			body := []byte(fmt.Sprintf(tc.body, id))
			ctx, resolved, failure, handled := h.prepareSharedFileReferences(caller, tc.protocol, body)
			if !handled || failure != nil {
				t.Fatalf("handled=%v error=%v", handled, failure)
			}
			if !sharedFilesContext(ctx) || helps.FileRequestFromContext(ctx) != nil {
				t.Fatal("portable images must not enable native execution")
			}
			if helps.ContainsLocalFileID(resolved) {
				t.Fatal("local ID leaked")
			}
			if gjson.GetBytes(resolved, "model").String() != "arbitrary-text-model" || gjson.GetBytes(resolved, "stream").Raw != gjson.GetBytes(body, "stream").Raw {
				t.Fatal("model or stream changed")
			}
			encoded := gjson.GetBytes(resolved, tc.dataPath).String()
			if tc.mimePath != "" {
				if gjson.GetBytes(resolved, tc.mimePath).String() != "image/jpeg" {
					t.Fatal("original MIME lost")
				}
			} else {
				if !strings.HasPrefix(encoded, "data:image/jpeg;base64,") {
					t.Fatal("original MIME lost")
				}
				encoded = strings.TrimPrefix(encoded, "data:image/jpeg;base64,")
			}
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil || !bytes.Equal(decoded, original) {
				t.Fatal("original bytes changed")
			}
		})
	}
}

func TestSharedFilesSameIDNativeAndPortable(t *testing.T) {
	h, _, id, original, _ := sharedFilesFixture(t)
	caller := access.WithAuthenticatedPrincipal(context.Background(), "alice")
	nativeBody := []byte(fmt.Sprintf(`{"input":[{"content":[{"type":"input_image","file_id":"%s"}]}]}`, id))
	ctx, _, failure, handled := h.prepareSharedFileReferences(caller, "openai-response", nativeBody)
	if !handled || failure != nil {
		t.Fatal(failure)
	}
	png, err := helps.FileRequestFromContext(ctx).Pending[id].Read(caller)
	if err != nil || !helps.ValidFilePNG(png) {
		t.Fatalf("native read: %v", err)
	}
	for _, protocol := range []string{"claude", "openai"} {
		template := `{"messages":[{"content":[{"type":"image","source":{"type":"file","file_id":"%s"}}]}]}`
		if protocol == "openai" {
			template = `{"messages":[{"content":[{"type":"file","file":{"file_id":"%s"}}]}]}`
		}
		_, resolved, failure, handled := h.prepareSharedFileReferences(caller, protocol, []byte(fmt.Sprintf(template, id)))
		if !handled || failure != nil || !bytes.Contains(resolved, []byte(base64.StdEncoding.EncodeToString(original))) {
			t.Fatalf("portable read of same ID: %v", failure)
		}
	}
}

func TestSharedFilesOrdinaryRequestsRemainExact(t *testing.T) {
	h := &BaseAPIHandler{}
	for _, protocol := range []string{"claude", "openai", "openai-response", "unknown"} {
		for _, body := range []string{
			" {\n \"model\":\"anything\", \"stream\":false, \"messages\":[{\"role\":\"user\",\"content\":\"hello\"}] } ",
			`{"model":"any","stream":true,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"private","signature":"opaque-signature"},{"type":"redacted_thinking","data":"opaque-redacted"},{"type":"text","text":"answer"}]}]}`,
			`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"file","file_id":"file_upstream_opaque"}}]}]}`,
			`{"input":[{"type":"message","content":[{"type":"input_image","file_id":"file-upstream"}]}]}`,
		} {
			ctx := context.Background()
			next, got, failure, handled := h.prepareSharedFileReferences(ctx, protocol, []byte(body))
			if handled || failure != nil || next != ctx || string(got) != body {
				t.Fatalf("ordinary %s request altered: %v %v", protocol, handled, failure)
			}
		}
	}
}

func TestSharedFilesReasoningAndMixedUpstreamRemainExact(t *testing.T) {
	h, _, id, _, _ := sharedFilesFixture(t)
	thinking := `{"type":"thinking", "thinking":"private", "signature":"opaque-signed"}`
	redacted := `{"type":"redacted_thinking", "data":"opaque-redacted"}`
	upstream := `{"type":"image","source":{"type":"file","file_id":"file_upstream"}}`
	body := []byte(fmt.Sprintf(`{"model":"any","stream":false,"messages":[{"role":"assistant","content":[%s,%s]},{"role":"user","content":[%s,{"type":"image","source":{"type":"file","file_id":"%s"}}]}]}`, thinking, redacted, upstream, id))
	_, got, failure, handled := h.prepareSharedFileReferences(access.WithAuthenticatedPrincipal(context.Background(), "alice"), "claude", body)
	if !handled || failure != nil {
		t.Fatal(failure)
	}
	for _, unchanged := range []string{thinking, redacted, upstream} {
		if !bytes.Contains(got, []byte(unchanged)) {
			t.Fatalf("changed preserved content: %s", unchanged)
		}
	}
}

func TestSharedFilesOwnershipDeletionExpiry(t *testing.T) {
	for _, scenario := range []string{"other-owner", "deleted", "expired", "anonymous", "disabled", "legacy-enabled"} {
		t.Run(scenario, func(t *testing.T) {
			h, store, id, _, now := sharedFilesFixture(t)
			principal := "alice"
			switch scenario {
			case "other-owner":
				principal = "bob"
			case "anonymous":
				principal = ""
			case "deleted":
				if err := store.Delete("alice", id); err != nil {
					t.Fatal(err)
				}
			case "expired":
				*now = now.Add(time.Hour)
			case "disabled":
				h.Cfg.Files.Enabled = false
			case "legacy-enabled":
				h.Cfg.Files.Enabled = false
				h.Cfg.CodexFilesPrototype.Enabled = true
			}
			body := []byte(fmt.Sprintf(`{"messages":[{"content":[{"type":"image","source":{"type":"file","file_id":"%s"}}]}]}`, id))
			_, got, failure, handled := h.prepareSharedFileReferences(access.WithAuthenticatedPrincipal(context.Background(), principal), "claude", body)
			if !handled {
				t.Fatal("canonical request not handled")
			}
			if scenario == "legacy-enabled" {
				if failure != nil {
					t.Fatal(failure)
				}
				return
			}
			if failure == nil || got != nil {
				t.Fatal("unauthorized request accepted")
			}
		})
	}
}

func TestSharedFilesNativeAttemptReadsAndMixedIDs(t *testing.T) {
	h, store, id, _, now := sharedFilesFixture(t)
	caller := access.WithAuthenticatedPrincipal(context.Background(), "alice")
	body := []byte(fmt.Sprintf(`{"model":"any","stream":false,"input":[{"type":"message","content":[{"type":"input_image","file_id":"%s"},{"type":"input_image","file_id":"file-upstream"},{"type":"input_image","file_id":"%s"}]},{"type":"function_call_output","call_id":"tool","output":[{"type":"input_image","file_id":"%s"}]}]}`, id, id, id))
	ctx, got, failure, handled := h.prepareSharedFileReferences(caller, "openai-response", body)
	if !handled || failure != nil || !bytes.Equal(got, body) {
		t.Fatalf("native prep failed: %v", failure)
	}
	f := helps.FileRequestFromContext(ctx)
	if !f.Local || len(f.Pending) != 1 || !f.Allowed["file-upstream"] {
		t.Fatal("missing native ownership state")
	}
	uploads := 0
	upload := func(_ context.Context, name string, png []byte) (string, error) {
		uploads++
		if name != "screenshot.png" || !helps.ValidFilePNG(png) {
			t.Fatal("native original not normalized at execution")
		}
		return fmt.Sprintf("file-upload-%d", uploads), nil
	}
	for i := 0; i < 2; i++ {
		attempt, payload, err := helps.PrepareFilePayload(ctx, body, upload)
		if err != nil {
			t.Fatal(err)
		}
		if helps.ContainsLocalFileID(payload) || gjson.GetBytes(payload, "input.0.content.1.file_id").String() != "file-upstream" {
			t.Fatal("local leak or upstream ID changed")
		}
		if err := helps.ValidateFilePayload(attempt, payload); err != nil {
			t.Fatal(err)
		}
	}
	if uploads != 2 {
		t.Fatal("retry upload was cached across attempts")
	}
	canceled, cancel := context.WithCancel(caller)
	cancel()
	if _, err := f.Pending[id].Read(canceled); err == nil {
		t.Fatal("cancellation ignored")
	}
	*now = now.Add(time.Hour)
	if _, err := f.Pending[id].Read(caller); err == nil {
		t.Fatal("expiry not rechecked")
	}
	if _, _, err := store.Content("alice", id); err == nil {
		t.Fatal("fixture not expired")
	}
}

func TestSharedFilesPendingRechecksDeletion(t *testing.T) {
	h, store, id, _, _ := sharedFilesFixture(t)
	caller := access.WithAuthenticatedPrincipal(context.Background(), "alice")
	body := []byte(fmt.Sprintf(`{"input":[{"content":[{"type":"input_image","file_id":"%s"}]}]}`, id))
	ctx, _, failure, _ := h.prepareSharedFileReferences(caller, "openai-response", body)
	if failure != nil {
		t.Fatal(failure)
	}
	if err := store.Delete("alice", id); err != nil {
		t.Fatal(err)
	}
	if _, err := helps.FileRequestFromContext(ctx).Pending[id].Read(caller); err == nil {
		t.Fatal("deletion not rechecked")
	}
}

func TestSharedFilesUnsupportedAndMalformedReferencesFail(t *testing.T) {
	h, _, id, _, _ := sharedFilesFixture(t)
	cases := []struct{ protocol, body string }{
		{"claude", `{"metadata":{"file_id":"%s"}}`},
		{"claude", `{"messages":[{"content":[{"type":"image","source":{"type":"file","file_id":["%s"]}}]}]}`},
		{"openai-response", `{"input":[{"content":[{"type":"input_image","file_id":{"nested":"%s"}}]}]}`},
		{"claude", `{"messages":[{"content":[{"type":"image","source":{"type":"url","file_id":"%s"}}]}]}`},
		{"claude", `{"messages":[{"content":[{"type":"image","source":{"type":"file","file_id":"%s","data":"mixed"}}]}]}`},
		{"openai", `{"messages":[{"content":[{"type":"file","file":{"file_id":"%s","file_data":"mixed"}}]}]}`},
		{"openai-response", `{"input":[{"content":[{"type":"input_image","file_id":"%s","image_url":"https://example.test"}]}]}`},
		{"openai-response", `{"metadata":{"type":"input_image","file_id":"%s"}}`},
		{"unknown", `{"file_id":"%s"}`},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			_, got, failure, handled := h.prepareSharedFileReferences(access.WithAuthenticatedPrincipal(context.Background(), "alice"), tc.protocol, []byte(fmt.Sprintf(tc.body, id)))
			if !handled || failure == nil || got != nil {
				t.Fatal("malformed canonical reference accepted")
			}
		})
	}
}
