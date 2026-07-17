package store

import (
	"errors"
	"testing"
)

// TestClaimTakesALibraryComicPrivate is the claim model in one test: a comic
// dropped into the watched folder is server-wide until an admin claims it, and
// claiming is what takes it out of everyone else's library.
func TestClaimTakesALibraryComicPrivate(t *testing.T) {
	st := testStore(t)
	admin, _ := st.CreateUser(NewID(), "root", true)
	bob, _ := st.CreateUser(NewID(), "bob", false)

	lib := ComicRow{ID: NewID(), Path: "Dropped/D.cbz", Title: "D", Source: SourceLibrary, PageCount: 3}
	if err := st.UpsertComic(lib); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := st.GetComic(bob.ID, lib.ID); err != nil {
		t.Fatalf("precondition: a library comic is server-wide: %v", err)
	}

	if err := st.ClaimComic(admin.ID, lib.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := st.GetComic(bob.ID, lib.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a claimed comic must leave everyone else's library, got err=%v", err)
	}
	assertListLen(t, st, bob.ID, 0)
	if _, err := st.GetComic(admin.ID, lib.ID); err != nil {
		t.Fatalf("the claimer keeps it: %v", err)
	}
	assertListLen(t, st, admin.ID, 1)

	// The file did not move, so the path stays relative to the library root and
	// the row is still the scanner's to reconcile.
	row, err := st.ComicRowByID(lib.ID)
	if err != nil {
		t.Fatalf("row: %v", err)
	}
	if row.Path != "Dropped/D.cbz" {
		t.Fatalf("claim rewrote the path to %q", row.Path)
	}
	if row.Source != SourceClaimed || row.OwnerID != admin.ID {
		t.Fatalf("row = source %q owner %q, want claimed/%s", row.Source, row.OwnerID, admin.ID)
	}
	paths, err := st.ListComicPaths()
	if err != nil {
		t.Fatalf("list paths: %v", err)
	}
	if paths["Dropped/D.cbz"] != lib.ID {
		t.Fatalf("a claimed comic dropped out of the scanner's diff set: %#v", paths)
	}

	// Unclaiming hands it back.
	if err := st.UnclaimComic(admin.ID, false, lib.ID); err != nil {
		t.Fatalf("unclaim: %v", err)
	}
	if _, err := st.GetComic(bob.ID, lib.ID); err != nil {
		t.Fatalf("unclaim should make it server-wide again: %v", err)
	}
}

// TestClaimedComicSharesLikeAnUpload: a claim is ownership, so the owner can opt
// it back in through a shared collection the same way they would an upload.
func TestClaimedComicSharesLikeAnUpload(t *testing.T) {
	st := testStore(t)
	admin, _ := st.CreateUser(NewID(), "root", true)
	bob, _ := st.CreateUser(NewID(), "bob", false)

	lib := ComicRow{ID: NewID(), Path: "L.cbz", Title: "L", Source: SourceLibrary, PageCount: 3}
	if err := st.UpsertComic(lib); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := st.ClaimComic(admin.ID, lib.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	col, err := st.CreateCollection(NewID(), admin.ID, "Picks", "", true)
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	if err := st.AddToCollection(admin.ID, col.ID, lib.ID); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := st.GetComic(bob.ID, lib.ID); err != nil {
		t.Fatalf("a claimed comic in the claimer's shared collection should be visible: %v", err)
	}
	if err := st.UpdateCollection(admin.ID, col.ID, updateShared(false)); err != nil {
		t.Fatalf("unshare: %v", err)
	}
	if _, err := st.GetComic(bob.ID, lib.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unsharing should take it back, got err=%v", err)
	}
}

// TestClaimOnlyAppliesToLibraryComics: claiming is defined for the comics that
// have no owner. An upload already has one, and taking it would be a way to
// steal it out from under them.
func TestClaimOnlyAppliesToLibraryComics(t *testing.T) {
	st := testStore(t)
	admin, _ := st.CreateUser(NewID(), "root", true)
	alice, _ := st.CreateUser(NewID(), "alice", false)

	upload := ComicRow{ID: NewID(), Path: "uploads/alice/u.cbz", Title: "U",
		OwnerID: alice.ID, Source: SourceUpload, PageCount: 2}
	if err := st.UpsertComic(upload); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := st.ClaimComic(admin.ID, upload.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an upload must not be claimable, got %v", err)
	}
	row, _ := st.ComicRowByID(upload.ID)
	if row.OwnerID != alice.ID || row.Source != SourceUpload {
		t.Fatalf("a refused claim still moved the row: source %q owner %q", row.Source, row.OwnerID)
	}

	// Claiming twice is not a way to take someone else's claim either.
	lib := ComicRow{ID: NewID(), Path: "L.cbz", Title: "L", Source: SourceLibrary, PageCount: 1}
	if err := st.UpsertComic(lib); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := st.ClaimComic(admin.ID, lib.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	other, _ := st.CreateUser(NewID(), "root2", true)
	if err := st.ClaimComic(other.ID, lib.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an already-claimed comic must not be re-claimable, got %v", err)
	}
}

// TestUnclaimIsTheClaimersOrAnAdmins guards the one asymmetry: a claim is
// personal, but an admin has to be able to undo one.
func TestUnclaimIsTheClaimersOrAnAdmins(t *testing.T) {
	st := testStore(t)
	claimer, _ := st.CreateUser(NewID(), "root", true)
	other, _ := st.CreateUser(NewID(), "root2", true)

	lib := ComicRow{ID: NewID(), Path: "L.cbz", Title: "L", Source: SourceLibrary, PageCount: 1}
	if err := st.UpsertComic(lib); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := st.ClaimComic(claimer.ID, lib.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := st.UnclaimComic(other.ID, false, lib.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a non-owner without admin unclaimed someone's comic, got %v", err)
	}
	if err := st.UnclaimComic(other.ID, true, lib.ID); err != nil {
		t.Fatalf("an admin should be able to undo any claim: %v", err)
	}
	// Unclaiming what is not claimed says so rather than silently passing.
	if err := st.UnclaimComic(claimer.ID, true, lib.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unclaiming an unclaimed comic should fail, got %v", err)
	}
}

// TestClaimedComicKeepsTagsAndProgress: claiming moves the comic between
// libraries, it does not rebuild the row, so everything hanging off the id
// survives — including the tags of a user who is about to lose sight of it.
func TestClaimedComicKeepsTagsAndProgress(t *testing.T) {
	st := testStore(t)
	admin, _ := st.CreateUser(NewID(), "root", true)
	bob, _ := st.CreateUser(NewID(), "bob", false)

	lib := ComicRow{ID: NewID(), Path: "L.cbz", Title: "L", Source: SourceLibrary, PageCount: 5}
	if err := st.UpsertComic(lib); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := st.SetComicTags(bob.ID, lib.ID, []string{"bobs-word"}); err != nil {
		t.Fatalf("bob tags: %v", err)
	}
	if _, err := st.SetProgress(bob.ID, lib.ID, 2, false); err != nil {
		t.Fatalf("bob progress: %v", err)
	}
	if err := st.ClaimComic(admin.ID, lib.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// Bob's tag is still on the row but its comic is no longer visible to him,
	// so the count must not advertise it.
	tags, err := st.ListTags(bob.ID)
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	if len(tags) != 0 {
		t.Fatalf("a tag survived on a comic bob can no longer see: %#v", tags)
	}
	// Unclaiming gives it back, tag and place intact.
	if err := st.UnclaimComic(admin.ID, false, lib.ID); err != nil {
		t.Fatalf("unclaim: %v", err)
	}
	assertTags(t, st, bob.ID, lib.ID, "bobs-word")
	prog, err := st.GetProgress(bob.ID, lib.ID)
	if err != nil || prog.Page != 2 {
		t.Fatalf("progress = %#v err=%v, want page 2", prog, err)
	}
}
