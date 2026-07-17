package library

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/store"
	"github.com/fsnotify/fsnotify"
)

// Watch reacts to files appearing, changing and disappearing under the root. It
// blocks until ctx is cancelled.
//
// fsnotify does not recurse: a watch is on one directory and says nothing about
// its children. So every directory under the root is added individually here,
// and the event stream is also how the watch set is maintained — a directory
// created later gets a watch when its Create event arrives, and a directory
// removed loses it. Without that, everything dropped into a folder made after
// startup would be invisible until the next sweep.
func (l *Library) Watch(ctx context.Context) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()

	if err := l.addTree(w, l.cfg.Root); err != nil {
		return err
	}

	d := &debouncer{quiet: l.cfg.Quiet, pending: map[string]time.Time{}}

	// The event reader and the dispatcher are separate goroutines on purpose.
	// Handling a file means waiting out the settle gap and then decoding a
	// cover, which is seconds; doing that on the event goroutine would let
	// fsnotify's kernel buffer overflow and drop the events for everything else
	// being copied in at the same time.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		l.dispatch(ctx, d)
	}()

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil
		case err, ok := <-w.Errors:
			if !ok {
				wg.Wait()
				return nil
			}
			log.Printf("library: watcher: %v", err)
		case ev, ok := <-w.Events:
			if !ok {
				wg.Wait()
				return nil
			}
			l.handleEvent(w, d, ev)
		}
	}
}

// handleEvent turns one fsnotify event into a watch-set change, a debounce
// arming, or nothing.
func (l *Library) handleEvent(w *fsnotify.Watcher, d *debouncer, ev fsnotify.Event) {
	rel, err := filepath.Rel(l.cfg.Root, ev.Name)
	if err != nil {
		return
	}
	rel = filepath.ToSlash(rel)

	if ev.Has(fsnotify.Create) {
		if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
			if skipDir(filepath.Base(ev.Name)) {
				return
			}
			// Files can land in a new directory before this watch is added --
			// `mv` of a full folder into the root is one event for the folder
			// and none for its contents. Walking it now both adds the watches
			// and arms everything already inside.
			if err := l.addTree(w, ev.Name); err != nil {
				log.Printf("library: watch %s: %v", rel, err)
			}
			l.armTree(d, ev.Name)
			return
		}
	}
	if ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename) {
		// fsnotify drops the watch for a removed directory on its own, and the
		// path may have been either a file or a directory -- it is gone, so
		// there is nothing left to stat and ask. Remove is idempotent and errors
		// harmlessly for a path that was never watched, which is the common case
		// here since most removals are files.
		_ = w.Remove(ev.Name)
	}
	if !isCandidate(filepath.Base(ev.Name)) {
		return
	}
	// Every remaining event kind -- create, write, remove, rename, chmod --
	// means the same thing: this path is not to be trusted right now. What
	// actually happened to it is decided at dispatch time by looking at the
	// file, not by reading the event bits, because the events for a single scp
	// are a stream whose individual members mean nothing.
	d.arm(rel)
}

// addTree watches dir and every directory beneath it.
func (l *Library) addTree(w *fsnotify.Watcher, dir string) error {
	return filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			// A directory that vanished mid-walk, or one that cannot be read,
			// is not a reason to give up the rest of the tree.
			return nil
		}
		if !e.IsDir() {
			return nil
		}
		if p != dir && skipDir(e.Name()) {
			return fs.SkipDir
		}
		if err := w.Add(p); err != nil {
			log.Printf("library: watch %s: %v", p, err)
		}
		return nil
	})
}

// armTree debounces every candidate file already sitting under dir.
func (l *Library) armTree(d *debouncer, dir string) {
	filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.IsDir() {
			if p != dir && skipDir(e.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !isCandidate(e.Name()) {
			return nil
		}
		if rel, err := filepath.Rel(l.cfg.Root, p); err == nil {
			d.arm(filepath.ToSlash(rel))
		}
		return nil
	})
}

// dispatch drains debounced paths and handles them one at a time.
//
// Serial handling is the point: it is what guarantees a path can never be
// reconciled twice at once, which the event stream would otherwise make easy
// (a write arriving while the previous version of the same file is being
// hashed). Events keep accumulating in the debouncer meanwhile, so nothing is
// lost while a file is being read, and the throughput that costs is irrelevant
// for a watcher whose whole job is a trickle of new files.
func (l *Library) dispatch(ctx context.Context, d *debouncer) {
	// The poll is a fraction of the quiet period so a path is picked up
	// promptly after it goes quiet rather than up to a full period later.
	tick := l.cfg.Quiet / 4
	if tick <= 0 {
		tick = time.Millisecond
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, rel := range d.due(time.Now()) {
			if ctx.Err() != nil {
				return
			}
			l.handlePath(ctx, d, rel)
		}
	}
}

