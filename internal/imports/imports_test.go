package imports

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	xdraw "golang.org/x/image/draw"

	"github.com/SeriousBug/dowitcher/internal/api"
)

// thumbnailFile is the file-path form of thumbnail, for tests that need to
// assert on MAE directly.
func thumbnailFile(path string) (image.Point, []byte, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		return image.Point{}, nil, err
	}
	return thumbnail(buf)
}

// synth draws a deterministic image whose content is a function of seed, so two
// different seeds are visually distinct well past the MAE threshold.
func synth(w, h int, seed int64, brightness int) *image.RGBA {
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	rnd := rand.New(rand.NewSource(seed))
	// Blocks rather than per-pixel noise: a 64x64 thumbnail averages per-pixel
	// noise away to nothing, which would make "distinct" images compare equal.
	const blocks = 8
	shade := make([]int, blocks*blocks)
	for i := range shade {
		shade[i] = rnd.Intn(256)
	}
	for y := range h {
		for x := range w {
			bx := x * blocks / w
			by := y * blocks / h
			v := shade[by*blocks+bx] + brightness
			v = min(max(v, 0), 255)
			im.Set(x, y, color.RGBA{uint8(v), uint8(v), uint8(v), 255})
		}
	}
	return im
}

func writePNG(t *testing.T, path string, im image.Image) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, im); err != nil {
		t.Fatal(err)
	}
}

// scaled downsamples with the same Lanczos path the pipeline uses, standing in
// for a gallery's low-res preview of a hi-res page.
func scaled(src image.Image, w, h int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	lanczos3.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Src, nil)
	return dst
}

// stretched draws a smooth pattern that depends only on normalized coordinates,
// so the same seed at any aspect ratio is the same artwork squashed differently.
// Rendering the pattern directly at each size avoids the resample round-trip
// that a stretch-then-squash would incur, and the low frequencies survive the
// 64x64 downsample, so two aspect ratios of one seed land within the MAE
// threshold of each other.
func stretched(w, h int, phase float64) *image.RGBA {
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			u := (float64(x) + 0.5) / float64(w)
			v := (float64(y) + 0.5) / float64(h)
			val := 128 +
				50*math.Sin(2*math.Pi*(1.5*u+phase)) +
				50*math.Sin(2*math.Pi*(2.5*v+phase))
			c := uint8(min(max(int(val), 0), 255))
			im.Set(x, y, color.RGBA{c, c, c, 255})
		}
	}
	return im
}

func writeJPEG(t *testing.T, path string, im image.Image) {
	t.Helper()
	writeJPEGQ(t, path, im, 95)
}

func writeJPEGQ(t *testing.T, path string, im image.Image, quality int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, im, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, dir string, opts api.ImportOptions) (*Result, string) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "out.cbz")
	res, err := Run(context.Background(), dir, out, opts, 0, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res, out
}

func zipEntries(t *testing.T, path string) []*zip.File {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r.File
}

func TestNaturalSortUsesRelativePath(t *testing.T) {
	// Sorting on the basename would interleave the two chapters' pages; sorting
	// on the relative path keeps each chapter contiguous, and 2 must precede 10.
	dir := t.TempDir()
	want := []string{
		"ch1/2.png", "ch1/10.png",
		"ch2/2.png", "ch2/10.png",
	}
	for _, p := range []string{"ch2/10.png", "ch1/2.png", "ch2/2.png", "ch1/10.png"} {
		writePNG(t, filepath.Join(dir, p), synth(20, 30, 1, 0))
	}
	files, err := collect(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.rel)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("collect order = %v, want %v", got, want)
	}
}

