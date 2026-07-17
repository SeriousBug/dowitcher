package library

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/SeriousBug/longbox/internal/cbz"
)

// CoverPathIn is where a comic's cached cover lives under coversDir. It returns
// "" for a hash too short to shard, which is the "no cover can be cached for
// this row" answer.
//
// The key is the content hash, not the comic id and not the path. A rename
// therefore does not orphan the thumbnail -- the same bytes keep the same cover
// file, and a library-wide reorganisation costs zero re-decodes. Two identical
// files share one cover for the same reason. And because the hash changes if
// and only if the archive's contents change, a file replaced in place misses
// the cache and regenerates, with no invalidation logic to get wrong.
//
// The first hex byte shards the tree: one directory per library is fine at a
// hundred books and a problem at a hundred thousand, and the cheap fix is the
// one every object store already uses.
//
// It is a package-level function, and exported, because the scanner is not the
// only thing that touches this cache: the cover handler in internal/server
// reads and writes the same tree. The two had drifted onto different layouts,
// so the scanner warmed a cache the handler could never hit and every first
// cover request paid a full decode (~800ms for AVIF) that had already been paid
// once. There is one scheme now, and this is it -- callers pass their own
// covers dir rather than each deriving it.
func CoverPathIn(coversDir, hash string) string {
	if coversDir == "" || len(hash) < 2 {
		return ""
	}
	return filepath.Join(coversDir, hash[:2], hash+".jpg")
}

// CoversDir is the cover cache root under a data dir. main hands the same
// directory to the scanner and to the server, so the derivation lives here
// rather than being spelled out at both ends.
func CoversDir(dataDir string) string { return filepath.Join(dataDir, "covers") }

// CoverPath is where this library's cached cover for hash lives.
func (l *Library) CoverPath(hash string) string {
	return CoverPathIn(CoversDir(l.cfg.DataDir), hash)
}

// Cover returns a comic's cover thumbnail as serve-ready JPEG, generating and
// caching it if it is not cached yet.
//
// It performs no visibility check. The caller has already resolved the comic
// through the store, which is where visibility is enforced.
func (l *Library) Cover(ctx context.Context, comicID string) ([]byte, error) {
	row, err := l.st.ComicRowByID(comicID)
	if err != nil {
		return nil, err
	}
	if row.ContentHash != "" {
		if b, err := os.ReadFile(l.CoverPath(row.ContentHash)); err == nil {
			return b, nil
		}
	}
	// Not cached. A comic added by the import pipeline has never been through a
	// scan, and a scan that was cancelled may not have reached this one, so the
	// miss is generated on demand rather than treated as an error.
	abs := row.Path
	if !filepath.IsAbs(abs) {
		abs = l.abs(row.Path)
	}
	a, err := cbz.Open(abs)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoCover, err)
	}
	defer a.Close()
	if a.PageCount() == 0 {
		return nil, ErrNoCover
	}
	hash := row.ContentHash
	if hash == "" {
		hash = a.Hash()
	}
	b, err := l.generateCover(a, hash)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoCover, err)
	}
	return b, nil
}

// ensureCover generates a cover unless one is already cached for this hash.
func (l *Library) ensureCover(ctx context.Context, a *cbz.Archive, hash string) error {
	p := l.CoverPath(hash)
	if p == "" {
		return nil
	}
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	_, err := l.generateCover(a, hash)
	return err
}

// generateCover decodes the cover page, scales it, and writes it to the cache.
// Only the cover is thumbnailed: a full AVIF decode runs ~800ms under the WASM
// decoder, so doing every page of every comic would turn a first scan into
// hours of work for images nobody has asked to see.
func (l *Library) generateCover(a *cbz.Archive, hash string) ([]byte, error) {
	rc, err := a.Cover()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := cbz.Thumbnail(rc, l.cfg.CoverWidth)
	if err != nil {
		return nil, err
	}
	if p := l.CoverPath(hash); p != "" {
		if err := writeFileAtomic(p, b); err != nil {
			return nil, err
		}
	}
	return b, nil
}

// writeFileAtomic writes through a temp file in the destination directory and
// renames it into place. The rename is atomic within a filesystem, so a reader
// racing a generation sees either no cover or a whole one, never the first half
// of a JPEG -- which matters here because scan workers and an on-demand request
// can be generating the same cover at the same moment.
func writeFileAtomic(p string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op once the rename has succeeded
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
