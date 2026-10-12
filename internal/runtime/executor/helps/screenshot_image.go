package helps

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"math"

	_ "golang.org/x/image/webp"
)

// ScreenshotPolicyVersion is part of every memoization key. No tiling is used.
const ScreenshotPolicyVersion = "screenshot-png-2048-v1"

// NormalizeScreenshot bounds decoding and preserves small screenshots losslessly.
func NormalizeScreenshot(data []byte) ([]byte, string, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg" && format != "webp") {
		return nil, "", fmt.Errorf("only valid PNG, JPEG and WebP screenshots are supported")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 16384 || cfg.Height > 16384 || int64(cfg.Width)*int64(cfg.Height) > 64*1024*1024 {
		return nil, "", fmt.Errorf("screenshot dimensions exceed decode limit")
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("invalid screenshot image")
	}
	w, h := cfg.Width, cfg.Height
	ratio := math.Min(1, math.Min(2048/float64(w), 2048/float64(h)))
	if ratio < 1 {
		nw, nh := int(math.Max(1, math.Round(float64(w)*ratio))), int(math.Max(1, math.Round(float64(h)*ratio)))
		dst := image.NewNRGBA(image.Rect(0, 0, nw, nh))
		bounds := src.Bounds()
		// Area averaging preserves thin screenshot strokes better than nearest-neighbor.
		for y := 0; y < nh; y++ {
			for x := 0; x < nw; x++ {
				x0, x1 := x*w/nw, (x+1)*w/nw
				y0, y1 := y*h/nh, (y+1)*h/nh
				var r, g, b, a, n uint64
				for sy := y0; sy < y1; sy++ {
					for sx := x0; sx < x1; sx++ {
						c := color.NRGBAModel.Convert(src.At(bounds.Min.X+sx, bounds.Min.Y+sy)).(color.NRGBA)
						r += uint64(c.R)
						g += uint64(c.G)
						b += uint64(c.B)
						a += uint64(c.A)
						n++
					}
				}
				dst.SetNRGBA(x, y, color.NRGBA{uint8(r / n), uint8(g / n), uint8(b / n), uint8(a / n)})
			}
		}
		src = dst
	}
	var out bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&out, src); err != nil {
		return nil, "", fmt.Errorf("screenshot normalization failed")
	}
	if out.Len() > 20*1024*1024 {
		return nil, "", fmt.Errorf("normalized screenshot exceeds byte limit")
	}
	return out.Bytes(), "image/png", nil
}
