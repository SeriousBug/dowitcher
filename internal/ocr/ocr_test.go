package ocr

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// renderText draws s as black text on a white page, the way a scanned comic
// caption reaches Tesseract. Rendering beats a checked-in fixture image because
// the expected text and the pixels cannot drift apart.
func renderText(t *testing.T, s string) []byte {
	t.Helper()
	parsed, err := opentype.Parse(goregular.TTF)
	if err != nil {
		t.Fatalf("parse font: %v", err)
	}
	face, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: 64, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		t.Fatalf("new face: %v", err)
	}
	img := image.NewGray(image.Rect(0, 0, 900, 200))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	d := font.Drawer{Dst: img, Src: image.NewUniform(color.Black), Face: face, Dot: fixed.P(40, 120)}
	d.DrawString(s)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

// TestTextRecognisesRenderedPages is the only proof that the vendored
// gogosseract fork actually runs against the wazero version the rest of the
// binary uses. It compiles a 2MB WASM module and runs real recognition, so it
// takes seconds — it still runs by default, because a skipped test proves
// nothing about the fork.
func TestTextRecognisesRenderedPages(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and runs the Tesseract WASM module")
	}

	engine, err := New("")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	want := []string{"HELLO COMIC", "The quick brown fox"}
	images := make([][]byte, len(want))
	for i, s := range want {
		images[i] = renderText(t, s)
	}

	got, err := engine.Text(context.Background(), images, time.Minute)
	if err != nil {
		t.Fatalf("Text: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d results, want %d", len(got), len(want))
	}
	for i, w := range want {
		if strings.TrimSpace(got[i]) != w {
			t.Errorf("image %d: got %q, want %q", i, got[i], w)
		}
	}
}

func TestNewRejectsUnreadableTrainingData(t *testing.T) {
	if _, err := New(t.TempDir() + "/absent.traineddata"); err == nil {
		t.Fatal("expected an error for a training data path that does not exist")
	}
}
