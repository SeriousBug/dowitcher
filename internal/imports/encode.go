package imports

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/gen2brain/avif"
	"github.com/gen2brain/webp"
	"golang.org/x/sync/errgroup"

	"github.com/SeriousBug/dowitcher/internal/api"
)

const defaultQuality = 70

// defaultConvertEncode is the format a PDF or non-zip archive (CBR/CB7/CBT) is
// re-encoded to when the caller names none. These sources are converted to CBZ
// once at ingest, so it is the moment to shrink them: AVIF cuts a page to a
// fraction of a JPEG's size at a quality difference the eye does not catch on
// comic art. A folder-of-images upload is left alone by default — its Encode
// comes from the user — so this only steers the auto-conversion paths.
const defaultConvertEncode = "avif"

// avifSpeed is the AV1 encoder effort [0,10], slower being smaller. 8 is the
// knee of the size/time curve for full-size comic pages: near the best size a
// slower speed reaches, without the minutes-per-page a low speed costs under
// wasm. Set explicitly because a zero value means the slowest speed, not a
// sensible default.
const avifSpeed = 8

// encodeExt maps an --encode format to the extension its pages get in the CBZ.
// package.py picks the ImageMagick output format purely from this extension;
// here it only names the entry, since the encoder is chosen explicitly.
var encodeExt = map[string]string{
	"avif": ".avif",
	"webp": ".webp",
	"jpeg": ".jpeg",
}

// goodEnoughExt are source formats an encode pass copies through untouched. AVIF
// and WebP are already space-efficient, so re-encoding one would spend CPU and a
// generation of quality to save little or nothing; a page in either is kept
// as-is regardless of the target format. JPEG is deliberately absent: it is an
// old codec that AVIF beats by a wide margin, so re-encoding a JPEG page is a
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
// without failing an already-running job. jpeg is stdlib, and gen2brain's avif
// and webp are libaom/libwebp compiled to WASM, run under wazero with a purego
// dlopen of the system library as a fast path when one happens to exist. Both
// paths are cgo-free, so the binary keeps encoding AVIF on distroless with no
// ImageMagick, no avifenc, and no shell-out at all.
func encodeOne(img image.Image, format string, quality int) ([]byte, error) {
	var buf bytes.Buffer
	var err error
	switch format {
	case "jpeg":
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality})
	case "webp":
		err = webp.Encode(&buf, img, webp.Options{Quality: quality})
	case "avif":
		// Both encoders instantiate a fresh wazero module per call over a
		// shared read-only compiled module, so concurrent calls are safe.
		//
		// Speed is set explicitly: gen2brain reads a zero-valued Speed as the
		// slowest AV1 effort (0), which on a full-size page under wasm runs into
		// minutes per page. 8 lands within a few percent of the best size at a
		// fraction of the time, the tradeoff a bulk background import wants.
		err = avif.Encode(&buf, img, avif.Options{Quality: quality, Speed: avifSpeed})
	default:
		return nil, fmt.Errorf("%w: %q", ErrBadEncode, format)
	}
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// encodeMemPerPixel estimates the peak memory a single concurrent AVIF encode
// holds, per source pixel. A page is decoded to RGBA (4 bytes/pixel) and libaom
// keeps several working buffers of that order while encoding, so the real peak
// runs a few times the raw RGBA. 24 bytes/pixel is deliberately pessimistic:
// sizing concurrency too low only costs speed, while sizing it too high is the
// OOM this guards against.
const encodeMemPerPixel = 24

// encodeMemFraction is the share of the memory budget the encode stage may fill
// with concurrent arenas. Half leaves room for the Go heap, the page bytes read
// off disk, and the rest of the process.
const encodeMemFraction = 0.5

// encodeUnknownConcurrency is the fan-out used when the memory budget cannot be
// read (a non-Linux dev host). Without a budget the code cannot prove a wide
// fan-out is safe, so it stays low and leans on encodePagesAdaptive to widen or
// narrow from there only if needed.
const encodeUnknownConcurrency = 2

// encodePagesAdaptive re-encodes every page, choosing how many to run at once so
// their combined peak memory stays under a fraction of the budget, and retrying
// the whole pass at half the width if the encoder still runs out of memory.
//
// The estimate is best effort: it derives per-page memory from the largest page
// and a pessimistic per-pixel constant, but real libaom use varies. The retry
// is the backstop for when the estimate guesses high on a machine whose true
// budget is tighter — a WASM OOM surfaces as an error (not a process crash), so
// it can be caught and the pass rerun narrower. A fatal host OOM cannot be
// caught; the budget-based cap is what keeps the pass away from that edge.
//
// override pins the concurrency when > 0 (DOWITCHER_IMPORT_ENCODE_CONCURRENCY),
// bypassing the estimate but still subject to the OOM retry.
func encodePagesAdaptive(ctx context.Context, pages []*srcFile, format string, quality int, workDir string, override int, progress ProgressFunc) ([]string, error) {
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

	for {
		out, err := encodePagesImpl(ctx, pages, format, quality, workDir, limit, progress)
		if err == nil {
			return out, nil
		}
		if ctx.Err() != nil {
			return nil, err
		}
		if limit > 1 && isMemoryError(err) {
			next := limit / 2
			log.Printf("import encode: out of memory at concurrency %d, retrying at %d: %v", limit, next, err)
			limit = next
			continue
		}
		return nil, err
	}
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

// memoryErrorSignatures are substrings that mark an encode failure as an
// out-of-memory condition worth retrying at a lower concurrency, rather than a
// deterministic failure (a corrupt or unsupported image) that a rerun cannot
// fix. The WASM encoders surface a linear-memory exhaustion as one of these.
var memoryErrorSignatures = []string{
	"out of memory",
	"cannot allocate",
	"failed to allocate",
	"out of bounds memory",
	"memory size",
}

func isMemoryError(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, s := range memoryErrorSignatures {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// encodePagesImpl is the encode pass the adaptive wrapper drives. It is a var so
// a test can substitute a deterministic failure and exercise the OOM backoff
// without inducing a real out-of-memory condition.
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
// AVIF encode holds a large libaom (WASM) memory arena, so a wide fan-out on a
// many-core host is an OOM risk, not a speedup. See encodePagesAdaptive.
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
		return fmt.Errorf("%w: %q (want avif, webp or jpeg)", ErrBadEncode, format)
	}
	if quality < 1 || quality > 100 {
		return fmt.Errorf("%w: %d (want 1-100)", ErrBadQuality, quality)
	}
	return nil
}

var errZeroDim = errors.New("image has a zero dimension")
