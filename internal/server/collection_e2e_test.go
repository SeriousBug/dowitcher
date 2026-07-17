package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/store"
)

func createCollection(t *testing.T, ts *httptest.Server, client *http.Client, req api.CreateCollectionRequest) api.Collection {
	t.Helper()
	resp, body := post(t, client, ts.URL+"/api/collections", mustJSON(t, req))
	if resp.StatusCode != 200 {
		t.Fatalf("create collection: %d %s", resp.StatusCode, body)
	}
	var col api.Collection
	if err := json.Unmarshal(body, &col); err != nil {
		t.Fatalf("decode collection: %v", err)
	}
	return col
}

// TestCollectionCRUD walks a collection through its whole life.
func TestCollectionCRUD(t *testing.T) {
	_, ts, st, cfg := libraryServer(t, nil)
	alice := adminClient(t, ts, st)

	col := createCollection(t, ts, alice, api.CreateCollectionRequest{Name: "Reading", Summary: "later"})
	if col.Name != "Reading" || col.Shared || col.OwnerName != "Alice" {
		t.Fatalf("new collection = %+v, want a private collection owned by Alice", col)
	}
	if resp, _ := post(t, alice, ts.URL+"/api/collections", mustJSON(t, api.CreateCollectionRequest{Name: "  "})); resp.StatusCode != 400 {
		t.Fatalf("a nameless collection should be a 400, got %d", resp.StatusCode)
	}

	name := "Read"
	resp, body := sendJSON(t, alice, "PUT", ts.URL+"/api/collections/"+col.ID,
		mustJSON(t, api.UpdateCollectionRequest{Name: &name}))
	if resp.StatusCode != 200 {
		t.Fatalf("update: %d %s", resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, &col); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if col.Name != "Read" {
		t.Fatalf("collection name = %q, want Read", col.Name)
	}

	// Membership, in order.
	var ids []string
	for _, rel := range []string{"a.cbz", "b.cbz", "c.cbz"} {
		row, _ := addComic(t, st, cfg.LibraryRoot, rel, 1, store.ComicRow{Title: rel})
		ids = append(ids, row.ID)
		if resp, body := post(t, alice, ts.URL+"/api/collections/"+col.ID+"/comics",
			mustJSON(t, api.CollectionComicRequest{ComicID: row.ID})); resp.StatusCode != 200 {
			t.Fatalf("add %s: %d %s", rel, resp.StatusCode, body)
		}
	}
	if got := listCollectionComics(t, ts, alice, col.ID); !sameIDs(got, ids) {
		t.Fatalf("collection order = %v, want insertion order %v", got, ids)
	}

	reversed := []string{ids[2], ids[1], ids[0]}
	if resp, body := sendJSON(t, alice, "PUT", ts.URL+"/api/collections/"+col.ID+"/order",
		mustJSON(t, api.ReorderCollectionRequest{ComicIDs: reversed})); resp.StatusCode != 200 {
		t.Fatalf("reorder: %d %s", resp.StatusCode, body)
	}
	if got := listCollectionComics(t, ts, alice, col.ID); !sameIDs(got, reversed) {
		t.Fatalf("collection order = %v, want %v", got, reversed)
	}

	if resp, body := doReq(t, alice, "DELETE", ts.URL+"/api/collections/"+col.ID+"/comics/"+ids[1]); resp.StatusCode != 200 {
		t.Fatalf("remove: %d %s", resp.StatusCode, body)
	}
	if got := listCollectionComics(t, ts, alice, col.ID); len(got) != 2 {
		t.Fatalf("after removal = %v, want 2 comics", got)
	}
	if resp, _ := doReq(t, alice, "DELETE", ts.URL+"/api/collections/"+col.ID+"/comics/"+ids[1]); resp.StatusCode != 404 {
		t.Fatalf("removing what is not there = %d, want 404", resp.StatusCode)
	}

	if resp, body := doReq(t, alice, "DELETE", ts.URL+"/api/collections/"+col.ID); resp.StatusCode != 200 {
		t.Fatalf("delete collection: %d %s", resp.StatusCode, body)
	}
	if resp, _ := getReq(t, alice, ts.URL+"/api/collections/"+col.ID); resp.StatusCode != 404 {
		t.Fatalf("a deleted collection = %d, want 404", resp.StatusCode)
	}
	// Deleting a collection must not take its comics with it.
	if _, err := st.GetComic(userID(t, st, "Alice"), ids[0]); err != nil {
		t.Fatalf("a collection's comics outlive it: %v", err)
	}
}

// TestSharedCollectionIsReadOnlyToOthers: sharing hands over a key to the front
// door, not to the house. Every mutation a non-owner attempts is a 404, because
// telling them "forbidden" would confirm whose it is.
func TestSharedCollectionIsReadOnlyToOthers(t *testing.T) {
	_, ts, st, cfg := libraryServer(t, nil)
	alice := adminClient(t, ts, st)
	bob := enrolledUser(t, ts, alice, "Bob")
	row, _ := addComic(t, st, cfg.LibraryRoot, "shared.cbz", 1, store.ComicRow{Title: "Shared"})

	col := createCollection(t, ts, alice, api.CreateCollectionRequest{Name: "Private"})
	if resp, _ := getReq(t, bob, ts.URL+"/api/collections/"+col.ID); resp.StatusCode != 404 {
		t.Fatalf("an unshared collection = %d to a stranger, want 404", resp.StatusCode)
	}

	// PUT shared is the share action.
	shared := true
	if resp, body := sendJSON(t, alice, "PUT", ts.URL+"/api/collections/"+col.ID,
		mustJSON(t, api.UpdateCollectionRequest{Shared: &shared})); resp.StatusCode != 200 {
		t.Fatalf("share: %d %s", resp.StatusCode, body)
	}

	if resp, body := getReq(t, bob, ts.URL+"/api/collections/"+col.ID); resp.StatusCode != 200 {
		t.Fatalf("a shared collection should be readable: %d %s", resp.StatusCode, body)
	}
	_, body := getReq(t, bob, ts.URL+"/api/collections")
	var cols []api.Collection
	if err := json.Unmarshal(body, &cols); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(cols) != 1 || cols[0].ID != col.ID {
		t.Fatalf("bob's collections = %+v, want the shared one", cols)
	}

	name := "hijacked"
	for _, c := range []struct {
		method, path string
		body         []byte
	}{
		{"PUT", "/api/collections/" + col.ID, mustJSON(t, api.UpdateCollectionRequest{Name: &name})},
		{"PUT", "/api/collections/" + col.ID, mustJSON(t, api.UpdateCollectionRequest{Shared: new(bool)})},
		{"DELETE", "/api/collections/" + col.ID, nil},
		{"POST", "/api/collections/" + col.ID + "/comics", mustJSON(t, api.CollectionComicRequest{ComicID: row.ID})},
		{"DELETE", "/api/collections/" + col.ID + "/comics/" + row.ID, nil},
		{"PUT", "/api/collections/" + col.ID + "/order", mustJSON(t, api.ReorderCollectionRequest{ComicIDs: []string{row.ID}})},
	} {
		resp, body := sendJSON(t, bob, c.method, ts.URL+c.path, c.body)
		if resp.StatusCode != 404 {
			t.Fatalf("%s %s as a non-owner = %d %s, want 404", c.method, c.path, resp.StatusCode, body)
		}
	}

	// None of that touched it.
	_, body = getReq(t, alice, ts.URL+"/api/collections/"+col.ID)
	var after api.Collection
	json.Unmarshal(body, &after)
	if after.Name != "Private" || !after.Shared {
		t.Fatalf("collection after the attempts = %+v, want it unchanged and still shared", after)
	}
}

func listCollectionComics(t *testing.T, ts *httptest.Server, client *http.Client, id string) []string {
	t.Helper()
	resp, body := getReq(t, client, ts.URL+"/api/comics?collection="+id)
	if resp.StatusCode != 200 {
		t.Fatalf("list collection comics: %d %s", resp.StatusCode, body)
	}
	var l api.ComicList
	if err := json.Unmarshal(body, &l); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	out := make([]string, len(l.Comics))
	for i, c := range l.Comics {
		out[i] = c.ID
	}
	return out
}

func sameIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
