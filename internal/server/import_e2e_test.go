package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/imports"
	"github.com/SeriousBug/dowitcher/internal/store"
)

// importServer is a library server with the real import manager attached, which
// is how main wires it.
func importServer(t *testing.T, opt func(*Config)) (*httptest.Server, *store.Store, Config) {
	t.Helper()
	srv, ts, st, cfg := libraryServer(t, opt)
	m, err := imports.NewManager(st, srv.Hub(), imports.ManagerConfig{
		UploadsDir:    cfg.UploadsDir,
		ReportDir:     filepath.Join(t.TempDir(), "reports"),
		ImportTempDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("new import manager: %v", err)
	}
	srv.SetImporter(m)
	// The queue no longer runs on submit: a worker pool drains it, so the test
	// needs Run going or every job would sit at "queued" forever.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go m.Run(ctx)
	return ts, st, cfg
}

// uploadBody builds the multipart request the importer expects: an options part
// plus one part per image.
func uploadBody(t *testing.T, opts api.ImportOptions, files map[string][]byte) (string, []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField(optionsPart, string(mustJSON(t, opts))); err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		w, err := mw.CreateFormFile("files", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return mw.FormDataContentType(), buf.Bytes()
}

func postUpload(t *testing.T, client *http.Client, url string, ct string, body []byte) (*http.Response, []byte) {
	t.Helper()
	resp, err := client.Post(url, ct, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, data
}

// TestImportUploadToComic is the whole import as a client sees it: post a folder,
// get a job, and end up with a comic you own.
func TestImportUploadToComic(t *testing.T) {
	ts, st, _ := importServer(t, nil)
	alice := adminClient(t, ts, st)

	col := createCollection(t, ts, alice, api.CreateCollectionRequest{Name: "Imports"})
	ct, body := uploadBody(t, api.ImportOptions{Name: "Uploaded", CollectionID: col.ID}, map[string][]byte{
		"folder/01.png": pagePNG(t, 1),
		"folder/02.png": pagePNG(t, 2),
		// A byte-identical page: the dedupe pass folds it into 01.
		"folder/03.png": pagePNG(t, 1),
	})
	resp, respBody := postUpload(t, alice, ts.URL+"/api/imports", ct, body)
	if resp.StatusCode != 200 {
		t.Fatalf("post import: %d %s", resp.StatusCode, respBody)
	}
	var job api.ImportJob
	if err := json.Unmarshal(respBody, &job); err != nil {
		t.Fatalf("decode job: %v", err)
	}
	if job.ID == "" {
		t.Fatal("an accepted import must name its job")
	}

	var done api.ImportJob
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		_, body := getReq(t, alice, ts.URL+"/api/imports")
		var jobs []api.ImportJob
		if err := json.Unmarshal(body, &jobs); err != nil {
			t.Fatalf("decode jobs: %v", err)
		}
		if len(jobs) == 1 && jobs[0].FinishedAt != 0 {
			done = jobs[0]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if done.Stage != api.StageDone {
		t.Fatalf("job = %+v, want a finished import", done)
	}
	if done.ComicID == "" || done.PageCount != 2 || done.ExactDupes != 1 {
		t.Fatalf("job = %+v, want 2 pages with 1 exact dupe", done)
	}

	// The comic is filed, owned, readable and in the collection asked for.
	resp, respBody = getReq(t, alice, ts.URL+"/api/comics/"+done.ComicID)
	if resp.StatusCode != 200 {
		t.Fatalf("the imported comic should be readable: %d %s", resp.StatusCode, respBody)
	}
	var detail api.ComicDetail
	json.Unmarshal(respBody, &detail)
	if detail.Comic.Title != "Uploaded" || len(detail.Pages) != 2 {
		t.Fatalf("imported comic = %+v with %d pages, want Uploaded with 2", detail.Comic, len(detail.Pages))
	}
	if got := listCollectionComics(t, ts, alice, col.ID); len(got) != 1 || got[0] != done.ComicID {
		t.Fatalf("collection = %v, want the imported comic", got)
	}
	if resp, _ := getReq(t, alice, ts.URL+"/api/comics/"+done.ComicID+"/pages/0"); resp.StatusCode != 200 {
		t.Fatalf("imported page: %d", resp.StatusCode)
	}

	// The dupe report says what got merged.
	resp, respBody = getReq(t, alice, ts.URL+"/api/imports/"+done.ID+"/dupes")
	if resp.StatusCode != 200 {
		t.Fatalf("dupes: %d %s", resp.StatusCode, respBody)
	}
	var groups []api.DupeGroup
	if err := json.Unmarshal(respBody, &groups); err != nil {
		t.Fatalf("decode dupes: %v", err)
	}
	if len(groups) != 1 || len(groups[0].Dropped) != 1 {
		t.Fatalf("dupe report = %#v, want one merged pair", groups)
	}

	// An import belongs to whoever started it.
	bob := enrolledUser(t, ts, alice, "Bob")
	_, respBody = getReq(t, bob, ts.URL+"/api/imports")
	var bobJobs []api.ImportJob
	json.Unmarshal(respBody, &bobJobs)
	if len(bobJobs) != 0 {
		t.Fatalf("bob's imports = %#v, want none", bobJobs)
	}
	if resp, _ := getReq(t, bob, ts.URL+"/api/imports/"+done.ID+"/dupes"); resp.StatusCode != 404 {
		t.Fatalf("bob reading alice's dupe report = %d, want 404", resp.StatusCode)
	}
	if resp, _ := post(t, bob, ts.URL+"/api/imports/"+done.ID+"/cancel", nil); resp.StatusCode != 404 {
		t.Fatalf("bob cancelling alice's import = %d, want 404", resp.StatusCode)
	}
	// Cancelling something that already ended is a conflict, not a success.
	if resp, _ := post(t, alice, ts.URL+"/api/imports/"+done.ID+"/cancel", nil); resp.StatusCode != 409 {
		t.Fatalf("cancelling a finished import = %d, want 409", resp.StatusCode)
	}
}

// TestImportRejectsNonImages: the wrong folder is caught at the door rather than
// twenty minutes later as an empty comic. The rejected job is marked failed so
// it does not spin forever.
func TestImportRejectsNonImages(t *testing.T) {
	ts, st, _ := importServer(t, nil)
	alice := adminClient(t, ts, st)

	ct, body := uploadBody(t, api.ImportOptions{}, map[string][]byte{"notes.txt": []byte("hello")})
	resp, respBody := postUpload(t, alice, ts.URL+"/api/imports", ct, body)
	if resp.StatusCode != 400 {
		t.Fatalf("uploading a text file = %d %s, want 400", resp.StatusCode, respBody)
	}
	_, respBody = getReq(t, alice, ts.URL+"/api/imports")
	var jobs []api.ImportJob
	json.Unmarshal(respBody, &jobs)
	if len(jobs) != 1 || jobs[0].Stage != api.StageFailed || jobs[0].FinishedAt == 0 {
		t.Fatalf("a refused upload must leave a failed job, got %#v", jobs)
	}
}

// TestImportEnforcesSizeCap: the cap is checked against the bytes as they land,
// not against a header the client controls.
func TestImportEnforcesSizeCap(t *testing.T) {
	ts, st, _ := importServer(t, func(c *Config) { c.MaxUploadBytes = 32 })
	alice := adminClient(t, ts, st)

	ct, body := uploadBody(t, api.ImportOptions{}, map[string][]byte{"big.png": pagePNG(t, 1)})
	resp, respBody := postUpload(t, alice, ts.URL+"/api/imports", ct, body)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("an oversized upload = %d %s, want 413", resp.StatusCode, respBody)
	}
}

// TestImportsWithoutAnImporter: a server with no pipeline refuses the upload
// rather than accepting bytes nothing will ever process.
func TestImportsWithoutAnImporter(t *testing.T) {
	_, ts, st, _ := libraryServer(t, nil)
	alice := adminClient(t, ts, st)
	ct, body := uploadBody(t, api.ImportOptions{}, map[string][]byte{"a.png": pagePNG(t, 1)})
	if resp, _ := postUpload(t, alice, ts.URL+"/api/imports", ct, body); resp.StatusCode != 503 {
		t.Fatalf("import without an importer = %d, want 503", resp.StatusCode)
	}
	// Listing is a store read, so it still works and simply says nothing.
	if resp, body := getReq(t, alice, ts.URL+"/api/imports"); resp.StatusCode != 200 || string(body) != "[]\n" {
		t.Fatalf("list imports = %d %q, want an empty list", resp.StatusCode, body)
	}
}
