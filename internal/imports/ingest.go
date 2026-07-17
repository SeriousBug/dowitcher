package imports

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"os"
	"slices"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/SeriousBug/longbox/internal/api"
)

// content is everything learned about one distinct SHA-256 digest.
//
// Keying on the digest rather than the path is what lets the sibling
// propagation package.py does explicitly fall out for free: byte-identical
// files decode to identical pixels by definition, so dims and thumb are
// properties of the digest, not of any one path that carries it.
type content struct {
	files []int // indices into the collected file slice, ascending
	dims  image.Point
	thumb []byte
	// decodeErr marks a digest that hashed but would not decode. Every file
	// carrying it drops out of grouping, matching package.py where a
	// representative missing from `thumb` takes its whole bucket with it.
	decodeErr error
}

// ingested is the result of the single read pass over the source files.
type ingested struct {
	byDigest   map[string]*content
	digestOf   map[int]string
	exactDupes int
	skipped    []string
}

// ingest reads, hashes and thumbnails every file in one pass.
//
// package.py makes two passes: hash_all() reads every file, then load_all()
// reads the unique ones again to decode them. That costs a second full read of
// nearly the whole set. Here each file is read exactly once and decoded from
// the bytes already in hand.
//
// The decode is still skipped for byte-identical duplicates: a worker claims
// its digest under the lock and only the claiming worker decodes. Which worker
// wins that race is nondeterministic but immaterial — the racers hold identical
// bytes, so they would produce identical dims, thumbnail and decode error. The
// representative *file* reported downstream is chosen by index afterwards, not
// by who won.
//
// decode is false in exact mode, where no pixel comparison ever runs and the
// decode would be pure waste.
func ingest(ctx context.Context, files []*srcFile, decode bool, workers int, progress ProgressFunc) (*ingested, error) {
	out := &ingested{
		byDigest: make(map[string]*content),
		digestOf: make(map[int]string),
	}

	var mu sync.Mutex
	var done int

	// gctx is kept distinct from ctx: errgroup cancels its derived context as
	// soon as Wait returns, so checking that one afterwards would report
	// Canceled on every successful run.
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)

	progress(api.StageHashing, 0, len(files))
	for _, f := range files {
		if gctx.Err() != nil {
			break
		}
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			// Reading whole-file is safe next to the decode it feeds: a decoded
			// bitmap is w*h*4 bytes and dwarfs the compressed source, so the
			// bytes are not what bounds memory here. Worker count is.
			buf, err := os.ReadFile(f.abs)

			mu.Lock()
			defer func() {
				done++
				progress(api.StageHashing, done, len(files))
				mu.Unlock()
			}()
			if err != nil {
				// Unreadable files are reported and skipped rather than
				// aborting the import, as in package.py.
				out.skipped = append(out.skipped, fmt.Sprintf("skip (unreadable): %s (%v)", f.rel, err))
				return nil
			}

			sum := sha256.Sum256(buf)
			digest := hex.EncodeToString(sum[:])
			out.digestOf[f.index] = digest

			c, seen := out.byDigest[digest]
			if seen {
				c.files = append(c.files, f.index)
				out.exactDupes++
				return nil
			}
			c = &content{files: []int{f.index}}
			out.byDigest[digest] = c

			if !decode {
				return nil
			}
			// Decode outside the lock; only this worker owns this digest.
			mu.Unlock()
			dims, thumb, derr := thumbnail(buf)
			mu.Lock()

			if derr != nil {
				c.decodeErr = derr
				out.skipped = append(out.skipped, fmt.Sprintf("skip (unreadable image): %s (%v)", f.rel, derr))
				return nil
			}
			c.dims, c.thumb = dims, thumb
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Workers append in completion order; sorting restores the collected order
	// so the representative and every report below are deterministic.
	for _, c := range out.byDigest {
		slices.Sort(c.files)
	}
	return out, nil
}
