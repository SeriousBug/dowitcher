package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/store"
)

// TestClaimLibraryComicAtTheEdge walks claiming over HTTP: an admin takes a
// comic that was dropped into the watched folder, and it leaves every other
// user's library without the file moving.
func TestClaimLibraryComicAtTheEdge(t *testing.T) {
	_, ts, st, cfg := libraryServer(t, nil)
	alice := adminClient(t, ts, st)
	bob := enrolledUser(t, ts, alice, "Bob")

	dropped, pages := addComic(t, st, cfg.LibraryRoot, "Dropped.cbz", 3,
		store.ComicRow{Title: "Dropped", Source: store.SourceLibrary})

	// Everyone sees it, and the client is told it is claimable.
	comic := getComic(t, bob, ts.URL, dropped.ID)
	if comic.Source != store.SourceLibrary || comic.OwnedByMe {
		t.Fatalf("comic = source %q ownedByMe %v, want library/false", comic.Source, comic.OwnedByMe)
	}

	if resp, body := doReq(t, alice, "POST", ts.URL+"/api/comics/"+dropped.ID+"/claim"); resp.StatusCode != 200 {
		t.Fatalf("claim: %d %s", resp.StatusCode, body)
	}

	// Bob loses it entirely: the row, the pages and the listing.
	for _, path := range []string{
		"/api/comics/" + dropped.ID,
		"/api/comics/" + dropped.ID + "/pages/0",
		"/api/comics/" + dropped.ID + "/cover",
	} {
		if resp, _ := getReq(t, bob, ts.URL+path); resp.StatusCode != 404 {
			t.Fatalf("GET %s after a claim = %d, want 404", path, resp.StatusCode)
		}
	}
	if n := listTotal(t, bob, ts.URL); n != 0 {
		t.Fatalf("a claimed comic is still in a stranger's library: total %d", n)
	}

	// Alice keeps it, can still read its pages off the library root, and is told
	// the claim is hers.
	comic = getComic(t, alice, ts.URL, dropped.ID)
	if comic.Source != store.SourceClaimed || !comic.OwnedByMe {
		t.Fatalf("comic = source %q ownedByMe %v, want claimed/true", comic.Source, comic.OwnedByMe)
	}
	if resp, body := getReq(t, alice, ts.URL+"/api/comics/"+dropped.ID+"/pages/0"); resp.StatusCode != 200 ||
		len(body) != len(pages[0]) {
		t.Fatalf("claiming broke page reads off the library root: %d", resp.StatusCode)
	}

	// A claimed comic is still a library file, so deleting it through the API is
	// refused the same way a library comic's is.
	if resp, _ := doReq(t, alice, "DELETE", ts.URL+"/api/comics/"+dropped.ID); resp.StatusCode != 400 {
		t.Fatalf("deleting a claimed comic = %d, want 400", resp.StatusCode)
	}

	// Unclaiming hands it back to the server.
	if resp, body := doReq(t, alice, "POST", ts.URL+"/api/comics/"+dropped.ID+"/unclaim"); resp.StatusCode != 200 {
		t.Fatalf("unclaim: %d %s", resp.StatusCode, body)
	}
	if resp, _ := getReq(t, bob, ts.URL+"/api/comics/"+dropped.ID); resp.StatusCode != 200 {
		t.Fatalf("unclaim did not return the comic to the server")
	}
}

// TestClaimIsAdminOnly: claiming removes a comic from everyone else's view, so
// it is gated at the route rather than left to whoever asks first.
func TestClaimIsAdminOnly(t *testing.T) {
	_, ts, st, cfg := libraryServer(t, nil)
	alice := adminClient(t, ts, st)
	bob := enrolledUser(t, ts, alice, "Bob")

	dropped, _ := addComic(t, st, cfg.LibraryRoot, "D.cbz", 2,
		store.ComicRow{Title: "D", Source: store.SourceLibrary})

	for _, path := range []string{"/claim", "/unclaim"} {
		if resp, _ := doReq(t, bob, "POST", ts.URL+"/api/comics/"+dropped.ID+path); resp.StatusCode != 403 {
			t.Fatalf("POST %s as a non-admin = %d, want 403", path, resp.StatusCode)
		}
	}
	row, err := st.ComicRowByID(dropped.ID)
	if err != nil || row.Source != store.SourceLibrary {
		t.Fatalf("a refused claim changed the row: source %q err=%v", row.Source, err)
	}
}

// TestClaimRefusesAnUpload: an upload already has an owner, and claiming is only
// defined for the comics that have none.
func TestClaimRefusesAnUpload(t *testing.T) {
	_, ts, st, cfg := libraryServer(t, nil)
	alice := adminClient(t, ts, st)
	aliceID := userID(t, st, "Alice")

	upload, _ := addComic(t, st, cfg.UploadsDir, "mine.cbz", 2,
		store.ComicRow{Title: "Mine", OwnerID: aliceID, Source: store.SourceUpload})

	// 400 rather than 404: Alice can see it, so "no such comic" would be a lie.
	if resp, body := doReq(t, alice, "POST", ts.URL+"/api/comics/"+upload.ID+"/claim"); resp.StatusCode != 400 {
		t.Fatalf("claiming an upload = %d %s, want 400", resp.StatusCode, body)
	}
	// Unclaiming something that was never claimed says so too.
	if resp, _ := doReq(t, alice, "POST", ts.URL+"/api/comics/"+upload.ID+"/unclaim"); resp.StatusCode != 400 {
		t.Fatalf("unclaiming an upload = %d, want 400", resp.StatusCode)
	}
}

// TestClaimOfAnInvisibleComicIs404: the claim routes are admin-gated, but they
// still go through the visibility check, so an id that names nothing behaves
// like every other unknown id.
func TestClaimOfAnInvisibleComicIs404(t *testing.T) {
	_, ts, st, _ := libraryServer(t, nil)
	alice := adminClient(t, ts, st)
	if resp, _ := doReq(t, alice, "POST", ts.URL+"/api/comics/nope/claim"); resp.StatusCode != 404 {
		t.Fatalf("claiming an unknown comic = %d, want 404", resp.StatusCode)
	}
}

func getComic(t *testing.T, c *http.Client, base, id string) api.Comic {
	t.Helper()
	resp, body := getReq(t, c, base+"/api/comics/"+id)
	if resp.StatusCode != 200 {
		t.Fatalf("get comic %s: %d %s", id, resp.StatusCode, body)
	}
	var d api.ComicDetail
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatalf("decode comic: %v", err)
	}
	return d.Comic
}

func listTotal(t *testing.T, c *http.Client, base string) int {
	t.Helper()
	_, body := getReq(t, c, base+"/api/comics")
	var list api.ComicList
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	return list.Total
}
