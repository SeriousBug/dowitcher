package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/SeriousBug/longbox/internal/api"
	"github.com/SeriousBug/longbox/internal/cbz"
	"github.com/SeriousBug/longbox/internal/store"
)

// libraryServer is testServer with the on-disk roots the comic handlers need to
// turn a row into a file. opt may override the config further.
func libraryServer(t *testing.T, opt func(*Config)) (*Server, *httptest.Server, *store.Store, Config) {
	t.Helper()
	dir := t.TempDir()
	var cfg Config
	srv, ts, st, _ := newTestServer(t, func(c *Config) {
		c.LibraryRoot = filepath.Join(dir, "library")
		c.UploadsDir = filepath.Join(dir, "uploads")
		c.CoverCacheDir = filepath.Join(dir, "covers")
		if opt != nil {
			opt(c)
		}
		cfg = *c
	})
	for _, d := range []string{cfg.LibraryRoot, cfg.UploadsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return srv, ts, st, cfg
}

// pagePNG is a deterministic page: a solid colour derived from n, so a test can
// tell which page it got back by its bytes.
func pagePNG(t *testing.T, n int) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 32, 48))
	for y := range 48 {
		for x := range 32 {
			im.Set(x, y, color.RGBA{uint8(n * 40), uint8(255 - n*40), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, im); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// writeTestCBZ writes a CBZ of n pages and returns the pages' bytes in order.
func writeTestCBZ(t *testing.T, path string, n int) [][]byte {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	pages := make([][]byte, n)
	for i := range n {
		w, err := zw.Create(strconv.Itoa(i+1) + ".png")
		if err != nil {
			t.Fatal(err)
		}
		pages[i] = pagePNG(t, i)
		if _, err := w.Write(pages[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return pages
}

// addComic writes a CBZ under root and registers it, the way the scanner or the
// importer would.
func addComic(t *testing.T, st *store.Store, root, rel string, pages int, row store.ComicRow) (store.ComicRow, [][]byte) {
	t.Helper()
	abs := filepath.Join(root, rel)
	data := writeTestCBZ(t, abs, pages)
	hash, err := cbz.ContentHash(abs)
	if err != nil {
		t.Fatalf("content hash: %v", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		t.Fatal(err)
	}
	row.ID = store.NewID()
	row.Path = rel
	row.ContentHash = hash
	row.PageCount = pages
	row.FileSize = info.Size()
	row.ModifiedAt = info.ModTime().Unix()
	if row.Title == "" {
		row.Title = rel
	}
	if row.Source == "" {
		row.Source = store.SourceLibrary
	}
	if err := st.UpsertComic(row); err != nil {
		t.Fatalf("upsert comic: %v", err)
	}
	return row, data
}

// enrolledUser brings up a client logged in as a new user. The first call must
// be the admin, since the bootstrap invite is what mints them.
func enrolledUser(t *testing.T, ts *httptest.Server, admin *http.Client, name string) *http.Client {
	t.Helper()
	_, body := post(t, admin, ts.URL+"/api/invites", mustJSON(t, api.CreateInviteRequest{IsAdmin: false}))
	var inv api.Invite
	if err := json.Unmarshal(body, &inv); err != nil {
		t.Fatalf("decode invite: %v", err)
	}
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	if resp, body := newPasskey(ts.URL).enroll(t, c, ts.URL, inv.Token, name); resp.StatusCode != 200 {
		t.Fatalf("enroll %s: %d %s", name, resp.StatusCode, body)
	}
	return c
}

func adminClient(t *testing.T, ts *httptest.Server, st *store.Store) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	if resp, body := newPasskey(ts.URL).enroll(t, c, ts.URL, bootstrapToken(t, st, ts.URL), "Alice"); resp.StatusCode != 200 {
		t.Fatalf("enroll admin: %d %s", resp.StatusCode, body)
	}
	return c
}

func sendJSON(t *testing.T, client *http.Client, method, url string, body []byte) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, data
}

func userID(t *testing.T, st *store.Store, name string) string {
	t.Helper()
	u, err := st.UserByName(name)
	if err != nil {
		t.Fatalf("user %s: %v", name, err)
	}
	return u.ID
}

// TestPageStreamingAndRevalidation pins the reader's hot path: the right bytes,
// cached hard, and a reload that costs a 304 rather than the page again.
func TestPageStreamingAndRevalidation(t *testing.T) {
	_, ts, st, cfg := libraryServer(t, nil)
	alice := adminClient(t, ts, st)
	row, pages := addComic(t, st, cfg.LibraryRoot, "Series/One.cbz", 3, store.ComicRow{})

	resp, body := getReq(t, alice, ts.URL+"/api/comics/"+row.ID+"/pages/1")
	if resp.StatusCode != 200 {
		t.Fatalf("page 1: %d %s", resp.StatusCode, body)
	}
	if !bytes.Equal(body, pages[1]) {
		t.Fatalf("page 1 served %d bytes, want the %d bytes of page 1", len(body), len(pages[1]))
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content type = %q, want image/png", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != immutableCache {
		t.Fatalf("cache-control = %q, want %q", cc, immutableCache)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("a page must carry an ETag or it can never be revalidated")
	}

	// The revalidation a reload performs.
	req, _ := http.NewRequest("GET", ts.URL+"/api/comics/"+row.ID+"/pages/1", nil)
	req.Header.Set("If-None-Match", etag)
	resp, err := alice.Do(req)
	if err != nil {
		t.Fatalf("revalidate: %v", err)
	}
	fresh, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("revalidating an unchanged page = %d, want 304", resp.StatusCode)
	}
	if len(fresh) != 0 {
		t.Fatalf("a 304 must carry no body, got %d bytes", len(fresh))
	}

	// Distinct pages must not share a tag, or the cache would serve one for the
	// other.
	resp2, body2 := getReq(t, alice, ts.URL+"/api/comics/"+row.ID+"/pages/2")
	if resp2.Header.Get("ETag") == etag {
		t.Fatal("two pages of a comic must not share an ETag")
	}
	if !bytes.Equal(body2, pages[2]) {
		t.Fatal("page 2 served the wrong bytes")
	}

	if resp, _ := getReq(t, alice, ts.URL+"/api/comics/"+row.ID+"/pages/9"); resp.StatusCode != 404 {
		t.Fatalf("a page past the end should be 404, got %d", resp.StatusCode)
	}
}

// TestCoverFallsBackToGenerating: a cold cache is slow, not broken, and the
// generated cover is cached for the next reader.
func TestCoverFallsBackToGenerating(t *testing.T) {
	_, ts, st, cfg := libraryServer(t, nil)
	alice := adminClient(t, ts, st)
	row, _ := addComic(t, st, cfg.LibraryRoot, "Cover.cbz", 2, store.ComicRow{})

	resp, body := getReq(t, alice, ts.URL+"/api/comics/"+row.ID+"/cover")
	if resp.StatusCode != 200 {
		t.Fatalf("cover with an empty cache: %d %s", resp.StatusCode, body)
	}
	if _, err := png.Decode(bytes.NewReader(body)); err == nil {
		t.Fatal("a cover should be the JPEG thumbnail, not the page itself")
	}
	cached := filepath.Join(cfg.CoverCacheDir, row.ContentHash+".jpg")
	if _, err := os.Stat(cached); err != nil {
		t.Fatalf("a generated cover should be cached for the next reader: %v", err)
	}
	// The cached file is what gets served from then on.
	if err := os.WriteFile(cached, []byte("cached-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, body := getReq(t, alice, ts.URL+"/api/comics/"+row.ID+"/cover"); string(body) != "cached-bytes" {
		t.Fatalf("a cached cover should be served from the cache, got %d bytes", len(body))
	}
}

// TestProgressRoundTrips is the cross-device sync: what one device PUTs is what
// every other device reads back.
func TestProgressRoundTrips(t *testing.T) {
	_, ts, st, cfg := libraryServer(t, nil)
	alice := adminClient(t, ts, st)
	row, _ := addComic(t, st, cfg.LibraryRoot, "Read.cbz", 5, store.ComicRow{})
	url := ts.URL + "/api/comics/" + row.ID

	resp, body := sendJSON(t, alice, "PUT", url+"/progress", mustJSON(t, api.ProgressRequest{Page: 2}))
	if resp.StatusCode != 200 {
		t.Fatalf("put progress: %d %s", resp.StatusCode, body)
	}
	var p api.Progress
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode progress: %v", err)
	}
	if p.Page != 2 || p.Completed || p.PageCount != 5 || p.ComicID != row.ID {
		t.Fatalf("progress = %+v, want page 2 of 5, unfinished", p)
	}

	// A second device is a second session for the same user.
	other := &http.Client{Jar: alice.Jar}
	_, body = getReq(t, other, url)
	var detail api.ComicDetail
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Progress == nil || detail.Progress.Page != 2 {
		t.Fatalf("detail progress = %+v, want page 2", detail.Progress)
	}
	if len(detail.Pages) != 5 {
		t.Fatalf("detail pages = %d, want 5", len(detail.Pages))
	}

	// The listing carries progress for the comics on the page.
	_, body = getReq(t, alice, ts.URL+"/api/comics")
	var list api.ComicList
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Progress) != 1 || list.Progress[0].Page != 2 {
		t.Fatalf("list progress = %+v, want page 2", list.Progress)
	}

	// The last page completes the comic without the client saying so.
	_, body = sendJSON(t, alice, "PUT", url+"/progress", mustJSON(t, api.ProgressRequest{Page: 4}))
	json.Unmarshal(body, &p)
	if !p.Completed {
		t.Fatalf("reaching the last page must complete the comic, got %+v", p)
	}
	// A page past the end is clamped rather than refused.
	_, body = sendJSON(t, alice, "PUT", url+"/progress", mustJSON(t, api.ProgressRequest{Page: 99}))
	json.Unmarshal(body, &p)
	if p.Page != 4 || !p.Completed {
		t.Fatalf("an out-of-range page should clamp to the last one, got %+v", p)
	}
}

// TestUnsharedUploadIsInvisible is the sharing model at the HTTP edge: another
// user's private upload does not exist as far as the API is concerned — not its
// row, not its pages, not its cover.
func TestUnsharedUploadIsInvisible(t *testing.T) {
	_, ts, st, cfg := libraryServer(t, nil)
	alice := adminClient(t, ts, st)
	bob := enrolledUser(t, ts, alice, "Bob")
	aliceID := userID(t, st, "Alice")

	upload, pages := addComic(t, st, cfg.UploadsDir, "secret.cbz", 3,
		store.ComicRow{Title: "Secret", OwnerID: aliceID, Source: store.SourceUpload})

	// The owner reads it.
	if resp, body := getReq(t, alice, ts.URL+"/api/comics/"+upload.ID+"/pages/0"); resp.StatusCode != 200 ||
		!bytes.Equal(body, pages[0]) {
		t.Fatalf("the owner must be able to read their own upload: %d", resp.StatusCode)
	}

	// Nobody else does, on any route that touches it.
	for _, path := range []string{
		"/api/comics/" + upload.ID,
		"/api/comics/" + upload.ID + "/pages/0",
		"/api/comics/" + upload.ID + "/cover",
	} {
		resp, body := getReq(t, bob, ts.URL+path)
		if resp.StatusCode != 404 {
			t.Fatalf("GET %s as a stranger = %d %s, want 404", path, resp.StatusCode, body)
		}
		if bytes.Equal(body, pages[0]) {
			t.Fatalf("GET %s leaked the page bytes", path)
		}
	}
	if resp, _ := sendJSON(t, bob, "PUT", ts.URL+"/api/comics/"+upload.ID+"/progress",
		mustJSON(t, api.ProgressRequest{Page: 1})); resp.StatusCode != 404 {
		t.Fatalf("progress on an invisible comic = %d, want 404", resp.StatusCode)
	}
	if resp, _ := sendJSON(t, bob, "PUT", ts.URL+"/api/comics/"+upload.ID+"/tags",
		mustJSON(t, api.SetTagsRequest{Tags: []string{"mine now"}})); resp.StatusCode != 404 {
		t.Fatalf("tagging an invisible comic = %d, want 404", resp.StatusCode)
	}
	if resp, _ := doReq(t, bob, "DELETE", ts.URL+"/api/comics/"+upload.ID); resp.StatusCode != 404 {
		t.Fatalf("deleting an invisible comic = %d, want 404", resp.StatusCode)
	}
	_, body := getReq(t, bob, ts.URL+"/api/comics")
	var list api.ComicList
	json.Unmarshal(body, &list)
	if list.Total != 0 || len(list.Comics) != 0 {
		t.Fatalf("a stranger's library should be empty, got %+v", list)
	}

	// Sharing the collection it sits in is the opt-in that changes that, and it
	// grants reading and nothing else.
	_, body = post(t, alice, ts.URL+"/api/collections", mustJSON(t,
		api.CreateCollectionRequest{Name: "Stash", Shared: true}))
	var col api.Collection
	if err := json.Unmarshal(body, &col); err != nil {
		t.Fatalf("decode collection: %v", err)
	}
	if resp, body := post(t, alice, ts.URL+"/api/collections/"+col.ID+"/comics",
		mustJSON(t, api.CollectionComicRequest{ComicID: upload.ID})); resp.StatusCode != 200 {
		t.Fatalf("add to collection: %d %s", resp.StatusCode, body)
	}
	resp, body := getReq(t, bob, ts.URL+"/api/comics/"+upload.ID+"/pages/0")
	if resp.StatusCode != 200 || !bytes.Equal(body, pages[0]) {
		t.Fatalf("a shared upload should be readable: %d", resp.StatusCode)
	}
	if resp, _ := doReq(t, bob, "DELETE", ts.URL+"/api/comics/"+upload.ID); resp.StatusCode != 403 {
		t.Fatalf("a shared upload is readable, not deletable: got %d, want 403", resp.StatusCode)
	}
}

// TestDeleteComic: an upload and its file go together, and a library comic
// cannot be deleted through the API at all.
func TestDeleteComic(t *testing.T) {
	_, ts, st, cfg := libraryServer(t, nil)
	alice := adminClient(t, ts, st)
	aliceID := userID(t, st, "Alice")

	lib, _ := addComic(t, st, cfg.LibraryRoot, "Library.cbz", 2, store.ComicRow{})
	if resp, body := doReq(t, alice, "DELETE", ts.URL+"/api/comics/"+lib.ID); resp.StatusCode != 400 {
		t.Fatalf("deleting a library comic should be refused: %d %s", resp.StatusCode, body)
	}
	if _, err := st.GetComic(aliceID, lib.ID); err != nil {
		t.Fatalf("a refused delete must leave the row alone: %v", err)
	}

	upload, _ := addComic(t, st, cfg.UploadsDir, "mine.cbz", 2,
		store.ComicRow{OwnerID: aliceID, Source: store.SourceUpload})
	if resp, body := doReq(t, alice, "DELETE", ts.URL+"/api/comics/"+upload.ID); resp.StatusCode != 200 {
		t.Fatalf("the owner should be able to delete their upload: %d %s", resp.StatusCode, body)
	}
	if _, err := st.GetComic(aliceID, upload.ID); err == nil {
		t.Fatal("a deleted upload should be gone from the store")
	}
	if _, err := os.Stat(filepath.Join(cfg.UploadsDir, upload.Path)); !os.IsNotExist(err) {
		t.Fatalf("a deleted upload should take its file with it, got err=%v", err)
	}
}

// TestListFiltersAndPagination pins the query parameters the library grid drives.
func TestListFiltersAndPagination(t *testing.T) {
	_, ts, st, cfg := libraryServer(t, nil)
	alice := adminClient(t, ts, st)

	a, _ := addComic(t, st, cfg.LibraryRoot, "a.cbz", 1, store.ComicRow{Title: "Bone", Series: "Bone"})
	addComic(t, st, cfg.LibraryRoot, "b.cbz", 1, store.ComicRow{Title: "Akira", Series: "Akira"})
	addComic(t, st, cfg.LibraryRoot, "c.cbz", 1, store.ComicRow{Title: "Akira 2", Series: "Akira"})
	if resp, body := sendJSON(t, alice, "PUT", ts.URL+"/api/comics/"+a.ID+"/tags",
		mustJSON(t, api.SetTagsRequest{Tags: []string{"horror"}})); resp.StatusCode != 200 {
		t.Fatalf("set tags: %d %s", resp.StatusCode, body)
	}

	list := func(query string) api.ComicList {
		t.Helper()
		resp, body := getReq(t, alice, ts.URL+"/api/comics"+query)
		if resp.StatusCode != 200 {
			t.Fatalf("GET /api/comics%s: %d %s", query, resp.StatusCode, body)
		}
		var l api.ComicList
		if err := json.Unmarshal(body, &l); err != nil {
			t.Fatalf("decode list: %v", err)
		}
		return l
	}

	if l := list(""); l.Total != 3 || len(l.Comics) != 3 {
		t.Fatalf("unfiltered list = %d of %d, want 3 of 3", len(l.Comics), l.Total)
	}
	if l := list("?series=Akira"); l.Total != 2 {
		t.Fatalf("series filter = %d, want 2", l.Total)
	}
	if l := list("?tag=horror"); l.Total != 1 || l.Comics[0].ID != a.ID {
		t.Fatalf("tag filter = %+v, want the tagged comic", l.Comics)
	}
	if l := list("?q=aki"); l.Total != 2 {
		t.Fatalf("substring search = %d, want 2", l.Total)
	}
	// A wildcard in the search box is a character, not a query.
	if l := list("?q=%25"); l.Total != 0 {
		t.Fatalf("a literal %% should match nothing, got %d", l.Total)
	}
	// Pagination reports the whole match, not just the page.
	l := list("?limit=2")
	if len(l.Comics) != 2 || l.Total != 3 || l.Limit != 2 {
		t.Fatalf("page = %d comics of %d, want 2 of 3", len(l.Comics), l.Total)
	}
	if l := list("?limit=2&offset=2"); len(l.Comics) != 1 || l.Offset != 2 {
		t.Fatalf("second page = %d comics, want 1", len(l.Comics))
	}
	if resp, _ := getReq(t, alice, ts.URL+"/api/comics?limit=nope"); resp.StatusCode != 400 {
		t.Fatalf("a junk limit should be a 400, got %d", resp.StatusCode)
	}

	_, body := getReq(t, alice, ts.URL+"/api/tags")
	var tags []api.Tag
	if err := json.Unmarshal(body, &tags); err != nil {
		t.Fatalf("decode tags: %v", err)
	}
	if len(tags) != 1 || tags[0].Name != "horror" || tags[0].Count != 1 {
		t.Fatalf("tags = %+v, want horror x1", tags)
	}
}

// TestLibraryRoutesWithoutALibrary: a server with no scanner attached reports an
// idle library rather than panicking, and refuses to pretend it scanned.
func TestLibraryRoutesWithoutALibrary(t *testing.T) {
	_, ts, st, _ := libraryServer(t, nil)
	alice := adminClient(t, ts, st)

	resp, body := getReq(t, alice, ts.URL+"/api/library/status")
	if resp.StatusCode != 200 {
		t.Fatalf("library status: %d %s", resp.StatusCode, body)
	}
	var status api.LibraryStatus
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.Scanning {
		t.Fatal("a server with no scanner is not scanning")
	}
	if resp, _ := post(t, alice, ts.URL+"/api/library/scan", nil); resp.StatusCode != 503 {
		t.Fatalf("scan without a library = %d, want 503", resp.StatusCode)
	}
	// The gate is admin-only regardless.
	bob := enrolledUser(t, ts, alice, "Bob")
	if resp, _ := post(t, bob, ts.URL+"/api/library/scan", nil); resp.StatusCode != 403 {
		t.Fatalf("scan as a non-admin = %d, want 403", resp.StatusCode)
	}
}
