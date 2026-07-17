package library

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/SeriousBug/dowitcher/internal/store"
)

// --- Case 1: new path, new hash -> insert ---

func TestScanInsertsNewComics(t *testing.T) {
	l, st, root, _ := newTestLib(t)
	writeCBZ(t, root, "Series One 01.cbz", 3, 10)
	writeCBZ(t, root, "nested/Series Two 02.cbz", 2, 60)

	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}

	paths, err := st.ListComicPaths()
	if err != nil {
		t.Fatalf("list paths: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("want 2 comics, got %d: %v", len(paths), paths)
	}
	// Paths are stored slash-separated and relative to the root, so the same DB
	// works whatever the mount point is.
	row := comicAt(t, st, "nested/Series Two 02.cbz")
	if row.PageCount != 2 {
		t.Errorf("page count = %d, want 2", row.PageCount)
	}
	if row.Series != "Series Two" || row.Number != "2" {
		t.Errorf("metadata = %q/%q, want %q/%q", row.Series, row.Number, "Series Two", "2")
	}
	if row.ContentHash == "" {
		t.Error("content hash not recorded; rename detection depends on it")
	}
	if row.Source != store.SourceLibrary || row.OwnerID != "" {
		t.Errorf("source/owner = %q/%q, want %q/empty", row.Source, row.OwnerID, store.SourceLibrary)
	}
	if row.Missing {
		t.Error("freshly scanned comic marked missing")
	}
}

// --- Case 2: known path, changed hash -> update in place, keep id ---

func TestScanReplacedInPlaceKeepsIDAndProgress(t *testing.T) {
	l, st, root, _ := newTestLib(t)
	u := mustUser(t, st)
	writeCBZ(t, root, "Book 01.cbz", 2, 10)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	before := comicAt(t, st, "Book 01.cbz")
	if err := st.SetComicTags(u.ID, before.ID, []string{"keep-me"}); err != nil {
		t.Fatalf("set tags: %v", err)
	}
	if _, err := st.SetProgress(u.ID, before.ID, 1, false); err != nil {
		t.Fatalf("set progress: %v", err)
	}

	// Same name, different contents: a rescan with more pages, which is what a
	// user replacing a file in place looks like.
	writeCBZ(t, root, "Book 01.cbz", 5, 90)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("rescan: %v", err)
	}

	after := comicAt(t, st, "Book 01.cbz")
	if after.ID != before.ID {
		t.Fatalf("id changed on in-place replace: %q -> %q", before.ID, after.ID)
	}
	if after.ContentHash == before.ContentHash {
		t.Error("content hash unchanged after the file was replaced")
	}
	if after.PageCount != 5 {
		t.Errorf("page count = %d, want 5 (metadata not refreshed)", after.PageCount)
	}
	c, err := st.GetComic(u.ID, after.ID)
	if err != nil {
		t.Fatalf("get comic: %v", err)
	}
	if len(c.Tags) != 1 || c.Tags[0] != "keep-me" {
		t.Errorf("tags = %v, want [keep-me]", c.Tags)
	}
	if _, err := st.GetProgress(u.ID, after.ID); err != nil {
		t.Errorf("progress lost on in-place replace: %v", err)
	}
}

// --- Case 3: new path, known hash, old path gone -> move ---

// TestScanRenamePreservesTagsAndProgress is the test the identity design exists
// for. Reorganising a library is the single most common thing a user does to it,
// and it must not cost them their tags or their place in a book.
func TestScanRenamePreservesTagsAndProgress(t *testing.T) {
	l, st, root, _ := newTestLib(t)
	u := mustUser(t, st)
	writeCBZ(t, root, "Untitled Scan.cbz", 4, 33)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}

	before := comicAt(t, st, "Untitled Scan.cbz")
	if err := st.SetComicTags(u.ID, before.ID, []string{"favourite", "sci-fi"}); err != nil {
		t.Fatalf("set tags: %v", err)
	}
	if _, err := st.SetProgress(u.ID, before.ID, 3, true); err != nil {
		t.Fatalf("set progress: %v", err)
	}

	// The user files it away properly: new folder, new name, same bytes.
	oldPath := filepath.Join(root, "Untitled Scan.cbz")
	newPath := filepath.Join(root, "Proper Series", "Proper Series 07.cbz")
	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatalf("rename: %v", err)
	}

	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("rescan: %v", err)
	}

	paths, err := st.ListComicPaths()
	if err != nil {
		t.Fatalf("list paths: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("rename produced %d rows, want 1 (the row was duplicated, not moved): %v", len(paths), paths)
	}
	after := comicAt(t, st, "Proper Series/Proper Series 07.cbz")
	if after.ID != before.ID {
		t.Fatalf("id changed across rename: %q -> %q — tags and progress are keyed on it", before.ID, after.ID)
	}
	if after.Missing {
		t.Error("moved comic left flagged missing")
	}
	// The filename is where most metadata comes from, so a rename has to
	// refresh it even though the bytes are identical.
	if after.Series != "Proper Series" || after.Number != "7" {
		t.Errorf("metadata not refreshed on rename: series=%q number=%q", after.Series, after.Number)
	}

	c, err := st.GetComic(u.ID, after.ID)
	if err != nil {
		t.Fatalf("get comic: %v", err)
	}
	if len(c.Tags) != 2 || c.Tags[0] != "favourite" || c.Tags[1] != "sci-fi" {
		t.Errorf("tags lost across rename: %v, want [favourite sci-fi]", c.Tags)
	}
	p, err := st.GetProgress(u.ID, after.ID)
	if err != nil {
		t.Fatalf("progress lost across rename: %v", err)
	}
	if p.Page != 3 || !p.Completed {
		t.Errorf("progress = page %d completed %v, want page 3 completed true", p.Page, p.Completed)
	}
}

