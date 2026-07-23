package library

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/cbz"
	"github.com/SeriousBug/dowitcher/internal/store"
)

// Scan walks the library root and reconciles every file it finds against the
// store, then flags the rows whose files are gone. It is safe to call
// concurrently with the watcher; a second concurrent Scan gets ErrScanning.
//
// Cancellation is checked before every file rather than only at the top: a scan
// of a real library is minutes of work, and SIGTERM must not have to wait for
// it.
func (l *Library) Scan(ctx context.Context) error {
	if !l.scanning.TryLock() {
		return ErrScanning
	}
	defer l.scanning.Unlock()

	files, converts, err := l.walk(ctx)
	if err != nil {
		return err
	}
	// The walk's result doubles as the answer to "is this other path still on
	// disk?", which is what tells a rename apart from a copy. Consulting the
	// set rather than re-statting keeps that decision consistent for the whole
	// scan even if the filesystem moves underneath it.
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		seen[f] = true
	}
	onDisk := func(rel string) bool { return seen[rel] }

	l.setStatus(func(s *api.LibraryStatus) {
		s.Scanning = true
		s.Done = 0
		s.Total = len(files)
	}, true)

	sem := make(chan struct{}, l.cfg.Workers)
	var wg sync.WaitGroup
	for _, rel := range files {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(rel string) {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			// One unreadable file must not sink the scan: the rest of the
			// library is still worth indexing, and the sweep retries this one.
			if err := l.reconcileFile(ctx, rel, onDisk); err != nil {
				log.Printf("library: %s: %v", rel, err)
			}
			l.setStatus(func(s *api.LibraryStatus) { s.Done++ }, false)
		}(rel)
	}
	wg.Wait()

	if ctx.Err() != nil {
		l.finish()
		return ctx.Err()
	}
	if err := l.markMissing(seen); err != nil {
		l.finish()
		return err
	}
	// Convertibles (PDFs and non-zip archives) are handed off after the CBZ
	// reconcile pass, not woven into it: they are not comic rows, they are work for
	// the import queue, and the dedupe map keeps a re-walk from re-queuing one that
	// has not changed.
	for _, rel := range converts {
		if ctx.Err() != nil {
			break
		}
		l.handleConvert(rel)
	}
	l.finish()
	return nil
}

// finish returns the status to rest. It is deliberately reached on the
// cancellation and error paths too: a status stuck at Scanning:true forever is
// how a spinner outlives the work it describes.
func (l *Library) finish() {
	count := l.count()
	l.setStatus(func(s *api.LibraryStatus) {
		s.Scanning = false
		s.LastScan = time.Now().Unix()
		s.ComicCount = count
		s.Done, s.Total = 0, 0
	}, true)
}

// markMissing flags every library row whose file the walk did not find.
//
// Rows are never deleted here. An unmounted volume, a typo'd DOWITCHER_LIBRARY or
// a container started before its NFS mount is ready all look exactly like "the
// library is empty", and deleting on that reading would destroy every tag and
// every reading position on the server. The flag is cleared the moment the file
// comes back.
//
// The known-path set is read after the workers have finished so a row a rename
// repointed during this scan is looked up under its new path and is not mistaken
// for a file that vanished.
func (l *Library) markMissing(seen map[string]bool) error {
	known, err := l.st.ListComicPaths()
	if err != nil {
		return err
	}
	for p, id := range known {
		if seen[p] {
			continue
		}
		if err := l.st.SetComicMissing(id, true); err != nil {
			return err
		}
	}
	return nil
}

