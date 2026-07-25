package imports

import (
	"bytes"
	"image"
	"image/color"
	"testing"

	_ "golang.org/x/image/webp"
)

// The lossy WebP encoder refuses an image carrying alpha, so a transparent page
// has to be flattened before it reaches the encoder. Getting this wrong fails
// the whole import over one PNG.
func TestEncodeOneWebPFlattensAlpha(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 32, 48))
	for y := range 48 {
		for x := range 32 {
			src.SetNRGBA(x, y, color.NRGBA{R: 200, G: 40, B: 40, A: uint8(x * 8 % 256)})
		}
	}

	out, err := encodeOne(src, "webp", 70)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	img, format, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if format != "webp" {
		t.Errorf("format = %q, want webp", format)
	}
	if got := img.Bounds().Size(); got != (image.Point{X: 32, Y: 48}) {
		t.Errorf("size = %v, want 32x48", got)
	}
	// Fully transparent pixels come back as the white they were composited over,
	// not as black or as garbage. The bound is loose because the encode is lossy
	// and the neighbouring red bleeds into the edge.
	r, g, b, _ := img.At(0, 0).RGBA()
	if r>>8 < 210 || g>>8 < 210 || b>>8 < 210 {
		t.Errorf("transparent pixel = (%d,%d,%d), want near-white", r>>8, g>>8, b>>8)
	}
}

func TestEncodeOneRejectsUnknownFormat(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	if _, err := encodeOne(img, "avif", 70); err == nil {
		t.Fatal("want an error for a format that is no longer an encode target")
	}
}
