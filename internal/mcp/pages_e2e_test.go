package mcp

import (
	"archive/zip"
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// pageColour gives every page of a test CBZ its own flat colour, far enough
// apart to survive the downscale and the JPEG re-encode read_comic_pages puts
// them through. It is what lets a test say which page came back rather than only
// that some page did.
func pageColour(i int) color.RGBA {
	return color.RGBA{R: uint8(20 + i*18), G: 60, B: uint8(230 - i*15), A: 255}
}

func writeColourCBZ(t *testing.T, path string, pages int) {
	t.Helper()
	names := make([]string, pages)
	images := make([][]byte, pages)
	for i := range pages {
		im := image.NewRGBA(image.Rect(0, 0, 400, 600))
		c := pageColour(i)
		for y := range 600 {
			for x := range 400 {
				im.Set(x, y, c)
			}
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, im); err != nil {
			t.Fatalf("encode page: %v", err)
		}
		names[i] = strconv.Itoa(i+1) + ".png"
		images[i] = buf.Bytes()
	}
	writeCBZ(t, path, names, images)
}

// letteredPages are what the OCR test expects to read back. They are rendered
// rather than checked in as a fixture image, so the expected string and the
// pixels cannot drift apart.
var letteredPages = []string{"HELLO COMIC", "The quick brown fox", "PAGE THREE"}

func writeLetteredCBZ(t *testing.T, path string) {
	t.Helper()
	names := make([]string, len(letteredPages))
	images := make([][]byte, len(letteredPages))
	for i, s := range letteredPages {
		names[i] = strconv.Itoa(i+1) + ".png"
		images[i] = renderText(t, s)
	}
	writeCBZ(t, path, names, images)
}

// renderText draws s as black lettering on a white page, the way a scan reaches
// Tesseract. PNG, so the only lossy step is the JPEG read_comic_pages itself
// produces.
func renderText(t *testing.T, s string) []byte {
	t.Helper()
	parsed, err := opentype.Parse(goregular.TTF)
	if err != nil {
		t.Fatalf("parse font: %v", err)
	}
	face, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: 64, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		t.Fatalf("new face: %v", err)
	}
	im := image.NewGray(image.Rect(0, 0, 900, 200))
	for i := range im.Pix {
		im.Pix[i] = 0xff
	}
	d := font.Drawer{Dst: im, Src: image.NewUniform(color.Black), Face: face, Dot: fixed.P(40, 120)}
	d.DrawString(s)

	var buf bytes.Buffer
	if err := png.Encode(&buf, im); err != nil {
		t.Fatalf("encode page: %v", err)
	}
	return buf.Bytes()
}

func writeCBZ(t *testing.T, path string, names []string, images [][]byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create cbz: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for i, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip entry: %v", err)
		}
		if _, err := w.Write(images[i]); err != nil {
			t.Fatalf("write entry: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
}

// callRaw runs a tool and hands back the whole result. read_comic_pages answers
// in content blocks rather than structured output, so the decoding `call` does
// has nothing to decode.
func callRaw(t *testing.T, s *sdk.ClientSession, name string, args any) *sdk.CallToolResult {
	t.Helper()
	res, err := s.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s: tool error: %v", name, contentText(res))
	}
	return res
}

// centreColour decodes a returned page and samples its middle, which for a flat
// test page is the whole page.
func centreColour(t *testing.T, jpg []byte) color.RGBA {
	t.Helper()
	im, err := jpeg.Decode(bytes.NewReader(jpg))
	if err != nil {
		t.Fatalf("decode returned page: %v", err)
	}
	b := im.Bounds()
	r, g, bl, _ := im.At(b.Dx()/2, b.Dy()/2).RGBA()
	return color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(bl >> 8), A: 255}
}

func closeEnough(a, b color.RGBA) bool {
	d := func(x, y uint8) int {
		if x > y {
			return int(x - y)
		}
		return int(y - x)
	}
	return d(a.R, b.R) < 12 && d(a.G, b.G) < 12 && d(a.B, b.B) < 12
}

// TestMCPReadPagesImage: the pages asked for are the pages that come back, in
// order, each one labelled with its 1-based number.
func TestMCPReadPagesImage(t *testing.T) {
	e := setup(t)
	up := comicID(t, e.store, e.aliceID, "AliceOnly")
	sess := connect(t, e.url, token(t, e.store, e.aliceID))

	res := callRaw(t, sess, "read_comic_pages", ReadComicPagesInput{
		ComicID: up, Format: "image", Pages: []int{4, 2},
	})
	if len(res.Content) != 4 {
		t.Fatalf("want a label and an image for each of two pages, got %d blocks", len(res.Content))
	}
	// The selection was given out of order; the answer is in reading order.
	for n, wantPage := range []int{2, 4} {
		label, ok := res.Content[n*2].(*sdk.TextContent)
		if !ok || !strings.Contains(label.Text, "Page "+strconv.Itoa(wantPage)) {
			t.Fatalf("block %d should label page %d, got %#v", n*2, wantPage, res.Content[n*2])
		}
		img, ok := res.Content[n*2+1].(*sdk.ImageContent)
		if !ok {
			t.Fatalf("block %d should be an image, got %#v", n*2+1, res.Content[n*2+1])
		}
		if img.MIMEType != "image/jpeg" {
			t.Errorf("mime = %q, want image/jpeg", img.MIMEType)
		}
		if got, want := centreColour(t, img.Data), pageColour(wantPage-1); !closeEnough(got, want) {
			t.Errorf("page %d colour = %v, want %v — the wrong page came back", wantPage, got, want)
		}
	}
}

