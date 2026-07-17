package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SeriousBug/longbox/internal/store"
)

// TestDebounceCoalescesEventStorm is the unit form of the problem the debouncer
// exists for: a single large file copied in over SSH is a long stream of write
// events, and reacting to each one means reading a file that is not finished.
func TestDebounceCoalescesEventStorm(t *testing.T) {
	d := &debouncer{quiet: 50 * time.Millisecond, pending: map[string]time.Time{}}

	start := time.Now()
	// The storm: one path, hundreds of events, spread over more than one quiet
	// period so a naive implementation would fire repeatedly mid-copy.
	for range 200 {
		d.arm("big.cbz")
		if due := d.due(time.Now()); len(due) != 0 {
			t.Fatalf("fired at %v, mid-storm: the file is still being written", time.Since(start))
		}
		time.Sleep(time.Millisecond)
	}

	// Nothing yet: the last event was moments ago.
	if due := d.due(time.Now()); len(due) != 0 {
		t.Fatalf("fired immediately after the last event, want a full quiet period first")
	}
	time.Sleep(60 * time.Millisecond)

	due := d.due(time.Now())
	if len(due) != 1 || due[0] != "big.cbz" {
		t.Fatalf("after the quiet period: due = %v, want exactly [big.cbz]", due)
	}
	// Firing is consuming: the storm collapses to one unit of work, not 200.
	if due := d.due(time.Now()); len(due) != 0 {
		t.Fatalf("path fired twice for one storm: %v", due)
	}
}

func TestDebounceKeepsPathsIndependent(t *testing.T) {
	d := &debouncer{quiet: 40 * time.Millisecond, pending: map[string]time.Time{}}
	d.arm("done.cbz")
	time.Sleep(50 * time.Millisecond)
	// still.cbz is armed late, so it must not come due alongside done.cbz.
	d.arm("still.cbz")

	due := d.due(time.Now())
	if len(due) != 1 || due[0] != "done.cbz" {
		t.Fatalf("due = %v, want only [done.cbz]", due)
	}
}

// TestWatcherWaitsOutASlowCopy is the real-world case the user asked for: a
// file dropped in over SSH, arriving in chunks. Nothing may be read until the
// copy is finished, and the whole storm must cost exactly one reconcile.
func TestWatcherWaitsOutASlowCopy(t *testing.T) {
	l, st, root, rec := newTestLib(t)
	l.cfg.Quiet = 150 * time.Millisecond
	l.cfg.Settle = 40 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := l.Watch(ctx); err != nil {
			t.Errorf("watch: %v", err)
		}
	}()
	waitForWatch(t)

	full := cbzBytes(t, 4, 25)
	p := filepath.Join(root, "slow.cbz")
	f, err := os.Create(p)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Dribble the file in, the way scp does.
	chunk := len(full)/20 + 1
	for off := 0; off < len(full); off += chunk {
		end := min(off+chunk, len(full))
		if _, err := f.Write(full[off:end]); err != nil {
			t.Fatalf("write chunk: %v", err)
		}
		f.Sync()
		time.Sleep(15 * time.Millisecond)
		// While the copy is in flight the comic must not exist: a CBZ read
		// mid-copy is a truncated archive.
		if _, err := st.ComicRowByPath("slow.cbz"); err == nil {
			t.Fatalf("comic imported while it was still being copied (%d/%d bytes written)", end, len(full))
		}
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	row := waitForComic(t, l, "slow.cbz")
	if row.PageCount != 4 {
		t.Errorf("page count = %d, want 4 — a truncated archive was imported", row.PageCount)
	}
	if row.FileSize != int64(len(full)) {
		t.Errorf("file size = %d, want %d", row.FileSize, len(full))
	}

	cancel()
	<-done

	// The storm must have produced one reconcile, not one per write. Each
	// successful handlePath forces a status push, so the pushes count them.
	var pushes int
	for _, s := range rec.all() {
		if !s.Scanning {
			pushes++
		}
	}
	if pushes != 1 {
		t.Errorf("%d status pushes for one file: the watcher reacted per event rather than once, quiet", pushes)
	}
}

