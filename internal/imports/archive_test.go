package imports

import (
	"archive/tar"
	"bytes"
	"context"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/cbz"
	"github.com/SeriousBug/dowitcher/internal/store"
)

// cbtFile writes a .cbt (TAR) of three synthetic PNG pages plus a non-image
// entry, and returns its path. TAR is the one container this package can build
// with the standard library, so it stands in for CBR/CB7 in the end-to-end test;
// the three backends share the same extract-then-pipeline path.
func cbtFile(t *testing.T, dir string) string {
	t.Helper()
	pngBytes := func(seed int64) []byte {
		var buf bytes.Buffer
		if err := png.Encode(&buf, synth(64, 96, seed, 0)); err != nil {
			t.Fatalf("encode: %v", err)
		}
		return buf.Bytes()
	}
	path := filepath.Join(dir, "My Archive.cbt")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	add := func(name string, b []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(b)), Mode: 0o644}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	add("001.png", pngBytes(1))
	add("002.png", pngBytes(2))
	add("003.png", pngBytes(3))
	add("ComicInfo.txt", []byte("not a page"))
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return path
}

// TestConvertsLibraryArchive: a CBT dropped in the library folder becomes an
// ownerless, server-wide comic whose CBZ lives in the uploads dir with its pages
// re-encoded to AVIF, and the source archive is left in place.
func TestConvertsLibraryArchive(t *testing.T) {
	m, st, _, _ := testManager(t)
	runWorkers(t, m)

	libDir := t.TempDir()
	cbt := cbtFile(t, libDir)

	m.EnqueueLibraryArchive(cbt)

	var comicID string
	waitFor(t, "the library archive to convert", func() bool {
		jobs, err := st.ListAllImportJobs(10)
		if err != nil || len(jobs) == 0 {
			return false
		}
		j := jobs[0]
		if j.FinishedAt == 0 {
			return false
		}
		comicID = j.ComicID
		return true
	})
	if comicID == "" {
		t.Fatal("the conversion produced no comic")
	}

	row, err := st.ComicRowByID(comicID)
	if err != nil {
		t.Fatalf("comic row: %v", err)
	}
	if row.Source != store.SourceLibraryArchive {
		t.Fatalf("source = %q, want %q", row.Source, store.SourceLibraryArchive)
	}
	if row.OwnerID != "" {
		t.Fatalf("a library-archive comic is ownerless, got owner %q", row.OwnerID)
	}
	if row.PageCount != 3 {
		t.Fatalf("page count = %d, want 3", row.PageCount)
	}

	// The source archive is on a read-only library by contract: it must survive.
	if _, err := os.Stat(cbt); err != nil {
		t.Fatalf("the source archive should be left in place: %v", err)
	}

	// The CBZ is in the uploads dir, and its pages defaulted to AVIF.
	cbzPath := filepath.Join(m.cfg.UploadsDir, row.Path)
	a, err := cbz.Open(cbzPath)
	if err != nil {
		t.Fatalf("open produced cbz: %v", err)
	}
	defer a.Close()
	for _, name := range a.PageNames() {
		if !strings.HasSuffix(strings.ToLower(name), ".avif") {
			t.Fatalf("page %q is not AVIF; the convert path should default to AVIF", name)
		}
	}
}

// TestUploadedArchiveIsOwned: an uploaded archive files as the uploader's comic,
// not a server-wide one.
func TestUploadedArchiveIsOwned(t *testing.T) {
	m, st, _, user := testManager(t)
	runWorkers(t, m)

	stage := t.TempDir()
	cbt := cbtFile(t, stage)

	job, err := m.Begin(user.ID)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := m.StartArchive(context.Background(), job.ID, cbt, api.ImportOptions{}); err != nil {
		t.Fatalf("start archive: %v", err)
	}

	waitFor(t, "the upload to finish", func() bool {
		return jobStage(t, st, user.ID, job.ID).FinishedAt != 0
	})
	done := jobStage(t, st, user.ID, job.ID)
	if done.Stage != api.StageDone {
		t.Fatalf("stage = %q message=%q, want done", done.Stage, done.Message)
	}
	row, err := st.ComicRowByID(done.ComicID)
	if err != nil {
		t.Fatalf("comic row: %v", err)
	}
	if row.Source != store.SourceUpload || row.OwnerID != user.ID {
		t.Fatalf("row = source %q owner %q, want an upload owned by the uploader", row.Source, row.OwnerID)
	}
}
