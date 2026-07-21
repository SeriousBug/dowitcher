package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SeriousBug/dowitcher/internal/auth"
	"github.com/SeriousBug/dowitcher/internal/oauth"
	"github.com/SeriousBug/dowitcher/internal/store"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// bearer injects a static Authorization header, standing in for an agent that
// holds an OAuth access token.
type bearer struct {
	token string
	base  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return b.base.RoundTrip(r)
}

type e2e struct {
	store   *store.Store
	url     string
	aliceID string
	bobID   string
}

// connect opens an MCP session authenticated with token against the test server.
func connect(t *testing.T, url, token string) *sdk.ClientSession {
	t.Helper()
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	tr := &sdk.StreamableClientTransport{
		Endpoint:   url + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{token: token, base: http.DefaultTransport}},
	}
	session, err := client.Connect(context.Background(), tr, nil)
	if err != nil {
		t.Fatalf("connect with token: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// call runs a tool and decodes its structured output into out. It fails the test
// if the tool returned an error result.
func call(t *testing.T, s *sdk.ClientSession, name string, args any, out any) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s: tool error: %v", name, contentText(res))
	}
	if out != nil {
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s: decode output: %v", name, err)
		}
	}
}

// callErr runs a tool expecting an error result, returning its message.
func callErr(t *testing.T, s *sdk.ClientSession, name string, args any) string {
	t.Helper()
	res, err := s.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error (want tool error): %v", name, err)
	}
	if !res.IsError {
		t.Fatalf("%s: expected an error result, got success", name)
	}
	return contentText(res)
}

