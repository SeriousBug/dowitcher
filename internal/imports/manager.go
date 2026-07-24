package imports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/cbz"
	"github.com/SeriousBug/dowitcher/internal/comicarchive"
	"github.com/SeriousBug/dowitcher/internal/store"
)

// ErrNotRunning means a cancel arrived for a job that no goroutine is working
// on and that is not queued either: it already finished, or it died with a
// previous process.
var ErrNotRunning = errors.New("imports: job is not running")

// ErrTooManyImports means the user already has as many imports queued or running
// as they are allowed.
var ErrTooManyImports = errors.New("imports: too many imports already queued")

// errFiling wraps a failure that happened after the CBZ was built, when filing
// it as a comic did not work. It gets a different user message than a pipeline
// failure, so the run path tells the two apart with errors.Is.
var errFiling = errors.New("imports: filing the comic failed")

// Job kinds decide how a worker processes a job and how restart resumes it.
const (
	// kindFolder is a folder of uploaded images run through the dedupe pipeline.
	kindFolder = "folder"
	// kindPDF is an uploaded PDF: its page images are extracted, then run through
	// the same pipeline, and the result is filed as the uploader's comic.
	kindPDF = "pdf"
	// kindLibraryPDF is a PDF dropped into the watched library folder. Its page
	// images are extracted and packed into a CBZ written to the uploads (data)
	// dir, then filed directly as an ownerless, server-wide comic
	// (store.SourceLibraryPDF). The library root is read-only by contract, so
	// nothing is written to it and the source PDF is left in place — a content-hash
	// dedupe keeps that from re-importing on the next scan. Such a job is ownerless
	// and visible only to admins.
	kindLibraryPDF = "library-pdf"
	// kindArchive is an uploaded non-zip comic container (CBR/CB7/CBT). Its page
	// images are extracted and run through the same pipeline as kindPDF, then filed
	// as the uploader's comic. A zip-based upload (CBZ) never reaches here — it is
	// adopted whole, since it is already the serving format.
	kindArchive = "archive"
	// kindLibraryArchive is a non-zip comic container dropped into the watched
	// library folder. It is to kindLibraryPDF what kindArchive is to kindPDF: the
	// same server-wide, ownerless, data-dir CBZ conversion, differing only in how
	// the pages are extracted (store.SourceLibraryArchive).
	kindLibraryArchive = "library-archive"
)

// withConvertDefaults applies the defaults the auto-conversion paths (PDF and
// non-zip archives) want when the caller named none. Chief among them is the
// encode format: these sources are transcoded to CBZ once at ingest, which is the
// moment to shrink them, so an unset Encode becomes AVIF. A CBZ adopt and a
// folder upload never pass through here, so their verbatim default is untouched.
func withConvertDefaults(opts api.ImportOptions) api.ImportOptions {
	if opts.Encode == "" {
		opts.Encode = defaultConvertEncode
	}
	return opts
}

// Broadcaster is the WS fan-out a job reports to. It is an interface so this
// package does not depend on the server package — the dependency runs the other
// way, and main wires the hub in.
//
// A job carries its owner, the folder name the uploader picked (usually the
// sensitive part), the comic it produced and its progress, so an owned job is
// never broadcast to everyone: it goes to its owner via BroadcastTo, and to
// admins via BroadcastToAdmins so the Import page's queue is complete for
// whoever may manage it. Broadcast is used only for the queue's paused flag,
// which is one server-wide boolean.
type Broadcaster interface {
	Broadcast(msg api.WSMessage)
	BroadcastTo(userID string, msg api.WSMessage)
	BroadcastToAdmins(msg api.WSMessage)
}

const (
	// progressInterval throttles job messages. The pipeline calls back per file,
	// which on a thousand-image folder is a thousand frames the hub would fan out
	// to every client — and a client that cannot keep up gets its frames dropped
	// anyway. Four updates a second is more than a progress bar can show.
	progressInterval = 250 * time.Millisecond
	// maxPerUser bounds the imports one user may have queued or running at once.
	// The queue drains at a fixed worker count regardless, so this is an
	// anti-abuse cap on the backlog a single client can pile up rather than a
	// concurrency limit: a client looping the upload endpoint would otherwise
	// grow the queue without bound.
	maxPerUser = 50
	// snapshotLimit bounds the job set the WS carries. The snapshot is the whole
	// set rather than a delta, so it is sent in full on every connect; a user who
	// has run five hundred imports does not need all of them to clear a spinner.
	snapshotLimit = 20
	// defaultWorkers is the queue's worker count when ManagerConfig.Workers is
	// unset. One: an import already fans its decode and encode across the cores,
	// and each concurrent AVIF encode holds a large libaom (WASM) memory arena, so
	// a second import running alongside doubles peak memory for no real throughput
	// on a machine the first import already saturates. Serialising keeps the
	// resource envelope of a home instance predictable; a bigger box can raise it.
	defaultWorkers = 1
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
	// ImportTempDir is where a PDF's extracted page images are staged before the
	// pipeline runs. A folder import stages in the handler; a PDF import stages
	// here because the extraction is the manager's own step.
	ImportTempDir string
	// MaxUploadBytes caps the total size of the images extracted from a PDF, the
	// PDF-bomb guard mirroring the upload cap. 0 uses defaultMaxExtractBytes.
	MaxUploadBytes int64
	// Workers is the number of queue workers. 0 uses defaultWorkers.
	Workers int
	// EncodeConcurrency pins how many pages a single import re-encodes at once.
	// 0 lets the pipeline size it from the memory budget; a positive value pins
	// it (DOWITCHER_IMPORT_ENCODE_CONCURRENCY).
	EncodeConcurrency int
}

