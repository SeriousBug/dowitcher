package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/store"
)

// TestTagsArePrivateAtTheEdge: two users tagging the same library comic never
// see each other's words, on any route that carries a tag.
func TestTagsArePrivateAtTheEdge(t *testing.T) {
	_, ts, st, cfg := libraryServer(t, nil)
	alice := adminClient(t, ts, st)
	bob := enrolledUser(t, ts, alice, "Bob")

	lib, _ := addComic(t, st, cfg.LibraryRoot, "shared.cbz", 2,
		store.ComicRow{Title: "Shared", Source: store.SourceLibrary})

	setTags(t, alice, ts.URL, lib.ID, "alice-word", "both-word")
	setTags(t, bob, ts.URL, lib.ID, "bob-word", "both-word")

	// The comic carries only the reader's own tags.
	if got := getComic(t, alice, ts.URL, lib.ID).Tags; !sameTags(got, []string{"alice-word", "both-word"}) {
		t.Fatalf("alice's comic tags = %v", got)
	}
	if got := getComic(t, bob, ts.URL, lib.ID).Tags; !sameTags(got, []string{"bob-word", "both-word"}) {
		t.Fatalf("bob's comic tags = %v", got)
	}

	// So does the tag index, and a word both coined counts once for each.
	for _, tc := range []struct {
		client *http.Client
		want   []string
	}{
		{alice, []string{"alice-word", "both-word"}},
		{bob, []string{"bob-word", "both-word"}},
	} {
		tags := listTags(t, tc.client, ts.URL)
		var names []string
		for _, tag := range tags {
			if tag.Count != 1 {
				t.Fatalf("tag %q count = %d, want 1", tag.Name, tag.Count)
			}
			names = append(names, tag.Name)
		}
		if !sameTags(names, tc.want) {
			t.Fatalf("tag index = %v, want %v", names, tc.want)
		}
	}

	// Filtering by a word only the other user coined finds nothing.
	if n := listTotalQuery(t, bob, ts.URL, "?tag=alice-word"); n != 0 {
		t.Fatalf("filtering by another user's tag returned %d comics", n)
	}
	if n := listTotalQuery(t, bob, ts.URL, "?tag=bob-word"); n != 1 {
		t.Fatalf("filtering by an own tag returned %d comics, want 1", n)
	}

	// Clearing one user's tags leaves the other's standing.
	setTags(t, bob, ts.URL, lib.ID)
	if got := getComic(t, alice, ts.URL, lib.ID).Tags; !sameTags(got, []string{"alice-word", "both-word"}) {
		t.Fatalf("bob's clear took alice's tags with it: %v", got)
	}
	if tags := listTags(t, bob, ts.URL); len(tags) != 0 {
		t.Fatalf("bob's tag index should be empty, got %v", tags)
	}
}

func setTags(t *testing.T, c *http.Client, base, id string, tags ...string) {
	t.Helper()
	if tags == nil {
		tags = []string{}
	}
	resp, body := sendJSON(t, c, "PUT", base+"/api/comics/"+id+"/tags",
		mustJSON(t, api.SetTagsRequest{Tags: tags}))
	if resp.StatusCode != 200 {
		t.Fatalf("set tags %v: %d %s", tags, resp.StatusCode, body)
	}
}

func listTags(t *testing.T, c *http.Client, base string) []api.Tag {
	t.Helper()
	resp, body := getReq(t, c, base+"/api/tags")
	if resp.StatusCode != 200 {
		t.Fatalf("list tags: %d %s", resp.StatusCode, body)
	}
	var tags []api.Tag
	if err := json.Unmarshal(body, &tags); err != nil {
		t.Fatalf("decode tags: %v", err)
	}
	return tags
}

func listTotalQuery(t *testing.T, c *http.Client, base, query string) int {
	t.Helper()
	_, body := getReq(t, c, base+"/api/comics"+query)
	var list api.ComicList
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	return list.Total
}

// sameTags compares tag sets. Both the API and the store return them sorted by
// name, so this is order-sensitive on purpose: an unsorted list would be a bug.
func sameTags(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
