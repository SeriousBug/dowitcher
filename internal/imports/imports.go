// Package imports builds a deduplicated, correctly ordered CBZ from a folder of
// images. It is a Go port of package.py.
//
// The pipeline: collect images recursively and natural-sort them by their path
// relative to the root; settle byte-identical copies by SHA-256; reduce each
// remaining file to a 64x64 grayscale thumbnail; cluster files whose thumbnails
// differ by no more than a mean-absolute-error threshold and whose aspect
// ratios match; keep the highest-resolution member of each cluster; and write
// the survivors into a zip under zero-padded sequential names.
//
// There is deliberately no perceptual hash here. The comparison is a plain MAE
// over raw grayscale buffers, and the default threshold of 3.0 is tuned to that
// specific representation: on the gallery it was derived from, duplicate pairs
// top out around MAE 2.2 and the nearest distinct pair sits near 17.8, so 3.0
// falls in an empty band. Swapping in a pHash or a Hamming distance would
// invalidate the threshold entirely.
//
// The package is pure: a directory in, a CBZ out, progress over a callback. No
// database, no HTTP, and the source files are never modified.
package imports

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/SeriousBug/dowitcher/internal/api"
)

var (
	ErrNoImages     = errors.New("no image files found")
	ErrNotDir       = errors.New("not a directory")
	ErrBadEncode    = errors.New("unsupported encode format")
	ErrBadQuality   = errors.New("quality out of range")
	ErrTooManyFiles = errors.New("too many files in one import")
)

// maxFiles bounds one import. The grouping sweep is O(n^2) in the number of
// distinct images and nothing else limits it: the 8GB upload cap allows ~200k
// small files, which is ~2e10 MAE comparisons — hours of every core, with every
// thumbnail resident, so memory gives out first. The pipeline has no way to
// refuse that once it has started, so it is refused before it starts.
//
// 5000 is well past any real book: a long collected edition is under 1000
// pages, and 5000 still leaves room for a folder carrying several chapters plus
// their previews. At 5000 the sweep is ~1.2e7 pairs, which is seconds.
const maxFiles = 5000

// ProgressFunc reports stage progress. Total is 0 when it is not yet known or
// does not apply. Implementations must be safe to call from any goroutine; the
// pipeline serializes calls but makes no ordering promise beyond that.
type ProgressFunc func(stage api.ImportStage, done, total int)

// Result is what an import produced.
type Result struct {
	// PageCount is the number of images written to the CBZ.
	PageCount int
	// SourceCount is the number of image files collected from the source.
	SourceCount int
	// ExactDupes counts files dropped as byte-identical to an earlier file.
	ExactDupes int
	// NearDupes counts the cluster merges the pixel comparison made. It is the
	// number of distinct images folded into another, not the file count.
	NearDupes int
	// Groups reports every cluster that dropped at least one file.
	Groups []api.DupeGroup
	// Skipped holds one human-readable line per file that could not be read or
	// decoded. These do not fail the import.
	Skipped []string
	// SourceBytes is the on-disk size of the kept pages before any re-encode.
	SourceBytes int64
	// OutBytes is the size of the finished CBZ.
	OutBytes int64
}

// Run builds a CBZ at outPath from the images under srcDir.
//
// outPath gains a .cbz suffix if it lacks one. progress may be nil.
func Run(ctx context.Context, srcDir, outPath string, opts api.ImportOptions, progress ProgressFunc) (*Result, error) {
	if progress == nil {
		progress = func(api.ImportStage, int, int) {}
	}
	threshold := opts.Threshold
	if threshold <= 0 {
		threshold = defaultThreshold
	}
	quality := opts.Quality
	if quality == 0 {
		quality = defaultQuality
	}
	if err := validateEncode(opts.Encode, quality); err != nil {
		return nil, err
	}

	info, err := os.Stat(srcDir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: %s", ErrNotDir, srcDir)
	}

	files, err := collect(srcDir)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%w in %s", ErrNoImages, srcDir)
	}
	if len(files) > maxFiles {
		return nil, fmt.Errorf("%w: %d files, limit is %d", ErrTooManyFiles, len(files), maxFiles)
	}

	workers := runtime.NumCPU()

	// Exact mode never compares pixels, so the decode is skipped outright
	// rather than done and thrown away.
	in, err := ingest(ctx, files, !opts.Exact, workers, progress)
	if err != nil {
		return nil, err
	}

	var groups []pageGroup
	var stats groupStats
	if opts.Exact {
		groups, stats = groupExact(files, in)
	} else {
		groups, stats, err = groupPages(ctx, files, in, threshold, progress)
		if err != nil {
			return nil, err
		}
	}
	if len(groups) == 0 {
		// Every file was unreadable or failed to decode; there is no comic
		// here, and an empty zip would be worse than an error.
		return nil, fmt.Errorf("%w in %s (all %d file(s) skipped)", ErrNoImages, srcDir, len(files))
	}

	pages := make([]*srcFile, len(groups))
	for i, g := range groups {
		pages[i] = files[g.keep]
	}

	res := &Result{
		PageCount:   len(pages),
		SourceCount: len(files),
		ExactDupes:  stats.exactDupes,
		NearDupes:   stats.nearDupes,
		Groups:      dupeReport(files, groups),
		Skipped:     in.skipped,
	}
	for _, p := range pages {
		res.SourceBytes += p.size
	}

	// Page paths handed to the packager: the originals, or their re-encoded
	// stand-ins. The temp dir outlives the encode and dies with the import.
	paths := make([]string, len(pages))
	for i, p := range pages {
		paths[i] = p.abs
	}
	if opts.Encode != "" {
		tmp, err := os.MkdirTemp("", "dowitcher-import-*")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(tmp)
		paths, err = encodePages(ctx, pages, opts.Encode, quality, tmp, progress)
		if err != nil {
			return nil, err
		}
	}

	title := opts.Name
	if title == "" {
		title = defaultTitle(srcDir)
	}
	out := forceCBZSuffix(outPath)
	outBytes, err := writeCBZ(ctx, out, title, paths, progress)
	if err != nil {
		return nil, err
	}
	res.OutBytes = outBytes

	progress(api.StageDone, len(pages), len(pages))
	return res, nil
}

// dupeReport turns the clusters into the user-facing report, naming files by
// their path relative to the root. Groups that dropped nothing are omitted:
// they are the overwhelming majority and say nothing.
func dupeReport(files []*srcFile, groups []pageGroup) []api.DupeGroup {
	var out []api.DupeGroup
	for _, g := range groups {
		if len(g.members) < 2 {
			continue
		}
		dropped := make([]string, 0, len(g.members)-1)
		for _, m := range g.members {
			if m != g.keep {
				dropped = append(dropped, files[m].rel)
			}
		}
		reason := "near"
		if g.exact {
			reason = "exact"
		}
		out = append(out, api.DupeGroup{
			Kept:    files[g.keep].rel,
			Dropped: dropped,
			Reason:  reason,
		})
	}
	return out
}

// defaultTitle names the comic after the source folder, as package.py names the
// output file.
func defaultTitle(srcDir string) string {
	abs, err := filepath.Abs(srcDir)
	if err != nil {
		abs = srcDir
	}
	return filepath.Base(abs)
}
