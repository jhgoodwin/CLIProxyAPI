package helps

import (
	"context"
	"testing"
)

func TestPrepareFilesRequiresEveryOriginalReferenceBeforeUpload(t *testing.T) {
	for _, body := range []string{
		`{"input":[{"role":"user","content":[{"type":"input_text","text":"describe"}]}]}`,
		`{"input":[{"role":"user","content":[{"type":"input_image","file_id":"file-cpa-a"}]}]}`,
	} {
		reads, uploads := 0, 0
		pending := PendingFile{Read: func(context.Context) ([]byte, error) { reads++; return []byte("png"), nil }}
		ctx := WithFileRequest(context.Background(), &FileRequest{Local: true, Pending: map[string]PendingFile{"file-cpa-a": pending, "file-cpa-b": pending}})
		_, out, err := PrepareFilePayload(ctx, []byte(body), func(context.Context, string, []byte) (string, error) {
			uploads++
			return "file-upstream", nil
		})
		if err == nil || out != nil || reads != 0 || uploads != 0 {
			t.Fatalf("dropped reference did not fail before upload: err=%v out=%s reads=%d uploads=%d", err, out, reads, uploads)
		}
	}
}

func TestPrepareFilesDeduplicatesAndAllowsFinalRemoval(t *testing.T) {
	reads, uploads := 0, 0
	pending := PendingFile{Read: func(context.Context) ([]byte, error) { reads++; return []byte("png"), nil }}
	ctx := WithFileRequest(context.Background(), &FileRequest{Local: true, Pending: map[string]PendingFile{"file-cpa-a": pending}})
	body := []byte(`{"input":[{"role":"user","content":[{"type":"input_image","file_id":"file-cpa-a"},{"type":"input_image","file_id":"file-cpa-a"}]}]}`)
	next, out, err := PrepareFilePayload(ctx, body, func(context.Context, string, []byte) (string, error) {
		uploads++
		return "file-upstream", nil
	})
	if err != nil || reads != 1 || uploads != 1 {
		t.Fatalf("duplicate reference: err=%v reads=%d uploads=%d", err, reads, uploads)
	}
	if err := ValidateFilePayload(next, out); err != nil {
		t.Fatal(err)
	}
	final := []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"intentional final removal"}]}]}`)
	if err := ValidateFilePayload(next, final); err != nil {
		t.Fatalf("intentional final removal rejected: %v", err)
	}
}