func TestExactDupesSettledByHash(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "a.png"), synth(40, 60, 1, 0))
	writePNG(t, filepath.Join(dir, "b.png"), synth(40, 60, 2, 0))
	// Byte-for-byte copy of a.png.
	raw, err := os.ReadFile(filepath.Join(dir, "a.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "c.png"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	res, _ := run(t, dir, api.ImportOptions{})
	if res.PageCount != 2 {
		t.Errorf("PageCount = %d, want 2", res.PageCount)
	}
	if res.ExactDupes != 1 {
		t.Errorf("ExactDupes = %d, want 1", res.ExactDupes)
	}
	var found bool
	for _, g := range res.Groups {
		if g.Reason == "exact" && g.Kept == "a.png" && len(g.Dropped) == 1 && g.Dropped[0] == "c.png" {
			found = true
		}
	}
	if !found {
		t.Errorf("no exact dupe group for a.png/c.png: %+v", res.Groups)
	}
}

// TestMAEThreshold pins the threshold behaviour: a brightness delta under the
// threshold groups, one over it does not.
func TestMAEThreshold(t *testing.T) {
	base := synth(64, 96, 7, 0)

	for _, tc := range []struct {
		name       string
		brightness int
		wantSame   bool
	}{
		{"under threshold", 2, true},
		{"over threshold", 40, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writePNG(t, filepath.Join(dir, "a.png"), base)
			writePNG(t, filepath.Join(dir, "b.png"), synth(64, 96, 7, tc.brightness))

			res, _ := run(t, dir, api.ImportOptions{})
			wantPages := 2
			if tc.wantSame {
				wantPages = 1
			}
			if res.PageCount != wantPages {
				t.Errorf("PageCount = %d, want %d", res.PageCount, wantPages)
			}
			if tc.wantSame && res.NearDupes != 1 {
				t.Errorf("NearDupes = %d, want 1", res.NearDupes)
			}
		})
	}
}

// TestAspectGate: the thumbnail is squashed to a square, so two images that
// squash to the SAME 64x64 buffer but have different aspect ratios are exactly
// the case the gate exists to catch.
//
// Both images render the same normalized pattern, one at 1:2 and one at 2:1, so
// the squashed thumbnails match on pixels and only the aspect ratios tell them
// apart. The premise guard below fails loudly if that ever stops holding, since
// a test that passes because the pixels diverged would prove nothing.
func TestAspectGate(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "tall.png"), stretched(64, 128, 0.3))
	writePNG(t, filepath.Join(dir, "wide.png"), stretched(128, 64, 0.3))

	// Guard the premise: without the gate these would group, so the pixels must
	// actually be within the threshold of each other.
	_, tt, err := thumbnailFile(filepath.Join(dir, "tall.png"))
	if err != nil {
		t.Fatal(err)
	}
	_, tw, err := thumbnailFile(filepath.Join(dir, "wide.png"))
	if err != nil {
		t.Fatal(err)
	}
	if got := mae(tt, tw); got > defaultThreshold {
		t.Fatalf("premise broken: MAE(tall,wide) = %.3f, want <= %.1f so that only "+
			"the aspect gate can separate them", got, defaultThreshold)
	}

	res, _ := run(t, dir, api.ImportOptions{})
	if res.PageCount != 2 {
		t.Errorf("PageCount = %d, want 2 (aspect gate must keep these apart)", res.PageCount)
	}
	if res.NearDupes != 0 {
		t.Errorf("NearDupes = %d, want 0", res.NearDupes)
	}
}

// TestTransitiveClustering: A~B and B~C group all three even when A vs C is
// itself over the threshold.
func TestTransitiveClustering(t *testing.T) {
	dir := t.TempDir()
	// A 2-step brightness ramp: each adjacent pair is under 3.0, the ends are
	// ~4 apart and would not group on their own.
	writePNG(t, filepath.Join(dir, "a.png"), synth(64, 96, 3, 0))
	writePNG(t, filepath.Join(dir, "b.png"), synth(64, 96, 3, 2))
	writePNG(t, filepath.Join(dir, "c.png"), synth(64, 96, 3, 4))

	// Guard the premise: the chain only proves transitivity if A vs C is over
	// the threshold to begin with.
	_, ta, err := thumbnailFile(filepath.Join(dir, "a.png"))
	if err != nil {
		t.Fatal(err)
	}
	_, tc, err := thumbnailFile(filepath.Join(dir, "c.png"))
	if err != nil {
		t.Fatal(err)
	}
	if got := mae(ta, tc); got <= defaultThreshold {
		t.Fatalf("premise broken: MAE(a,c) = %.3f, want > %.1f", got, defaultThreshold)
	}

	res, _ := run(t, dir, api.ImportOptions{})
	if res.PageCount != 1 {
		t.Errorf("PageCount = %d, want 1 (transitive clustering)", res.PageCount)
	}
	if res.NearDupes != 2 {
		t.Errorf("NearDupes = %d, want 2 merges", res.NearDupes)
	}
}

