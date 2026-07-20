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

// makeJPEG encodes a distinct JPEG per seed so the extracted pages can be told
// apart and matched back to the page they came from.
func makeJPEG(t *testing.T, seed int64) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, synth(400, 600, seed, 0), &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

// buildPDF writes a PDF whose pages each embed one of imgs, the shape a scanned
// comic PDF has. pdfcpu stores a JPEG as a DCTDecode stream verbatim, so the
// bytes come back out of ExtractPDF unchanged.
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

func TestExtractPDFOrderAndLossless(t *testing.T) {
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "book.pdf")
	pages := [][]byte{
		makeJPEG(t, 1),
		makeJPEG(t, 2),
		makeJPEG(t, 3),
	}
	buildPDF(t, pdfPath, pages)

	destDir := t.TempDir()
	n, err := ExtractPDF(context.Background(), pdfPath, destDir, 8<<30, nil)
	if err != nil {
		t.Fatalf("ExtractPDF: %v", err)
	}
	if n != len(pages) {
		t.Fatalf("extracted %d images, want %d", n, len(pages))
	}

	// collect natural-sorts by name, which is the order the pipeline reads pages
	// in — so extracted file i must be the JPEG embedded on page i+1.
	files, err := collect(destDir)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(files) != len(pages) {
		t.Fatalf("collect found %d files, want %d", len(files), len(pages))
	}
	for i, f := range files {
		if ext := filepath.Ext(f.abs); ext != ".jpg" {
			t.Errorf("page %d extension = %q, want .jpg", i, ext)
		}
		got, err := os.ReadFile(f.abs)
		if err != nil {
			t.Fatalf("read extracted page %d: %v", i, err)
		}
		if !bytes.Equal(got, pages[i]) {
			t.Errorf("page %d bytes differ from the embedded JPEG (extraction should be lossless)", i)
		}
	}
}

func TestExtractPDFRejectsNonPDF(t *testing.T) {
	dir := t.TempDir()
	// A file named .pdf that is not one — the extension is not trusted.
	bad := filepath.Join(dir, "notreally.pdf")
	if err := os.WriteFile(bad, []byte("this is plainly not a PDF\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractPDF(context.Background(), bad, t.TempDir(), 8<<30, nil); !errors.Is(err, ErrNotPDF) {
		t.Fatalf("err = %v, want ErrNotPDF", err)
	}
}

func TestExtractPDFRejectsTruncated(t *testing.T) {
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "book.pdf")
	buildPDF(t, pdfPath, [][]byte{makeJPEG(t, 1), makeJPEG(t, 2)})

	full, err := os.ReadFile(pdfPath)
	if err != nil {
		t.Fatal(err)
	}
	// Cut the file off mid-download, the way two of the real fixtures were
	// broken. pdfcpu opens it and errors rather than yielding pages.
	truncated := filepath.Join(dir, "truncated.pdf")
	if err := os.WriteFile(truncated, full[:len(full)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractPDF(context.Background(), truncated, t.TempDir(), 8<<30, nil); !errors.Is(err, ErrNotPDF) {
		t.Fatalf("err = %v, want ErrNotPDF", err)
	}
}

func TestExtractPDFBudget(t *testing.T) {
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "book.pdf")
	pages := [][]byte{makeJPEG(t, 1), makeJPEG(t, 2), makeJPEG(t, 3)}
	buildPDF(t, pdfPath, pages)

	// A budget below the first page's size must be refused rather than written
	// past — the PDF-bomb guard.
	if _, err := ExtractPDF(context.Background(), pdfPath, t.TempDir(), 10, nil); !errors.Is(err, ErrPDFTooBig) {
		t.Fatalf("err = %v, want ErrPDFTooBig", err)
	}
}
