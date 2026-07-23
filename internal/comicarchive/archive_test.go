package comicarchive

import (
	"archive/tar"
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// tinyPNG is a valid 4x6 PNG, small enough to inline as a page in a fixture.
func tinyPNG(t *testing.T, c color.RGBA) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 4, 6))
	for x := 0; x < 4; x++ {
		for y := 0; y < 6; y++ {
			im.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, im); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func openerFor(b []byte) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
}

// extractedNames lists the files written under dir, slash-relative and sorted, so
// a test can assert on what an extraction produced regardless of walk order.
func extractedNames(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	sort.Strings(out)
	return out
}

func TestExtractListWritesImagesAndSkipsTheRest(t *testing.T) {
	red := tinyPNG(t, color.RGBA{255, 0, 0, 255})
	green := tinyPNG(t, color.RGBA{0, 255, 0, 255})
	entries := []listEntry{
		{name: "01.png", open: openerFor(red)},
		{name: "sub/02.png", open: openerFor(green)},
		{name: "readme.txt", open: openerFor([]byte("not a page"))},
		{name: "__MACOSX/._01.png", open: openerFor(red)},
		{name: "notes/", open: openerFor(nil)},
	}
	dir := t.TempDir()
	n, err := extractList(context.Background(), entries, dir, 1<<20, func(int, int) {})
	if err != nil {
		t.Fatalf("extractList: %v", err)
	}
	if n != 2 {
		t.Fatalf("wrote %d images, want 2", n)
	}
	got := extractedNames(t, dir)
	want := []string{"01.png", "sub/02.png"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("extracted %v, want %v", got, want)
	}
}

func TestExtractListNoImages(t *testing.T) {
	entries := []listEntry{{name: "readme.txt", open: openerFor([]byte("x"))}}
	if _, err := extractList(context.Background(), entries, t.TempDir(), 1<<20, func(int, int) {}); err != ErrNoImages {
		t.Fatalf("err = %v, want ErrNoImages", err)
	}
}

func TestExtractListBudget(t *testing.T) {
	big := tinyPNG(t, color.RGBA{0, 0, 255, 255})
	entries := []listEntry{{name: "01.png", open: openerFor(big)}}
	// A budget below the single page's size must trip the bomb guard.
	if _, err := extractList(context.Background(), entries, t.TempDir(), int64(len(big)-1), func(int, int) {}); err != ErrTooBig {
		t.Fatalf("err = %v, want ErrTooBig", err)
	}
}

func TestExtractListRejectsTraversal(t *testing.T) {
	png := tinyPNG(t, color.RGBA{1, 2, 3, 255})
	entries := []listEntry{
		{name: "../escape.png", open: openerFor(png)},
		{name: "ok.png", open: openerFor(png)},
	}
	dir := t.TempDir()
	n, err := extractList(context.Background(), entries, dir, 1<<20, func(int, int) {})
	if err != nil {
		t.Fatalf("extractList: %v", err)
	}
	if n != 1 {
		t.Fatalf("wrote %d, want 1 (traversing entry skipped)", n)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.png")); err == nil {
		t.Fatal("traversing entry escaped the destination dir")
	}
}

func TestExtractTAR(t *testing.T) {
	red := tinyPNG(t, color.RGBA{255, 0, 0, 255})
	blue := tinyPNG(t, color.RGBA{0, 0, 255, 255})
	src := filepath.Join(t.TempDir(), "book.cbt")
	f, err := os.Create(src)
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
	add("page1.png", red)
	add("info.txt", []byte("skip me"))
	add("page2.png", blue)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	dir := t.TempDir()
	n, err := Extract(context.Background(), src, dir, 1<<20, nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if n != 2 {
		t.Fatalf("wrote %d, want 2", n)
	}
	if got := extractedNames(t, dir); strings.Join(got, ",") != "page1.png,page2.png" {
		t.Fatalf("extracted %v", got)
	}
}

// TestExtract7z runs the real sevenzip decoder over a committed fixture: three
// PNG pages (01,02,10) and a readme, so it also proves the non-image entry is
// dropped. The fixture is tiny; regenerate with `7z a sample.cb7 *.png *.txt`.
func TestExtract7z(t *testing.T) {
	dir := t.TempDir()
	n, err := Extract(context.Background(), "testdata/sample.cb7", dir, 1<<20, nil)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if n != 3 {
		t.Fatalf("wrote %d, want 3", n)
	}
	if got := extractedNames(t, dir); strings.Join(got, ",") != "01.png,02.png,10.png" {
		t.Fatalf("extracted %v", got)
	}
}

func TestIsArchiveName(t *testing.T) {
	yes := []string{"a.cbr", "A.CBR", "b.rar", "c.cb7", "d.7z", "e.cbt", "f.tar"}
	no := []string{"a.cbz", "b.zip", "c.pdf", "d.png", "e"}
	for _, n := range yes {
		if !IsArchiveName(n) {
			t.Errorf("IsArchiveName(%q) = false, want true", n)
		}
	}
	for _, n := range no {
		if IsArchiveName(n) {
			t.Errorf("IsArchiveName(%q) = true, want false", n)
		}
	}
}

func TestSafeName(t *testing.T) {
	cases := map[string]bool{
		"a/b.png":      true,
		"01.png":       true,
		"../x.png":     false,
		"/abs.png":     false,
		`win\path.png`: false,
		"c:/x.png":     false,
		"":             false,
		"..":           false,
		"./a.png":      true,
	}
	for name, wantOK := range cases {
		if _, ok := safeName(name); ok != wantOK {
			t.Errorf("safeName(%q) ok = %v, want %v", name, ok, wantOK)
		}
	}
}

func TestExtractUnsupported(t *testing.T) {
	if _, err := Extract(context.Background(), "book.zip", t.TempDir(), 1<<20, nil); err == nil {
		t.Fatal("expected ErrUnsupported for a .zip")
	}
}
