package server

import (
	"context"
	"os"
	"testing"

	"github.com/SeriousBug/longbox/internal/library"
	"github.com/SeriousBug/longbox/internal/store"
)

// TestScannerWarmedCoverIsAHandlerCacheHit pins the two halves of the cover
// cache to one layout.
//
// The scanner thumbnails every comic it walks and writes the result into
// dataDir/covers; the handler reads that same directory. They had drifted onto
// different path schemes, so the scanner's work was never once hit: every
// comic's first cover request paid a full decode (~800ms for an AVIF page) that
// had already been paid during the scan. That wasted decode is also the
// amplification a decompression bomb rides in on, which is why this is pinned
// rather than left to the two packages to agree by convention.
//
// The test drives the real scanner rather than writing a file into the tree by
// hand: a hand-placed file would only assert that this test and the handler
// agree, which is not the property that broke.
func TestScannerWarmedCoverIsAHandlerCacheHit(t *testing.T) {
	_, ts, st, cfg := libraryServer(t, func(c *Config) {
		// What main does: one covers dir, derived once, handed to both.
		c.CoverCacheDir = library.CoversDir(c.LibraryRoot + "-data")
	})
	alice := adminClient(t, ts, st)
	row, _ := addComic(t, st, cfg.LibraryRoot, "Warmed.cbz", 2, store.ComicRow{})

	lib := library.New(st, library.Config{
		Root:    cfg.LibraryRoot,
		DataDir: cfg.LibraryRoot + "-data",
	}, nil)
	if err := lib.Scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}

	// The scanner cached a cover for this comic's bytes...
	warmed := lib.CoverPath(row.ContentHash)
	if _, err := os.Stat(warmed); err != nil {
		t.Fatalf("the scanner did not cache a cover at %s: %v", warmed, err)
	}
	// ...and the handler must look for it in exactly that place. A marker is
	// used rather than comparing JPEGs because the handler regenerating would
	// produce identical bytes -- and a cache that is never hit but always
	// regenerates the same answer is precisely the bug, invisible to any
	// assertion on content.
	if err := os.WriteFile(warmed, []byte("warmed-by-the-scanner"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp, body := getReq(t, alice, ts.URL+"/api/comics/"+row.ID+"/cover")
	if resp.StatusCode != 200 {
		t.Fatalf("cover: %d %s", resp.StatusCode, body)
	}
	if string(body) != "warmed-by-the-scanner" {
		t.Fatalf("the handler missed the cover the scanner warmed and decoded the page again; "+
			"got %d bytes back", len(body))
	}
}
