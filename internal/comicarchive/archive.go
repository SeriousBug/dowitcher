// Package comicarchive extracts the page images out of the comic-archive
// containers that are not zip: CBR (RAR), CB7 (7z) and CBT (TAR). It writes them
// to a directory the import pipeline then reads, so the rest of the system only
// ever deals in CBZ. It is deliberately low-level — no database, no HTTP, no
// dependency on internal/imports — the way internal/cbz is, so the importer can
// use it without a cycle.
//
// Why transcode rather than serve pages straight from these containers: a zip
// carries a central directory, so internal/cbz can open any page by index for a
// few bytes of I/O. RAR and TAR have no index and 7z can be solid, so reaching
// page N means decoding everything before it. The reader, cover and hash layers
// all assume cheap random access, which only zip gives — so a dropped CBR/CB7/CBT
// is converted to a CBZ once at ingest and never read in its original form again.
package comicarchive

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/bodgit/sevenzip"
	rardecode "github.com/nwaples/rardecode/v2"
)

var (
	// ErrUnsupported means the path is not a container this package handles.
	ErrUnsupported = errors.New("comicarchive: unsupported container")
	// ErrNoImages means the container opened but held no page images.
	ErrNoImages = errors.New("comicarchive: no images in archive")
	// ErrTooBig means the extracted images exceeded the byte budget: a
	// decompression bomb, or simply a book past the upload cap.
	ErrTooBig = errors.New("comicarchive: extracted images exceed the size cap")
	// ErrUnreadable wraps a container that would not open or decode. The caller
	// turns it into a user-facing "could not be read" rather than a bomb/no-image
	// message.
	ErrUnreadable = errors.New("comicarchive: archive could not be read")
)

// imageExts is the set of entry extensions treated as pages, matching
// internal/cbz and internal/imports so a page that survives extraction is one the
// pipeline will also accept.
var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
	".webp": true, ".bmp": true, ".tiff": true, ".avif": true,
}

// Progress reports extraction advance. done counts images written, total the
// images known to remain; total is 0 when it cannot be known ahead of time (a
// streamed TAR), in which case only done advances.
type Progress func(done, total int)

// IsArchiveName reports whether name is a non-zip comic container this package
// extracts. Name-only, like imports.IsCBZName: the file is opened and decoded
// before any of it is believed. Bare .rar/.7z/.tar are accepted alongside the
// comic-specific extensions because plenty of comics are left under them.
func IsArchiveName(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".cbr", ".rar", ".cb7", ".7z", ".cbt", ".tar":
		return true
	}
	return false
}

// Extract writes every page image in srcPath under destDir, keeping the entry's
// own name (traversal-cleaned) so a natural sort of destDir reproduces reading
// order the same way internal/cbz orders a zip. budget caps total bytes written.
// Returns the number of images written.
func Extract(ctx context.Context, srcPath, destDir string, budget int64, progress Progress) (int, error) {
	if progress == nil {
		progress = func(int, int) {}
	}
	switch strings.ToLower(filepath.Ext(srcPath)) {
	case ".cbr", ".rar":
		return extractRAR(ctx, srcPath, destDir, budget, progress)
	case ".cb7", ".7z":
		return extract7z(ctx, srcPath, destDir, budget, progress)
	case ".cbt", ".tar":
		return extractTAR(ctx, srcPath, destDir, budget, progress)
	}
	return 0, fmt.Errorf("%w: %s", ErrUnsupported, filepath.Ext(srcPath))
}

// listEntry is one openable archive member, the shape RAR and 7z share.
type listEntry struct {
	name string
	open func() (io.ReadCloser, error)
}

func extractRAR(ctx context.Context, srcPath, destDir string, budget int64, progress Progress) (int, error) {
	files, err := rardecode.List(srcPath)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrUnreadable, err)
	}
	entries := make([]listEntry, 0, len(files))
	for _, f := range files {
		if f.IsDir {
			continue
		}
		entries = append(entries, listEntry{name: f.Name, open: f.Open})
	}
	return extractList(ctx, entries, destDir, budget, progress)
}

func extract7z(ctx context.Context, srcPath, destDir string, budget int64, progress Progress) (int, error) {
	rc, err := sevenzip.OpenReader(srcPath)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrUnreadable, err)
	}
	defer rc.Close()
	entries := make([]listEntry, 0, len(rc.File))
	for _, f := range rc.File {
		if f.FileInfo().IsDir() {
			continue
		}
		entries = append(entries, listEntry{name: f.Name, open: f.Open})
	}
	return extractList(ctx, entries, destDir, budget, progress)
}

