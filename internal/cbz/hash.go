package cbz

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
)

// ContentHash is a stable identity for a CBZ, used by the scanner to recognise
// a renamed or moved file as an existing library row instead of orphaning its
// tags and reading progress.
//
// It hashes the zip's central directory — each entry's name, uncompressed size
// and CRC-32 — rather than the file bytes. Every scan would otherwise have to
// read gigabytes per comic just to notice that nothing changed, which makes a
// rescan of a real library take minutes instead of milliseconds. The central
// directory is a few KB and already carries a CRC per entry, so this hash
// changes if and only if the archive's contents change, at a fraction of the
// I/O.
//
// What it deliberately does not capture: compression level, entry order,
// timestamps, and the zip's own framing. Two archives holding identical pages
// under identical names hash the same even if one was recompressed — which is
// the behaviour we want, since that file is the same comic.
func ContentHash(p string) (string, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return "", fmt.Errorf("cbz: open %s: %w", p, err)
	}
	defer zr.Close()
	return hashEntries(zr.File), nil
}

// Hash computes the content hash of an already-open archive.
func (a *Archive) Hash() string { return hashEntries(a.zr.File) }

func hashEntries(files []*zip.File) string {
	lines := make([]string, 0, len(files))
	for _, f := range files {
		if f.FileInfo().IsDir() {
			continue
		}
		// Field separators that cannot occur in the numeric fields keep the
		// digest unambiguous: without them "a" + size 11 and "a1" + size 1
		// would hash alike.
		lines = append(lines, f.Name+"\x00"+
			strconv.FormatUint(f.UncompressedSize64, 10)+"\x00"+
			strconv.FormatUint(uint64(f.CRC32), 10))
	}
	// Sorting makes the hash independent of the order entries happen to sit in
	// the central directory, so a repack that reorders nothing else matches.
	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}
