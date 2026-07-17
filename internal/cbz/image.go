package cbz

import (
	"archive/zip"
	"bytes"
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
	src, _, err := image.Decode(r)
	if err != nil {
		return nil, fmt.Errorf("cbz: decode: %w", err)
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("cbz: empty image")
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
