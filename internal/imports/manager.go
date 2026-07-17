package imports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SeriousBug/longbox/internal/api"
	"github.com/SeriousBug/longbox/internal/cbz"
	"github.com/SeriousBug/longbox/internal/store"
)

// ErrNotRunning means a cancel arrived for a job that no goroutine is working
// on: it already finished, or it died with a previous process.
var ErrNotRunning = errors.New("imports: job is not running")

// Broadcaster is the WS fan-out a job reports to. It is an interface so this
// package does not depend on the server package — the dependency runs the other
// way, and main wires the hub in.
type Broadcaster interface {
	Broadcast(api.WSMessage)
}

const (
	// progressInterval throttles job messages. The pipeline calls back per file,
	// which on a thousand-image folder is a thousand frames the hub would fan out
	// to every client — and a client that cannot keep up gets its frames dropped
	// anyway. Four updates a second is more than a progress bar can show.
	progressInterval = 250 * time.Millisecond
	// snapshotLimit bounds the job set the WS carries. The snapshot is the whole
	// set rather than a delta, so it is sent in full on every connect; a user who
	// has run five hundred imports does not need all of them to clear a spinner.
	snapshotLimit = 20
)

// ManagerConfig locates what a job produces.
type ManagerConfig struct {
	// UploadsDir is where a finished CBZ is filed. A comic row's Path is
	// relative to it.
	UploadsDir string
	// ReportDir holds one dupe report per job. The report is a file rather than
	// a column because it is a page of JSON that only one screen ever reads, and
	// it must outlive the process that produced it.
	ReportDir string
}

// Manager turns the pure pipeline into running jobs: it owns their goroutines,
// their progress reporting, and the rows that say what happened.
type Manager struct {
	store *store.Store
	hub   Broadcaster
	cfg   ManagerConfig

	mu sync.Mutex
	// running holds the jobs this process is working on. It is the live view;
	// the DB row is the durable one, and JobSnapshot layers this over that.
	running map[string]*liveJob
}

type liveJob struct {
	snap     api.ImportJob
	cancel   context.CancelFunc
	lastEmit time.Time
}

// NewManager prepares the directories and recovers orphaned jobs.
func NewManager(st *store.Store, hub Broadcaster, cfg ManagerConfig) (*Manager, error) {
	if cfg.UploadsDir == "" {
		return nil, errors.New("imports: no uploads dir configured")
	}
	for _, d := range []string{cfg.UploadsDir, cfg.ReportDir} {
		if d == "" {
			continue
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("imports: %w", err)
		}
	}
	m := &Manager{store: st, hub: hub, cfg: cfg, running: map[string]*liveJob{}}
	if err := m.recover(); err != nil {
		return nil, err
	}
	return m, nil
}

// recover fails the jobs a crash left behind.
//
// A row that still says "running" after a restart is a lie: the goroutine that
// would have finished it died with the process, and nothing will ever move it.
// Left alone it is a spinner that never stops. Marking it failed at startup, the
// one moment we know for certain that nothing is running, is the only place this
// can be got right.
func (m *Manager) recover() error {
	jobs, err := m.store.ListUnfinishedImportJobs()
	if err != nil {
		return fmt.Errorf("imports: recover jobs: %w", err)
	}
	now := time.Now().Unix()
	for _, j := range jobs {
		j.Stage = api.StageFailed
		j.Message = "interrupted by a server restart"
		j.FinishedAt = now
		if err := m.store.SaveImportJob(j); err != nil {
			return fmt.Errorf("imports: recover job %s: %w", j.ID, err)
		}
		log.Printf("import %s was interrupted by a restart, marked failed", j.ID)
	}
	return nil
}

// Begin registers a job before its bytes arrive, so an upload that takes minutes
// shows up as an import immediately instead of appearing only once it lands.
func (m *Manager) Begin(userID string) (api.ImportJob, error) {
	j := api.ImportJob{
		ID:        store.NewID(),
		OwnerID:   userID,
		Stage:     api.StageUploading,
		StartedAt: time.Now().Unix(),
	}
	if err := m.store.SaveImportJob(j); err != nil {
		return api.ImportJob{}, err
	}
	m.mu.Lock()
	m.running[j.ID] = &liveJob{snap: j}
	m.mu.Unlock()
	m.broadcast(j)
	return j, nil
}

// Uploaded reports how many files have arrived. Total stays unknown until the
// upload ends, so this drives a count rather than a bar.
func (m *Manager) Uploaded(jobID string, files int) {
	m.tick(jobID, func(j *api.ImportJob) bool {
		j.Done = files
		return false
	})
}