// defaultMaxExtractBytes caps a PDF's extracted images when MaxUploadBytes is
// unset. It matches the server's DefaultMaxUploadBytes: an extraction should be
// allowed to produce as much as an upload of the same content would.
const defaultMaxExtractBytes = 8 << 30

// Manager turns the pure pipeline into a queue drained by a worker pool: it owns
// the jobs' order, their goroutines, their progress reporting, and the rows that
// say what happened.
type Manager struct {
	store *store.Store
	hub   Broadcaster
	cfg   ManagerConfig

	mu sync.Mutex
	// cond wakes workers when a job is enqueued, the queue is resumed, or the
	// process starts draining.
	cond *sync.Cond
	// live holds every unfinished job this process tracks: uploading, queued and
	// running alike. It is the live view; the DB row is the durable one, and
	// JobSnapshot layers this over that.
	live map[string]*liveJob
	// queue is the ordered ids of jobs waiting for a worker, lowest queue_seq
	// first. A worker pops the front.
	queue []string
	// paused holds the queue: an in-flight job runs on, but no new one is picked.
	paused bool
	// draining is set when baseCtx is cancelled (shutdown). A worker whose job is
	// cancelled while draining leaves the row unfinished so a restart resumes it,
	// the opposite of a user cancel, which is terminal.
	draining bool
	workers  int
	// seq is the monotonic source of queue_seq, seeded from max(queue_seq)+1 so
	// it stays ahead of every row a restart recovers.
	seq     int64
	baseCtx context.Context
}

type liveJob struct {
	snap     api.ImportJob
	cancel   context.CancelFunc
	lastEmit time.Time
	// inputPath is the staged folder or source PDF the job runs from, and
	// optionsJSON the api.ImportOptions it runs with. Both are server-only and
	// mirror the DB's input_path/options columns so a worker needs no round trip.
	inputPath   string
	optionsJSON string
}

// NewManager prepares the directories and seeds the queue's counter. It does not
// recover orphaned jobs — that is Run's first act, because recovery re-enqueues
// survivors onto the queue the workers drain.
func NewManager(st *store.Store, hub Broadcaster, cfg ManagerConfig) (*Manager, error) {
	if cfg.UploadsDir == "" {
		return nil, errors.New("imports: no uploads dir configured")
	}
	for _, d := range []string{cfg.UploadsDir, cfg.ReportDir, cfg.ImportTempDir} {
		if d == "" {
			continue
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("imports: %w", err)
		}
	}
	workers := cfg.Workers
	if workers <= 0 {
		workers = defaultWorkers
	}
	m := &Manager{store: st, hub: hub, cfg: cfg, live: map[string]*liveJob{}, workers: workers}
	m.cond = sync.NewCond(&m.mu)

	next, err := st.MaxImportQueueSeq()
	if err != nil {
		return nil, fmt.Errorf("imports: seed queue seq: %w", err)
	}
	m.seq = next + 1

	paused, err := st.QueuePaused()
	if err != nil {
		return nil, fmt.Errorf("imports: read queue paused: %w", err)
	}
	m.paused = paused
	return m, nil
}

// Run recovers orphaned jobs, then drives the worker pool until ctx is
// cancelled. main calls it in a goroutine after SetImporter.
func (m *Manager) Run(ctx context.Context) {
	m.mu.Lock()
	m.baseCtx = ctx
	m.mu.Unlock()

	m.recover()

	// When ctx is cancelled the process is going down: flip draining and wake
	// every worker so an idle one returns instead of blocking on cond forever.
	go func() {
		<-ctx.Done()
		m.mu.Lock()
		m.draining = true
		m.cond.Broadcast()
		m.mu.Unlock()
	}()

	var wg sync.WaitGroup
	for i := 0; i < m.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.worker()
		}()
	}
	wg.Wait()
}

// worker is one drain loop: wait for a job, take the lowest-seq one, process it
// by kind. It returns when the process is draining.
func (m *Manager) worker() {
	for {
		m.mu.Lock()
		for (m.paused || len(m.queue) == 0) && !m.draining {
			m.cond.Wait()
		}
		if m.draining {
			m.mu.Unlock()
			return
		}
		id := m.queue[0]
		m.queue = m.queue[1:]
		j, ok := m.live[id]
		if !ok {
			// Cancelled out of the queue between the signal and here.
			m.mu.Unlock()
			continue
		}
		runCtx, cancel := context.WithCancel(m.baseCtx)
		j.cancel = cancel
		j.snap.Stage = stageForKind(j.snap.Kind)
		j.snap.Done, j.snap.Total = 0, 0
		snap := j.snap
		inputPath := j.inputPath
		optionsJSON := j.optionsJSON
		m.mu.Unlock()

		var opts api.ImportOptions
		if optionsJSON != "" {
			if err := json.Unmarshal([]byte(optionsJSON), &opts); err != nil {
				log.Printf("import %s: bad options json: %v", snap.ID, err)
			}
		}

		m.save(snap)
		m.broadcast(snap)

		switch snap.Kind {
		case kindLibraryPDF:
			m.runLibraryPDF(runCtx, snap, inputPath, opts)
		case kindPDF:
			m.runPDF(runCtx, snap, inputPath, opts)
		case kindLibraryArchive:
			m.runLibraryArchive(runCtx, snap, inputPath, opts)
		case kindArchive:
			m.runArchive(runCtx, snap, inputPath, opts)
		default:
			m.run(runCtx, snap, inputPath, opts)
		}
	}
}

