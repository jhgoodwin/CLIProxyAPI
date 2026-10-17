package anthropicfiles

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	m := image.NewRGBA(image.Rect(0, 0, 2, 2))
	m.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&b, m); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestLifecycleRestartIsolationExpiry(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	root := t.TempDir()
	s, err := Open(root, 10, 10000, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	data := testPNG(t)
	m, err := s.Put("alice", "folder/original.png", "image/png", data, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"", "bob"} {
		if _, err = s.Get(p, m.ID); err == nil {
			t.Fatalf("access accepted for %q", p)
		}
		if _, _, err = s.Content(p, m.ID); err == nil {
			t.Fatal("content access accepted")
		}
		if err = s.Delete(p, m.ID); err == nil {
			t.Fatal("delete access accepted")
		}
	}
	if _, err = s.List("", ListOptions{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root, 10, 10000, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, content, err := s.Content("alice", m.ID)
	if err != nil || !bytes.Equal(data, content) || got.Filename != m.Filename || got.ExpiresAt != m.ExpiresAt {
		t.Fatalf("recovery: %+v %v", got, err)
	}
	for _, path := range []string{root, filepath.Join(root, m.ID), filepath.Join(root, m.ID, "content"), filepath.Join(root, m.ID, "metadata.json")} {
		fi, e := os.Stat(path)
		if e != nil {
			t.Fatal(e)
		}
		if fi.Mode().Perm()&0077 != 0 {
			t.Fatalf("insecure permissions %s: %v", path, fi.Mode())
		}
	}
	now = now.Add(time.Hour)
	if _, err = s.Get("alice", m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("expired data retained: %v %v", entries, err)
	}
}
func TestQuotasPaginationAfterDeletionAndExpiry(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	data := testPNG(t)
	s, err := Open(t.TempDir(), 3, int64(3*len(data)), WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var ids []string
	for i := 0; i < 3; i++ {
		now = now.Add(time.Second)
		m, e := s.Put("alice", "a.png", "image/png", data, time.Hour)
		if e != nil {
			t.Fatal(e)
		}
		ids = append(ids, m.ID)
	}
	if _, err = s.Put("bob", "a.png", "image/png", data, time.Hour); !errors.Is(err, ErrQuota) {
		t.Fatal(err)
	}
	page, err := s.List("alice", ListOptions{Limit: 1})
	if err != nil || !page.HasMore || page.LastID != ids[2] {
		t.Fatalf("page: %+v %v", page, err)
	}
	if err = s.Delete("alice", ids[2]); err != nil {
		t.Fatal(err)
	}
	page, err = s.List("alice", ListOptions{Limit: 1, AfterID: ids[2]})
	if err != nil || page.LastID != ids[1] || !page.HasMore {
		t.Fatalf("deleted cursor: %+v %v", page, err)
	}
	now = now.Add(time.Hour)
	page, err = s.List("alice", ListOptions{AfterID: ids[1]})
	if err != nil || len(page.Data) != 0 {
		t.Fatalf("expired cursor: %+v %v", page, err)
	}
	if _, err = s.List("bob", ListOptions{AfterID: ids[1]}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err = s.Put("bob", "a.png", "image/png", data, time.Hour); err != nil {
		t.Fatalf("expiry failed to release quota: %v", err)
	}
	tiny, err := Open(t.TempDir(), 100, int64(len(data)-1))
	if err != nil {
		t.Fatal(err)
	}
	defer tiny.Close()
	if _, err = tiny.Put("alice", "a.png", "image/png", data, time.Hour); !errors.Is(err, ErrQuota) {
		t.Fatal(err)
	}
}
func TestRecoveryInterruptedAndCorrupt(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{".stage-interrupted", ".trash-interrupted"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name, "partial"), []byte("partial"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(root, 10, 10000)
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Put("alice", "a.png", "image/png", testPNG(t), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, m.ID, "content"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(root, 10, 10000); err == nil {
		t.Fatal("corrupt store recovered")
	}
}
func TestImageValidationAndExpiryBounds(t *testing.T) {
	data := testPNG(t)
	for _, bad := range [][]byte{nil, []byte("not an image"), data[:len(data)/2]} {
		if _, err := ValidateImage(bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted malformed image: %v", err)
		}
	}
	if _, err := ValidateImage(data, "image/jpeg"); !errors.Is(err, ErrInvalid) {
		t.Fatal("MIME mismatch accepted")
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1, 1)), nil); err != nil {
		t.Fatal(err)
	}
	if mime, err := ValidateImage(b.Bytes()); err != nil || mime != "image/jpeg" {
		t.Fatal(mime, err)
	}
	b.Reset()
	if err := gif.Encode(&b, image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.Black}), nil); err != nil {
		t.Fatal(err)
	}
	if mime, err := ValidateImage(b.Bytes()); err != nil || mime != "image/gif" {
		t.Fatal(mime, err)
	}
	s, err := Open(t.TempDir(), 10, 10000)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, ttl := range []time.Duration{-time.Second, MaxTTL + time.Second} {
		if _, err = s.Put("a", "a.png", "image/png", data, ttl); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	m, err := s.Put("a", "a.png", "image/png", data, 0)
	if err != nil || m.ExpiresAt.Sub(m.CreatedAt) != DefaultTTL || m.Purpose != "user_data" {
		t.Fatal(m, err)
	}
}

func TestPurposePersistenceAndLegacyMetadata(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root, 10, 10000)
	if err != nil {
		t.Fatal(err)
	}
	data := testPNG(t)
	vision, err := s.PutWithPurpose("alice", "vision.png", "image/png", "vision", data, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := s.Put("alice", "legacy.png", "image/png", data, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PutWithPurpose("alice", "bad.png", "image/png", "assistants", data, time.Hour); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, legacy.ID, "metadata.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Published records from before purpose persistence remain readable.
	raw = bytes.Replace(raw, []byte(`"purpose":"user_data",`), nil, 1)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root, 10, 10000)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, want := range []Metadata{vision, legacy} {
		got, content, err := s.Content("alice", want.ID)
		if err != nil || got.Purpose != want.Purpose || !bytes.Equal(content, data) {
			t.Fatalf("recovered purpose/bytes: %+v %v", got, err)
		}
	}
}

func TestBackwardPaginationReturnsAdjacentPage(t *testing.T) {
	s, err := Open(t.TempDir(), 100, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	data := testPNG(t)
	for i := 0; i < 60; i++ {
		if _, err = s.Put("alice", "page.png", "image/png", data, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.List("alice", ListOptions{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.List("alice", ListOptions{Limit: 20, AfterID: first.LastID})
	if err != nil {
		t.Fatal(err)
	}
	third, err := s.List("alice", ListOptions{Limit: 20, AfterID: second.LastID})
	if err != nil {
		t.Fatal(err)
	}
	backward, err := s.List("alice", ListOptions{Limit: 20, BeforeID: third.FirstID})
	if err != nil {
		t.Fatal(err)
	}
	if backward.FirstID != second.FirstID || backward.LastID != second.LastID || !backward.HasMore {
		t.Fatalf("backward page skipped adjacent page: %+v versus %+v", backward, second)
	}
}