// Start hands a fully uploaded folder to the pipeline and returns at once. ctx
// is the detached request context, so the import outlives the request.
func (m *Manager) Start(ctx context.Context, jobID, srcDir string, opts api.ImportOptions) error {
	if opts.Name == "" {
		opts.Name = uploadTitle(srcDir)
	}
	runCtx, cancel := context.WithCancel(ctx)

	m.mu.Lock()
	j, ok := m.running[jobID]
	if !ok {
		m.mu.Unlock()
		cancel()
		return store.ErrNotFound
	}
	j.cancel = cancel
	j.snap.Name = opts.Name
	j.snap.Stage = api.StageReading
	j.snap.Done, j.snap.Total = 0, 0
	snap := j.snap
	m.mu.Unlock()

	m.save(snap)
	m.broadcast(snap)
	go m.run(runCtx, snap, srcDir, opts)
	return nil
}

// Fail marks a job that died before the pipeline ever got it. Without it a
// broken upload leaves a job stuck in "uploading" until the next restart sweeps
// it.
func (m *Manager) Fail(jobID, msg string) {
	m.finish(jobID, func(j *api.ImportJob) {
		j.Stage = api.StageFailed
		j.Message = msg
	})
}

// Cancel stops a running job on behalf of its owner. The store's ownership check
// runs first, so a job that is not yours is not found rather than forbidden.
func (m *Manager) Cancel(userID, jobID string) error {
	if _, err := m.store.GetImportJob(userID, jobID); err != nil {
		return err
	}
	m.mu.Lock()
	j, ok := m.running[jobID]
	var cancel context.CancelFunc
	if ok {
		cancel = j.cancel
	}
	m.mu.Unlock()
	if cancel == nil {
		return ErrNotRunning
	}
	// The pipeline checks ctx between files and stages, so the goroutine winds
	// itself up and reports the failure; there is nothing to wait for here.
	cancel()
	return nil
}

// Dupes returns a finished job's merge report.
func (m *Manager) Dupes(userID, jobID string) ([]api.DupeGroup, error) {
	if _, err := m.store.GetImportJob(userID, jobID); err != nil {
		return nil, err
	}
	p := m.reportPath(jobID)
	if p == "" {
		return nil, store.ErrNotFound
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			// A job that failed, or is still running, has no report yet. That is
			// a miss, not a broken server.
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	var out []api.DupeGroup
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []api.DupeGroup{}
	}
	return out, nil
}