// stageForKind is the first stage a worker reports when it picks a job up.
func stageForKind(kind string) api.ImportStage {
	switch kind {
	case kindPDF, kindLibraryPDF, kindArchive, kindLibraryArchive:
		return api.StageExtracting
	}
	return api.StageReading
}

// recover resolves the jobs a crash or shutdown left unfinished. A job whose
// staged input (or source PDF) still exists is reset to queued and re-enqueued;
// the rest are failed, because nothing will ever move them and a spinner that
// never stops is worse than an honest failure.
func (m *Manager) recover() {
	jobs, err := m.store.ListRecoverableImportJobs()
	if err != nil {
		log.Printf("imports: recover jobs: %v", err)
		return
	}
	// Preserve the pre-crash order so a resume is still FIFO.
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Job.QueueSeq < jobs[j].Job.QueueSeq })
	now := time.Now().Unix()
	for _, r := range jobs {
		if r.InputPath != "" && pathExists(r.InputPath) {
			job := r.Job
			job.Stage = api.StageQueued
			job.FinishedAt = 0
			m.mu.Lock()
			// A concurrent EnqueueLibraryPDF (the initial scan racing recovery) may
			// have already taken this exact row into the live view; skip it rather
			// than queue it twice.
			if _, ok := m.live[job.ID]; ok {
				m.mu.Unlock()
				continue
			}
			job.QueueSeq = int(m.seq)
			m.seq++
			m.live[job.ID] = &liveJob{snap: job, inputPath: r.InputPath, optionsJSON: r.Options}
			m.queue = append(m.queue, job.ID)
			m.mu.Unlock()
			m.save(job)
			m.broadcast(job)
			log.Printf("import %s re-queued after restart", job.ID)
			continue
		}
		job := r.Job
		job.Stage = api.StageFailed
		job.Message = "interrupted by a server restart"
		job.FinishedAt = now
		m.save(job)
		m.broadcast(job)
		log.Printf("import %s could not be resumed after restart, marked failed", job.ID)
	}
}

