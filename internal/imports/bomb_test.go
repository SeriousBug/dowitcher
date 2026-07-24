package imports

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SeriousBug/dowitcher/internal/api"
)

// bombPNG builds a real PNG whose IHDR declares w by h and whose IDAT is a few
// bytes of nothing. image.DecodeConfig reads the IHDR and nothing else, so this
// is exactly the file the ceiling exists to stop: a header that costs nothing to
// send and w*h*4 bytes to believe.
//
// It is assembled by hand because png.Encode will only ever write a header that
// matches the pixels it was actually given.
func bombPNG(w, h int) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})

	chunk := func(kind string, data []byte) {
		binary.Write(&buf, binary.BigEndian, uint32(len(data)))
		body := append([]byte(kind), data...)
		buf.Write(body)
		binary.Write(&buf, binary.BigEndian, crc32.ChecksumIEEE(body))
	}

	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:8], uint32(h))
	ihdr[8] = 8  // bit depth
	ihdr[9] = 2  // colour type: truecolour
	ihdr[10] = 0 // deflate
	ihdr[11] = 0 // adaptive filtering
	ihdr[12] = 0 // no interlace
	chunk("IHDR", ihdr)

	// A truncated deflate stream. It never has to be valid: nothing may get far
	// enough to inflate it.
	chunk("IDAT", []byte{0x78, 0x9c, 0x00})
	chunk("IEND", nil)
	return buf.Bytes()
}

// bombDims is deliberately the size the finding named. Nothing here allocates
// it — a test that OOMs is a test proving the fix is absent.
const bombW, bombH = 30000, 30000

func TestThumbnailRejectsDimensionBomb(t *testing.T) {
	if _, _, err := thumbnail(bombPNG(bombW, bombH)); !errors.Is(err, errImageTooLarge) {
		t.Fatalf("thumbnail(%dx%d) err = %v, want errImageTooLarge", bombW, bombH, err)
	}
	// The ceiling must not be a decode failure wearing a hat: a header this
	// small is perfectly parseable, and a real image just under the ceiling has
	// to still go through.
	if _, err := headerDims(bombPNG(bombW, bombH)); !errors.Is(err, errImageTooLarge) {
		t.Fatalf("headerDims err = %v, want errImageTooLarge", err)
	}
	var ok bytes.Buffer
	if err := png.Encode(&ok, synth(64, 96, 1, 0)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := thumbnail(ok.Bytes()); err != nil {
		t.Fatalf("a real page must still thumbnail: %v", err)
	}
}

// TestBombRejectedOnIngestInExactMode is the persistent half of the finding.
// Exact mode decodes nothing, so the bomb is never looked at during the import
// and gets copied verbatim into the CBZ — where the cover generator decodes it
// on every library grid load. The header check on ingest is what stops it being
// laundered in, so the assertion is on the CBZ's contents, not on an error.
func TestBombRejectedOnIngestInExactMode(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "1_real.png"), synth(64, 96, 1, 0))
	if err := os.WriteFile(filepath.Join(dir, "2_bomb.png"), bombPNG(bombW, bombH), 0o644); err != nil {
		t.Fatal(err)
	}

	res, out := run(t, dir, api.ImportOptions{Exact: true})
	if res.PageCount != 1 {
		t.Fatalf("PageCount = %d, want 1: the bomb must not become a page", res.PageCount)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], "2_bomb.png") {
		t.Fatalf("Skipped = %v, want one line naming 2_bomb.png", res.Skipped)
	}
	for _, f := range zipEntries(t, out) {
		if f.Name == "ComicInfo.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(got, bombPNG(bombW, bombH)) {
			t.Fatalf("entry %q is the bomb: exact mode packaged it into the library", f.Name)
		}
	}
}

// TestExactModeBombWithARealTwin: the bomb's bytes must not ride in on another
// file's digest either, and every copy of it is named rather than silently
// folded into the first one's skip line.
func TestExactModeBombWithARealTwin(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "1_real.png"), synth(64, 96, 1, 0))
	raw := bombPNG(bombW, bombH)
	for _, name := range []string{"2_bomb.png", "3_bomb_copy.png"} {
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, _ := run(t, dir, api.ImportOptions{Exact: true})
	if res.PageCount != 1 {
		t.Fatalf("PageCount = %d, want 1", res.PageCount)
	}
	if len(res.Skipped) != 2 {
		t.Fatalf("Skipped = %v, want both copies named", res.Skipped)
	}
}

func TestBombRejectedInDefaultMode(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "1_real.png"), synth(64, 96, 1, 0))
	if err := os.WriteFile(filepath.Join(dir, "2_bomb.png"), bombPNG(bombW, bombH), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _ := run(t, dir, api.ImportOptions{})
	if res.PageCount != 1 {
		t.Fatalf("PageCount = %d, want 1", res.PageCount)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], "2_bomb.png") {
		t.Fatalf("Skipped = %v, want one line naming 2_bomb.png", res.Skipped)
	}
}

// TestFileCountCap: the O(n^2) sweep has no way to refuse work once it has
// started, so the refusal has to happen before it does.
func TestFileCountCap(t *testing.T) {
	dir := t.TempDir()
	// collect() filters on the extension and never opens these, and the cap is
	// checked before anything does — so empty files are enough and the test does
	// not spend a minute encoding 5001 PNGs.
	for i := range maxFiles + 1 {
		if err := os.WriteFile(filepath.Join(dir, pad(i)+".png"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "o.cbz")
	_, err := Run(context.Background(), dir, out, api.ImportOptions{}, 0, nil)
	if !errors.Is(err, ErrTooManyFiles) {
		t.Fatalf("err = %v, want ErrTooManyFiles", err)
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Error("wrote a CBZ despite refusing the import")
	}

	// The message the frontend shows verbatim must say what to do about it and
	// must not carry a path off the server's disk.
	msg := failMessage(err)
	if !strings.Contains(msg, "separate books") {
		t.Errorf("failMessage = %q, want actionable text", msg)
	}
	if strings.Contains(msg, dir) || strings.Contains(msg, string(filepath.Separator)) {
		t.Errorf("failMessage = %q, leaks a server path", msg)
	}
}

func TestFileCountCapAllowsARealBook(t *testing.T) {
	dir := t.TempDir()
	for i := range 3 {
		writePNG(t, filepath.Join(dir, pad(i)+".png"), synth(32, 48, int64(i+1), 0))
	}
	if _, err := Run(context.Background(), dir, filepath.Join(t.TempDir(), "o.cbz"),
		api.ImportOptions{}, 0, nil); err != nil {
		t.Fatalf("a normal import must not trip the cap: %v", err)
	}
}

func pad(i int) string { return fmt.Sprintf("%05d", i) }
