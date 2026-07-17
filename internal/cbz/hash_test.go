package cbz

import (
	"os"
	"path/filepath"
	"testing"
)

func TestContentHashSurvivesRename(t *testing.T) {
	entries := []entry{{"1.png", pngBytes(t, 8, 8)}, {"2.png", pngBytes(t, 8, 9)}}
	a := writeZip(t, "Series 001.cbz", entries)
	ha, err := ContentHash(a)
	if err != nil {
		t.Fatal(err)
	}

	// A rename is the case this hash exists for: the same bytes at a new path
	// must match the existing library row.
	renamed := filepath.Join(filepath.Dir(a), "Renamed 001.cbz")
	if err := os.Rename(a, renamed); err != nil {
		t.Fatal(err)
	}
	hb, err := ContentHash(renamed)
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Errorf("rename changed the hash: %s != %s", ha, hb)
	}
}

func TestContentHashIsStableAcrossIdenticalArchives(t *testing.T) {
	entries := []entry{{"1.png", pngBytes(t, 8, 8)}, {"2.png", pngBytes(t, 8, 9)}}
	h1, err := ContentHash(writeZip(t, "a.cbz", entries))
	if err != nil {
		t.Fatal(err)
	}
	h2, err := ContentHash(writeZip(t, "b.cbz", entries))
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Errorf("identical archives hashed differently: %s != %s", h1, h2)
	}
}

func TestContentHashIgnoresEntryOrder(t *testing.T) {
	p1 := pngBytes(t, 8, 8)
	p2 := pngBytes(t, 8, 9)
	h1, err := ContentHash(writeZip(t, "a.cbz", []entry{{"1.png", p1}, {"2.png", p2}}))
	if err != nil {
		t.Fatal(err)
	}
	h2, err := ContentHash(writeZip(t, "b.cbz", []entry{{"2.png", p2}, {"1.png", p1}}))
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Errorf("entry order changed the hash: %s != %s", h1, h2)
	}
}

func TestContentHashChangesWithContent(t *testing.T) {
	base := []entry{{"1.png", pngBytes(t, 8, 8)}}
	h0, err := ContentHash(writeZip(t, "base.cbz", base))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]entry{
		"different page bytes": {{"1.png", pngBytes(t, 9, 9)}},
		"renamed entry":        {{"01.png", pngBytes(t, 8, 8)}},
		"extra entry":          {{"1.png", pngBytes(t, 8, 8)}, {"2.png", pngBytes(t, 8, 8)}},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			h, err := ContentHash(writeZip(t, "x.cbz", entries))
			if err != nil {
				t.Fatal(err)
			}
			if h == h0 {
				t.Errorf("hash did not change for %s", name)
			}
		})
	}
}

func TestArchiveHashMatchesContentHash(t *testing.T) {
	p := writeZip(t, "Series 001.cbz", []entry{{"1.png", pngBytes(t, 8, 8)}})
	want, err := ContentHash(p)
	if err != nil {
		t.Fatal(err)
	}
	a, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if got := a.Hash(); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestContentHashMissingFile(t *testing.T) {
	if _, err := ContentHash(filepath.Join(t.TempDir(), "nope.cbz")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}