// Begin registers a job before its bytes arrive, so an upload that takes minutes
// shows up as an import immediately instead of appearing only once it lands.
//
// It is also where the per-user cap is enforced, because it is the only point
// that runs before the upload does. Start is the other candidate and is the
// wrong one: by then the user has spent minutes pushing several GB that the
// server has written to disk, and refusing it there wastes all of that on both
// ends. Rejecting here costs the client one request.
func (m *Manager) Begin(userID string) (api.ImportJob, error) {
	j := api.ImportJob{
		ID:        store.NewID(),
		OwnerID:   userID,
		Kind:      kindFolder,
		Stage:     api.StageUploading,
		StartedAt: time.Now().Unix(),
	}
	// Counting and claiming the slot happen under one lock, or two uploads
	// starting together would both count the other's absence and both proceed.
	m.mu.Lock()
	n := 0
	for _, live := range m.live {
		if live.snap.OwnerID == userID {
			n++
		}
	}
	if n >= maxPerUser {
		m.mu.Unlock()
		// No row is written: a job that never started should not sit in the
		// user's history as a failure they have to read.
		return api.ImportJob{}, fmt.Errorf("%w: %d already queued", ErrTooManyImports, n)
	}
	m.live[j.ID] = &liveJob{snap: j}
	m.mu.Unlock()

	if err := m.store.SaveImportJob(j); err != nil {
		// The slot was claimed before the row existed; a job with no row is one
		// nothing will ever clear, so it must not keep occupying the cap.
		m.mu.Lock()
		delete(m.live, j.ID)
		m.mu.Unlock()
		return api.ImportJob{}, err
	}
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

// Start enqueues a fully uploaded folder. It no longer runs the import: the job
// waits in the queue until a worker picks it up. ctx is ignored — a queued job's
// lifecycle is owned by the manager, not the request that posted it.
func (m *Manager) Start(_ context.Context, jobID, srcDir string, opts api.ImportOptions) error {
	if opts.Name == "" {
		opts.Name = uploadTitle(srcDir)
	}
	return m.enqueueUploaded(jobID, kindFolder, srcDir, opts)
}

// StartPDF enqueues a fully uploaded PDF. Like Start it only queues the work; a
// worker extracts and runs it later.
func (m *Manager) StartPDF(_ context.Context, jobID, pdfPath string, opts api.ImportOptions) error {
	if opts.Name == "" {
		opts.Name = stemTitle(pdfPath)
	}
	return m.enqueueUploaded(jobID, kindPDF, pdfPath, opts)
}

// StartArchive enqueues a fully uploaded non-zip comic container (CBR/CB7/CBT).
// Like StartPDF it only queues the work; a worker extracts and runs it later.
func (m *Manager) StartArchive(_ context.Context, jobID, archivePath string, opts api.ImportOptions) error {
	if opts.Name == "" {
		opts.Name = stemTitle(archivePath)
	}
	return m.enqueueUploaded(jobID, kindArchive, archivePath, opts)
}

// enqueueUploaded moves a job that Begin created from uploading to queued,
// recording its staged input and options and pushing it onto the queue.
func (m *Manager) enqueueUploaded(jobID, kind, inputPath string, opts api.ImportOptions) error {
	optsJSON, err := json.Marshal(opts)
	if err != nil {
		return err
	}
	m.mu.Lock()
	j, ok := m.live[jobID]
	if !ok {
		m.mu.Unlock()
		return store.ErrNotFound
	}
	j.snap.Kind = kind
	j.snap.Name = opts.Name
	j.snap.Stage = api.StageQueued
	j.snap.Done, j.snap.Total = 0, 0
	j.snap.QueueSeq = int(m.seq)
	m.seq++
	j.inputPath = inputPath
	j.optionsJSON = string(optsJSON)
	snap := j.snap
	m.queue = append(m.queue, jobID)
	m.cond.Signal()
	m.mu.Unlock()

	if err := m.store.SetImportJobInput(jobID, inputPath, string(optsJSON)); err != nil {
		log.Printf("import %s: save input: %v", jobID, err)
	}
	m.save(snap)
	m.broadcast(snap)
	return nil
}

// EnqueueLibraryPDF queues a PDF dropped in the library folder for conversion to
// a server-wide CBZ.
func (m *Manager) EnqueueLibraryPDF(pdfPath string) {
	m.enqueueLibraryConvert(pdfPath, kindLibraryPDF)
}

// EnqueueLibraryArchive queues a non-zip comic container (CBR/CB7/CBT) dropped in
// the library folder for conversion to a server-wide CBZ.
func (m *Manager) EnqueueLibraryArchive(archivePath string) {
	m.enqueueLibraryConvert(archivePath, kindLibraryArchive)
}

// enqueueLibraryConvert queues a library-dropped file for conversion to a
// server-wide CBZ. The job is ownerless. It dedupes against any unfinished job
// already carrying the same input path, so scan, watch and repeated sweeps
// handing off the same file only ever queue it once. kind selects the conversion
// (a PDF's pages are rasterised; an archive's are unpacked); the dedupe, ordering
// and idempotency are identical.
func (m *Manager) enqueueLibraryConvert(inputPath, kind string) {
	optsJSON := "{}"
	j := api.ImportJob{
		ID:        store.NewID(),
		Kind:      kind,
		Name:      stemTitle(inputPath),
		Stage:     api.StageQueued,
		StartedAt: time.Now().Unix(),
	}
	m.mu.Lock()
	for _, live := range m.live {
		if live.inputPath == inputPath && live.snap.FinishedAt == 0 {
			m.mu.Unlock()
			return
		}
	}
	// The live map misses a row the recovery pass has not re-enqueued yet (the
	// initial scan can reach a dropped file before recovery runs). The DB is the
	// shared ground truth: if an unfinished job already carries this path,
	// recovery will re-queue it, so a fresh one must not be created. A job is put
	// in the live map before its row is written, so this can never see the job it
	// is about to create.
	if has, err := m.store.HasUnfinishedImportJobForInput(inputPath); err != nil {
		m.mu.Unlock()
		log.Printf("%s: dedup check %s: %v", kind, inputPath, err)
		return
	} else if has {
		m.mu.Unlock()
		return
	}
	// The source file is never deleted (its folder is read-only), so every scan
	// after a restart hands it off again. If a past run already turned this file
	// into a comic, skip it rather than re-run the conversion. This record is
	// protected from the clear-finished-imports action, so it is durable; the
	// filing step's content-hash check is the last-resort backstop if it is lost
	// some other way.
	if has, err := m.store.HasImportedInput(inputPath); err != nil {
		m.mu.Unlock()
		log.Printf("%s: import check %s: %v", kind, inputPath, err)
		return
	} else if has {
		m.mu.Unlock()
		return
	}
	j.QueueSeq = int(m.seq)
	m.seq++
	m.live[j.ID] = &liveJob{snap: j, inputPath: inputPath, optionsJSON: optsJSON}
	m.queue = append(m.queue, j.ID)
	m.cond.Signal()
	m.mu.Unlock()

	if err := m.store.SaveImportJob(j); err != nil {
		log.Printf("%s %s: save: %v", kind, j.ID, err)
		m.mu.Lock()
		delete(m.live, j.ID)
		m.removeFromQueueLocked(j.ID)
		m.mu.Unlock()
		return
	}
	if err := m.store.SetImportJobInput(j.ID, inputPath, optsJSON); err != nil {
		log.Printf("%s %s: save input: %v", kind, j.ID, err)
	}
	m.broadcast(j)
}

// Pause and Resume hold and release the queue's dequeue. Broadcast to everyone
// because the paused flag is one server-wide, non-sensitive boolean.
func (m *Manager) Pause() error  { return m.setPaused(true) }
func (m *Manager) Resume() error { return m.setPaused(false) }

func (m *Manager) setPaused(paused bool) error {
	m.mu.Lock()
	m.paused = paused
	m.cond.Broadcast()
	m.mu.Unlock()
	if err := m.store.SetQueuePaused(paused); err != nil {
		return err
	}
	if m.hub != nil {
		m.hub.Broadcast(api.WSMessage{Type: api.WSTypeQueue, Queue: &api.QueueState{Paused: paused}})
	}
	return nil
}

// Paused reports the queue's current paused flag.
func (m *Manager) Paused() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.paused
}

