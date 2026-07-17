package cbz

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"path"

	// Registering the decoders is what makes image.DecodeConfig work on every
	// format we accept as a page. AVIF comes from gen2brain/avif, which decodes
	// through a wazero-hosted WASM build of libavif: pure Go, so CGO_ENABLED=0
	// and the distroless/static image both survive it. Header reads cost ~0.1ms;
	// a full decode is ~1s per page under WASM, which is why dimensions come
	// from DecodeConfig and full decodes happen only for thumbnails.
	_ "github.com/gen2brain/avif"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/webp"

	"golang.org/x/image/draw"
)

func pathBase(p string) string { return path.Base(p) }

// ErrImageTooLarge marks an entry the decoders must not be pointed at. It is a
// hostile or corrupt file, not a page we failed to render, and callers that
// distinguish the two (the cover cache regenerates on a miss forever) need to
// be able to tell.
var ErrImageTooLarge = errors.New("cbz: image is too large to decode")

// maxPixels caps what Thumbnail will decode. An image header declares its own
// dimensions and image.Decode believes it, allocating width*height*4 bytes
// before anything gets to object: a 100KB PNG whose IHDR says 30000x30000 is a
// 3.6GB allocation. That is fatal rather than slow, and it repeats — covers are
// generated on demand and the library grid asks for every one of them, so a
// single such page in the library kills the server on every load.
//
// 50MP is ~8600x5800, past any real scan: a double-page spread at 600dpi is
// ~44MP. Anything beyond it is not a comic page.
const maxPixels = 50_000_000

const (
	// maxEntryBytes and maxEntryRatio judge a cover entry on what the zip's
	// central directory already claims about it, before a byte is decompressed.
	// maxPixels bounds the decoded frame, but an entry can still be a bomb the
	// decoder would stream in full — and both numbers are free, they are read
	// from the directory Open already parsed.
	//
	// A page file is a few MB; 256MB is far past the largest plausible one. The
	// ratio only applies above ratioFloor because a small image can legitimately
	// compress to almost nothing, whereas a 100MB entry deflating from 50KB is
	// not a scan of anything.
	maxEntryBytes = 256 << 20
	maxEntryRatio = 1000
	ratioFloor    = 1 << 20
)

// checkEntrySize screens a page entry by its declared sizes.
func checkEntrySize(f *zip.File) error {
	if f.UncompressedSize64 > maxEntryBytes {
		return fmt.Errorf("%w: entry %s declares %d bytes", ErrImageTooLarge, f.Name, f.UncompressedSize64)
	}
	if f.CompressedSize64 > 0 && f.UncompressedSize64 >= ratioFloor &&
		f.UncompressedSize64/f.CompressedSize64 > maxEntryRatio {
		return fmt.Errorf("%w: entry %s expands %dx", ErrImageTooLarge, f.Name,
			f.UncompressedSize64/f.CompressedSize64)
	}
	return nil
}

// checkPixels is the gate every full decode goes through.
func checkPixels(w, h int) error {
	if w <= 0 || h <= 0 {
		return fmt.Errorf("cbz: empty image")
	}
	if int64(w)*int64(h) > maxPixels {
		return fmt.Errorf("%w: %dx%d", ErrImageTooLarge, w, h)
	}
	return nil
}

// entryDimensions reads an entry's image header. It reads only what
// DecodeConfig consumes rather than the whole entry, which keeps listing the
// pages of a gigabyte-scale archive off the critical path.
func entryDimensions(f *zip.File) (int, int, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, 0, fmt.Errorf("cbz: open entry %s: %w", f.Name, err)
	}
	defer rc.Close()
	cfg, _, err := image.DecodeConfig(rc)
	if err != nil {
		return 0, 0, fmt.Errorf("cbz: decode config %s: %w", f.Name, err)
	}
	return cfg.Width, cfg.Height, nil
}

// Dimensions reports an image's size from its header. Zero values with a nil
// error are never returned: a format we cannot parse yields an error and the
// caller decides whether unknown dimensions are fatal (they are not, for
// api.Page).
func Dimensions(r io.Reader) (int, int, error) {
	cfg, _, err := image.DecodeConfig(r)
	if err != nil {
		return 0, 0, fmt.Errorf("cbz: decode config: %w", err)
	}
	return cfg.Width, cfg.Height, nil
}

// Thumbnail scales an image down to at most maxW wide, preserving aspect ratio,
// and encodes it as JPEG for the library grid. CatmullRom is chosen over a
// cheaper kernel because thumbnails are generated once per comic and then
// served forever; the quality is worth the one-off cost.
//
// Images already narrower than maxW are re-encoded rather than passed through:
// the caller wants a JPEG of known dimensions, and the source may be any of the
// page formats.
func Thumbnail(r io.Reader, maxW int) ([]byte, error) {
	if maxW <= 0 {
		return nil, fmt.Errorf("cbz: thumbnail width must be positive, got %d", maxW)
	}
	// The header decides whether the decode may run at all, and it is a ~0.1ms
	// read against ~1s for the decode it guards. r is a zip entry and cannot be
	// rewound, so what DecodeConfig consumes (its own read-ahead included) is
	// teed off and replayed into the decode rather than re-read.
	var head bytes.Buffer
	cfg, _, err := image.DecodeConfig(io.TeeReader(r, &head))
	if err != nil {
		return nil, fmt.Errorf("cbz: decode config: %w", err)
	}
	if err := checkPixels(cfg.Width, cfg.Height); err != nil {
		return nil, err
	}

	src, _, err := image.Decode(io.MultiReader(&head, r))
	if err != nil {
		return nil, fmt.Errorf("cbz: decode: %w", err)
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	// The header is not the bitmap: a decoder may hand back bounds that differ
	// from what the header promised, so the real ones are checked too.
	if err := checkPixels(w, h); err != nil {
		return nil, err
	}
	if w > maxW {
		// Round the height rather than truncating so a wide, short page does
		// not collapse to zero rows.
		h = (h*maxW + w/2) / w
		if h < 1 {
			h = 1
		}
		w = maxW
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85}); err != nil {
		return nil, fmt.Errorf("cbz: encode thumbnail: %w", err)
	}
	return buf.Bytes(), nil
}