func contentText(res *sdk.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

func setup(t *testing.T) e2e {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	alice, _ := st.CreateUser(store.NewID(), "alice", true)
	bob, _ := st.CreateUser(store.NewID(), "bob", false)

	// A library comic (server-wide), plus one private upload each.
	must(t, st.UpsertComic(store.ComicRow{ID: store.NewID(), Path: "lib/Public.cbz", Title: "Public", Source: store.SourceLibrary, PageCount: 10}))
	must(t, st.UpsertComic(store.ComicRow{ID: store.NewID(), Path: "uploads/alice/a.cbz", Title: "AliceOnly", Source: store.SourceUpload, OwnerID: alice.ID, PageCount: 5}))
	must(t, st.UpsertComic(store.ComicRow{ID: store.NewID(), Path: "uploads/bob/b.cbz", Title: "BobOnly", Source: store.SourceUpload, OwnerID: bob.ID, PageCount: 7}))

	srv := httptest.NewServer(New(st, "test", "http://mcp.test").Handler())
	t.Cleanup(srv.Close)
	return e2e{store: st, url: srv.URL, aliceID: alice.ID, bobID: bob.ID}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
}

// token mints an OAuth access token directly through the store, standing in for
// the browser flow that would otherwise produce one. A client row is created
// first because the access token references it by FK.
func token(t *testing.T, st *store.Store, userID string) string {
	t.Helper()
	clientID := store.NewID()
	if err := st.CreateOAuthClient(clientID, "agent", []string{"https://example.test/cb"}); err != nil {
		t.Fatalf("create client: %v", err)
	}
	secret := oauth.NewToken()
	exp := time.Now().Add(time.Hour).Unix()
	if err := st.CreateAccessToken(auth.HashToken(secret), clientID, userID, "mcp", exp); err != nil {
		t.Fatalf("mint token: %v", err)
	}
	return secret
}

// TestMCPListReflectsVisibility: each user's list is filtered by the same SQL
// visibility rules as the HTTP API — the library comic plus their own upload,
// never the other user's upload.
func TestMCPListReflectsVisibility(t *testing.T) {
	e := setup(t)
	sess := connect(t, e.url, token(t, e.store, e.aliceID))

	var out ListComicsOutput
	call(t, sess, "list_comics", ListComicsInput{}, &out)

	titles := map[string]bool{}
	for _, c := range out.Comics {
		titles[c.Title] = true
	}
	if !titles["Public"] || !titles["AliceOnly"] {
		t.Errorf("alice should see the library comic and her own upload, got %v", titles)
	}
	if titles["BobOnly"] {
		t.Errorf("alice must not see bob's upload through MCP: %v", titles)
	}
}

// TestMCPGetComicHidesOthersUploads: fetching a comic the caller can't see is a
// not-found, not a leak.
func TestMCPGetComicHidesOthersUploads(t *testing.T) {
	e := setup(t)
	bobUpload := comicID(t, e.store, e.bobID, "BobOnly")

	sess := connect(t, e.url, token(t, e.store, e.aliceID))
	msg := callErr(t, sess, "get_comic", ComicIDInput{ComicID: bobUpload})
	if msg == "" {
		t.Error("expected a not-found message")
	}
}

// TestMCPClaimIsAdminOnly: the admin gate the HTTP route gets from requireAdmin
// is enforced in the MCP layer too.
func TestMCPClaimIsAdminOnly(t *testing.T) {
	e := setup(t)
	libComic := comicID(t, e.store, e.aliceID, "Public")

	// Bob is not an admin: claim is refused.
	bob := connect(t, e.url, token(t, e.store, e.bobID))
	callErr(t, bob, "claim_comic", ComicIDInput{ComicID: libComic})

	// Bob still sees the library comic — the refused claim changed nothing.
	var bobList ListComicsOutput
	call(t, bob, "list_comics", ListComicsInput{}, &bobList)
	if !hasTitle(bobList.Comics, "Public") {
		t.Error("a refused claim should leave the library comic visible to bob")
	}

	// Alice is an admin: claim succeeds and the comic leaves bob's view.
	alice := connect(t, e.url, token(t, e.store, e.aliceID))
	var claimed ComicOutput
	call(t, alice, "claim_comic", ComicIDInput{ComicID: libComic}, &claimed)
	if claimed.Comic.Source != store.SourceClaimed || !claimed.Comic.OwnedByMe {
		t.Errorf("claimed comic should be owned+claimed, got %+v", claimed.Comic)
	}

	bob2 := connect(t, e.url, token(t, e.store, e.bobID))
	call(t, bob2, "list_comics", ListComicsInput{}, &bobList)
	if hasTitle(bobList.Comics, "Public") {
		t.Error("after an admin claim, the comic must drop out of bob's view")
	}
}

// TestMCPBulkTagAndRename: one tag_comic call tags several comics at once, an
// unseen id is skipped rather than fatal, and rename is gated on ownership.
func TestMCPBulkTagAndRename(t *testing.T) {
	e := setup(t)
	lib := comicID(t, e.store, e.aliceID, "Public")
	up := comicID(t, e.store, e.aliceID, "AliceOnly")
	sess := connect(t, e.url, token(t, e.store, e.aliceID))

	var out BulkTagOutput
	call(t, sess, "tag_comic", TagComicInput{ComicIDs: []string{lib, up}, Tags: []string{"read"}}, &out)
	if len(out.Comics) != 2 {
		t.Fatalf("bulk tag should touch both comics, got %d", len(out.Comics))
	}
	for _, c := range out.Comics {
		if len(c.Tags) != 1 || c.Tags[0] != "read" {
			t.Errorf("comic %s tags = %v, want [read]", c.ID, c.Tags)
		}
	}

	// An unseen id is recorded in Skipped, not fatal to the batch.
	call(t, sess, "tag_comic", TagComicInput{ComicIDs: []string{lib, "nope"}, Tags: []string{"fav"}}, &out)
	if len(out.Skipped) != 1 || out.Skipped[0] != "nope" {
		t.Errorf("skipped = %v, want [nope]", out.Skipped)
	}

	// Alice owns her upload, so she may rename it.
	var renamed ComicOutput
	call(t, sess, "rename_comic", RenameComicInput{ComicID: up, Title: "Renamed"}, &renamed)
	if renamed.Comic.Title != "Renamed" {
		t.Errorf("title = %q, want Renamed", renamed.Comic.Title)
	}

	// Bob can see the library comic but neither owns it nor is admin, so the
	// ownership gate refuses his rename.
	bob := connect(t, e.url, token(t, e.store, e.bobID))
	if msg := callErr(t, bob, "rename_comic", RenameComicInput{ComicID: lib, Title: "Hacked"}); msg == "" {
		t.Error("a non-owner non-admin renaming a library comic should error")
	}
}

// TestMCPTagRoundTrip: tagging adds without dropping, and the tag shows up in
// list_tags for that user only.
func TestMCPTagRoundTrip(t *testing.T) {
	e := setup(t)
	lib := comicID(t, e.store, e.aliceID, "Public")
	sess := connect(t, e.url, token(t, e.store, e.aliceID))

	var tagged BulkTagOutput
	call(t, sess, "tag_comic", TagComicInput{ComicIDs: []string{lib}, Tags: []string{"read", "favorite"}}, &tagged)
	call(t, sess, "tag_comic", TagComicInput{ComicIDs: []string{lib}, Tags: []string{"classic"}}, &tagged)
	if len(tagged.Comics) != 1 || len(tagged.Comics[0].Tags) != 3 {
		t.Errorf("a second tag call should add, not replace; got %+v", tagged.Comics)
	}

	var tags ListTagsOutput
	call(t, sess, "list_tags", struct{}{}, &tags)
	if len(tags.Tags) != 3 {
		t.Errorf("list_tags should show all three, got %+v", tags.Tags)
	}

	// Bob shares no tags with alice — tags are per-user.
	bobSess := connect(t, e.url, token(t, e.store, e.bobID))
	var bobTags ListTagsOutput
	call(t, bobSess, "list_tags", struct{}{}, &bobTags)
	if len(bobTags.Tags) != 0 {
		t.Errorf("alice's tags must not appear for bob, got %+v", bobTags.Tags)
	}
}

// TestMCPRejectsMissingAndBadToken: no token and a garbage token are both
// refused before any tool runs.
func TestMCPRejectsMissingAndBadToken(t *testing.T) {
	e := setup(t)
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	for _, tok := range []string{"", "dwt_not-a-real-token"} {
		tr := &sdk.StreamableClientTransport{
			Endpoint:   e.url + "/mcp",
			HTTPClient: &http.Client{Transport: bearer{token: tok, base: http.DefaultTransport}},
		}
		if _, err := client.Connect(context.Background(), tr, nil); err == nil {
			t.Errorf("connect with token %q should fail with 401", tok)
		}
	}
}

func comicID(t *testing.T, st *store.Store, userID, title string) string {
	t.Helper()
	comics, _, err := st.ListComicsFiltered(userID, store.ComicFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, c := range comics {
		if c.Title == title {
			return c.ID
		}
	}
	t.Fatalf("no comic titled %q visible to %s", title, userID)
	return ""
}

func hasTitle(comics []comicView, title string) bool {
	for _, c := range comics {
		if c.Title == title {
			return true
		}
	}
	return false
}
