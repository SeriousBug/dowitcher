package imports

import (
	"bytes"
	"context"
	"errors"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"testing"

	pdfapi "github.com/pdfcpu/pdfcpu/pkg/api"
)

// makeJPEG encodes a distinct block-patterned JPEG per seed. The 8x8 block
// pattern survives the 64x64 grayscale thumbnail, so a rasterised page can be
// matched back to the source image it was rendered from even though rendering
// is not byte-preserving.
func makeJPEG(t *testing.T, seed int64) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, synth(400, 600, seed, 0), &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

// buildPDF writes a PDF whose pages each embed one of imgs, the shape a scanned
// comic PDF has.
func buildPDF(t *testing.T, path string, imgs [][]byte) {
	t.Helper()
	readers := make([]io.Reader, len(imgs))
	for i := range imgs {
		readers[i] = bytes.NewReader(imgs[i])
	}
	out, err := os.Create(path)
	if err != nil {
		t.Fatalf("create pdf: %v", err)
	}
	if err := pdfapi.ImportImages(nil, out, readers, nil, nil); err != nil {
		out.Close()
		t.Fatalf("import images into pdf: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close pdf: %v", err)
	}
}

// bestMatch returns the index of the source thumbnail closest to page, the way
// the rasterised page of source i should be nearest source i.
func bestMatch(t *testing.T, page []byte, srcThumbs [][]byte) int {
	t.Helper()
	_, th, err := thumbnail(page)
	if err != nil {
		t.Fatalf("thumbnail rendered page: %v", err)
	}
	best, bestErr := -1, 0.0
	for i, s := range srcThumbs {
		if d := mae(th, s); best == -1 || d < bestErr {
			best, bestErr = i, d
		}
	}
	return best
}

func TestRasterizePDFOrderAndPageCount(t *testing.T) {
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "book.pdf")
	pages := [][]byte{
		makeJPEG(t, 1),
		makeJPEG(t, 2),
		makeJPEG(t, 3),
	}
	buildPDF(t, pdfPath, pages)

	destDir := t.TempDir()
	n, err := RasterizePDF(context.Background(), pdfPath, destDir, 8<<30, nil)
	if err != nil {
		t.Fatalf("RasterizePDF: %v", err)
	}
	if n != len(pages) {
		t.Fatalf("rasterised %d pages, want %d", n, len(pages))
	}

	// collect natural-sorts by name, which is the order the pipeline reads pages
	// in — so rasterised file i must be rendered from the image embedded on page
	// i+1. Rendering is not byte-preserving, so order is checked by matching each
	// page's thumbnail back to its source rather than by comparing bytes.
	files, err := collect(destDir)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(files) != len(pages) {
		t.Fatalf("collect found %d files, want %d", len(files), len(pages))
	}

	srcThumbs := make([][]byte, len(pages))
	for i := range pages {
		_, th, err := thumbnail(pages[i])
		if err != nil {
			t.Fatalf("thumbnail source %d: %v", i, err)
		}
		srcThumbs[i] = th
	}

	for i, f := range files {
		if ext := filepath.Ext(f.abs); ext != ".jpg" {
			t.Errorf("page %d extension = %q, want .jpg", i, ext)
		}
		buf, err := os.ReadFile(f.abs)
		if err != nil {
			t.Fatalf("read rasterised page %d: %v", i, err)
		}
		if got := bestMatch(t, buf, srcThumbs); got != i {
			t.Errorf("rasterised page %d best-matches source %d, want %d (pages out of order)", i, got, i)
		}
	}
}

func TestRasterizePDFRejectsNonPDF(t *testing.T) {
	dir := t.TempDir()
	// A file named .pdf that is not one — the extension is not trusted.
	bad := filepath.Join(dir, "notreally.pdf")
	if err := os.WriteFile(bad, []byte("this is plainly not a PDF\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RasterizePDF(context.Background(), bad, t.TempDir(), 8<<30, nil); !errors.Is(err, ErrNotPDF) {
		t.Fatalf("err = %v, want ErrNotPDF", err)
	}
}

func TestRasterizePDFRejectsTruncated(t *testing.T) {
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "book.pdf")
	buildPDF(t, pdfPath, [][]byte{makeJPEG(t, 1), makeJPEG(t, 2)})

	full, err := os.ReadFile(pdfPath)
	if err != nil {
		t.Fatal(err)
	}
	// Cut the file off mid-download, the way two of the real fixtures were
	// broken. pdfium refuses it as "incorrect format" rather than rendering.
	truncated := filepath.Join(dir, "truncated.pdf")
	if err := os.WriteFile(truncated, full[:len(full)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RasterizePDF(context.Background(), truncated, t.TempDir(), 8<<30, nil); !errors.Is(err, ErrNotPDF) {
		t.Fatalf("err = %v, want ErrNotPDF", err)
	}
}

func TestRasterizePDFBudget(t *testing.T) {
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "book.pdf")
	pages := [][]byte{makeJPEG(t, 1), makeJPEG(t, 2), makeJPEG(t, 3)}
	buildPDF(t, pdfPath, pages)

	// A budget below one rendered page must be refused rather than written past —
	// the PDF-bomb guard.
	if _, err := RasterizePDF(context.Background(), pdfPath, t.TempDir(), 10, nil); !errors.Is(err, ErrPDFTooBig) {
		t.Fatalf("err = %v, want ErrPDFTooBig", err)
	}
}
