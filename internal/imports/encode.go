package imports

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	webp "github.com/SeriousBug/webp-go-pure"
	"golang.org/x/sync/errgroup"

	"github.com/SeriousBug/dowitcher/internal/api"
)

const defaultQuality = 70

// defaultConvertEncode is the format a PDF or non-zip archive (CBR/CB7/CBT) is
// re-encoded to when the caller names none. These sources are converted to CBZ
// once at ingest, so it is the moment to shrink them: WebP cuts a page to a
// fraction of a JPEG's size at a quality difference the eye does not catch on
// comic art. A folder-of-images upload is left alone by default — its Encode
// comes from the user — so this only steers the auto-conversion paths.
const defaultConvertEncode = "webp"

// webpEffort is the VP8 encoder effort [0,9], slower being smaller. Measured on
// a 3000x4600 page, effort 2 is the knee: it lands at the same output size as
// effort 4 in under half the time, and effort 6 and up cost multiples more for
// nothing. Set explicitly because the library's default is effort 0, which
// gives up ~25% on size.
const webpEffort = 2

// encodeExt maps an --encode format to the extension its pages get in the CBZ.
// package.py picks the ImageMagick output format purely from this extension;
// here it only names the entry, since the encoder is chosen explicitly.
var encodeExt = map[string]string{
	"webp": ".webp",
	"jpeg": ".jpeg",
}

// goodEnoughExt are source formats an encode pass copies through untouched. AVIF
// and WebP are already space-efficient, so re-encoding one would spend CPU and a
// generation of quality to save little or nothing; a page in either is kept
// as-is regardless of the target format. JPEG is deliberately absent: it is an
// old codec that WebP beats by a wide margin, so re-encoding a JPEG page is a
// real size win at imperceptible added loss.
var goodEnoughExt = map[string]bool{
	".avif": true,
	".webp": true,
}

// encodeOne re-encodes a decoded image to fmt at quality.
//
// package.py shells out to ImageMagick (`magick src -quality N dest`). This
// does it in process instead, for two reasons. The build needs CGO_ENABLED=0
// for the distroless image, which rules out every cgo binding; and an external
// `magick` is a runtime dependency the server cannot check for at import time
// without failing an already-running job. jpeg is stdlib and webp-go-pure is a
// pure-Go VP8 encoder, so the binary keeps encoding on distroless with no
// ImageMagick, no cwebp, and no shell-out at all.
//
// AVIF used to be an option here, through a WASM build of libaom. It produced
// smaller pages, but each concurrent encode held ~180 bytes per source pixel of
// wasm linear memory — ~2.5GB for one full-size page — which is what OOM-killed
// the server on large imports. The pure-Go VP8 encoder holds ~10 bytes/pixel
// for the same page, roughly 18x less, and that is what makes a wide fan-out
// safe. Existing AVIF pages still decode; only the encode target changed.
func encodeOne(img image.Image, format string, quality int) ([]byte, error) {
	switch format {
	case "jpeg":
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	case "webp":
		src, err := toWebPImage(img)
		if err != nil {
			return nil, err
		}
		return webp.EncodeLossy(src, &webp.LossyOptions{Quality: uint8(quality), Effort: webpEffort})
	default:
		return nil, fmt.Errorf("%w: %q", ErrBadEncode, format)
	}
}

// toWebPImage converts a decoded page into the encoder's packed RGBA buffer.
//
// The lossy encoder rejects an image that carries alpha, so a page with any
// transparency is composited over white first — that is what a reader shows
// anyway, and refusing the page instead would fail the whole import over a
// single PNG with an alpha channel. An already-opaque RGBA is handed over
// without the copy, which is the common case: it is what image.Decode returns
// for PNG, and the extra buffer is another 4 bytes per pixel of live memory.
func toWebPImage(img image.Image) (*webp.Image, error) {
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return nil, errZeroDim
	}
	rgba, ok := img.(*image.RGBA)
	if !ok || !rgba.Opaque() || rgba.Stride != b.Dx()*4 {
		dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
		draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Over)
		rgba = dst
	}
	return &webp.Image{Width: b.Dx(), Height: b.Dy(), RGBA: rgba.Pix}, nil
}

// encodeMemPerPixel estimates the peak memory a single concurrent WebP encode
// holds, per source pixel. Measured at 9-13 bytes/pixel on a 3000x4600 page
// across concurrency 1 to 32: the decoded RGBA is 4 of those, the encoder's own
// working buffers the rest. 24 bytes/pixel is deliberately pessimistic: sizing
// concurrency too low only costs speed, while sizing it too high is the OOM
// this guards against.
const encodeMemPerPixel = 24

// encodeMemFraction is the share of the memory budget the encode stage may fill
// with concurrent arenas. Half leaves room for the Go heap, the page bytes read
// off disk, and the rest of the process.
const encodeMemFraction = 0.5

// encodeUnknownConcurrency is the fan-out used when the memory budget cannot be
// read (a non-Linux dev host). Without a budget the code cannot prove a wide
// fan-out is safe, so it stays low.
const encodeUnknownConcurrency = 2