// Reorder rewrites the queued order from the full ordered id list, mirroring
// ReorderCollection. Ids not currently queued are ignored; any queued id the
// list omits keeps its place at the end. Running jobs are untouched.
func (m *Manager) Reorder(jobIDs []string) error {
	m.mu.Lock()
	queued := map[string]bool{}
	for _, id := range m.queue {
		queued[id] = true
	}
	var newQueue []string
	for _, id := range jobIDs {
		if queued[id] {
			newQueue = append(newQueue, id)
			delete(queued, id)
		}
	}
	for _, id := range m.queue {
		if queued[id] {
			newQueue = append(newQueue, id)
		}
	}
	base := m.seq
	ids := make([]string, len(newQueue))
	snaps := make([]api.ImportJob, 0, len(newQueue))
	for i, id := range newQueue {
		ids[i] = id
		if j, ok := m.live[id]; ok {
			j.snap.QueueSeq = int(base) + i
			snaps = append(snaps, j.snap)
		}
	}
	m.queue = newQueue
	m.seq = base + int64(len(newQueue))
	m.mu.Unlock()

	if err := m.store.ReorderImportJobs(ids, base); err != nil {
		return err
	}
	for _, s := range snaps {
		m.broadcast(s)
	}
	return nil
}

// extractFunc unpacks a source file's page images into destDir. It closes over
// the budget and per-job progress, so runUploadedConvert and runLibraryConvert
// stay agnostic to whether the source is a PDF or an archive.
type extractFunc func(ctx context.Context, src, destDir string) error

// pdfExtract and archiveExtract are the two extractFuncs, bound to a job so their
// progress lands on it. Both report the StageExtracting stage the archive/PDF
// paths open with.
func (m *Manager) pdfExtract(jobID string) extractFunc {
	return func(ctx context.Context, src, destDir string) error {
		_, err := ExtractPDF(ctx, src, destDir, m.extractBudget(), m.progress(jobID))
		return err
	}
}

func (m *Manager) archiveExtract(jobID string) extractFunc {
	p := m.progress(jobID)
	return func(ctx context.Context, src, destDir string) error {
		_, err := comicarchive.Extract(ctx, src, destDir, m.extractBudget(), func(done, total int) {
			p(api.StageExtracting, done, total)
		})
		return err
	}
}

// runPDF and runArchive extract an uploaded book, then run the shared pipeline
// and file the result as the uploader's comic.
func (m *Manager) runPDF(ctx context.Context, job api.ImportJob, pdfPath string, opts api.ImportOptions) {
	m.runUploadedConvert(ctx, job, pdfPath, opts, m.pdfExtract(job.ID))
}

func (m *Manager) runArchive(ctx context.Context, job api.ImportJob, archivePath string, opts api.ImportOptions) {
	m.runUploadedConvert(ctx, job, archivePath, opts, m.archiveExtract(job.ID))
}

// runUploadedConvert extracts an uploaded self-contained book (a PDF or a non-zip
// archive) into page images and files it as the uploader's comic. The source is
// kept until the pipeline succeeds so a drain mid-pipeline still has it to
// re-extract on restart; on a terminal failure or success its staging dir is
// removed. An unset Encode defaults to AVIF, since converting to CBZ is the moment
// to shrink these sources.
func (m *Manager) runUploadedConvert(ctx context.Context, job api.ImportJob, srcPath string, opts api.ImportOptions, extract extractFunc) {
	srcStageDir := filepath.Dir(srcPath)
	srcDir, err := os.MkdirTemp(m.cfg.ImportTempDir, "dowitcher-convert-*")
	if err != nil {
		log.Printf("import %s: convert temp dir: %v", job.ID, err)
		os.RemoveAll(srcStageDir)
		m.Fail(job.ID, "the server had nowhere to unpack the upload")
		return
	}
	// The extracted images are transient either way.
	defer os.RemoveAll(srcDir)

	if err := extract(ctx, srcPath, srcDir); err != nil {
		if m.drained(err) {
			m.requeueForRestart(job.ID)
			return
		}
		log.Printf("import %s: extract: %v", job.ID, err)
		os.RemoveAll(srcStageDir)
		m.Fail(job.ID, failMessage(err))
		return
	}

	if err := m.pipeline(ctx, job, srcDir, withConvertDefaults(opts)); err != nil {
		if m.drained(err) {
			m.requeueForRestart(job.ID)
			return
		}
		os.RemoveAll(srcStageDir)
		m.failPipeline(job.ID, err)
		return
	}
	os.RemoveAll(srcStageDir)
}

