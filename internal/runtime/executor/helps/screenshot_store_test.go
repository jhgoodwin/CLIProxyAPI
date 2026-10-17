package helps

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

func TestScreenshotStoreOriginalMemoExpiry(t *testing.T) {
	r := NewCodexFileRegistry()
	r.root = t.TempDir()
	clock := time.Unix(1000, 0)
	r.now = func() time.Time { return clock }
	var source bytes.Buffer
	_ = jpeg.Encode(&source, image.NewRGBA(image.Rect(0, 0, 803, 444)), nil)
	f, e := r.Store(CodexFile{Scope: FileCallerScope("a"), MIME: "image/jpeg"}, source.Bytes(), 3, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	_, original, e := r.Content(f.ID, "a", time.Hour)
	if e != nil || !bytes.Equal(original, source.Bytes()) {
		t.Fatal("original modified")
	}
	if _, e = r.Get(f.ID, "b", time.Hour); e == nil {
		t.Fatal("crosscaller")
	}
	variant, _ := os.ReadFile(f.VariantPath)
	stat, _ := os.Stat(f.VariantPath)
	again, e := r.Store(CodexFile{Scope: FileCallerScope("a")}, source.Bytes(), 3, time.Hour)
	if e != nil || again.VariantKey != f.VariantKey || !again.MemoHit || f.MemoHit {
		t.Fatal("memo key")
	}
	copyVariant, _ := os.ReadFile(again.VariantPath)
	if !bytes.Equal(variant, copyVariant) {
		t.Fatal("memo changed")
	}
	body := []byte(`{"input":[{"role":"user","content":[{"type":"input_image","file_id":"` + f.ID + `"},{"type":"input_image","file_id":"external-file"}]},{"type":"function_call_output","call_id":"c","output":[{"type":"input_image","file_id":"` + again.ID + `"}]}]}`)
	ctx, resolved, e := r.Resolve(context.Background(), body, "a", time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	if FileRequestFromContext(ctx).Owner != "" || !FileRequestFromContext(ctx).Local {
		t.Fatal("account dependence")
	}
	if !bytes.Equal(resolved, body) || gjson.GetBytes(resolved, "input.1.output.0.file_id").String() != again.ID {
		t.Fatal("local references were expanded before credential selection")
	}
	emitted, e := FileRequestFromContext(ctx).Pending[again.ID].Read(ctx)
	if e != nil || !bytes.Equal(emitted, variant) {
		t.Fatal("memo output")
	}
	after, _ := os.Stat(f.VariantPath)
	if !after.ModTime().Equal(stat.ModTime()) {
		t.Fatal("reference rewrote cache")
	}
	if e = r.Delete(f.ID, "a", time.Hour); e != nil {
		t.Fatal(e)
	}
	if _, _, e = r.Resolve(context.Background(), []byte(`{"input":[{"content":[{"type":"input_image","file_id":"`+again.ID+`"}]}]}`), "a", time.Hour); e != nil {
		t.Fatal("delete broke remaining copy")
	}
	clock = clock.Add(time.Hour)
	if _, e = r.Get(again.ID, "a", time.Hour); e == nil {
		t.Fatal("expiry")
	}
	if _, e = os.Stat(again.OriginalPath); !os.IsNotExist(e) {
		t.Fatal("expiry retained original")
	}
}
func TestScreenshotNormalize4K(t *testing.T) {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 3840, 2160)))
	out, mime, e := NormalizeScreenshot(b.Bytes())
	if e != nil || mime != "image/png" {
		t.Fatal(e)
	}
	cfg, e := png.DecodeConfig(bytes.NewReader(out))
	if e != nil || cfg.Width != 2048 || cfg.Height != 1152 {
		t.Fatal("4K aspect ratio")
	}
}
func TestScreenshotExactPixels(t *testing.T) {
	var fixture bytes.Buffer
	if errPNG := png.Encode(&fixture, image.NewRGBA(image.Rect(0, 0, 803, 444))); errPNG != nil {
		t.Fatal(errPNG)
	}
	data := fixture.Bytes()
	out, _, e := NormalizeScreenshot(data)
	if e != nil {
		t.Fatal(e)
	}
	src, _, _ := image.Decode(bytes.NewReader(data))
	dst, e := png.Decode(bytes.NewReader(out))
	if e != nil || src.Bounds() != dst.Bounds() {
		t.Fatal("fixture dimensions")
	}
	for y := 0; y < 444; y++ {
		for x := 0; x < 803; x++ {
			a, b, c, d := src.At(x, y).RGBA()
			w, z, v, u := dst.At(x, y).RGBA()
			if a != w || b != z || c != v || d != u {
				t.Fatal("small screenshot fidelity")
			}
		}
	}
}
