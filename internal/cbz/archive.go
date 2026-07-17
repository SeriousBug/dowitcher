// Package cbz is the CBZ reading layer: filesystem and zip in, data out. It has
// no knowledge of the database or of HTTP, so the scanner, the import pipeline
// and the reader handlers can all share it without dragging in each other's
// dependencies.
package cbz

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/SeriousBug/dowitcher/internal/api"
)

var (
	// ErrNoPages means the zip opened fine but holds no images. Callers treat
	// this as a bad file rather than an empty comic.
	ErrNoPages = errors.New("cbz: archive contains no pages")
	// ErrPageRange is returned by Page for an index outside the page list.
	ErrPageRange = errors.New("cbz: page index out of range")
	// ErrUnsafeEntry marks a zip entry whose name escapes the archive root.
	ErrUnsafeEntry = errors.New("cbz: unsafe entry name")
)

// imageExts is the set of entry extensions we treat as pages. Anything else in
// the zip (readme.txt, ComicInfo.xml, thumbs.db) is not a page.
var imageExts = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".tiff": "image/tiff",
	".avif": "image/avif",
}

// Archive is an open CBZ. It holds the zip open for the lifetime of the reader
// session, so it must be closed.
type Archive struct {
	zr    *zip.ReadCloser
	path  string
	pages []*zip.File
	info  ComicInfo
}

// Open reads the zip's central directory and resolves the page list and
// ComicInfo.xml. Page bytes are not touched, so this stays cheap on the
// multi-gigabyte archives the library is full of.
func Open(p string) (*Archive, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return nil, fmt.Errorf("cbz: open %s: %w", p, err)
	}
	a := &Archive{zr: zr, path: p}
	for _, f := range zr.File {
		if isPage(f) {
			a.pages = append(a.pages, f)
		}
	}
	sort.Slice(a.pages, func(i, j int) bool {
		return natLess(a.pages[i].Name, a.pages[j].Name)
	})
	info, err := readComicInfo(zr.File)
	if err != nil {
		zr.Close()
		return nil, err
	}
	a.info = info
	return a, nil
}

func (a *Archive) Close() error { return a.zr.Close() }

// Path is the file Open was given.
func (a *Archive) Path() string { return a.path }

// PageCount is the number of image entries, which is authoritative over
// ComicInfo.xml's PageCount — that field is frequently stale or absent.
func (a *Archive) PageCount() int { return len(a.pages) }

// Info is the parsed ComicInfo.xml, or the zero value when the archive has none.
func (a *Archive) Info() ComicInfo { return a.info }

// Pages describes every page for the reader payload. Width and Height are read
// from image headers, not by decoding, and stay zero for any entry whose header
// we cannot parse — api.Page marks them omitempty for exactly that reason.
func (a *Archive) Pages() ([]api.Page, error) {
	if len(a.pages) == 0 {
		return nil, ErrNoPages
	}
	out := make([]api.Page, len(a.pages))
	for i, f := range a.pages {
		p := api.Page{Index: i, Name: f.Name}
		if w, h, err := entryDimensions(f); err == nil {
			p.Width, p.Height = w, h
		}
		out[i] = p
	}
	return out, nil
}

// PageNames lists page entry names in reading order.
func (a *Archive) PageNames() []string {
	out := make([]string, len(a.pages))
	for i, f := range a.pages {
		out[i] = f.Name
	}
	return out
}

// Page opens page i and reports its content type. The caller closes the reader.
func (a *Archive) Page(i int) (io.ReadCloser, string, error) {
	if i < 0 || i >= len(a.pages) {
		return nil, "", ErrPageRange
	}
	f := a.pages[i]
	if !safeEntryName(f.Name) {
		// We only ever read entries, so a traversing name cannot overwrite
		// anything here. It is still rejected rather than served: an entry
		// named "../../etc/passwd" means the archive is hostile or corrupt, and
		// the name is handed to callers (and on to clients) as api.Page.Name,
		// where a later consumer may well join it onto a path.
		return nil, "", fmt.Errorf("%w: %q", ErrUnsafeEntry, f.Name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, "", fmt.Errorf("cbz: open entry %s: %w", f.Name, err)
	}
	return rc, contentType(f.Name), nil
}

// Cover opens the page ComicInfo.xml marks Type="FrontCover", falling back to
// the first page. The marked page is addressed by its ComicInfo Image index,
// which counts the archive's images in the same order we do.
// The declared-size screen lives here rather than in Thumbnail because this is
// the last point that still holds the *zip.File: every cover generator goes
// Cover -> Thumbnail, and Thumbnail only ever sees an io.Reader, which cannot
// say what the central directory claimed. Refusing here costs no I/O and keeps
// Thumbnail's signature — and its callers — alone.
func (a *Archive) Cover() (io.ReadCloser, error) {
	i := 0
	if n, ok := a.info.frontCover(); ok && n >= 0 && n < len(a.pages) {
		i = n
	}
	if i < len(a.pages) {
		if err := checkEntrySize(a.pages[i]); err != nil {
			return nil, err
		}
	}
	rc, _, err := a.Page(i)
	return rc, err
}

func isPage(f *zip.File) bool {
	if f.FileInfo().IsDir() || strings.HasSuffix(f.Name, "/") {
		return false
	}
	if isJunk(f.Name) {
		return false
	}
	_, ok := imageExts[strings.ToLower(path.Ext(f.Name))]
	return ok
}

// isJunk filters the resource forks and metadata that macOS's Archive Utility
// stuffs into zips. They are real image entries by extension and would
// otherwise show up as duplicate, garbage pages throughout the book.
func isJunk(name string) bool {
	if strings.HasPrefix(name, "__MACOSX/") || strings.Contains(name, "/__MACOSX/") {
		return true
	}
	base := path.Base(name)
	return base == ".DS_Store" || strings.HasPrefix(base, "._")
}

func contentType(name string) string {
	if ct, ok := imageExts[strings.ToLower(path.Ext(name))]; ok {
		return ct
	}
	return "application/octet-stream"
}

// safeEntryName rejects absolute paths and any name that climbs out of the
// archive root once cleaned.
func safeEntryName(name string) bool {
	if name == "" || path.IsAbs(name) || strings.HasPrefix(name, "/") {
		return false
	}
	// Windows-authored zips can carry backslash separators and a drive letter;
	// path.Clean would treat the whole thing as one harmless filename.
	if strings.Contains(name, `\`) || (len(name) > 1 && name[1] == ':') {
		return false
	}
	c := path.Clean(name)
	return c != ".." && !strings.HasPrefix(c, "../")
}