// runLibraryPDF and runLibraryArchive convert a file dropped in the library
// folder into a CBZ in the uploads (data) dir and file it as an ownerless,
// server-wide comic.
func (m *Manager) runLibraryPDF(ctx context.Context, job api.ImportJob, pdfPath string, opts api.ImportOptions) {
	m.runLibraryConvert(ctx, job, pdfPath, opts, store.SourceLibraryPDF, m.pdfExtract(job.ID))
}

func (m *Manager) runLibraryArchive(ctx context.Context, job api.ImportJob, archivePath string, opts api.ImportOptions) {
	m.runLibraryConvert(ctx, job, archivePath, opts, store.SourceLibraryArchive, m.archiveExtract(job.ID))
}

// runLibraryConvert converts a library-dropped file into a CBZ in the uploads
// (data) dir and files it as an ownerless, server-wide comic. The library root is
// read-only by contract, so the source's folder is never written to: the CBZ goes
// to writable storage and the source is left exactly where it was. That makes the
// conversion idempotent by content hash rather than by removing the source, which
// is what lets a read-only library mount work at all.
func (m *Manager) runLibraryConvert(ctx context.Context, job api.ImportJob, srcPath string, opts api.ImportOptions, source string, extract extractFunc) {
	srcDir, err := os.MkdirTemp(m.cfg.ImportTempDir, "dowitcher-libconvert-*")
	if err != nil {
		log.Printf("%s %s: temp dir: %v", source, job.ID, err)
		m.Fail(job.ID, "the server had nowhere to unpack the file")
		return
	}
	defer os.RemoveAll(srcDir)

	if err := extract(ctx, srcPath, srcDir); err != nil {
		if m.drained(err) {
			m.requeueForRestart(job.ID)
			return
		}
		log.Printf("%s %s: extract: %v", source, job.ID, err)
		m.Fail(job.ID, failMessage(err))
		return
	}

	opts = withConvertDefaults(opts)
	if opts.Name == "" {
		opts.Name = stemTitle(srcPath)
	}
	// The comic's id names its file, the same as an ordinary upload: a chosen name
	// would collide with the unique path index the moment two sources share a title.
	comicID := store.NewID()
	outPath := filepath.Join(m.cfg.UploadsDir, comicID+".cbz")
	res, err := Run(ctx, srcDir, outPath, opts, m.cfg.EncodeConcurrency, m.progress(job.ID))
	if err != nil {
		os.Remove(outPath)
		if m.drained(err) {
			m.requeueForRestart(job.ID)
			return
		}
		log.Printf("%s %s: pipeline: %v", source, job.ID, err)
		m.Fail(job.ID, failMessage(err))
		return
	}

	if err := m.fileLibraryConverted(job, comicID, outPath, res, source); err != nil {
		os.Remove(outPath)
		log.Printf("%s %s: file: %v", source, job.ID, err)
		m.Fail(job.ID, "the comic was built but could not be added to the library")
		return
	}
}