// walk lists the files under the root as slash-separated paths relative to it:
// the CBZ/ZIP candidates the scanner reconciles, and separately the convertibles
// (PDFs and non-zip archives) handed to the import queue.
func (l *Library) walk(ctx context.Context) (files, converts []string, err error) {
	err = filepath.WalkDir(l.cfg.Root, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			// One directory the process cannot read (a permissions mistake in a
			// mounted volume is common) must not blank the whole library, which
			// is what aborting here would do by way of markMissing.
			log.Printf("library: walk %s: %v", p, err)
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if p != l.cfg.Root && skipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(l.cfg.Root, p)
		if err != nil {
			return nil
		}
		switch {
		case isCandidate(d.Name()):
			files = append(files, filepath.ToSlash(rel))
		case isConvertible(d.Name()):
			converts = append(converts, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return files, converts, nil
}

// skipDir excludes directories that never hold a library comic: dotfile
// directories, and the incomplete-download staging that the usual downloaders
// leave lying around next to the finished files.
func skipDir(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch strings.ToLower(name) {
	case "@eadir", "#recycle", "lost+found", "__macosx":
		return true
	}
	return false
}

// isCandidate matches the files worth opening.
//
// .zip is accepted alongside .cbz because a CBZ *is* a zip and comics in the
// wild are routinely left under the generic extension — refusing them would
// silently ignore a large slice of a real library for a reason the user cannot
// see. Nothing is assumed from the extension either way: a candidate has to
// open as a zip and contain at least one image entry to become a comic, which
// costs a central-directory read and rejects the ordinary .zip of documents
// that happens to be sitting in the folder.
func isCandidate(name string) bool {
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "._") {
		return false
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".cbz", ".zip":
		return true
	}
	return false
}

// reconcileFile brings one file's row in line with the file itself, and makes
// sure its cover is cached. onDisk reports whether some other stored path still
// exists, which is how record tells a rename from a copy.
func (l *Library) reconcileFile(ctx context.Context, rel string, onDisk func(string) bool) error {
	abs := l.abs(rel)
	fi, err := os.Stat(abs)
	if err != nil {
		return err
	}
	a, err := cbz.Open(abs)
	if err != nil {
		return err
	}
	defer a.Close()
	if a.PageCount() == 0 {
		// A zip with no image entries is not a comic. This is the check that
		// makes accepting the .zip extension safe.
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	meta := cbz.Comic(a)
	row := store.ComicRow{
		Path:        rel,
		ContentHash: a.Hash(),
		Title:       meta.Title,
		Series:      meta.Series,
		Number:      meta.Number,
		Volume:      meta.Volume,
		Summary:     meta.Summary,
		PageCount:   meta.PageCount,
		FileSize:    fi.Size(),
		ModifiedAt:  fi.ModTime().Unix(),
		Source:      store.SourceLibrary,
	}
	if _, err := l.record(row, onDisk); err != nil {
		return err
	}
	// A cover that will not generate is a cosmetic problem; the comic is in the
	// library and readable either way, so it must not fail the row.
	if err := l.ensureCover(ctx, a, row.ContentHash); err != nil && ctx.Err() == nil {
		log.Printf("library: cover %s: %v", rel, err)
	}
	return nil
}

// record writes one comic, resolving which of the four things that can have
// happened to it this is. Identity is the path first and the content hash
// second, and the order matters: the path is what the user thinks identifies a
// comic, and the hash is what survives them disagreeing with the filesystem
// about it.
//
//   - New path, unknown hash: a new comic. Insert.
//   - Known path, changed hash: the file was replaced in place (a better scan,
//     a recompress). Update the metadata on the existing row, which keeps its
//     id and therefore its tags and everyone's reading position.
//   - New path, known hash, old path gone: a rename or a move, which is the
//     case the content hash exists for. Repoint the row instead of inserting a
//     duplicate, and a reorganisation of the whole library costs nobody their
//     tags or their place.
//   - New path, known hash, old path still there: a copy, not a move. Insert.
//
// The fourth bullet is why onDisk is a parameter. Without it every duplicated
// file would drag the original's row along behind it.
func (l *Library) record(row store.ComicRow, onDisk func(string) bool) (string, error) {
	l.dbMu.Lock()
	defer l.dbMu.Unlock()

	existing, err := l.st.ComicRowByPath(row.Path)
	switch {
	case err == nil:
		if existing.Source == store.SourceUpload {
			// An uploaded comic that happens to live under the root belongs to
			// the import pipeline, which owns its row and its ownership fields.
			// Two writers on one row would just take turns undoing each other.
			return existing.ID, nil
		}
		// A claimed comic falls through: its file is under the root and this
		// scanner is still its only writer, so it gets the same metadata refresh
		// as any library comic. The upsert never writes owner_id or source, so
		// refreshing it cannot un-claim it.
		// Known path. The hash may or may not have changed; either way the
		// upsert conflicts on path and updates in place, so the id survives and
		// tags and progress stay attached.
		row.ID = existing.ID
		row.AddedAt = existing.AddedAt
		return row.ID, l.st.UpsertComic(row)
	case !errors.Is(err, store.ErrNotFound):
		return "", err
	}

	moved, err := l.st.ComicRowByHash(row.ContentHash)
	switch {
	// Claimed rows are eligible to be repointed for the same reason they are
	// refreshed above, and for a sharper one: inserting a fresh row for a
	// renamed claimed file would publish it to the whole server as a new
	// library comic while the claimed row lingered, so a rename would silently
	// undo the claim.
	case err == nil && moved.Source != store.SourceUpload && !onDisk(moved.Path):
		if err := l.st.MovedComic(moved.ID, row.Path, row.ModifiedAt); err != nil {
			return "", err
		}
		// The move repointed the row; the upsert then refreshes the metadata,
		// which matters because most of it is derived from the filename and the
		// filename is exactly what just changed.
		row.ID = moved.ID
		row.AddedAt = moved.AddedAt
		return row.ID, l.st.UpsertComic(row)
	case err != nil && !errors.Is(err, store.ErrNotFound):
		return "", err
	}

	row.ID = store.NewID()
	row.AddedAt = time.Now().Unix()
	return row.ID, l.st.UpsertComic(row)
}
