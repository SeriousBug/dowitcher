package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"strconv"
	"testing"

	"github.com/SeriousBug/dowitcher/internal/api"
)

// cbzBytes builds a CBZ of n pages in memory.
func cbzBytes(t *testing.T, n int) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := range n {
		w, err := zw.Create(strconv.Itoa(i+1) + ".png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(pagePNG(t, i+1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// cbzUploadBody builds the multipart POST /api/comics expects: an options part
// plus the archive.
func cbzUploadBody(t *testing.T, opts api.ImportOptions, name string, data []byte) (string, []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField(optionsPart, string(mustJSON(t, opts))); err != nil {
		t.Fatal(err)
	}
	w, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return mw.FormDataContentType(), buf.Bytes()
}

// TestUploadCBZCreatesComic is the whole feature as a client sees it: post an
// archive, get a comic back, and be able to read it straight away.
func TestUploadCBZCreatesComic(t *testing.T) {
	ts, st, _ := importServer(t, nil)
	alice := adminClient(t, ts, st)

	col := createCollection(t, ts, alice, api.CreateCollectionRequest{Name: "Shelf"})
	ct, body := cbzUploadBody(t, api.ImportOptions{Name: "Dropped In", CollectionID: col.ID},
		"whatever.cbz", cbzBytes(t, 3))
	resp, respBody := postUpload(t, alice, ts.URL+"/api/comics", ct, body)
	if resp.StatusCode != 200 {
		t.Fatalf("post cbz: %d %s", resp.StatusCode, respBody)
	}
	var comic api.Comic
	if err := json.Unmarshal(respBody, &comic); err != nil {
		t.Fatalf("decode comic: %v", err)
	}
	if comic.ID == "" || comic.Title != "Dropped In" || comic.PageCount != 3 {
		t.Fatalf("comic = %+v, want Dropped In with 3 pages", comic)
	}
	if comic.FileSize == 0 {
		t.Error("an uploaded comic should carry the archive's size")
	}

	resp, respBody = getReq(t, alice, ts.URL+"/api/comics/"+comic.ID)
	if resp.StatusCode != 200 {
		t.Fatalf("the uploaded comic should be readable: %d %s", resp.StatusCode, respBody)
	}
	var detail api.ComicDetail
	json.Unmarshal(respBody, &detail)
	if len(detail.Pages) != 3 {
		t.Fatalf("pages = %d, want 3", len(detail.Pages))
	}
	if resp, _ := getReq(t, alice, ts.URL+"/api/comics/"+comic.ID+"/pages/0"); resp.StatusCode != 200 {
		t.Fatalf("uploaded page: %d", resp.StatusCode)
	}
	if got := listCollectionComics(t, ts, alice, col.ID); len(got) != 1 || got[0] != comic.ID {
		t.Fatalf("collection = %v, want the uploaded comic", got)
	}
}

// TestUploadCBZReadsMetadataFromTheUploadedName pins the reason the archive is
// opened before it is moved: it is renamed to its comic id, and an id parses to
// no series at all.
func TestUploadCBZReadsMetadataFromTheUploadedName(t *testing.T) {
	ts, st, _ := importServer(t, nil)
	alice := adminClient(t, ts, st)

	ct, body := cbzUploadBody(t, api.ImportOptions{}, "Saga v02 05.cbz", cbzBytes(t, 1))
	resp, respBody := postUpload(t, alice, ts.URL+"/api/comics", ct, body)
	if resp.StatusCode != 200 {
		t.Fatalf("post cbz: %d %s", resp.StatusCode, respBody)
	}
	var comic api.Comic
	json.Unmarshal(respBody, &comic)
	if comic.Series != "Saga" || comic.Volume != "2" || comic.Number != "5" {
		t.Fatalf("comic = %+v, want the name parsed into Saga v2 #5", comic)
	}
}

func TestUploadCBZRejections(t *testing.T) {
	cases := []struct {
		name     string
		file     string
		data     []byte
		wantCode int
	}{
		{"not a cbz", "page.png", pagePNG(t, 1), 400},
		{"not a zip", "broken.cbz", []byte("this is not an archive"), 400},
		{"no pages", "empty.cbz", cbzBytes(t, 0), 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, st, _ := importServer(t, nil)
			alice := adminClient(t, ts, st)
			ct, body := cbzUploadBody(t, api.ImportOptions{}, tc.file, tc.data)
			resp, respBody := postUpload(t, alice, ts.URL+"/api/comics", ct, body)
			if resp.StatusCode != tc.wantCode {
				t.Fatalf("post %s: %d %s, want %d", tc.file, resp.StatusCode, respBody, tc.wantCode)
			}
			_, listBody := getReq(t, alice, ts.URL+"/api/comics")
			var list api.ComicList
			json.Unmarshal(listBody, &list)
			if len(list.Comics) != 0 {
				t.Fatalf("a refused upload must not leave a comic behind, got %d", len(list.Comics))
			}
		})
	}
}

// TestUploadCBZNeedsAuth: the route creates a comic owned by the caller, so an
// anonymous one has no business reaching it.
func TestUploadCBZNeedsAuth(t *testing.T) {
	ts, _, _ := importServer(t, nil)
	ct, body := cbzUploadBody(t, api.ImportOptions{}, "x.cbz", cbzBytes(t, 1))
	resp, _ := postUpload(t, ts.Client(), ts.URL+"/api/comics", ct, body)
	if resp.StatusCode != 401 {
		t.Fatalf("anonymous upload = %d, want 401", resp.StatusCode)
	}
}
