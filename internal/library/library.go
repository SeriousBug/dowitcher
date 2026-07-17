// Package library keeps the store in line with the CBZ files sitting under the
// library root. The filesystem is the source of truth and the user is free to
// add, replace, rename and remove files at any time, including while the server
// is not running, so nothing here trusts what the database already says: every
// path is re-derived from the file it names.
//
// Three things drive that reconciliation, in descending order of how much they
// can be relied on. The startup scan populates a fresh instance. The fsnotify
// watcher reacts to a file appearing within a couple of seconds of the copy
// finishing. The periodic sweep re-walks the whole root and is the backstop for
// everything the watcher cannot see: dropped kernel events, an NFS or SMB mount
// that delivers none at all, and whatever changed while the process was down.
package library

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/store"
)

// ErrScanning is returned by Scan when a scan is already in flight. Two scans
// over the same root would race each other's writes and double the IO to reach
// the same answer, so the second caller is told no rather than queued.
var ErrScanning = errors.New("library: scan already in progress")

// ErrNoCover means a comic has no cached cover and none could be produced.
var ErrNoCover = errors.New("library: no cover")

// CoverContentType is what Cover's bytes always are. Thumbnails are normalised
// to JPEG on the way into the cache so the handler never has to sniff.
const CoverContentType = "image/jpeg"

// StatusFunc receives a status snapshot whenever the scanner's progress
// changes.
//
// It is a callback rather than a direct call onto the WS hub because
// internal/server already depends on this package through its Library
// interface; reaching back for the hub would close an import cycle. main wires
// this to hub.Broadcast.
type StatusFunc func(api.LibraryStatus)

// Config is the scanner's tuning. Every zero value is replaced with the default
// in New, so a caller that only sets Root and DataDir gets sensible behaviour.
type Config struct {
	// Root is the watched library directory (DOWITCHER_LIBRARY). Comic paths are
	// stored relative to it.
	Root string
	// DataDir is where the cover cache lives (DOWITCHER_DATA).
	DataDir string
	// SweepInterval is how often the periodic sweep re-walks the root.
	SweepInterval time.Duration
	// Quiet is how long a path must go without a filesystem event before the
	// watcher will touch it. See watch.go — this is what stops a half-copied
	// CBZ from being read.
	Quiet time.Duration
	// Settle is the gap between the two size checks that confirm a file has
	// stopped growing.
	Settle time.Duration
	// Workers bounds the scan pool.
	Workers int
	// CoverWidth is the cover thumbnail's long edge in pixels.
	CoverWidth int
}

const (
	defaultSweepInterval = 15 * time.Minute
	defaultQuiet         = 2 * time.Second
	defaultSettle        = 500 * time.Millisecond
	defaultCoverWidth    = 400
	// publishEvery throttles status pushes. A scan of a large library advances
	// its counter thousands of times, and every advance would otherwise be a
	// JSON marshal plus a broadcast to every client for a progress bar nobody
	// can read at that rate.
	publishEvery = 250 * time.Millisecond
)

// Library scans and watches the library root. The zero value is not usable; use
// New.
type Library struct {
	st       *store.Store
	cfg      Config
	onStatus StatusFunc

	mu     sync.Mutex
	status api.LibraryStatus

	// dbMu serialises the read-then-write in record. Matching a path, falling
	// back to a hash and then writing is not atomic in SQL, and two scan
	// workers landing on it at once could both decide the same row is theirs to
	// move. The expensive part of a scan (opening, hashing, thumbnailing) is
	// outside this lock, so holding it costs nothing worth measuring.
	dbMu sync.Mutex

	// scanning is held for the duration of a scan. TryLock rather than Lock:
	// see ErrScanning.
	scanning sync.Mutex

	pubMu   sync.Mutex
	lastPub time.Time
}

// New builds a Library. onStatus may be nil, which is what a test that does not
// care about progress passes.
func New(st *store.Store, cfg Config, onStatus StatusFunc) *Library {
	if cfg.SweepInterval <= 0 {
		cfg.SweepInterval = defaultSweepInterval
	}
	if cfg.Quiet <= 0 {
		cfg.Quiet = defaultQuiet
	}
	if cfg.Settle <= 0 {
		cfg.Settle = defaultSettle
	}
	if cfg.CoverWidth <= 0 {
		cfg.CoverWidth = defaultCoverWidth
	}
	if cfg.Workers <= 0 {
		// Sized off the CPU count rather than higher: a scan's cost is
		// dominated by cover thumbnailing, and a full AVIF decode runs ~5x
		// slower under the WASM decoder than native. Oversubscribing would only
		// add scheduling overhead to work that is already CPU-saturated.
		cfg.Workers = runtime.NumCPU()
	}
	l := &Library{st: st, cfg: cfg, onStatus: onStatus}
	l.status.Root = cfg.Root
	l.status.ComicCount = l.count()
	return l
}

// Status is what the scanner is doing right now.
func (l *Library) Status() api.LibraryStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.status
}

// Run drives the watcher and the periodic sweep until ctx is cancelled. It does
// not scan first: main runs Scan explicitly before it starts serving, so a
// fresh instance answers its first request with a populated library.
func (l *Library) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := l.Watch(ctx); err != nil && ctx.Err() == nil {
			// A watcher that cannot start is not fatal: the sweep still finds
			// everything, just up to SweepInterval later than it would have.
			log.Printf("library: watch: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		l.Sweep(ctx)
	}()
	wg.Wait()
}

// Sweep re-walks the root on an interval to catch what the watcher missed.
//
// The timer is rebuilt each iteration rather than being a ticker fixed at
// construction so the interval is re-read every loop, and so a scan that
// overruns the interval does not immediately queue another behind itself.
func (l *Library) Sweep(ctx context.Context) {
	for {
		t := time.NewTimer(l.cfg.SweepInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		// ErrScanning is expected here and not worth a line in the log: a
		// manual scan is doing this sweep's job already.
		if err := l.Scan(ctx); err != nil && !errors.Is(err, ErrScanning) && ctx.Err() == nil {
			log.Printf("library: sweep: %v", err)
		}
	}
}

// abs resolves a stored (slash-separated, root-relative) path to a real one.
func (l *Library) abs(rel string) string {
	return filepath.Join(l.cfg.Root, filepath.FromSlash(rel))
}

// count is the number of library comics whose file is present. A failure here
// only makes the status card wrong, so it reports zero rather than propagating.
func (l *Library) count() int {
	n, err := l.st.CountLibraryComics()
	if err != nil {
		log.Printf("library: count comics: %v", err)
		return 0
	}
	return n
}

// setStatus mutates the status under the lock and publishes the result.
func (l *Library) setStatus(f func(s *api.LibraryStatus), force bool) {
	l.mu.Lock()
	f(&l.status)
	snap := l.status
	l.mu.Unlock()
	l.publish(snap, force)
}

// publish pushes a snapshot to the callback, at most once per publishEvery
// unless force says this transition matters (a scan starting or finishing) and
// must not be swallowed by the throttle.
func (l *Library) publish(s api.LibraryStatus, force bool) {
	if l.onStatus == nil {
		return
	}
	l.pubMu.Lock()
	if !force && time.Since(l.lastPub) < publishEvery {
		l.pubMu.Unlock()
		return
	}
	l.lastPub = time.Now()
	l.pubMu.Unlock()
	l.onStatus(s)
}
