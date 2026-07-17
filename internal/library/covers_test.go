package library

import (
	"bytes"
	"context"
	"image"
	"os"
	"path/filepath"
	"testing"
)

func TestScanCachesCoversKeyedByHash(t *testing.T) {
	l, st, root, _ := newTestLib(t)
	writeCBZ(t, root, "Cover Test 01.cbz", 3, 40)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	row := comicAt(t, st, "Cover Test 01.cbz")

	p := l.CoverPath(row.ContentHash)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("cover not cached at %s: %v", p, err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("cached cover is not a decodable image: %v", err)
	}
	if format != "jpeg" {
		t.Errorf("cover format = %q, want jpeg (%s is what the handler advertises)", format, CoverContentType)
	}
	// The fixture pages are narrower than the thumbnail target, so the cover
	// keeps its size rather than being upscaled.
	if cfg.Width != 24 || cfg.Height != 32 {
		t.Errorf("cover = %dx%d, want 24x32", cfg.Width, cfg.Height)
	}
	// Sharded so a big library does not put every cover in one directory.
	if got := filepath.Base(filepath.Dir(p)); got != row.ContentHash[:2] {
		t.Errorf("cover shard dir = %q, want %q", got, row.ContentHash[:2])
	}
}

// TestCoverSurvivesRename is the cover half of the rename design: the cache is
// keyed by content hash, so moving a file costs no re-decode. That matters
// because a full AVIF decode is ~800ms, and re-thumbnailing a library after a
// reorganisation would be hours of work for identical bytes.
func TestCoverSurvivesRename(t *testing.T) {
	l, st, root, _ := newTestLib(t)
	writeCBZ(t, root, "old name.cbz", 2, 65)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	row := comicAt(t, st, "old name.cbz")
	coverPath := l.CoverPath(row.ContentHash)
	before, err := os.ReadFile(coverPath)
	if err != nil {
		t.Fatalf("read cover: %v", err)
	}
	// Rewriting the cache entry with a marker proves the rescan reuses it
	// rather than regenerating over the top.
	if err := os.WriteFile(coverPath, before, 0o644); err != nil {
		t.Fatalf("write cover: %v", err)
	}
	stat, err := os.Stat(coverPath)
	if err != nil {
		t.Fatalf("stat cover: %v", err)
	}

	if err := os.Rename(filepath.Join(root, "old name.cbz"), filepath.Join(root, "new name.cbz")); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("rescan: %v", err)
	}

	after := comicAt(t, st, "new name.cbz")
	if after.ContentHash != row.ContentHash {
		t.Fatalf("content hash changed on rename: the cover cache would have been orphaned")
	}
	stat2, err := os.Stat(l.CoverPath(after.ContentHash))
	if err != nil {
		t.Fatalf("cover orphaned by rename: %v", err)
	}
	if !stat2.ModTime().Equal(stat.ModTime()) {
		t.Error("cover regenerated on a rename, though the bytes did not change")
	}
}

func TestCoverRegeneratesOnHashChange(t *testing.T) {
	l, st, root, _ := newTestLib(t)
	writeCBZ(t, root, "book.cbz", 2, 30)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	first := comicAt(t, st, "book.cbz")
	firstCover, err := os.ReadFile(l.CoverPath(first.ContentHash))
	if err != nil {
		t.Fatalf("read cover: %v", err)
	}

	// Replaced in place with different pages.
	writeCBZ(t, root, "book.cbz", 2, 200)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	second := comicAt(t, st, "book.cbz")
	if second.ContentHash == first.ContentHash {
		t.Fatal("content hash unchanged after replacing the file")
	}
	secondCover, err := os.ReadFile(l.CoverPath(second.ContentHash))
	if err != nil {
		t.Fatalf("cover not regenerated for the new hash: %v", err)
	}
	if bytes.Equal(firstCover, secondCover) {
		t.Error("regenerated cover is identical to the old one")
	}
}

func TestCoverGeneratesOnDemandWhenNotCached(t *testing.T) {
	l, st, root, _ := newTestLib(t)
	writeCBZ(t, root, "ondemand.cbz", 2, 55)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	row := comicAt(t, st, "ondemand.cbz")
	// A comic the cover pass never reached: a cancelled scan, or a row the
	// import pipeline wrote directly.
	if err := os.RemoveAll(filepath.Join(l.cfg.DataDir, "covers")); err != nil {
		t.Fatalf("clear cache: %v", err)
	}

	b, err := l.Cover(context.Background(), row.ID)
	if err != nil {
		t.Fatalf("cover on demand: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("empty cover")
	}
	if _, _, err := image.DecodeConfig(bytes.NewReader(b)); err != nil {
		t.Fatalf("on-demand cover is not an image: %v", err)
	}
	// Having generated it, it is cached for next time.
	if _, err := os.Stat(l.CoverPath(row.ContentHash)); err != nil {
		t.Errorf("on-demand cover not written to the cache: %v", err)
	}
}

func TestCoverUnknownComic(t *testing.T) {
	l, _, _, _ := newTestLib(t)
	if _, err := l.Cover(context.Background(), "nope"); err == nil {
		t.Fatal("want an error for an unknown comic")
	}
}
