package helps

import (
	"context"
	"strings"
	"testing"
	"time"
)

func filePayload(id string) []byte {
	return []byte(`{"input":[{"role":"user","content":[{"type":"input_image","file_id":"` + id + `"}]}]}`)
}
func TestFileRegistryScopeExpiryCapacity(t *testing.T) {
	r := NewCodexFileRegistry()
	now := time.Unix(100, 0)
	r.now = func() time.Time { return now }
	f, err := r.Add(CodexFile{UpstreamID: "upstream", Owner: "a", Scope: FileCallerScope("alice"), Ready: true}, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Add(CodexFile{}, 1, time.Minute); err == nil {
		t.Fatal("capacity not bounded")
	}
	if _, _, err = r.Resolve(context.Background(), filePayload(f.ID), "bob", time.Minute); err == nil {
		t.Fatal("cross-caller access")
	}
	ctx, body, err := r.Resolve(context.Background(), filePayload(f.ID), "alice", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if FileRequestFromContext(ctx).Owner != "a" || !strings.Contains(string(body), `"file_id":"upstream"`) || strings.Contains(string(body), "image_url") {
		t.Fatal("bad mapping")
	}
	now = now.Add(time.Minute)
	if _, _, err = r.Resolve(context.Background(), filePayload(f.ID), "alice", time.Minute); err == nil {
		t.Fatal("expiry")
	}
	if _, err = r.Add(CodexFile{}, 1, time.Minute); err != nil {
		t.Fatal("expired entries not pruned")
	}
}
func TestFileRegistryMixedOwnersAndReadiness(t *testing.T) {
	r := NewCodexFileRegistry()
	scope := FileCallerScope("alice")
	a, _ := r.Add(CodexFile{UpstreamID: "a", Owner: "a", Scope: scope, Ready: true}, 5, time.Hour)
	b, _ := r.Add(CodexFile{UpstreamID: "b", Owner: "b", Scope: scope, Ready: true}, 5, time.Hour)
	body := []byte(`{"input":[{"content":[{"type":"input_image","file_id":"` + a.ID + `"},{"type":"input_image","file_id":"` + b.ID + `"}]}]}`)
	if _, _, err := r.Resolve(context.Background(), body, "alice", time.Hour); err == nil {
		t.Fatal("mixed owners accepted")
	}
	pending, _ := r.Add(CodexFile{UpstreamID: "c", Owner: "a", Scope: scope}, 5, time.Hour)
	if _, _, err := r.Resolve(context.Background(), filePayload(pending.ID), "alice", time.Hour); err == nil {
		t.Fatal("pending accepted")
	}
}
func TestFinalFileValidationAllowsRemovalNotChanges(t *testing.T) {
	ctx := WithFileRequest(context.Background(), &FileRequest{Owner: "owner", Allowed: map[string]bool{"upstream": true}})
	for _, body := range [][]byte{filePayload("upstream"), []byte(`{"input":[]}`)} {
		if err := ValidateFilePayload(ctx, body); err != nil {
			t.Fatal(err)
		}
	}
	for _, body := range [][]byte{filePayload("changed"), filePayload(PrototypeFilePrefix + "unknown")} {
		if err := ValidateFilePayload(ctx, body); err == nil {
			t.Fatal("changed/unresolved accepted")
		}
	}
	if err := ValidateFileOwner(ctx, "other", "codex"); err == nil {
		t.Fatal("owner fallback accepted")
	}
}
