package store

import (
	"errors"
	"testing"
)

// TestSharedComicCannotBeRelaundered walks the laundering path end to end: a
// recipient of a shared upload puts it in a collection of their own and shares
// that. A third-party collection is a view of somebody else's comic, not a grant
// over it, so the uploader's unshare must still take the comic back from
// everyone.
func TestSharedComicCannotBeRelaundered(t *testing.T) {
	st := testStore(t)
	alice, _ := st.CreateUser(NewID(), "alice", false)
	bob, _ := st.CreateUser(NewID(), "bob", false)
	carol, _ := st.CreateUser(NewID(), "carol", false)

	comic := ComicRow{ID: NewID(), Path: "uploads/alice/c.cbz", Title: "C",
		OwnerID: alice.ID, Source: SourceUpload, PageCount: 4}
	if err := st.UpsertComic(comic); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// 1. Alice shares a collection holding her upload.
	aliceCol, err := st.CreateCollection(NewID(), alice.ID, "Alice shared", "", true)
	if err != nil {
		t.Fatalf("create alice collection: %v", err)
	}
	if err := st.AddToCollection(alice.ID, aliceCol.ID, comic.ID); err != nil {
		t.Fatalf("alice add: %v", err)
	}
	if _, err := st.GetComic(bob.ID, comic.ID); err != nil {
		t.Fatalf("bob should see the shared upload: %v", err)
	}

	// 2. Bob copies it into a collection of his own and shares that. He may: the
	// comic is visible to him, and a collection is a view.
	bobCol, err := st.CreateCollection(NewID(), bob.ID, "Bob shared", "", true)
	if err != nil {
		t.Fatalf("create bob collection: %v", err)
	}
	if err := st.AddToCollection(bob.ID, bobCol.ID, comic.ID); err != nil {
		t.Fatalf("bob add: %v", err)
	}
	if _, err := st.GetComic(bob.ID, comic.ID); err != nil {
		t.Fatalf("bob's own collection must not cost him the view he already had: %v", err)
	}
	if _, err := st.GetComic(carol.ID, comic.ID); err != nil {
		t.Fatalf("carol should see it while alice's share is on: %v", err)
	}

	// 3. Alice unshares. Her opt-in is the only thing that ever exposed the
	// comic, so withdrawing it must be enough, even though Bob's shared
	// collection still lists it.
	if err := st.UpdateCollection(alice.ID, aliceCol.ID, updateShared(false)); err != nil {
		t.Fatalf("unshare: %v", err)
	}
	if _, err := st.GetComic(carol.ID, comic.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unshared upload still visible via a third party's shared collection, err=%v", err)
	}
	if _, err := st.GetComic(bob.ID, comic.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bob keeps access to an unshared upload by holding it in his own collection, err=%v", err)
	}
	assertListLen(t, st, carol.ID, 0)
	assertListLen(t, st, bob.ID, 0)

	if _, err := st.GetComic(alice.ID, comic.ID); err != nil {
		t.Fatalf("owner must keep their own upload: %v", err)
	}

	// Bob's collection still contains the row; it is the visibility rule, not
	// membership, that changed.
	comics, err := st.ListComicsInCollection(bob.ID, bobCol.ID)
	if err != nil {
		t.Fatalf("list bob's collection: %v", err)
	}
	if len(comics) != 0 {
		t.Fatalf("bob's collection shows %d comics he may no longer see", len(comics))
	}
}

// TestLibraryComicStaysVisibleInAnyonesCollection guards the owner_id-matching
// arm against the obvious regression: a library comic has no owner, so the arm
// must never be what decides it. The source arm already covers it.
func TestLibraryComicStaysVisibleInAnyonesCollection(t *testing.T) {
	st := testStore(t)
	alice, _ := st.CreateUser(NewID(), "alice", false)
	bob, _ := st.CreateUser(NewID(), "bob", false)

	lib := ComicRow{ID: NewID(), Path: "Library/L.cbz", Title: "L", Source: SourceLibrary, PageCount: 3}
	if err := st.UpsertComic(lib); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	col, err := st.CreateCollection(NewID(), alice.ID, "Alice's picks", "", true)
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	if err := st.AddToCollection(alice.ID, col.ID, lib.ID); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := st.GetComic(bob.ID, lib.ID); err != nil {
		t.Fatalf("library comic in someone else's shared collection: %v", err)
	}
	if err := st.UpdateCollection(alice.ID, col.ID, updateShared(false)); err != nil {
		t.Fatalf("unshare: %v", err)
	}
	if _, err := st.GetComic(bob.ID, lib.ID); err != nil {
		t.Fatalf("library comic must stay server-wide regardless of any collection: %v", err)
	}
	assertListLen(t, st, bob.ID, 1)
}

