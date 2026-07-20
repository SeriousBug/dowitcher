package imports

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/cbz"
	"github.com/SeriousBug/dowitcher/internal/store"
)

// ErrNotCBZ means the uploaded file did not read as a zip.
var ErrNotCBZ = errors.New("imports: not a readable cbz")

// Adopt files a ready-made CBZ as a comic owned by ownerID and returns it as the
// uploader can see it.
//
// There is no pipeline and no job: the archive is already the artifact, so the
// only work is reading what it says about itself and moving it into place. That
// finishes in the time a stat and a rename take, which is why this is synchronous
// and reports through its error rather than over the WS like a real import.
//
// srcPath must still carry the name the uploader gave it. The metadata is read
// before the move for exactly that reason — ParseFilename is the only source of a
// series and number for the majority of archives, which carry no ComicInfo.xml,
// and the file is about to be renamed to an id that would parse to nothing.
//
// On success srcPath no longer exists. On failure it is left alone for the caller
// to clean up.
func (m *Manager) Adopt(ownerID, srcPath string, opts api.ImportOptions) (api.Comic, error) {
	a, err := cbz.Open(srcPath)
	if err != nil {
		// The zip reader's error names the server's temp path; the uploader gets
		// the sentinel and the handler's wording instead.
		return api.Comic{}, fmt.Errorf("%w: %v", ErrNotCBZ, err)
	}
	c := cbz.Comic(a)
	hash := a.Hash()
	pageCount := a.PageCount()
	a.Close()
	if pageCount == 0 {
		return api.Comic{}, ErrNoImages
	}
	if opts.Name != "" {
		// The name the uploader typed is the one thing they said out loud about
		// this file, so it beats both ComicInfo.xml and the filename.
		c.Title = opts.Name
	}

	info, err := os.Stat(srcPath)
	if err != nil {
		return api.Comic{}, err
	}

	// Same rule as the pipeline's output: an upload is addressed by id, never by
	// a name the user chose, because Path is unique and two people uploading the
	// same book would collide.
	comicID := store.NewID()
	outPath := filepath.Join(m.cfg.UploadsDir, comicID+".cbz")
	if err := moveFile(srcPath, outPath); err != nil {
		return api.Comic{}, err
	}
	row := comicRow(comicID, ownerID, outPath, c, hash, pageCount, info.Size())
	if err := m.fileComic(row, opts.CollectionID); err != nil {
		os.Remove(outPath)
		return api.Comic{}, err
	}
	return m.store.GetComic(ownerID, comicID)
}

// moveFile renames src onto dst, copying when the two are on different
// filesystems. ImportTempDir and UploadsDir are configured separately and a
// deployment that puts the scratch space on a different volume from the library
// is the normal one, so the cross-device rename failure is expected rather than
// exceptional.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	os.Remove(src)
	return nil
}

// IsCBZName reports whether a filename is one this package will adopt. .zip is
// accepted alongside .cbz for the same reason the library scanner accepts it: a
// CBZ is a zip, and plenty of them are named as one.
func IsCBZName(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".cbz", ".zip":
		return true
	}
	return false
}

// IsPDFName reports whether a filename is a PDF the import pipeline will unpack.
// Name-only, like IsCBZName: the extractor opens and parses the file before it
// believes any of this.
func IsPDFName(name string) bool {
	return strings.ToLower(filepath.Ext(name)) == ".pdf"
}
