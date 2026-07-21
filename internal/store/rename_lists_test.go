package store

import (
	"errors"
	"testing"
)

// TestRenameComicOverridesScannedTitle: a rename sets title_override, the read
// paths return it, and a rescan that rewrites the scanned title leaves the
// override standing.
func TestRenameComicOverridesScannedTitle(t *testing.T) {
	st := testStore(t)
	alice, err := st.CreateUser(NewID(), "alice", false)
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	row := ComicRow{ID: NewID(), Path: "Series/Issue 1.cbz", Title: "Scanned Title", Source: SourceLibrary}
	if err := st.UpsertComic(row); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := st.RenameComic(row.ID, "My Better Title"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	c, err := st.GetComic(alice.ID, row.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if c.Title != "My Better Title" {
		t.Errorf("effective title = %q, want the override", c.Title)
	}

	// The scanner re-upserts the same path with the file's title; the override
	// must survive it, the way owner_id and source do.
	row.Title = "Scanned Title Again"
	if err := st.UpsertComic(row); err != nil {
		t.Fatalf("rescan upsert: %v", err)
	}
	c, err = st.GetComic(alice.ID, row.ID)
	if err != nil {
		t.Fatalf("get after rescan: %v", err)
	}
	if c.Title != "My Better Title" {
		t.Errorf("rescan clobbered the override; title = %q", c.Title)
	}

	// The raw row still carries the scanned title, so the scanner's diff is
	// unaffected by the override.
	raw, err := st.ComicRowByID(row.ID)
	if err != nil {
		t.Fatalf("row: %v", err)
	}
	if raw.Title != "Scanned Title Again" {
		t.Errorf("raw row title = %q, want the scanned one", raw.Title)
	}

	// The renamed comic is findable by its new title.
	got, total, err := st.ListComicsFiltered(alice.ID, ComicFilter{Query: "Better"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if total != 1 || len(got) != 1 || got[0].ID != row.ID {
		t.Errorf("search by override title found %d, want the renamed comic", total)
	}
}

func TestRenameComicMissing(t *testing.T) {
	st := testStore(t)
	if err := st.RenameComic(NewID(), "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rename of a missing comic = %v, want ErrNotFound", err)
	}
}

// TestCollectionKindFilter: kind is stored, normalised, and filters the listing.
func TestCollectionKindFilter(t *testing.T) {
	st := testStore(t)
	alice, err := st.CreateUser(NewID(), "alice", false)
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}

	col, err := st.CreateCollection(NewID(), alice.ID, "A shelf", "", "", false)
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	if col.Kind != KindCollection {
		t.Errorf("default kind = %q, want %q", col.Kind, KindCollection)
	}
	list, err := st.CreateCollection(NewID(), alice.ID, "In order", "", KindReadingList, false)
	if err != nil {
		t.Fatalf("create reading list: %v", err)
	}
	if list.Kind != KindReadingList {
		t.Errorf("kind = %q, want %q", list.Kind, KindReadingList)
	}
	// An unknown kind folds down to a plain collection.
	weird, err := st.CreateCollection(NewID(), alice.ID, "Weird", "", "banana", false)
	if err != nil {
		t.Fatalf("create weird: %v", err)
	}
	if weird.Kind != KindCollection {
		t.Errorf("unknown kind = %q, want it normalised to %q", weird.Kind, KindCollection)
	}

	all, err := st.ListCollections(alice.ID, "")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("list all = %d, want 3", len(all))
	}
	lists, err := st.ListCollections(alice.ID, KindReadingList)
	if err != nil {
		t.Fatalf("list reading lists: %v", err)
	}
	if len(lists) != 1 || lists[0].ID != list.ID {
		t.Errorf("kind filter returned %d, want only the reading list", len(lists))
	}
}

// TestCollectionCoverFallbackAndPin: a collection's cover defaults to the first
// comic in order, and a pin overrides that.
func TestCollectionCoverFallbackAndPin(t *testing.T) {
	st := testStore(t)
	alice, err := st.CreateUser(NewID(), "alice", false)
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	first := ComicRow{ID: NewID(), Path: "Lib/one.cbz", Title: "One", Source: SourceLibrary}
	second := ComicRow{ID: NewID(), Path: "Lib/two.cbz", Title: "Two", Source: SourceLibrary}
	for _, r := range []ComicRow{first, second} {
		if err := st.UpsertComic(r); err != nil {
			t.Fatalf("upsert: %v", err)
		}
	}
	col, err := st.CreateCollection(NewID(), alice.ID, "Run", "", "", false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Empty collection: no cover to fall back to.
	got, err := st.GetCollection(alice.ID, col.ID)
	if err != nil {
		t.Fatalf("get empty: %v", err)
	}
	if got.CoverComicID != "" {
		t.Errorf("empty collection cover = %q, want none", got.CoverComicID)
	}

	if err := st.AddToCollection(alice.ID, col.ID, first.ID); err != nil {
		t.Fatalf("add first: %v", err)
	}
	if err := st.AddToCollection(alice.ID, col.ID, second.ID); err != nil {
		t.Fatalf("add second: %v", err)
	}
	got, err = st.GetCollection(alice.ID, col.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.CoverComicID != first.ID {
		t.Errorf("cover fallback = %q, want the first comic %q", got.CoverComicID, first.ID)
	}

	// Pinning the second comic wins over the fallback.
	if err := st.SetCollectionCover(alice.ID, col.ID, second.ID); err != nil {
		t.Fatalf("set cover: %v", err)
	}
	got, err = st.GetCollection(alice.ID, col.ID)
	if err != nil {
		t.Fatalf("get after pin: %v", err)
	}
	if got.CoverComicID != second.ID {
		t.Errorf("pinned cover = %q, want %q", got.CoverComicID, second.ID)
	}
}