// TestScanCopyIsNotAMove covers the trap in the hash fallback: identical
// content whose original is still on disk is a second copy, not a move, and
// repointing the original's row at it would make the first file vanish from the
// library.
func TestScanCopyIsNotAMove(t *testing.T) {
	l, st, root, _ := newTestLib(t)
	writeCBZ(t, root, "Original.cbz", 3, 44)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	first := comicAt(t, st, "Original.cbz")

	// Same bytes, both present.
	writeCBZ(t, root, "backup/Original.cbz", 3, 44)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("rescan: %v", err)
	}

	paths, err := st.ListComicPaths()
	if err != nil {
		t.Fatalf("list paths: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("want 2 rows for two copies, got %d: %v", len(paths), paths)
	}
	still := comicAt(t, st, "Original.cbz")
	if still.ID != first.ID {
		t.Errorf("the original's row was repointed at the copy: %q -> %q", first.ID, still.ID)
	}
	if still.Missing {
		t.Error("original flagged missing though its file is still there")
	}
}

// --- Case 4: known path, gone from disk -> missing, never deleted ---

func TestScanFlagsMissingAndClearsOnReturn(t *testing.T) {
	l, st, root, _ := newTestLib(t)
	u := mustUser(t, st)
	writeCBZ(t, root, "Gone 01.cbz", 2, 20)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	row := comicAt(t, st, "Gone 01.cbz")
	if err := st.SetComicTags(u.ID, row.ID, []string{"unmounted"}); err != nil {
		t.Fatalf("set tags: %v", err)
	}

	// The volume goes away.
	if err := os.Remove(filepath.Join(root, "Gone 01.cbz")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("rescan: %v", err)
	}

	gone, err := st.ComicRowByPath("Gone 01.cbz")
	if err != nil {
		t.Fatalf("row deleted rather than flagged missing — tags and progress are gone for good: %v", err)
	}
	if !gone.Missing {
		t.Error("missing flag not set")
	}
	if n := l.Status().ComicCount; n != 0 {
		t.Errorf("comic count = %d, want 0 while the file is away", n)
	}

	// The volume comes back.
	writeCBZ(t, root, "Gone 01.cbz", 2, 20)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	back := comicAt(t, st, "Gone 01.cbz")
	if back.Missing {
		t.Error("missing flag not cleared after the file reappeared")
	}
	if back.ID != row.ID {
		t.Errorf("id changed across the absence: %q -> %q", row.ID, back.ID)
	}
	c, err := st.GetComic(u.ID, back.ID)
	if err != nil {
		t.Fatalf("get comic: %v", err)
	}
	if len(c.Tags) != 1 || c.Tags[0] != "unmounted" {
		t.Errorf("tags = %v, want [unmounted] — the whole point of not deleting", c.Tags)
	}
}