// JobSnapshot is the complete job set the hub tells userID about: the persisted
// history, capped, with this process's live state laid over it. The overlay
// matters because a running job's row is only rewritten on a stage change, so
// the DB alone would report a stale position.
func (m *Manager) JobSnapshot(userID string) []api.ImportJob {
	rows, err := m.store.ListImportJobs(userID)
	if err != nil {
		log.Printf("import job snapshot: %v", err)
		return []api.ImportJob{}
	}
	if len(rows) > snapshotLimit {
		rows = rows[:snapshotLimit]
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, r := range rows {
		if j, ok := m.running[r.ID]; ok {
			rows[i] = j.snap
		}
	}
	return rows
}

// run is the job goroutine: pipeline, then file the result.
func (m *Manager) run(ctx context.Context, job api.ImportJob, srcDir string, opts api.ImportOptions) {
	// The uploaded images have served their purpose either way; the CBZ is the
	// artifact worth keeping.
	defer os.RemoveAll(srcDir)

	// The comic's id is minted before the CBZ is written so it can name the file:
	// an upload is addressed by id, never by a name the user chose, because a
	// name would collide with the unique path index the moment two people import
	// the same folder.
	comicID := store.NewID()
	outPath := filepath.Join(m.cfg.UploadsDir, comicID+".cbz")

	res, err := Run(ctx, srcDir, outPath, opts, m.progress(job.ID))
	if err != nil {
		log.Printf("import %s: %v", job.ID, err)
		m.Fail(job.ID, failMessage(err))
		return
	}
	if err := m.file(job, comicID, outPath, opts, res); err != nil {
		log.Printf("import %s: file result: %v", job.ID, err)
		os.Remove(outPath)
		m.Fail(job.ID, "the comic was built but could not be added to the library")
		return
	}
}

// file records the finished CBZ as a comic owned by the uploader.
func (m *Manager) file(job api.ImportJob, comicID, outPath string, opts api.ImportOptions, res *Result) error {
	a, err := cbz.Open(outPath)
	if err != nil {
		return err
	}
	// The metadata comes from reading back what was written rather than from the
	// options, so an upload and a library comic are described by the same code
	// and cannot drift apart.
	c := cbz.Comic(a)
	hash := a.Hash()
	a.Close()

	now := time.Now().Unix()
	row := store.ComicRow{
		ID:          comicID,
		Path:        filepath.Base(outPath),
		ContentHash: hash,
		Title:       c.Title,
		Series:      c.Series,
		Number:      c.Number,
		Volume:      c.Volume,
		Summary:     c.Summary,
		PageCount:   res.PageCount,
		FileSize:    res.OutBytes,
		AddedAt:     now,
		ModifiedAt:  now,
		OwnerID:     job.OwnerID,
		Source:      store.SourceUpload,
	}
	if err := m.store.UpsertComic(row); err != nil {
		return err
	}
	if opts.CollectionID != "" {
		// A collection that vanished or was never theirs must not lose them the
		// comic they just waited on: the import succeeded, the filing did not.
		if err := m.store.AddToCollection(job.OwnerID, opts.CollectionID, comicID); err != nil {
			log.Printf("import %s: add to collection %s: %v", job.ID, opts.CollectionID, err)
		}
	}
	if err := m.writeReport(job.ID, res.Groups); err != nil {
		log.Printf("import %s: write dupe report: %v", job.ID, err)
	}

	m.finish(job.ID, func(j *api.ImportJob) {
		j.Stage = api.StageDone
		j.ComicID = comicID
		j.PageCount = res.PageCount
		j.SourceCount = res.SourceCount
		j.ExactDupes = res.ExactDupes
		j.NearDupes = res.NearDupes
		j.Done, j.Total = res.PageCount, res.PageCount
	})
	return nil
}

func (m *Manager) writeReport(jobID string, groups []api.DupeGroup) error {
	p := m.reportPath(jobID)
	if p == "" {
		return nil
	}
	if groups == nil {
		groups = []api.DupeGroup{}
	}
	data, err := json.Marshal(groups)
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

func (m *Manager) reportPath(jobID string) string {
	if m.cfg.ReportDir == "" {
		return ""
	}
	return filepath.Join(m.cfg.ReportDir, jobID+".json")
}

// progress is the pipeline's callback, throttled onto the hub.
func (m *Manager) progress(jobID string) ProgressFunc {
	return func(stage api.ImportStage, done, total int) {
		// The pipeline calls done when the CBZ exists; the job is not done until
		// the comic is in the library. Reporting it here would send the client
		// off to fetch a comic that does not exist yet.
		if stage == api.StageDone {
			return
		}
		m.tick(jobID, func(j *api.ImportJob) bool {
			changed := j.Stage != stage
			j.Stage = stage
			j.Done, j.Total = done, total
			return changed
		})
	}
}

// tick applies mutate to the live job and reports it, at most once per
// progressInterval unless mutate says this update is worth forcing (a stage
// change, which is the only thing the user reads as a step).
//
// The DB row is written on a forced update only. It is the record of what
// happened, not a progress bar, and a write per file would be thousands of
// serialised transactions competing with every reader on the one SQLite
// connection.
func (m *Manager) tick(jobID string, mutate func(*api.ImportJob) bool) {
	m.mu.Lock()
	j, ok := m.running[jobID]
	if !ok {
		m.mu.Unlock()
		return
	}
	force := mutate(&j.snap)
	now := time.Now()
	send := force || now.Sub(j.lastEmit) >= progressInterval
	if send {
		j.lastEmit = now
	}
	snap := j.snap
	m.mu.Unlock()

	if !send {
		return
	}
	if force {
		m.save(snap)
	}
	m.broadcast(snap)
}

// finish applies a terminal mutation, persists it and drops the live job. The
// row is written before the job leaves the map so a snapshot taken in between
// cannot fall back to the stale row.
func (m *Manager) finish(jobID string, mutate func(*api.ImportJob)) {
	m.mu.Lock()
	j, ok := m.running[jobID]
	if !ok {
		m.mu.Unlock()
		return
	}
	mutate(&j.snap)
	j.snap.FinishedAt = time.Now().Unix()
	snap := j.snap
	cancel := j.cancel
	m.mu.Unlock()

	m.save(snap)

	m.mu.Lock()
	delete(m.running, jobID)
	m.mu.Unlock()
	if cancel != nil {
		// Releases the context regardless of how the job ended.
		cancel()
	}
	m.broadcast(snap)
}

func (m *Manager) save(j api.ImportJob) {
	if err := m.store.SaveImportJob(j); err != nil {
		log.Printf("save import job %s: %v", j.ID, err)
	}
}

func (m *Manager) broadcast(j api.ImportJob) {
	if m.hub == nil {
		return
	}
	m.hub.Broadcast(api.WSMessage{Type: api.WSTypeJob, Job: &j})
}

// failMessage maps a pipeline error onto something worth showing a user. The
// error itself is logged and not shown: it carries temp paths and file names
// from the server's disk, and none of that is the uploader's business.
func failMessage(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, ErrNoImages):
		return "no readable images in the upload"
	case errors.Is(err, ErrBadEncode):
		return "unsupported output format"
	case errors.Is(err, ErrBadQuality):
		return "quality must be between 1 and 100"
	}
	return "the import failed; the server log has the details"
}

// uploadTitle names an import after the folder the user picked, which arrives as
// the leading path segment shared by every uploaded file. The alternative is the
// temp directory's random name, which is no kind of title.
func uploadTitle(srcDir string) string {
	entries, err := os.ReadDir(srcDir)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		return "Untitled import"
	}
	return entries[0].Name()
}

// IsImageName reports whether a filename is one the pipeline would collect. The
// upload handler screens parts with it so that what it accepts and what the
// pipeline reads cannot disagree.
func IsImageName(name string) bool {
	return imageExts[strings.ToLower(filepath.Ext(name))]
}
