package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/SeriousBug/dowitcher/internal/store"
)

// TestScanKeepsAClaim: the scanner is still the writer of a claimed comic's
// metadata, because the file never left the root — but it must never be the
// thing that decides who owns it.
func TestScanKeepsAClaim(t *testing.T) {
	l, st, root, _ := newTestLib(t)
	u := mustUser(t, st)
	writeCBZ(t, root, "Book 01.cbz", 2, 10)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	before := comicAt(t, st, "Book 01.cbz")
	if err := st.ClaimComic(u.ID, before.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// A rescan of an untouched file leaves the claim alone.
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	assertClaimed(t, st, "Book 01.cbz", before.ID, u.ID)

	// So does a replace in place, which still refreshes the metadata: the row is
	// the scanner's to update and the claim is not part of what it updates.
	writeCBZ(t, root, "Book 01.cbz", 5, 90)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("rescan after replace: %v", err)
	}
	row := assertClaimed(t, st, "Book 01.cbz", before.ID, u.ID)
	if row.PageCount != 5 {
		t.Fatalf("page count = %d, want 5: a claimed comic stopped being refreshed", row.PageCount)
	}
}

// TestScanRenameKeepsAClaim is the leak this guards: if a renamed claimed file
// were inserted as a new row rather than repointed, the comic would reappear in
// everyone's library as a fresh library comic and the rename would have
// silently undone the claim.
func TestScanRenameKeepsAClaim(t *testing.T) {
	l, st, root, _ := newTestLib(t)
	u := mustUser(t, st)
	writeCBZ(t, root, "Untitled.cbz", 4, 33)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	before := comicAt(t, st, "Untitled.cbz")
	if err := st.ClaimComic(u.ID, before.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}

	newPath := filepath.Join(root, "Proper", "Proper 07.cbz")
	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Rename(filepath.Join(root, "Untitled.cbz"), newPath); err != nil {
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
		t.Fatalf("renaming a claimed comic produced %d rows, want 1: %v", len(paths), paths)
	}
	assertClaimed(t, st, "Proper/Proper 07.cbz", before.ID, u.ID)
}

// TestScanFlagsAMissingClaim: a claimed row is still diffed against the
// filesystem, so a deleted file is flagged rather than left looking readable.
func TestScanFlagsAMissingClaim(t *testing.T) {
	l, st, root, _ := newTestLib(t)
	u := mustUser(t, st)
	writeCBZ(t, root, "Gone.cbz", 2, 20)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	row := comicAt(t, st, "Gone.cbz")
	if err := st.ClaimComic(u.ID, row.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "Gone.cbz")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	gone, err := st.ComicRowByPath("Gone.cbz")
	if err != nil {
		t.Fatalf("row: %v", err)
	}
	if !gone.Missing {
		t.Fatal("a claimed comic whose file vanished was never flagged missing")
	}
	if gone.Source != store.SourceClaimed || gone.OwnerID != u.ID {
		t.Fatalf("the missing sweep dropped the claim: source %q owner %q", gone.Source, gone.OwnerID)
	}
}

func assertClaimed(t *testing.T, st *store.Store, path, wantID, wantOwner string) store.ComicRow {
	t.Helper()
	row, err := st.ComicRowByPath(path)
	if err != nil {
		t.Fatalf("row at %s: %v", path, err)
	}
	if row.ID != wantID {
		t.Fatalf("id at %s = %q, want %q: the row was replaced rather than kept", path, row.ID, wantID)
	}
	if row.Source != store.SourceClaimed || row.OwnerID != wantOwner {
		t.Fatalf("row at %s = source %q owner %q, want claimed/%s", path, row.Source, row.OwnerID, wantOwner)
	}
	return row
}
