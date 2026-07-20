package server

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
	"time"

	pdfapi "github.com/pdfcpu/pdfcpu/pkg/api"

	"github.com/SeriousBug/dowitcher/internal/api"
)

// buildTestPDF embeds each image as its own page, the shape of a scanned comic
// PDF, and returns the PDF bytes.
func buildTestPDF(t *testing.T, imgs [][]byte) []byte {
	t.Helper()
	readers := make([]io.Reader, len(imgs))
	for i := range imgs {
		readers[i] = bytes.NewReader(imgs[i])
	}
	var buf bytes.Buffer
	if err := pdfapi.ImportImages(nil, &buf, readers, nil, nil); err != nil {
		t.Fatalf("build test pdf: %v", err)
	}
	return buf.Bytes()
}

// TestImportPDFToComic posts a PDF to the import endpoint and asserts a comic
// with the right page count lands, its title taken from the filename.
func TestImportPDFToComic(t *testing.T) {
	ts, st, _ := importServer(t, nil)
	alice := adminClient(t, ts, st)

	pdf := buildTestPDF(t, [][]byte{pagePNG(t, 1), pagePNG(t, 2), pagePNG(t, 3)})
	// Name left empty so the server derives the title from the filename.
	ct, body := uploadBody(t, api.ImportOptions{}, map[string][]byte{"Adam Sarlech.pdf": pdf})
	resp, respBody := postUpload(t, alice, ts.URL+"/api/imports", ct, body)
	if resp.StatusCode != 200 {
		t.Fatalf("post pdf import: %d %s", resp.StatusCode, respBody)
	}
	var job api.ImportJob
	if err := json.Unmarshal(respBody, &job); err != nil {
		t.Fatalf("decode job: %v", err)
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
	if done.ComicID == "" || done.PageCount != 3 {
		t.Fatalf("job = %+v, want 3 pages", done)
	}

	resp, respBody = getReq(t, alice, ts.URL+"/api/comics/"+done.ComicID)
	if resp.StatusCode != 200 {
		t.Fatalf("the imported comic should be readable: %d %s", resp.StatusCode, respBody)
	}
	var detail api.ComicDetail
	json.Unmarshal(respBody, &detail)
	if detail.Comic.Title != "Adam Sarlech" || len(detail.Pages) != 3 {
		t.Fatalf("imported comic = %+v with %d pages, want \"Adam Sarlech\" with 3", detail.Comic, len(detail.Pages))
	}
}

// TestImportRejectsPDFMixedWithImages: a PDF is a self-contained book, so mixing
// it with loose images is refused at the door.
func TestImportRejectsPDFMixedWithImages(t *testing.T) {
	ts, st, _ := importServer(t, nil)
	alice := adminClient(t, ts, st)

	pdf := buildTestPDF(t, [][]byte{pagePNG(t, 1)})
	ct, body := uploadBody(t, api.ImportOptions{}, map[string][]byte{
		"book.pdf":  pdf,
		"stray.png": pagePNG(t, 2),
	})
	resp, respBody := postUpload(t, alice, ts.URL+"/api/imports", ct, body)
	if resp.StatusCode != 400 {
		t.Fatalf("mixing a PDF with images = %d %s, want 400", resp.StatusCode, respBody)
	}
}

// TestImportRejectsBadPDF: a file named .pdf that is not one fails the job
// cleanly rather than crashing the extractor.
func TestImportRejectsBadPDF(t *testing.T) {
	ts, st, _ := importServer(t, nil)
	alice := adminClient(t, ts, st)

	ct, body := uploadBody(t, api.ImportOptions{}, map[string][]byte{
		"broken.pdf": []byte("not a pdf at all"),
	})
	resp, respBody := postUpload(t, alice, ts.URL+"/api/imports", ct, body)
	// The upload itself is accepted; the extraction fails asynchronously.
	if resp.StatusCode != 200 {
		t.Fatalf("post bad pdf: %d %s", resp.StatusCode, respBody)
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		_, body := getReq(t, alice, ts.URL+"/api/imports")
		var jobs []api.ImportJob
		json.Unmarshal(body, &jobs)
		if len(jobs) == 1 && jobs[0].FinishedAt != 0 {
			if jobs[0].Stage != api.StageFailed {
				t.Fatalf("job = %+v, want a failed import", jobs[0])
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the bad PDF never resolved to a failed job")
}