// TestScanEmptyRootDoesNotDeleteAnything is the disaster case in miniature: a
// mount that is not ready yet reads as an empty library.
func TestScanEmptyRootFlagsRatherThanDeletes(t *testing.T) {
	l, st, root, _ := newTestLib(t)
	writeCBZ(t, root, "a.cbz", 1, 5)
	writeCBZ(t, root, "b.cbz", 1, 90)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, n := range []string{"a.cbz", "b.cbz"} {
		if err := os.Remove(filepath.Join(root, n)); err != nil {
			t.Fatalf("remove: %v", err)
		}
	}
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	paths, err := st.ListComicPaths()
	if err != nil {
		t.Fatalf("list paths: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("rows dropped on an empty root: %d left, want 2 flagged missing", len(paths))
	}
}

// --- File selection ---

func TestScanAcceptsMisnamedZipAndRejectsNonComics(t *testing.T) {
	l, st, root, _ := newTestLib(t)
	// A comic under the generic extension, which is how plenty of them arrive.
	writeRaw(t, root, "Misnamed 03.zip", cbzBytes(t, 2, 70))
	// A zip of documents that happens to live in the library.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("notes.txt")
	w.Write([]byte("not a comic"))
	zw.Close()
	writeRaw(t, root, "documents.zip", buf.Bytes())
	// Not a zip at all.
	writeRaw(t, root, "broken.cbz", []byte("garbage"))
	// Ignored by extension.
	writeRaw(t, root, "cover.jpg", jpegBytes(t, 1))

	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	paths, err := st.ListComicPaths()
	if err != nil {
		t.Fatalf("list paths: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("want only the misnamed comic, got %d: %v", len(paths), paths)
	}
	if _, ok := paths["Misnamed 03.zip"]; !ok {
		t.Errorf("misnamed .zip comic not picked up: %v", paths)
	}
}

func TestScanSkipsJunkDirectories(t *testing.T) {
	l, st, root, _ := newTestLib(t)
	writeCBZ(t, root, "real.cbz", 1, 10)
	writeCBZ(t, root, "@eaDir/thumb.cbz", 1, 20)
	writeCBZ(t, root, ".trash/old.cbz", 1, 30)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	paths, err := st.ListComicPaths()
	if err != nil {
		t.Fatalf("list paths: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("want 1 comic, got %d: %v", len(paths), paths)
	}
}

// --- Concurrency and lifecycle ---

func TestScanIsSingleFlight(t *testing.T) {
	l, _, root, _ := newTestLib(t)
	writeCBZ(t, root, "a.cbz", 1, 10)

	l.scanning.Lock()
	defer l.scanning.Unlock()
	if err := l.Scan(context.Background()); !errors.Is(err, ErrScanning) {
		t.Errorf("second concurrent scan: err = %v, want ErrScanning", err)
	}
}

func TestScanRespectsCancellation(t *testing.T) {
	l, st, root, _ := newTestLib(t)
	for i := range 40 {
		writeCBZ(t, root, pageName(i)+".cbz", 2, uint8(i))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := l.Scan(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("scan of a cancelled context: err = %v, want context.Canceled", err)
	}
	// A cancelled scan must not conclude that the library is empty and flag
	// everything it never got to.
	paths, err := st.ListComicPaths()
	if err != nil {
		t.Fatalf("list paths: %v", err)
	}
	if len(paths) != 0 {
		t.Errorf("cancelled scan wrote %d rows", len(paths))
	}
	if l.Status().Scanning {
		t.Error("status stuck at scanning after cancellation")
	}
}

func TestScanReportsProgress(t *testing.T) {
	l, _, root, rec := newTestLib(t)
	for i := range 5 {
		writeCBZ(t, root, pageName(i)+".cbz", 1, uint8(i*20))
	}
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	msgs := rec.all()
	if len(msgs) < 2 {
		t.Fatalf("want at least a start and a finish status, got %d", len(msgs))
	}
	if !msgs[0].Scanning || msgs[0].Total != 5 {
		t.Errorf("first status = %+v, want scanning with total 5", msgs[0])
	}
	last := msgs[len(msgs)-1]
	if last.Scanning {
		t.Error("final status still says scanning")
	}
	if last.ComicCount != 5 {
		t.Errorf("final comic count = %d, want 5", last.ComicCount)
	}
	if last.LastScan == 0 {
		t.Error("final status has no last-scan time")
	}
	if last.Root != root {
		t.Errorf("status root = %q, want %q", last.Root, root)
	}
}

// TestScanDoesNotTouchUploads makes sure the scanner leaves the import
// pipeline's rows alone: two writers on one row would take turns undoing each
// other's ownership fields.
func TestScanLeavesUploadRowsAlone(t *testing.T) {
	l, st, root, _ := newTestLib(t)
	u := mustUser(t, st)
	writeCBZ(t, root, "uploaded.cbz", 2, 50)
	if err := st.UpsertComic(store.ComicRow{
		ID: store.NewID(), Path: "uploaded.cbz", Title: "As Uploaded",
		OwnerID: u.ID, Source: store.SourceUpload,
	}); err != nil {
		t.Fatalf("seed upload row: %v", err)
	}

	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	row := comicAt(t, st, "uploaded.cbz")
	if row.Source != store.SourceUpload || row.OwnerID != u.ID {
		t.Errorf("scanner claimed an upload row: source=%q owner=%q", row.Source, row.OwnerID)
	}
	if row.Title != "As Uploaded" {
		t.Errorf("scanner overwrote an upload's metadata: title=%q", row.Title)
	}
}
