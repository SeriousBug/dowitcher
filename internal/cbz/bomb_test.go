package cbz

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"os"
	"path/filepath"
	"testing"
)

// bombPNG builds a real PNG whose IHDR declares w by h and whose IDAT is three
// bytes of truncated deflate. image.DecodeConfig reads the IHDR and stops, which
// is the whole attack: the header costs 70 bytes to send and width*height*4
// bytes to believe.
//
// It is assembled by hand because png.Encode only ever writes a header matching
// the pixels it was handed.
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
	chunk("IDAT", []byte{0x78, 0x9c, 0x00})
	chunk("IEND", nil)
	return buf.Bytes()
}

// bombDims is the size the finding named: 30000x30000 is a ~3.6GB allocation.
// Nothing in this file allocates it — the ceiling is checked before image.Decode
// is reached, and a test that OOMs is a test proving the fix is gone.
const bombW, bombH = 30000, 30000

func TestThumbnailRejectsDimensionBomb(t *testing.T) {
	_, err := Thumbnail(bytes.NewReader(bombPNG(bombW, bombH)), 200)
	if !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("Thumbnail(%dx%d) err = %v, want ErrImageTooLarge", bombW, bombH, err)
	}
}

// TestThumbnailDecodesAfterTheHeaderCheck guards the plumbing the ceiling
// needed: DecodeConfig consumes the header out of a reader that cannot be
// rewound, so the decode replays it. If the replay were wrong every real cover
// would break, which no bomb test would catch.
func TestThumbnailDecodesAfterTheHeaderCheck(t *testing.T) {
	out, err := Thumbnail(bytes.NewReader(pngBytes(t, 400, 200)), 100)
	if err != nil {
		t.Fatalf("Thumbnail: %v", err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("thumbnail is not a decodable image: %v", err)
	}
	if format != "jpeg" || cfg.Width != 100 || cfg.Height != 50 {
		t.Fatalf("thumbnail = %s %dx%d, want jpeg 100x50", format, cfg.Width, cfg.Height)
	}
}

// TestCoverRejectsOversizedEntry: the entry's declared sizes come free with the
// central directory Open already read, so an entry that is not a page on its own
// numbers never gets streamed.
func TestCoverRejectsOversizedEntry(t *testing.T) {
	p := writeZipRaw(t, "Bomb 01.cbz", "1.png", bombPNG(bombW, bombH), maxEntryBytes+1, 4096)
	a, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.Cover(); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("Cover err = %v, want ErrImageTooLarge", err)
	}
}

func TestCoverRejectsImplausibleCompressionRatio(t *testing.T) {
	p := writeZipRaw(t, "Bomb 02.cbz", "1.png", bombPNG(bombW, bombH), 64<<20, 1024)
	a, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.Cover(); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("Cover err = %v, want ErrImageTooLarge", err)
	}
}

// TestCoverAcceptsARealPage pins the other side of the screen: a normal page is
// stored at a ratio near 1 and must not trip either bound.
func TestCoverAcceptsARealPage(t *testing.T) {
	p := writeZip(t, "Fine 01.cbz", []entry{{"1.png", pngBytes(t, 40, 60)}})
	a, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	rc, err := a.Cover()
	if err != nil {
		t.Fatalf("a real page must still be servable as a cover: %v", err)
	}
	rc.Close()
}

func TestCheckEntrySize(t *testing.T) {
	for _, tc := range []struct {
		name         string
		uncompressed uint64
		compressed   uint64
		ok           bool
	}{
		{"a stored jpeg", 2 << 20, 2 << 20, true},
		{"a deflated png", 3 << 20, 2 << 20, true},
		// Below the floor a wild ratio is just a small, flat image.
		{"a tiny flat image", 4096, 1, true},
		{"past the byte cap", maxEntryBytes + 1, 1 << 20, false},
		{"past the ratio cap", 64 << 20, 1024, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &zip.File{FileHeader: zip.FileHeader{
				Name:               "1.png",
				UncompressedSize64: tc.uncompressed,
				CompressedSize64:   tc.compressed,
			}}
			err := checkEntrySize(f)
			if tc.ok && err != nil {
				t.Fatalf("checkEntrySize = %v, want nil", err)
			}
			if !tc.ok && !errors.Is(err, ErrImageTooLarge) {
				t.Fatalf("checkEntrySize = %v, want ErrImageTooLarge", err)
			}
		})
	}
}

// writeZipRaw writes a zip whose central directory claims sizes the entry does
// not have. A real bomb lies in exactly this way, and archive/zip will not lie
// on request, so the header is written by hand.
func writeZipRaw(t *testing.T, name, entryName string, data []byte, uncompressed, compressed uint64) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name:               entryName,
		Method:             zip.Store,
		CRC32:              crc32.ChecksumIEEE(data),
		UncompressedSize64: uncompressed,
		CompressedSize64:   compressed,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}
