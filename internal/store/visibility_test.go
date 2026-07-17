package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/SeriousBug/longbox/internal/api"
)

func updateShared(v bool) api.UpdateCollectionRequest { return api.UpdateCollectionRequest{Shared: &v} }
func updateName(v string) api.UpdateCollectionRequest { return api.UpdateCollectionRequest{Name: &v} }

func testStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// TestVisibilityUploadIsPrivateUntilShared is the sharing model in one test: an
// upload is invisible to everyone but its owner, and sharing the collection it
// sits in is what changes that.
func TestVisibilityUploadIsPrivateUntilShared(t *testing.T) {
	st := testStore(t)

	alice, err := st.CreateUser(NewID(), "alice", true)
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := st.CreateUser(NewID(), "bob", false)
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}

	upload := ComicRow{
		ID: NewID(), Path: "uploads/alice/secret.cbz", Title: "Secret",
		OwnerID: alice.ID, Source: SourceUpload, PageCount: 10,
	}
	if err := st.UpsertComic(upload); err != nil {
		t.Fatalf("upsert upload: %v", err)
	}
	libraryComic := ComicRow{ID: NewID(), Path: "Shared/Public.cbz", Title: "Public", Source: SourceLibrary}
	if err := st.UpsertComic(libraryComic); err != nil {
		t.Fatalf("upsert library comic: %v", err)
	}

	// A library comic is server-wide: both users see it.
	for _, u := range []string{alice.ID, bob.ID} {
		if _, err := st.GetComic(u, libraryComic.ID); err != nil {
			t.Fatalf("library comic should be visible to %s: %v", u, err)
		}
	}

	// The upload is Alice's alone.
	if _, err := st.GetComic(alice.ID, upload.ID); err != nil {
		t.Fatalf("owner should see own upload: %v", err)
	}
	if _, err := st.GetComic(bob.ID, upload.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("private upload must be invisible to another user, got err=%v", err)
	}
	assertListLen(t, st, bob.ID, 1)

	// A private collection does not change that.
	col, err := st.CreateCollection(NewID(), alice.ID, "Alice's stash", "", false)
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	if err := st.AddToCollection(alice.ID, col.ID, upload.ID); err != nil {
		t.Fatalf("add to collection: %v", err)
	}
	if _, err := st.GetComic(bob.ID, upload.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("upload in a private collection must stay invisible, got err=%v", err)
	}

	// Sharing the collection is the opt-in that exposes it.
	shared := true
	if err := st.UpdateCollection(alice.ID, col.ID, updateShared(shared)); err != nil {
		t.Fatalf("share collection: %v", err)
	}
	if _, err := st.GetComic(bob.ID, upload.ID); err != nil {
		t.Fatalf("upload in a shared collection should be visible: %v", err)
	}
	assertListLen(t, st, bob.ID, 2)

	// Unsharing takes it back.
	unshared := false
	if err := st.UpdateCollection(alice.ID, col.ID, updateShared(unshared)); err != nil {
		t.Fatalf("unshare collection: %v", err)
	}
	if _, err := st.GetComic(bob.ID, upload.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unsharing should hide the upload again, got err=%v", err)
	}
}

// TestVisibilityCoversDerivedReads checks the paths that read comics indirectly,
// which are the ones most likely to forget the rule.
func TestVisibilityCoversDerivedReads(t *testing.T) {
	st := testStore(t)
	alice, _ := st.CreateUser(NewID(), "alice", true)
	bob, _ := st.CreateUser(NewID(), "bob", false)

	upload := ComicRow{ID: NewID(), Path: "uploads/a.cbz", Title: "A", OwnerID: alice.ID, Source: SourceUpload, PageCount: 5}
	if err := st.UpsertComic(upload); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := st.SetComicTags(alice.ID, upload.ID, []string{"private-tag"}); err != nil {
		t.Fatalf("set tags: %v", err)
	}

	// Tag counts must not advertise a comic the caller cannot open.
	tags, err := st.ListTags(bob.ID)
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	if len(tags) != 0 {
		t.Fatalf("tags on a private upload leaked to another user: %#v", tags)
	}
	if tags, err = st.ListTags(alice.ID); err != nil || len(tags) != 1 || tags[0].Count != 1 {
		t.Fatalf("owner should see their own tag: %#v err=%v", tags, err)
	}

	// Tagging and progress on an invisible comic are refused, not silently
	// applied to a comic the caller cannot read back.
	if err := st.SetComicTags(bob.ID, upload.ID, []string{"nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tagging an invisible comic should fail, got %v", err)
	}
	if _, err := st.SetProgress(bob.ID, upload.ID, 3, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("progress on an invisible comic should fail, got %v", err)
	}
	if _, err := st.SetProgress(alice.ID, upload.ID, 3, false); err != nil {
		t.Fatalf("owner progress: %v", err)
	}
	if n, err := st.CountVisibleComics(bob.ID); err != nil || n != 0 {
		t.Fatalf("count for bob = %d err=%v, want 0", n, err)
	}
}

// TestSharedCollectionIsReadOnlyToOthers: sharing exposes a collection, it does
// not hand over the keys.
func TestSharedCollectionIsReadOnlyToOthers(t *testing.T) {
	st := testStore(t)
	alice, _ := st.CreateUser(NewID(), "alice", true)
	bob, _ := st.CreateUser(NewID(), "bob", false)

	col, err := st.CreateCollection(NewID(), alice.ID, "Shared", "", true)
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	if _, err := st.GetCollection(bob.ID, col.ID); err != nil {
		t.Fatalf("bob should see a shared collection: %v", err)
	}
	name := "hijacked"
	if err := st.UpdateCollection(bob.ID, col.ID, updateName(name)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("non-owner must not update a shared collection, got %v", err)
	}
	if err := st.DeleteCollection(bob.ID, col.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("non-owner must not delete a shared collection, got %v", err)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatalf("reopen should replay no migrations: %v", err)
	}
	st.Close()
}

func assertListLen(t *testing.T, st *Store, userID string, want int) {
	t.Helper()
	comics, err := st.ListComics(userID)
	if err != nil {
		t.Fatalf("list comics: %v", err)
	}
	if len(comics) != want {
		t.Fatalf("ListComics(%s) = %d comics, want %d", userID, len(comics), want)
	}
}
