package cbz

import (
	"archive/zip"
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// pngBytes builds a real PNG so DecodeConfig has something to parse.
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 0x40, 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func jpegBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type entry struct {
	name string
	data []byte
}

func writeZip(t *testing.T, name string, entries []entry) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOpenListsPagesInNaturalOrder(t *testing.T) {
	p := writeZip(t, "Test 01.cbz", []entry{
		{"10.png", pngBytes(t, 4, 6)},
		{"2.png", pngBytes(t, 4, 6)},
		{"1.png", pngBytes(t, 4, 6)},
	})
	a, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	want := []string{"1.png", "2.png", "10.png"}
	got := a.PageNames()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestOpenFiltersNonImagesAndJunk(t *testing.T) {
	p := writeZip(t, "Junk 01.cbz", []entry{
		{"__MACOSX/._1.png", pngBytes(t, 4, 4)},
		{"pages/._2.png", pngBytes(t, 4, 4)},
		{".DS_Store", []byte("junk")},
		{"pages/.DS_Store", []byte("junk")},
		{"readme.txt", []byte("hello")},
		{"pages/1.png", pngBytes(t, 4, 4)},
		{"pages/2.jpg", jpegBytes(t, 4, 4)},
	})
	a, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	got := a.PageNames()
	if len(got) != 2 || got[0] != "pages/1.png" || got[1] != "pages/2.jpg" {
		t.Fatalf("got %v, want [pages/1.png pages/2.jpg]", got)
	}
}

func TestPagesReportsDimensions(t *testing.T) {
	p := writeZip(t, "Dims 01.cbz", []entry{
		{"1.png", pngBytes(t, 40, 60)},
		{"2.jpg", jpegBytes(t, 20, 30)},
		{"3.png", []byte("not actually a png")},
	})
	a, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	pages, err := a.Pages()
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 3 {
		t.Fatalf("got %d pages, want 3", len(pages))
	}
	if pages[0].Width != 40 || pages[0].Height != 60 {
		t.Errorf("page 0: got %dx%d, want 40x60", pages[0].Width, pages[0].Height)
	}
	if pages[1].Width != 20 || pages[1].Height != 30 {
		t.Errorf("page 1: got %dx%d, want 20x30", pages[1].Width, pages[1].Height)
	}
	// An unparseable entry stays a page with unknown dimensions rather than
	// failing the listing.
	if pages[2].Width != 0 || pages[2].Height != 0 {
		t.Errorf("page 2: got %dx%d, want 0x0", pages[2].Width, pages[2].Height)
	}
	if pages[2].Index != 2 || pages[2].Name != "3.png" {
		t.Errorf("page 2: got index=%d name=%q", pages[2].Index, pages[2].Name)
	}
}

func TestPageReadsEntryAndContentType(t *testing.T) {
	want := pngBytes(t, 8, 8)
	p := writeZip(t, "Read 01.cbz", []entry{
		{"1.png", want},
		{"2.jpg", jpegBytes(t, 8, 8)},
		{"3.avif", []byte("avif-ish")},
	})
	a, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	rc, ct, err := a.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("page bytes differ from what was written")
	}
	if ct != "image/png" {
		t.Errorf("got content type %q, want image/png", ct)
	}

	if _, ct, _ = a.Page(1); ct != "image/jpeg" {
		t.Errorf("got content type %q, want image/jpeg", ct)
	}
	if _, ct, _ = a.Page(2); ct != "image/avif" {
		t.Errorf("got content type %q, want image/avif", ct)
	}

	for _, i := range []int{-1, 3} {
		if _, _, err := a.Page(i); !errors.Is(err, ErrPageRange) {
			t.Errorf("Page(%d): got %v, want ErrPageRange", i, err)
		}
	}
}

func TestPageRejectsTraversingEntry(t *testing.T) {
	p := writeZip(t, "Evil 01.cbz", []entry{
		{"../../etc/passwd.png", pngBytes(t, 4, 4)},
		{"ok.png", pngBytes(t, 4, 4)},
	})
	a, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	// The traversing entry sorts first, so page 0 is the hostile one.
	if _, _, err := a.Page(0); !errors.Is(err, ErrUnsafeEntry) {
		t.Fatalf("got %v, want ErrUnsafeEntry", err)
	}
}

func TestSafeEntryName(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{"1.png", true},
		{"pages/1.png", true},
		{"a/../b/1.png", true},
		{"", false},
		{"/etc/passwd", false},
		{"../1.png", false},
		{"../../etc/passwd", false},
		{"a/../../1.png", false},
		{`..\..\windows\system32`, false},
		{`C:\evil.png`, false},
	}
	for _, tc := range tests {
		if got := safeEntryName(tc.name); got != tc.ok {
			t.Errorf("safeEntryName(%q) = %v, want %v", tc.name, got, tc.ok)
		}
	}
}

func TestOpenNoPages(t *testing.T) {
	p := writeZip(t, "Empty 01.cbz", []entry{{"readme.txt", []byte("nothing here")}})
	a, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.PageCount() != 0 {
		t.Fatalf("got %d pages, want 0", a.PageCount())
	}
	if _, err := a.Pages(); !errors.Is(err, ErrNoPages) {
		t.Fatalf("got %v, want ErrNoPages", err)
	}
}

func TestOpenNonZip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "broken.cbz")
	if err := os.WriteFile(p, []byte("definitely not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(p); err == nil {
		t.Fatal("expected an error opening a non-zip")
	}
}

func TestCoverDefaultsToFirstPage(t *testing.T) {
	first := pngBytes(t, 10, 20)
	p := writeZip(t, "Cover 01.cbz", []entry{
		{"2.png", pngBytes(t, 30, 40)},
		{"1.png", first},
	})
	a, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	rc, err := a.Cover()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, first) {
		t.Error("cover is not page 0")
	}
}

func TestCoverUsesComicInfoFrontCover(t *testing.T) {
	cover := pngBytes(t, 33, 44)
	p := writeZip(t, "Cover 02.cbz", []entry{
		{"1.png", pngBytes(t, 10, 10)},
		{"2.png", cover},
		{"ComicInfo.xml", []byte(`<?xml version="1.0"?>
<ComicInfo>
  <Pages>
    <Page Image="0" Type="Other"/>
    <Page Image="1" Type="FrontCover"/>
  </Pages>
</ComicInfo>`)},
	})
	a, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	rc, err := a.Cover()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, cover) {
		t.Error("cover is not the FrontCover-marked page")
	}
}

func TestCoverIgnoresOutOfRangeFrontCover(t *testing.T) {
	first := pngBytes(t, 10, 20)
	p := writeZip(t, "Cover 03.cbz", []entry{
		{"1.png", first},
		{"ComicInfo.xml", []byte(`<ComicInfo><Pages><Page Image="99" Type="FrontCover"/></Pages></ComicInfo>`)},
	})
	a, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	rc, err := a.Cover()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, first) {
		t.Error("a bogus FrontCover index should fall back to page 0")
	}
}
