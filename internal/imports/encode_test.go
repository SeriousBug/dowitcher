package imports

import (
	"archive/zip"
	"bytes"
	"context"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/gen2brain/avif"
	"github.com/gen2brain/webp"

	"github.com/SeriousBug/dowitcher/internal/api"
)

// TestEncodeSkipsGoodEnoughSources: an encode pass re-encodes a source that is
// not already space-efficient (here a PNG) into the target format, but copies a
// source that is already AVIF or WebP through untouched, keeping its own bytes
// and extension. Re-encoding those would spend a generation of quality for no
// real size gain.
func TestEncodeSkipsGoodEnoughSources(t *testing.T) {
	srcDir := t.TempDir()

	// Three visually distinct pages so dedupe keeps all three, one per format.
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, synth(400, 600, 10, 0)); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	var webpBuf bytes.Buffer
	if err := webp.Encode(&webpBuf, synth(400, 600, 20, 0), webp.Options{Quality: 80}); err != nil {
		t.Fatalf("encode webp: %v", err)
	}
	var avifBuf bytes.Buffer
	if err := avif.Encode(&avifBuf, synth(400, 600, 30, 0), avif.Options{Quality: 60, Speed: avifSpeed}); err != nil {
		t.Fatalf("encode avif: %v", err)
	}

	// Names sort so page 1 is the PNG, 2 the WebP, 3 the AVIF.
	write := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(srcDir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("1.png", pngBuf.Bytes())
	write("2.webp", webpBuf.Bytes())
	write("3.avif", avifBuf.Bytes())

	out := filepath.Join(t.TempDir(), "book.cbz")
	if _, err := Run(context.Background(), srcDir, out, api.ImportOptions{Name: "Mixed", Encode: "avif", Quality: 60}, nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	entries := map[string][]byte{}
	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatalf("open cbz: %v", err)
	}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		entries[f.Name] = b
	}
	zr.Close()

	// The PNG became AVIF; the already-good WebP and AVIF kept their format.
	if _, ok := entries["01.avif"]; !ok {
		t.Errorf("PNG page was not re-encoded to avif; entries: %v", keys(entries))
	}
	if got, ok := entries["02.webp"]; !ok {
		t.Errorf("WebP page did not keep its .webp extension; entries: %v", keys(entries))
	} else if !bytes.Equal(got, webpBuf.Bytes()) {
		t.Error("WebP page was re-encoded rather than copied verbatim")
	}
	if got, ok := entries["03.avif"]; !ok {
		t.Errorf("AVIF page did not keep its .avif extension; entries: %v", keys(entries))
	} else if !bytes.Equal(got, avifBuf.Bytes()) {
		t.Error("AVIF page was re-encoded rather than copied verbatim")
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
