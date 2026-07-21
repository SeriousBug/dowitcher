package library

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/store"
)

// pdfRecorder counts OnPDF hand-offs and remembers the paths.
type pdfRecorder struct {
	mu    sync.Mutex
	paths []string
}

func (r *pdfRecorder) fn(p string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = append(r.paths, p)
}

func (r *pdfRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.paths)
}

// newPDFLib is newTestLib with an OnPDF callback wired.
func newPDFLib(t *testing.T) (*Library, string, *pdfRecorder) {
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
	rec := &pdfRecorder{}
	l := New(st, Config{Root: root, DataDir: data, OnPDF: rec.fn}, func(api.LibraryStatus) {})
	return l, root, rec
}

// TestScanHandsOffPDFOnce: a PDF under the root reaches OnPDF, but a re-walk of
// an unchanged file does not hand it off again — the dedupe map absorbs the
// repeated sweep. A changed file (new modtime) is handed off afresh.
func TestScanHandsOffPDFOnce(t *testing.T) {
	l, root, rec := newPDFLib(t)
	ctx := context.Background()

	pdfPath := filepath.Join(root, "book.pdf")
	if err := os.WriteFile(pdfPath, []byte("%PDF-1.4 one"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := l.Scan(ctx); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("OnPDF called %d times on first scan, want 1", rec.count())
	}
	if rec.paths[0] != pdfPath {
		t.Fatalf("OnPDF path = %q, want the absolute path %q", rec.paths[0], pdfPath)
	}

	// A second sweep of the same file must not re-queue it.
	if err := l.Scan(ctx); err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("OnPDF called %d times across two sweeps, want 1 — dedupe failed", rec.count())
	}

	// Changing the file makes it new again.
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(pdfPath, []byte("%PDF-1.4 two, longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := l.Scan(ctx); err != nil {
		t.Fatalf("third scan: %v", err)
	}
	if rec.count() != 2 {
		t.Fatalf("a changed PDF must be handed off again, got %d hand-offs", rec.count())
	}
}

// TestScanStillReconcilesCBZ: adding PDF detection did not break the CBZ path —
// a .cbz under the root is still reconciled as a server-wide library comic, and
// is not mistaken for a PDF.
func TestScanStillReconcilesCBZ(t *testing.T) {
	l, root, rec := newPDFLib(t)
	writeCBZ(t, root, "Book 01.cbz", 2, 10)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if rec.count() != 0 {
		t.Fatalf("a CBZ must not be handed off as a PDF, got %d", rec.count())
	}
	row, err := l.st.ComicRowByPath("Book 01.cbz")
	if err != nil {
		t.Fatalf("the CBZ should reconcile as a comic: %v", err)
	}
	if row.Source != store.SourceLibrary {
		t.Fatalf("source = %q, want library", row.Source)
	}
}