func TestWatcherPicksUpNewDirectories(t *testing.T) {
	l, _, root, _ := newTestLib(t)
	l.cfg.Quiet = 100 * time.Millisecond
	l.cfg.Settle = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Watch(ctx)
	waitForWatch(t)

	// fsnotify does not recurse, so a comic in a directory created after the
	// watch started only arrives if the watcher added the new directory itself.
	writeCBZ(t, root, "New Series/New Series 01.cbz", 2, 80)
	row := waitForComic(t, l, "New Series/New Series 01.cbz")
	if row.Series != "New Series" {
		t.Errorf("series = %q, want %q", row.Series, "New Series")
	}
}

func TestWatcherFlagsRemovedFileMissing(t *testing.T) {
	l, st, root, _ := newTestLib(t)
	l.cfg.Quiet = 100 * time.Millisecond
	l.cfg.Settle = 20 * time.Millisecond
	writeCBZ(t, root, "here.cbz", 2, 15)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	before := comicAt(t, st, "here.cbz")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Watch(ctx)
	waitForWatch(t)

	if err := os.Remove(filepath.Join(root, "here.cbz")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	deadline := time.Now().Add(4 * time.Second)
	for {
		row, err := st.ComicRowByPath("here.cbz")
		if err == nil && row.Missing {
			if row.ID != before.ID {
				t.Fatalf("id changed: %q -> %q", before.ID, row.ID)
			}
			return
		}
		if err != nil {
			t.Fatalf("row deleted rather than flagged missing: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("removed file never flagged missing")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestWatcherRenameKeepsOneRow proves the four-case reconciliation holds on the
// event path too, where a rename arrives as a remove and a create in whichever
// order the kernel felt like.
func TestWatcherRenameKeepsOneRow(t *testing.T) {
	l, st, root, _ := newTestLib(t)
	u := mustUser(t, st)
	l.cfg.Quiet = 100 * time.Millisecond
	l.cfg.Settle = 20 * time.Millisecond
	writeCBZ(t, root, "before.cbz", 3, 77)
	if err := l.Scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	before := comicAt(t, st, "before.cbz")
	if _, err := st.SetProgress(u.ID, before.ID, 2, false); err != nil {
		t.Fatalf("set progress: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.Watch(ctx)
	waitForWatch(t)

	if err := os.Rename(filepath.Join(root, "before.cbz"), filepath.Join(root, "after.cbz")); err != nil {
		t.Fatalf("rename: %v", err)
	}

	row := waitForComic(t, l, "after.cbz")
	if row.ID != before.ID {
		t.Fatalf("id changed across a watched rename: %q -> %q", before.ID, row.ID)
	}
	if row.Missing {
		t.Error("renamed comic left flagged missing")
	}
	paths, err := st.ListComicPaths()
	if err != nil {
		t.Fatalf("list paths: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("watched rename produced %d rows, want 1: %v", len(paths), paths)
	}
	p, err := st.GetProgress(u.ID, row.ID)
	if err != nil || p.Page != 2 {
		t.Errorf("progress lost across a watched rename: %+v %v", p, err)
	}
}

func TestSweepRescansOnItsInterval(t *testing.T) {
	l, _, root, _ := newTestLib(t)
	// A file that appears with no event to announce it — a dropped kernel
	// event, or an NFS mount that delivers none at all.
	l.cfg.SweepInterval = 80 * time.Millisecond
	writeCBZ(t, root, "silent.cbz", 2, 12)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		l.Sweep(ctx)
	}()

	waitForComic(t, l, "silent.cbz")
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sweep did not stop on cancellation")
	}
}

// waitForWatch gives the watcher a moment to register its inotify watches
// before the test starts making changes it expects to be seen.
func waitForWatch(t *testing.T) {
	t.Helper()
	time.Sleep(150 * time.Millisecond)
}

// waitForComic polls for a comic to land, since the watcher is asynchronous by
// design.
func waitForComic(t *testing.T, l *Library, rel string) store.ComicRow {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, err := l.st.ComicRowByPath(rel)
		if err == nil && !r.Missing {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("comic %q never appeared", rel)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