// TestKeeperIsHighestResolution: a preview and its hi-res original must collapse
// to the hi-res one.
//
// The names put the preview FIRST in collected order. That is what makes the
// test bite: if keeper selection degraded to "first member", it would pick the
// preview, so only the resolution rule can produce the expected result.
func TestKeeperIsHighestResolution(t *testing.T) {
	dir := t.TempDir()
	hi := synth(256, 384, 11, 0)
	writePNG(t, filepath.Join(dir, "1_preview.png"), scaled(hi, 64, 96))
	writePNG(t, filepath.Join(dir, "2_full.png"), hi)

	res, _ := run(t, dir, api.ImportOptions{})
	if res.PageCount != 1 {
		t.Fatalf("PageCount = %d, want 1", res.PageCount)
	}
	if res.Groups[0].Kept != "2_full.png" {
		t.Errorf("Kept = %q, want 2_full.png", res.Groups[0].Kept)
	}
}

// TestKeeperTieBreaksOnFileSize: same pixel count, different bytes on disk. The
// larger file wins, standing in for the less-compressed copy of a page.
func TestKeeperTieBreaksOnFileSize(t *testing.T) {
	dir := t.TempDir()
	im := synth(64, 96, 13, 0)
	// Same pixels, same dimensions, different encoders: the JPEGs differ in
	// bytes but decode to near-identical images, so they group and the tie
	// falls to file size.
	writeJPEGQ(t, filepath.Join(dir, "1_small.jpg"), im, 40)
	writeJPEGQ(t, filepath.Join(dir, "2_large.jpg"), im, 100)

	res, _ := run(t, dir, api.ImportOptions{})
	if res.PageCount != 1 {
		t.Fatalf("PageCount = %d, want 1", res.PageCount)
	}
	a, err := os.Stat(filepath.Join(dir, "1_small.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(filepath.Join(dir, "2_large.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Size() >= b.Size() {
		t.Fatalf("premise broken: 1_small.jpg (%d) is not smaller than 2_large.jpg (%d)", a.Size(), b.Size())
	}
	if res.Groups[0].Kept != "2_large.jpg" {
		t.Errorf("Kept = %q, want 2_large.jpg (tie on pixels breaks to file size)", res.Groups[0].Kept)
	}
}

// TestGroupOrderFollowsEarliestMember is the regression test for the ordering
// rule. A post bundling two pages uploads previews first, then the hi-res
// copies: p1, p2, full1, full2. Ordering groups by the KEPT file would yield
// full1, full2 — which happens to be right here — so the case that actually
// catches the bug is the interleaved one below, where the kept files appear in
// the opposite order to the previews.
func TestGroupOrderFollowsEarliestMember(t *testing.T) {
	dir := t.TempDir()
	page1 := synth(256, 384, 21, 0)
	page2 := synth(256, 384, 22, 0)

	// Collected order: 1_preview_page1, 2_preview_page2, 3_full_page2,
	// 4_full_page1. Group for page1 = {1, 4}, group for page2 = {2, 3}.
	// By earliest member: page1 (1) then page2 (2)  -> correct.
	// By kept file:       page2 (3) then page1 (4)  -> reversed.
	writePNG(t, filepath.Join(dir, "1.png"), scaled(page1, 64, 96))
	writePNG(t, filepath.Join(dir, "2.png"), scaled(page2, 64, 96))
	writePNG(t, filepath.Join(dir, "3.png"), page2)
	writePNG(t, filepath.Join(dir, "4.png"), page1)

	res, out := run(t, dir, api.ImportOptions{})
	if res.PageCount != 2 {
		t.Fatalf("PageCount = %d, want 2", res.PageCount)
	}
	want := []string{"4.png", "3.png"} // page1's keeper first, then page2's
	var got []string
	for _, g := range res.Groups {
		got = append(got, g.Kept)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("group order = %v, want %v (groups must sort by earliest member, not by kept file)", got, want)
	}

	// The zip must carry that same order.
	entries := zipEntries(t, out)
	if entries[0].Name != "01.png" || entries[1].Name != "02.png" {
		t.Errorf("entry names = %q,%q", entries[0].Name, entries[1].Name)
	}
	first := decodeEntry(t, entries[0])
	if !sameish(t, first, page1) {
		t.Error("01.png is not page 1: groups were ordered by the kept file")
	}
}

func decodeEntry(t *testing.T, f *zip.File) image.Image {
	t.Helper()
	rc, err := f.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	im, _, err := image.Decode(rc)
	if err != nil {
		t.Fatal(err)
	}
	return im
}

// sameish compares two images through the pipeline's own thumbnail+MAE, so it
// tolerates re-encoding but not a different page.
func sameish(t *testing.T, a, b image.Image) bool {
	t.Helper()
	ta := thumbOf(t, a)
	tb := thumbOf(t, b)
	return mae(ta, tb) <= defaultThreshold
}

func thumbOf(t *testing.T, im image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, im); err != nil {
		t.Fatal(err)
	}
	_, th, err := thumbnail(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return th
}

func TestZipEntryNamingAndMethod(t *testing.T) {
	dir := t.TempDir()
	// A jpeg and a png: the jpeg must be stored, the png deflated.
	writePNG(t, filepath.Join(dir, "a.png"), synth(40, 60, 31, 0))
	writeJPEG(t, filepath.Join(dir, "b.jpg"), synth(40, 60, 32, 0))

	_, out := run(t, dir, api.ImportOptions{})
	entries := zipEntries(t, out)

	byName := map[string]*zip.File{}
	for _, f := range entries {
		byName[f.Name] = f
	}
	if _, ok := byName["01.png"]; !ok {
		t.Errorf("missing 01.png, got %v", names(entries))
	}
	if _, ok := byName["02.jpg"]; !ok {
		t.Errorf("missing 02.jpg, got %v", names(entries))
	}
	if m := byName["01.png"].Method; m != zip.Deflate {
		t.Errorf("01.png method = %d, want Deflate(%d)", m, zip.Deflate)
	}
	// Already-compressed formats must be stored, not deflated again.
	if m := byName["02.jpg"].Method; m != zip.Store {
		t.Errorf("02.jpg method = %d, want Store(%d)", m, zip.Store)
	}
	// Flat: no directories.
	for _, f := range entries {
		if strings.Contains(f.Name, "/") {
			t.Errorf("entry %q is not flat", f.Name)
		}
	}
}

func names(fs []*zip.File) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Name)
	}
	return out
}