// TestTagsArePerUser is the tag model in one test: a tag is the caller's own
// label, so seeing a comic is enough to tag it, and two users tagging the same
// comic never see each other's words.
func TestTagsArePerUser(t *testing.T) {
	st := testStore(t)
	alice, _ := st.CreateUser(NewID(), "alice", false)
	bob, _ := st.CreateUser(NewID(), "bob", false)

	comic := ComicRow{ID: NewID(), Path: "uploads/alice/t.cbz", Title: "T",
		OwnerID: alice.ID, Source: SourceUpload, PageCount: 2}
	if err := st.UpsertComic(comic); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	col, err := st.CreateCollection(NewID(), alice.ID, "Shared", "", true)
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	if err := st.AddToCollection(alice.ID, col.ID, comic.ID); err != nil {
		t.Fatalf("add: %v", err)
	}

	if err := st.SetComicTags(alice.ID, comic.ID, []string{"owner-tag"}); err != nil {
		t.Fatalf("owner should be able to tag their upload: %v", err)
	}
	// Bob can see the shared upload, so he may tag it — for himself. That is not
	// vandalism of Alice's tags, because it cannot touch them.
	if err := st.SetComicTags(bob.ID, comic.ID, []string{"bob-tag"}); err != nil {
		t.Fatalf("a reader should be able to tag a comic they can see: %v", err)
	}
	assertTags(t, st, alice.ID, comic.ID, "owner-tag")
	assertTags(t, st, bob.ID, comic.ID, "bob-tag")

	// The same word from two users is two private rows, not one shared one.
	if err := st.SetComicTags(bob.ID, comic.ID, []string{"owner-tag"}); err != nil {
		t.Fatalf("bob reuses alice's word: %v", err)
	}
	for _, u := range []string{alice.ID, bob.ID} {
		tags, err := st.ListTags(u)
		if err != nil || len(tags) != 1 || tags[0].Name != "owner-tag" || tags[0].Count != 1 {
			t.Fatalf("ListTags(%s) = %#v err=%v, want one owner-tag with count 1", u, tags, err)
		}
	}

	// Bob clearing his tags leaves Alice's alone.
	if err := st.SetComicTags(bob.ID, comic.ID, nil); err != nil {
		t.Fatalf("bob clears: %v", err)
	}
	assertTags(t, st, alice.ID, comic.ID, "owner-tag")
	assertTags(t, st, bob.ID, comic.ID)
}

// TestSetComicTagsRequiresVisibility: visibility is the only gate on tagging,
// so it has to actually hold.
func TestSetComicTagsRequiresVisibility(t *testing.T) {
	st := testStore(t)
	alice, _ := st.CreateUser(NewID(), "alice", false)
	bob, _ := st.CreateUser(NewID(), "bob", false)

	comic := ComicRow{ID: NewID(), Path: "uploads/alice/p.cbz", Title: "P",
		OwnerID: alice.ID, Source: SourceUpload, PageCount: 2}
	if err := st.UpsertComic(comic); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := st.SetComicTags(bob.ID, comic.ID, []string{"nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tagging a private upload should fail, got %v", err)
	}
}

// TestSetComicTagsOnLibraryComicIsOpen: a library comic is server-wide, so
// everyone can tag it, each in their own vocabulary.
func TestSetComicTagsOnLibraryComicIsOpen(t *testing.T) {
	st := testStore(t)
	bob, _ := st.CreateUser(NewID(), "bob", false)

	lib := ComicRow{ID: NewID(), Path: "Library/L.cbz", Title: "L", Source: SourceLibrary, PageCount: 3}
	if err := st.UpsertComic(lib); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := st.SetComicTags(bob.ID, lib.ID, []string{"shelf"}); err != nil {
		t.Fatalf("any user should be able to tag a library comic: %v", err)
	}
	assertTags(t, st, bob.ID, lib.ID, "shelf")
}

// TestListComicsFilteredGatesCollection: the filter must not reach into a
// collection the caller cannot see, even though the visibility fragment means it
// could not return anything extra today.
func TestListComicsFilteredGatesCollection(t *testing.T) {
	st := testStore(t)
	alice, _ := st.CreateUser(NewID(), "alice", false)
	bob, _ := st.CreateUser(NewID(), "bob", false)

	lib := ComicRow{ID: NewID(), Path: "Library/L.cbz", Title: "L", Source: SourceLibrary, PageCount: 3}
	if err := st.UpsertComic(lib); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// A library comic is visible to Bob on its own, so only the collection gate
	// can keep it out of a listing filtered by Alice's private collection.
	private, err := st.CreateCollection(NewID(), alice.ID, "Alice private", "", false)
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	if err := st.AddToCollection(alice.ID, private.ID, lib.ID); err != nil {
		t.Fatalf("add: %v", err)
	}

	out, total, err := st.ListComicsFiltered(bob.ID, ComicFilter{Collection: private.ID})
	if err != nil {
		t.Fatalf("filtered list: %v", err)
	}
	if len(out) != 0 || total != 0 {
		t.Fatalf("filtering by a private collection returned %d comics (total %d)", len(out), total)
	}

	// The owner still gets their own collection, and sharing opens it to Bob.
	if out, total, err = st.ListComicsFiltered(alice.ID, ComicFilter{Collection: private.ID}); err != nil || len(out) != 1 || total != 1 {
		t.Fatalf("owner filtered list = %d comics (total %d) err=%v, want 1", len(out), total, err)
	}
	if err := st.UpdateCollection(alice.ID, private.ID, updateShared(true)); err != nil {
		t.Fatalf("share: %v", err)
	}
	if out, total, err = st.ListComicsFiltered(bob.ID, ComicFilter{Collection: private.ID}); err != nil || len(out) != 1 || total != 1 {
		t.Fatalf("shared collection filtered list = %d comics (total %d) err=%v, want 1", len(out), total, err)
	}
}

func assertTags(t *testing.T, st *Store, userID, comicID string, want ...string) {
	t.Helper()
	c, err := st.GetComic(userID, comicID)
	if err != nil {
		t.Fatalf("get comic: %v", err)
	}
	if len(c.Tags) != len(want) {
		t.Fatalf("tags = %#v, want %#v", c.Tags, want)
	}
	for i := range want {
		if c.Tags[i] != want[i] {
			t.Fatalf("tags = %#v, want %#v", c.Tags, want)
		}
	}
}