// handlePath reconciles one debounced path.
func (l *Library) handlePath(ctx context.Context, d *debouncer, rel string) {
	abs := l.abs(rel)
	fi, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			l.gone(rel)
			return
		}
		log.Printf("library: %s: %v", rel, err)
		return
	}
	if !l.stable(ctx, abs, fi.Size()) {
		// Still being written. Re-arming rather than waiting in a loop here
		// keeps the dispatcher free for other files and gives this one another
		// full quiet period, which is what a slow link over SSH needs.
		d.arm(rel)
		return
	}
	// os.Stat is the honest answer to "is the old path still there?" for a
	// single event, where there is no scan-wide set to consult.
	onDisk := func(p string) bool {
		_, err := os.Stat(l.abs(p))
		return err == nil
	}
	if err := l.reconcileFile(ctx, rel, onDisk); err != nil {
		if ctx.Err() != nil {
			return
		}
		// A file that is still not readable is left for the sweep. This is the
		// backstop for a copy whose size held steady across the settle gap --
		// a stalled transfer, or a filesystem that buffers in bursts.
		log.Printf("library: %s: %v", rel, err)
		return
	}
	l.setStatus(func(s *api.LibraryStatus) { s.ComicCount = l.count() }, true)
}

// gone flags a vanished file's row missing.
func (l *Library) gone(rel string) {
	if !l.flagMissing(rel) {
		return
	}
	l.setStatus(func(s *api.LibraryStatus) { s.ComicCount = l.count() }, true)
}

// flagMissing marks rel's row missing, reporting whether anything changed. It
// is split out so the status push happens after dbMu is released rather than
// holding the scanner's write lock across it.
func (l *Library) flagMissing(rel string) bool {
	l.dbMu.Lock()
	defer l.dbMu.Unlock()
	row, err := l.st.ComicRowByPath(rel)
	if err != nil {
		return false
	}
	if row.Source != store.SourceLibrary {
		return false
	}
	// A rename shows up as a remove of the old path and a create of the new
	// one, in either order. Flagging missing here is safe whichever order they
	// arrive in: if the create is handled second it matches this row by content
	// hash and repoints it, clearing the flag on the way past.
	if err := l.st.SetComicMissing(row.ID, true); err != nil {
		log.Printf("library: %s: %v", rel, err)
		return false
	}
	return true
}

// stable reports whether a file has stopped growing, by looking again after the
// settle gap.
//
// The quiet period has already elapsed with no events for this path, so this is
// belt and braces -- but the braces earn their place. fsnotify's events are the
// kernel's opinion, and on an NFS or SMB mount, or under a queue that
// overflowed, that opinion is incomplete; a size that is still climbing is the
// file itself saying so. Reading a CBZ that is still being copied is the most
// likely way this package can produce a wrong answer, because a truncated
// archive that happens to parse imports as a comic with half its pages.
//
// A truncated zip usually fails to open outright -- the central directory lives
// at the end of the file, so there is nothing to read until the last byte
// lands. That is the real protection. This check is what keeps it from being
// the only one.
func (l *Library) stable(ctx context.Context, abs string, size int64) bool {
	t := time.NewTimer(l.cfg.Settle)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return false
	}
	return fi.Size() == size
}

// debouncer coalesces the event storm for a path into one deadline.
//
// A large CBZ copied in over SSH is a long stream of writes, and the file is
// not a readable zip until the last one lands. Reacting per event would mean
// hundreds of failed opens per file and, at the end of it, a real chance of
// catching the file at a moment where it parses but is not complete. So an
// event does not schedule work; it pushes a deadline out. The path is handled
// once, quiet after the copy has finished.
type debouncer struct {
	quiet time.Duration

	mu      sync.Mutex
	pending map[string]time.Time
}

// arm (re)starts the quiet period for a path.
func (d *debouncer) arm(rel string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pending[rel] = time.Now().Add(d.quiet)
}

// due removes and returns every path whose quiet period has elapsed.
func (d *debouncer) due(now time.Time) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for rel, deadline := range d.pending {
		if now.Before(deadline) {
			continue
		}
		out = append(out, rel)
		delete(d.pending, rel)
	}
	return out
}