// TestPadWidth pins the zero-padding rule: two digits minimum, widening past 99.
func TestPadWidth(t *testing.T) {
	for _, tc := range []struct{ pages, want int }{
		{1, 2}, {9, 2}, {99, 2}, {100, 3}, {1000, 4},
	} {
		if got := padWidth(tc.pages); got != tc.want {
			t.Errorf("padWidth(%d) = %d, want %d", tc.pages, got, tc.want)
		}
	}
}

func TestComicInfoWritten(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "a.png"), synth(40, 60, 41, 0))
	writePNG(t, filepath.Join(dir, "b.png"), synth(40, 60, 42, 0))

	_, out := run(t, dir, api.ImportOptions{Name: "My Comic"})
	var ci *zip.File
	for _, f := range zipEntries(t, out) {
		if f.Name == "ComicInfo.xml" {
			ci = f
		}
	}
	if ci == nil {
		t.Fatalf("no ComicInfo.xml, got %v", names(zipEntries(t, out)))
	}
	rc, err := ci.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	var got struct {
		Title     string `xml:"Title"`
		PageCount int    `xml:"PageCount"`
		Notes     string `xml:"Notes"`
	}
	if err := xml.NewDecoder(rc).Decode(&got); err != nil {
		t.Fatalf("ComicInfo.xml is not valid XML: %v", err)
	}
	if got.Title != "My Comic" {
		t.Errorf("Title = %q, want %q", got.Title, "My Comic")
	}
	if got.PageCount != 2 {
		t.Errorf("PageCount = %d, want 2", got.PageCount)
	}
	if !strings.Contains(got.Notes, "Dowitcher") {
		t.Errorf("Notes = %q, want it to mention Dowitcher", got.Notes)
	}
}

