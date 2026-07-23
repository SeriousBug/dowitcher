package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/store"
)

// newArchiveLib wires both OnPDF and OnArchive to separate recorders, so a test
// can prove a dropped file reaches the right one.
func newArchiveLib(t *testing.T) (*Library, string, *pdfRecorder, *pdfRecorder) {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "library")
	data := filepath.Join(dir, "data")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	pdfRec, archRec := &pdfRecorder{}, &pdfRecorder{}
	l := New(st, Config{Root: root, DataDir: data, OnPDF: pdfRec.fn, OnArchive: archRec.fn},
		func(api.LibraryStatus) {})
	return l, root, pdfRec, archRec
}

// TestScanHandsOffArchive: a CBR under the root reaches OnArchive (not OnPDF),
// deduped across sweeps the same way a PDF is.
func TestScanHandsOffArchive(t *testing.T) {
	l, root, pdfRec, archRec := newArchiveLib(t)
	ctx := context.Background()

	cbrPath := filepath.Join(root, "book.cbr")
	if err := os.WriteFile(cbrPath, []byte("Rar!\x1a\x07\x00 fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := l.Scan(ctx); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if archRec.count() != 1 {
		t.Fatalf("OnArchive called %d times, want 1", archRec.count())
	}
	if pdfRec.count() != 0 {
		t.Fatalf("OnPDF called %d times for a .cbr, want 0", pdfRec.count())
	}
	if archRec.paths[0] != cbrPath {
		t.Fatalf("OnArchive path = %q, want %q", archRec.paths[0], cbrPath)
	}

	// A re-walk of the unchanged file must not hand it off again.
	if err := l.Scan(ctx); err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if archRec.count() != 1 {
		t.Fatalf("OnArchive called %d times across two sweeps, want 1 — dedupe failed", archRec.count())
	}
}

func TestArchiveDetection(t *testing.T) {
	convertible := []string{"a.cbr", "b.rar", "c.cb7", "d.7z", "e.cbt", "f.tar", "g.pdf"}
	for _, n := range convertible {
		if !isConvertible(n) {
			t.Errorf("isConvertible(%q) = false, want true", n)
		}
	}
	// A CBZ/ZIP is a scanner candidate, never a convertible: it is already the
	// serving format.
	for _, n := range []string{"x.cbz", "y.zip"} {
		if isConvertible(n) {
			t.Errorf("isConvertible(%q) = true, want false (it is a candidate)", n)
		}
		if !isCandidate(n) {
			t.Errorf("isCandidate(%q) = false, want true", n)
		}
	}
	// isArchive excludes PDFs — those are isPDF's job.
	if isArchive("a.pdf") {
		t.Error("isArchive(a.pdf) = true, want false")
	}
}