// extractList writes the image entries from a materialised entry list. RAR and 7z
// both hand back the whole member list up front, so the image count is known and
// drives a real progress total. Entries are opened in list order, which is the
// archive's own order — the sequential access pattern a solid archive wants.
func extractList(ctx context.Context, entries []listEntry, destDir string, budget int64, progress Progress) (int, error) {
	total := 0
	for _, e := range entries {
		if isPage(e.name) {
			total++
		}
	}
	if total == 0 {
		return 0, ErrNoImages
	}
	written := 0
	var used int64
	progress(0, total)
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		if !isPage(e.name) {
			continue
		}
		rc, err := e.open()
		if err != nil {
			return written, fmt.Errorf("%w: %v", ErrUnreadable, err)
		}
		n, err := writeImage(destDir, e.name, rc, budget-used)
		rc.Close()
		if err != nil {
			return written, err
		}
		used += n
		written++
		progress(written, total)
	}
	if written == 0 {
		return 0, ErrNoImages
	}
	return written, nil
}

func extractTAR(ctx context.Context, srcPath, destDir string, budget int64, progress Progress) (int, error) {
	f, err := os.Open(srcPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	written := 0
	var used int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return written, fmt.Errorf("%w: %v", ErrUnreadable, err)
		}
		if hdr.Typeflag == tar.TypeDir || !isPage(hdr.Name) {
			continue
		}
		n, err := writeImage(destDir, hdr.Name, tr, budget-used)
		if err != nil {
			return written, err
		}
		used += n
		written++
		// A streamed TAR has no member count until it is exhausted, so the total
		// stays unknown and only the done count advances.
		progress(written, 0)
	}
	if written == 0 {
		return 0, ErrNoImages
	}
	return written, nil
}

// writeImage streams one entry to destDir under its cleaned name, refusing to
// write past remaining so a bomb cannot expand beyond the budget. The caller has
// already vetted the name with isPage, so the clean here cannot fail.
func writeImage(destDir, name string, r io.Reader, remaining int64) (int64, error) {
	if remaining <= 0 {
		return 0, ErrTooBig
	}
	rel, _ := safeName(name)
	dst := filepath.Join(destDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return 0, err
	}
	out, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer out.Close()
	n, err := io.Copy(out, io.LimitReader(r, remaining+1))
	if err != nil {
		return n, err
	}
	if n > remaining {
		return n, ErrTooBig
	}
	return n, nil
}

// isPage reports whether an entry is a page image worth extracting: an image by
// extension, not macOS junk, and with a name that stays inside the destination.
func isPage(name string) bool {
	if !isImage(name) {
		return false
	}
	_, ok := safeName(name)
	return ok
}

func isImage(name string) bool {
	if isJunk(name) {
		return false
	}
	return imageExts[strings.ToLower(path.Ext(name))]
}

// isJunk filters the resource forks and metadata that macOS's Archive Utility
// leaves in archives, mirroring internal/cbz.isJunk: they are image entries by
// extension and would otherwise become duplicate garbage pages.
func isJunk(name string) bool {
	name = filepath.ToSlash(name)
	if strings.HasPrefix(name, "__MACOSX/") || strings.Contains(name, "/__MACOSX/") {
		return true
	}
	base := path.Base(name)
	return base == ".DS_Store" || strings.HasPrefix(base, "._")
}

// safeName cleans an entry name to a slash path relative to the extraction root,
// rejecting anything absolute or traversing. It mirrors internal/cbz.safeEntryName
// but returns the cleaned path, since here the name becomes a real file on disk.
func safeName(name string) (string, bool) {
	name = filepath.ToSlash(name)
	if name == "" || path.IsAbs(name) || strings.HasPrefix(name, "/") {
		return "", false
	}
	// A Windows-authored archive can carry a drive letter or backslashes that
	// path.Clean would fold into one harmless-looking filename.
	if strings.Contains(name, `\`) || (len(name) > 1 && name[1] == ':') {
		return "", false
	}
	c := path.Clean(name)
	if c == ".." || strings.HasPrefix(c, "../") || c == "." {
		return "", false
	}
	return c, true
}