// TestForceCBZSuffix covers the with_suffix bug that is deliberately not ported.
func TestForceCBZSuffix(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"comic", "comic.cbz"},
		{"comic.cbz", "comic.cbz"},
		{"comic.CBZ", "comic.CBZ"},
		// Python's with_suffix(".cbz") truncates at the last dot and yields
		// "On Full Display (avif-q70.cbz"; appending must keep the name whole.
		{"On Full Display (avif-q70)", "On Full Display (avif-q70).cbz"},
		{"a.b.c", "a.b.c.cbz"},
	} {
		if got := forceCBZSuffix(tc.in); got != tc.want {
			t.Errorf("forceCBZSuffix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEmptyFolderIsAnError(t *testing.T) {
	dir := t.TempDir()
	_, err := Run(context.Background(), dir, filepath.Join(t.TempDir(), "o.cbz"), api.ImportOptions{}, 0, nil)
	if !errors.Is(err, ErrNoImages) {
		t.Errorf("err = %v, want ErrNoImages", err)
	}
	// A folder holding only non-images is equally empty.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Run(context.Background(), dir, filepath.Join(t.TempDir(), "o.cbz"), api.ImportOptions{}, 0, nil)
	if !errors.Is(err, ErrNoImages) {
		t.Errorf("err = %v, want ErrNoImages", err)
	}
}

// TestUndecodableFileIsSkipped: a file with an image extension but garbage
// bytes must drop out cleanly rather than abort the import.
func TestUndecodableFileIsSkipped(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "a.png"), synth(40, 60, 51, 0))
	if err := os.WriteFile(filepath.Join(dir, "b.png"), []byte("not a png"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, out := run(t, dir, api.ImportOptions{})
	if res.PageCount != 1 {
		t.Errorf("PageCount = %d, want 1", res.PageCount)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], "b.png") {
		t.Errorf("Skipped = %v, want one line naming b.png", res.Skipped)
	}
	if n := len(zipEntries(t, out)); n != 2 { // 1 page + ComicInfo.xml
		t.Errorf("zip has %d entries, want 2", n)
	}
}

// TestAllFilesUndecodable must error rather than write an empty CBZ. package.py
// would divide by out_bytes here and raise ZeroDivisionError.
func TestAllFilesUndecodable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.png"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "o.cbz")
	_, err := Run(context.Background(), dir, out, api.ImportOptions{}, 0, nil)
	if !errors.Is(err, ErrNoImages) {
		t.Errorf("err = %v, want ErrNoImages", err)
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Error("wrote a CBZ despite having no pages")
	}
}