// TestMCPReadPagesText proves the OCR path end to end: the words the test drew
// onto page 2 are the words the tool reads back off it, and only page 2's.
func TestMCPReadPagesText(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and runs the Tesseract WASM module")
	}
	e := setup(t)
	lettered := comicID(t, e.store, e.aliceID, "Lettered")
	sess := connect(t, e.url, token(t, e.store, e.aliceID))

	res := callRaw(t, sess, "read_comic_pages", ReadComicPagesInput{
		ComicID: lettered, Format: "text", Pages: []int{2},
	})
	if len(res.Content) != 1 {
		t.Fatalf("want one block for one page, got %d", len(res.Content))
	}
	block, ok := res.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("want a text block, got %#v", res.Content[0])
	}
	if !strings.Contains(block.Text, letteredPages[1]) {
		t.Errorf("page 2 text = %q, want it to contain %q", block.Text, letteredPages[1])
	}
	if strings.Contains(block.Text, letteredPages[0]) {
		t.Errorf("page 2 text = %q, but that is page 1's lettering", block.Text)
	}
	if !strings.HasPrefix(block.Text, "Page 2:") {
		t.Errorf("text should be labelled with its page number, got %q", block.Text)
	}
}

// TestMCPReadPagesRefusesInvisibleComic: reading is gated by the same visibility
// as every other tool, so another user's upload is not readable page by page.
func TestMCPReadPagesRefusesInvisibleComic(t *testing.T) {
	e := setup(t)
	aliceUpload := comicID(t, e.store, e.aliceID, "AliceOnly")

	bob := connect(t, e.url, token(t, e.store, e.bobID))
	msg := callErr(t, bob, "read_comic_pages", ReadComicPagesInput{
		ComicID: aliceUpload, Format: "image", Pages: []int{1},
	})
	if !strings.Contains(msg, "not found") {
		t.Errorf("want a not-found refusal, got %q", msg)
	}
}

// TestMCPReadPagesRejectsBadSelection: a page past the end, a half-given range
// and no selection at all each fail with a message that says what to do instead.
func TestMCPReadPagesRejectsBadSelection(t *testing.T) {
	e := setup(t)
	up := comicID(t, e.store, e.aliceID, "AliceOnly") // five pages
	sess := connect(t, e.url, token(t, e.store, e.aliceID))

	msg := callErr(t, sess, "read_comic_pages", ReadComicPagesInput{
		ComicID: up, Format: "image", Pages: []int{99},
	})
	if !strings.Contains(msg, "pages 1 to 5") {
		t.Errorf("an out-of-range page should name the real range, got %q", msg)
	}

	msg = callErr(t, sess, "read_comic_pages", ReadComicPagesInput{
		ComicID: up, Format: "image", FromPage: 2,
	})
	if !strings.Contains(msg, "toPage") {
		t.Errorf("a half-given range should say so, got %q", msg)
	}

	msg = callErr(t, sess, "read_comic_pages", ReadComicPagesInput{ComicID: up, Format: "image"})
	if !strings.Contains(msg, "at least one page") {
		t.Errorf("an empty selection should ask for one, got %q", msg)
	}
}

// TestMCPReadPagesEnforcesPageCap: both formats refuse an oversized selection
// rather than truncating it, and each names its own limit.
func TestMCPReadPagesEnforcesPageCap(t *testing.T) {
	e := setup(t)
	lib := comicID(t, e.store, e.aliceID, "Public") // twelve pages
	sess := connect(t, e.url, token(t, e.store, e.aliceID))

	msg := callErr(t, sess, "read_comic_pages", ReadComicPagesInput{
		ComicID: lib, Format: "image", FromPage: 1, ToPage: maxImagePages + 1,
	})
	if !strings.Contains(msg, strconv.Itoa(maxImagePages)) {
		t.Errorf("the image cap should name its limit, got %q", msg)
	}

	msg = callErr(t, sess, "read_comic_pages", ReadComicPagesInput{
		ComicID: lib, Format: "text", FromPage: 1, ToPage: maxTextPages + 1,
	})
	if !strings.Contains(msg, strconv.Itoa(maxTextPages)) {
		t.Errorf("the text cap should name its limit, got %q", msg)
	}

	// Exactly the limit is allowed: the cap is a maximum, not an off-by-one.
	callRaw(t, sess, "read_comic_pages", ReadComicPagesInput{
		ComicID: lib, Format: "image", FromPage: 1, ToPage: maxImagePages,
	})
}

// TestMCPReadPagesReadsLibraryComic: reads reach the library root, not only the
// uploads dir. Writes and deletes still do not, which TestMCPDeleteComic covers.
func TestMCPReadPagesReadsLibraryComic(t *testing.T) {
	e := setup(t)
	lib := comicID(t, e.store, e.aliceID, "Public")
	sess := connect(t, e.url, token(t, e.store, e.aliceID))

	res := callRaw(t, sess, "read_comic_pages", ReadComicPagesInput{
		ComicID: lib, Format: "image", Pages: []int{3},
	})
	img, ok := res.Content[1].(*sdk.ImageContent)
	if !ok {
		t.Fatalf("want an image block, got %#v", res.Content[1])
	}
	if got, want := centreColour(t, img.Data), pageColour(2); !closeEnough(got, want) {
		t.Errorf("library page 3 colour = %v, want %v", got, want)
	}
}