// encodePagesBounded re-encodes every page, choosing how many to run at once so
// their combined peak memory stays under a fraction of the budget.
//
// The estimate is best effort: it derives per-page memory from the largest page
// and a pessimistic per-pixel constant. There is no retry-narrower backstop,
// because a pure-Go encoder that exhausts memory takes the process with it
// rather than returning an error something could catch — staying under the
// budget is the only defence, which is why the constant is set high.
//
// override pins the concurrency when > 0 (DOWITCHER_IMPORT_ENCODE_CONCURRENCY),
// bypassing the estimate.
func encodePagesBounded(ctx context.Context, pages []*srcFile, format string, quality int, workDir string, override int, progress ProgressFunc) ([]string, error) {
	var limit int
	var reason string
	if override > 0 {
		limit = min(override, len(pages))
		reason = fmt.Sprintf("configured concurrency %d", override)
	} else {
		limit, reason = autoEncodeConcurrency(maxEncodePixels(pages))
	}
	if limit < 1 {
		limit = 1
	}
	log.Printf("import encode: %d page(s), concurrency %d (%s)", len(pages), limit, reason)

	return encodePagesImpl(ctx, pages, format, quality, workDir, limit, progress)
}

// autoEncodeConcurrency picks how many pages to encode at once so their combined
// peak memory stays under encodeMemFraction of the budget, clamped to [1, NumCPU].
func autoEncodeConcurrency(maxPixels int) (n int, reason string) {
	cores := runtime.NumCPU()
	if maxPixels <= 0 {
		maxPixels = 1
	}
	budget, ok := availableMemoryBytes()
	if !ok {
		n = min(cores, encodeUnknownConcurrency)
		return n, fmt.Sprintf("memory budget unknown, defaulting to %d", n)
	}
	perEncode := uint64(maxPixels) * encodeMemPerPixel
	fit := int(float64(budget) * encodeMemFraction / float64(perEncode))
	n = min(max(fit, 1), cores)
	return n, fmt.Sprintf("budget %d MiB, per-page est %d MiB, fits %d, cores %d",
		budget>>20, perEncode>>20, fit, cores)
}

// maxEncodePixels returns the pixel count of the largest page that will actually
// be re-encoded, reading only image headers. Pages copied through untouched
// (already-efficient AVIF/WebP) hold no encoder arena, so they are excluded and
// do not inflate the concurrency estimate.
func maxEncodePixels(pages []*srcFile) int {
	most := 0
	for _, p := range pages {
		if goodEnoughExt[strings.ToLower(filepath.Ext(p.abs))] {
			continue
		}
		f, err := os.Open(p.abs)
		if err != nil {
			continue
		}
		cfg, _, err := image.DecodeConfig(f)
		f.Close()
		if err != nil {
			continue
		}
		if px := cfg.Width * cfg.Height; px > most {
			most = px
		}
	}
	return most
}

// encodePagesImpl is the encode pass the bounded wrapper drives. It is a var so
// a test can observe the concurrency the wrapper picked without running a real
// encode.
var encodePagesImpl = encodePages

// encodePages re-encodes every page into workDir and returns the new paths in
// the same order, running at most limit encodes concurrently.
//
// Any failure aborts the whole import: package.py exits before opening the zip
// so a failed encode never leaves a half-built CBZ, and that behaviour is worth
// keeping. Pages land in a temp dir rather than streaming into the archive for
// the same reason.
//
// limit is bounded by the caller rather than fixed at NumCPU: each concurrent
// encode holds a working set proportional to the page's pixel count, so a wide
// fan-out over big pages on a small container is an OOM risk, not a speedup.
// See encodePagesBounded.
func encodePages(ctx context.Context, pages []*srcFile, format string, quality int, workDir string, limit int, progress ProgressFunc) ([]string, error) {
	width := padWidth(len(pages))
	out := make([]string, len(pages))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(limit)

	var mu sync.Mutex
	done := 0
	progress(api.StageEncoding, 0, len(pages))

	for i, p := range pages {
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			buf, err := os.ReadFile(p.abs)
			if err != nil {
				return fmt.Errorf("encode %s: %w", p.rel, err)
			}
			ext, enc := encodeExt[format], buf
			if srcExt := strings.ToLower(filepath.Ext(p.abs)); goodEnoughExt[srcExt] {
				// Already space-efficient: keep the original bytes and its own
				// extension rather than re-encoding it into the target format.
				ext = srcExt
			} else {
				img, _, err := image.Decode(bytes.NewReader(buf))
				if err != nil {
					return fmt.Errorf("encode %s: %w", p.rel, err)
				}
				enc, err = encodeOne(img, format, quality)
				if err != nil {
					return fmt.Errorf("encode %s: %w", p.rel, err)
				}
			}
			dest := filepath.Join(workDir, fmt.Sprintf("%0*d%s", width, i+1, ext))
			if err := os.WriteFile(dest, enc, 0o600); err != nil {
				return fmt.Errorf("encode %s: %w", p.rel, err)
			}
			out[i] = dest

			mu.Lock()
			done++
			progress(api.StageEncoding, done, len(pages))
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// validateEncode checks the encode options before any work starts, so a bad
// format or quality fails the job immediately rather than after the pixel
// sweep.
func validateEncode(format string, quality int) error {
	if format == "" {
		return nil
	}
	if _, ok := encodeExt[format]; !ok {
		return fmt.Errorf("%w: %q (want webp or jpeg)", ErrBadEncode, format)
	}
	if quality < 1 || quality > 100 {
		return fmt.Errorf("%w: %d (want 1-100)", ErrBadQuality, quality)
	}
	return nil
}

var errZeroDim = errors.New("image has a zero dimension")