func TestExactModeSkipsPixelGrouping(t *testing.T) {
	dir := t.TempDir()
	hi := synth(256, 384, 61, 0)
	writePNG(t, filepath.Join(dir, "1.png"), hi)
	writePNG(t, filepath.Join(dir, "2.png"), scaled(hi, 64, 96)) // near dupe
	raw, err := os.ReadFile(filepath.Join(dir, "1.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "3.png"), raw, 0o644); err != nil { // exact dupe
		t.Fatal(err)
	}

	res, _ := run(t, dir, api.ImportOptions{Exact: true})
	// The exact dupe goes; the near dupe stays, because exact mode never
	// compares pixels.
	if res.PageCount != 2 {
		t.Errorf("PageCount = %d, want 2", res.PageCount)
	}
	if res.ExactDupes != 1 {
		t.Errorf("ExactDupes = %d, want 1", res.ExactDupes)
	}
	if res.NearDupes != 0 {
		t.Errorf("NearDupes = %d, want 0", res.NearDupes)
	}
}

func TestEncodeFormats(t *testing.T) {
	// AVIF goes through a WASM-hosted libaom and is slow; keep the fixture tiny.
	for _, format := range []string{"jpeg", "webp", "avif"} {
		t.Run(format, func(t *testing.T) {
			dir := t.TempDir()
			writePNG(t, filepath.Join(dir, "a.png"), synth(32, 48, 71, 0))
			writePNG(t, filepath.Join(dir, "b.png"), synth(32, 48, 72, 0))

			_, out := run(t, dir, api.ImportOptions{Encode: format, Quality: 70})
			entries := zipEntries(t, out)
			want := "01" + encodeExt[format]
			if entries[0].Name != want {
				t.Errorf("entry = %q, want %q", entries[0].Name, want)
			}
			// Every encode target is already compressed, so all must be stored.
			if entries[0].Method != zip.Store {
				t.Errorf("%s method = %d, want Store", want, entries[0].Method)
			}
			// The bytes must really be in that format.
			rc, err := entries[0].Open()
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Close()
			_, gotFmt, err := image.DecodeConfig(rc)
			if err != nil {
				t.Fatalf("decode %s: %v", want, err)
			}
			if gotFmt != format {
				t.Errorf("format = %q, want %q", gotFmt, format)
			}
		})
	}
}

func TestBadEncodeOptionsRejected(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "a.png"), synth(32, 48, 81, 0))

	if _, err := Run(context.Background(), dir, filepath.Join(t.TempDir(), "o.cbz"),
		api.ImportOptions{Encode: "jxl"}, 0, nil); !errors.Is(err, ErrBadEncode) {
		t.Errorf("err = %v, want ErrBadEncode", err)
	}
	if _, err := Run(context.Background(), dir, filepath.Join(t.TempDir(), "o.cbz"),
		api.ImportOptions{Encode: "jpeg", Quality: 300}, 0, nil); !errors.Is(err, ErrBadQuality) {
		t.Errorf("err = %v, want ErrBadQuality", err)
	}
}

func TestContextCancellation(t *testing.T) {
	dir := t.TempDir()
	for i := range 40 {
		writePNG(t, filepath.Join(dir, string(rune('a'+i%26))+string(rune('a'+i/26))+".png"),
			synth(64, 96, int64(100+i), 0))
	}
	ctx, cancel := context.WithCancel(context.Background())
	out := filepath.Join(t.TempDir(), "o.cbz")
	// Cancel as soon as the pipeline reports any progress at all.
	_, err := Run(ctx, dir, out, api.ImportOptions{}, 0, func(api.ImportStage, int, int) {
		cancel()
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Error("wrote a CBZ despite cancellation")
	}
}

func TestProgressReachesDone(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "a.png"), synth(40, 60, 91, 0))
	writePNG(t, filepath.Join(dir, "b.png"), synth(40, 60, 92, 0))

	seen := map[api.ImportStage]bool{}
	out := filepath.Join(t.TempDir(), "o.cbz")
	if _, err := Run(context.Background(), dir, out, api.ImportOptions{}, 0,
		func(s api.ImportStage, done, total int) {
			seen[s] = true
			if done > total {
				t.Errorf("stage %s: done %d > total %d", s, done, total)
			}
		}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []api.ImportStage{api.StageReading, api.StageGrouping, api.StagePackaging, api.StageDone} {
		if !seen[s] {
			t.Errorf("stage %s never reported", s)
		}
	}
}

func TestNaturalKeyOrdering(t *testing.T) {
	// Pairs that must compare less-than, covering the numeric run, the
	// case-insensitive fallback, and the prefix rule.
	for _, tc := range []struct{ a, b string }{
		{"2.png", "10.png"},
		{"page2.png", "page10.png"},
		{"a/1.png", "a/2.png"},
		{"a/9.png", "b/1.png"},
		{"APPLE.png", "banana.png"}, // lowercased before compare
		{"1.png", "1a.png"},         // prefix sorts first
	} {
		if compareNatural(naturalKey(tc.a), naturalKey(tc.b)) >= 0 {
			t.Errorf("compareNatural(%q, %q) >= 0, want < 0", tc.a, tc.b)
		}
		if compareNatural(naturalKey(tc.b), naturalKey(tc.a)) <= 0 {
			t.Errorf("compareNatural(%q, %q) <= 0, want > 0", tc.b, tc.a)
		}
	}
}