// fileLibraryConverted records a converted library file as an ownerless,
// server-wide comic with the given source. It reads the metadata back from the
// CBZ so a converted comic and an upload are described by the same code, and it
// dedupes on content hash: because the source file is never deleted, an unchanged
// file re-handed off after a history wipe would otherwise be filed a second time.
// On a hash hit the fresh CBZ is discarded and the job points at the comic that
// already exists.
func (m *Manager) fileLibraryConverted(job api.ImportJob, comicID, outPath string, res *Result, source string) error {
	a, err := cbz.Open(outPath)
	if err != nil {
		return err
	}
	c := cbz.Comic(a)
	hash := a.Hash()
	a.Close()

	if existing, err := m.store.ServerWideComicByHash(hash); err == nil {
		os.Remove(outPath)
		m.finish(job.ID, func(j *api.ImportJob) {
			j.Stage = api.StageDone
			j.ComicID = existing.ID
			j.PageCount = res.PageCount
			j.Done, j.Total = res.PageCount, res.PageCount
		})
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}

	row := comicRow(comicID, "", outPath, c, hash, res.PageCount, res.OutBytes)
	row.Source = source
	if err := m.store.UpsertComic(row); err != nil {
		return err
	}
	if err := m.writeReport(job.ID, res.Groups); err != nil {
		log.Printf("%s %s: write dupe report: %v", source, job.ID, err)
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

// run is the folder-import goroutine body: pipeline, then file the result.
func (m *Manager) run(ctx context.Context, job api.ImportJob, srcDir string, opts api.ImportOptions) {
	if err := m.pipeline(ctx, job, srcDir, opts); err != nil {
		if m.drained(err) {
			// Leave srcDir and the unfinished row so a restart resumes.
			m.requeueForRestart(job.ID)
			return
		}
		os.RemoveAll(srcDir)
		m.failPipeline(job.ID, err)
		return
	}
	os.RemoveAll(srcDir)
}

// pipeline builds a CBZ from srcDir and files it as the job owner's comic,
// returning nil on success. It does not touch srcDir or the source — the caller
// owns cleanup and inspects the error for a drain-time cancellation.
func (m *Manager) pipeline(ctx context.Context, job api.ImportJob, srcDir string, opts api.ImportOptions) error {
	// The comic's id is minted before the CBZ is written so it can name the file:
	// an upload is addressed by id, never by a name the user chose, because a
	// name would collide with the unique path index the moment two people import
	// the same folder.
	comicID := store.NewID()
	outPath := filepath.Join(m.cfg.UploadsDir, comicID+".cbz")

	res, err := Run(ctx, srcDir, outPath, opts, m.cfg.EncodeConcurrency, m.progress(job.ID))
	if err != nil {
		return err
	}
	if err := m.file(job, comicID, outPath, opts, res); err != nil {
		os.Remove(outPath)
		return fmt.Errorf("%w: %v", errFiling, err)
	}
	return nil
}

// failPipeline turns a pipeline error into the user's failure message: a filing
// error gets the generic "built but not added" text, everything else the
// mapped pipeline message.
func (m *Manager) failPipeline(jobID string, err error) {
	if errors.Is(err, errFiling) {
		log.Printf("import %s: file result: %v", jobID, err)
		m.Fail(jobID, "the comic was built but could not be added to the library")
		return
	}
	log.Printf("import %s: %v", jobID, err)
	m.Fail(jobID, failMessage(err))
}

// drained reports whether an error is a shutdown cancellation, in which case the
// job must be left unfinished for a restart to resume rather than failed.
func (m *Manager) drained(err error) bool {
	if !errors.Is(err, context.Canceled) {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.draining
}

// requeueForRestart drops a job from the live view without ending its row, so
// the finished_at=0 row plus its still-present input let recover() re-enqueue it
// on the next start. The input is deliberately not cleaned.
func (m *Manager) requeueForRestart(jobID string) {
	m.mu.Lock()
	j, ok := m.live[jobID]
	if !ok {
		m.mu.Unlock()
		return
	}
	j.snap.Stage = api.StageQueued
	j.snap.FinishedAt = 0
	snap := j.snap
	cancel := j.cancel
	delete(m.live, jobID)
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.save(snap)
	log.Printf("import %s left queued for restart (server draining)", jobID)
}

// extractBudget is the byte cap on a PDF's extracted images.
func (m *Manager) extractBudget() int64 {
	if m.cfg.MaxUploadBytes > 0 {
		return m.cfg.MaxUploadBytes
	}
	return defaultMaxExtractBytes
}

// Fail marks a job that died before it produced a comic. Without it a broken
// upload leaves a job stuck in "uploading" until the next restart sweeps it.
func (m *Manager) Fail(jobID, msg string) {
	m.finish(jobID, func(j *api.ImportJob) {
		j.Stage = api.StageFailed
		j.Message = msg
	})
}

// Cancel stops a job on behalf of its owner. The store's ownership check runs
// first, so a job that is not yours is not found rather than forbidden.
func (m *Manager) Cancel(userID, jobID string) error {
	if _, err := m.store.GetImportJob(userID, jobID); err != nil {
		return err
	}
	return m.cancelJob(jobID)
}

// CancelAny stops any job without the ownership check, for an admin. It is the
// only way to cancel an ownerless library-pdf job, which no per-user lookup
// returns.
func (m *Manager) CancelAny(jobID string) error {
	return m.cancelJob(jobID)
}

// cancelJob removes a queued job or cancels a running one. A queued removal
// cleans the staged input and ends the row as cancelled; a running job winds
// itself up through its context and reports the failure itself.
func (m *Manager) cancelJob(jobID string) error {
	m.mu.Lock()
	for i, id := range m.queue {
		if id != jobID {
			continue
		}
		m.queue = append(m.queue[:i], m.queue[i+1:]...)
		var kind, inputPath string
		if j, ok := m.live[jobID]; ok {
			kind, inputPath = j.snap.Kind, j.inputPath
		}
		m.mu.Unlock()
		cleanInput(kind, inputPath)
		m.finish(jobID, func(j *api.ImportJob) {
			j.Stage = api.StageFailed
			j.Message = "cancelled"
		})
		return nil
	}
	j, ok := m.live[jobID]
	var cancel context.CancelFunc
	if ok {
		cancel = j.cancel
	}
	m.mu.Unlock()
	if cancel == nil {
		// Not queued and not running: an uploading job with no context yet, or one
		// that already finished.
		return ErrNotRunning
	}
	cancel()
	return nil
}

// removeFromQueueLocked splices a job id out of the queue. Caller holds mu.
func (m *Manager) removeFromQueueLocked(jobID string) {
	for i, id := range m.queue {
		if id == jobID {
			m.queue = append(m.queue[:i], m.queue[i+1:]...)
			return
		}
	}
}

// cleanInput removes a cancelled or drained job's staged input. A library-pdf's
// input is the user's own file in the library folder and is left alone.
func cleanInput(kind, inputPath string) {
	if inputPath == "" {
		return
	}
	switch kind {
	case kindFolder:
		os.RemoveAll(inputPath)
	case kindPDF, kindArchive:
		// The uploaded book sits alone in its own staging dir; remove the dir.
		os.RemoveAll(filepath.Dir(inputPath))
	}
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

// JobSnapshot is the complete job set the hub tells a client about, with this
// process's live state laid over the persisted rows. An admin gets the
// server-wide set — every job, including ownerless library-pdf ones — while
// everyone else gets only their own.
func (m *Manager) JobSnapshot(userID string, isAdmin bool) []api.ImportJob {
	var rows []api.ImportJob
	var err error
	if isAdmin {
		rows, err = m.store.ListAllImportJobs(snapshotLimit)
	} else {
		rows, err = m.store.ListImportJobs(userID)
		if err == nil && len(rows) > snapshotLimit {
			rows = rows[:snapshotLimit]
		}
	}
	if err != nil {
		log.Printf("import job snapshot: %v", err)
		return []api.ImportJob{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, r := range rows {
		if j, ok := m.live[r.ID]; ok {
			rows[i] = j.snap
		}
	}
	return rows
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

	row := comicRow(comicID, job.OwnerID, outPath, c, hash, res.PageCount, res.OutBytes)
	if err := m.fileComic(row, opts.CollectionID); err != nil {
		return err
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

// comicRow builds the row for a CBZ sitting at its final path. c is the metadata
// read from the archive; pageCount and size are passed rather than re-derived
// because the pipeline already knows both and the adopter has to stat anyway.
func comicRow(comicID, ownerID, outPath string, c api.Comic, hash string, pageCount int, size int64) store.ComicRow {
	now := time.Now().Unix()
	return store.ComicRow{
		ID:          comicID,
		Path:        filepath.Base(outPath),
		ContentHash: hash,
		Title:       c.Title,
		Series:      c.Series,
		Number:      c.Number,
		Volume:      c.Volume,
		Summary:     c.Summary,
		PageCount:   pageCount,
		FileSize:    size,
		AddedAt:     now,
		ModifiedAt:  now,
		OwnerID:     ownerID,
		Source:      store.SourceUpload,
	}
}

// fileComic writes the row and files the comic into the requested collection.
func (m *Manager) fileComic(row store.ComicRow, collectionID string) error {
	if err := m.store.UpsertComic(row); err != nil {
		return err
	}
	if collectionID != "" {
		// A collection that vanished or was never theirs must not lose them the
		// comic they just waited on: the import succeeded, the filing did not.
		if err := m.store.AddToCollection(row.OwnerID, collectionID, row.ID); err != nil {
			log.Printf("comic %s: add to collection %s: %v", row.ID, collectionID, err)
		}
	}
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
	j, ok := m.live[jobID]
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
	j, ok := m.live[jobID]
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
	delete(m.live, jobID)
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

// broadcast reports a job to its owner and to every admin. An owned job reaches
// its owner (whose folder name and comic id it carries) plus the admins who may
// manage the queue; an ownerless library-pdf job reaches admins alone. It never
// goes to Broadcast, which would put a per-user payload on every socket.
func (m *Manager) broadcast(j api.ImportJob) {
	if m.hub == nil {
		return
	}
	msg := api.WSMessage{Type: api.WSTypeJob, Job: &j}
	if j.OwnerID != "" {
		m.hub.BroadcastTo(j.OwnerID, msg)
	}
	m.hub.BroadcastToAdmins(msg)
}

// failMessage maps a pipeline error onto something worth showing a user. The
// error itself is logged and not shown: it carries temp paths and file names
// from the server's disk, and none of that is the uploader's business.
func failMessage(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, ErrNotPDF):
		return "that file could not be read as a PDF"
	case errors.Is(err, ErrPDFTooBig):
		return "the images in that PDF are larger than the server allows"
	case errors.Is(err, comicarchive.ErrUnsupported):
		return "that archive format is not supported"
	case errors.Is(err, comicarchive.ErrUnreadable):
		return "that file could not be read as a comic archive"
	case errors.Is(err, comicarchive.ErrTooBig):
		return "the images in that archive are larger than the server allows"
	case errors.Is(err, comicarchive.ErrNoImages):
		return "no readable images in the archive"
	case errors.Is(err, ErrNoImages):
		return "no readable images in the upload"
	case errors.Is(err, ErrBadEncode):
		return "unsupported output format"
	case errors.Is(err, ErrBadQuality):
		return "quality must be between 1 and 100"
	case errors.Is(err, ErrTooManyFiles):
		return fmt.Sprintf("this folder has more than %d images; import it as separate books", maxFiles)
	}
	return "the import failed; the server log has the details"
}

// pathExists reports whether a staged input is still on disk, for restart
// recovery to decide whether a job can resume.
func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
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

// stemTitle names a PDF or archive import after the uploaded filename, minus its
// extension. The extracted images sit under random temp names, so uploadTitle's
// folder-name trick does not apply — the filename is the only title the user gave.
func stemTitle(srcPath string) string {
	base := filepath.Base(srcPath)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	if name == "" {
		return "Untitled import"
	}
	return name
}

// IsImageName reports whether a filename is one the pipeline would collect. The
// upload handler screens parts with it so that what it accepts and what the
// pipeline reads cannot disagree.
func IsImageName(name string) bool {
	return imageExts[strings.ToLower(filepath.Ext(name))]
}
