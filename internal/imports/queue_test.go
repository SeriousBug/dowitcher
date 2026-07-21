package imports

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/store"
)

// TestEnqueueNotRun: Start no longer runs the import. Without a worker pool the
// job sits at "queued" and produces no comic.
func TestEnqueueNotRun(t *testing.T) {
	m, st, _, user := testManager(t)
	src := srcFolder(t)

	job, err := m.Begin(user.ID)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := m.Start(context.Background(), job.ID, src, api.ImportOptions{}); err != nil {
		t.Fatalf("start: %v", err)
	}
	// No Run, so no worker drains it: it stays queued.
	got := jobStage(t, st, user.ID, job.ID)
	if got.Stage != api.StageQueued {
		t.Fatalf("stage = %q, want queued — Start must not run the import", got.Stage)
	}
	if got.FinishedAt != 0 {
		t.Fatal("a queued job has not finished")
	}
}

// TestQueueDrainsFIFO: one worker drains jobs in queue order.
func TestQueueDrainsFIFO(t *testing.T) {
	m, st, _, user := testManager(t)

	first, _ := m.Begin(user.ID)
	if err := m.Start(context.Background(), first.ID, srcFolder(t), api.ImportOptions{}); err != nil {
		t.Fatalf("start first: %v", err)
	}
	second, _ := m.Begin(user.ID)
	if err := m.Start(context.Background(), second.ID, srcFolder(t), api.ImportOptions{}); err != nil {
		t.Fatalf("start second: %v", err)
	}
	if a, b := jobStage(t, st, user.ID, first.ID), jobStage(t, st, user.ID, second.ID); a.QueueSeq >= b.QueueSeq {
		t.Fatalf("queue_seq = %d,%d, want the first enqueued to be lower", a.QueueSeq, b.QueueSeq)
	}

	runWorkers(t, m)
	waitFor(t, "both jobs to finish", func() bool {
		return jobStage(t, st, user.ID, first.ID).FinishedAt != 0 &&
			jobStage(t, st, user.ID, second.ID).FinishedAt != 0
	})
	if jobStage(t, st, user.ID, first.ID).FinishedAt > jobStage(t, st, user.ID, second.ID).FinishedAt {
		t.Fatal("the first-queued job should have finished first with one worker")
	}
}

// TestPauseHaltsDequeue: a paused queue does not start a queued job; resuming
// drains it.
func TestPauseHaltsDequeue(t *testing.T) {
	m, st, _, user := testManager(t)
	if err := m.Pause(); err != nil {
		t.Fatalf("pause: %v", err)
	}
	runWorkers(t, m)

	job, _ := m.Begin(user.ID)
	if err := m.Start(context.Background(), job.ID, srcFolder(t), api.ImportOptions{}); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Give a worker every chance to wrongly pick it up.
	for i := 0; i < 20; i++ {
		if jobStage(t, st, user.ID, job.ID).Stage != api.StageQueued {
			t.Fatal("a paused queue must not start a job")
		}
	}

	if err := m.Resume(); err != nil {
		t.Fatalf("resume: %v", err)
	}
	waitFor(t, "the resumed job to finish", func() bool {
		return jobStage(t, st, user.ID, job.ID).FinishedAt != 0
	})
	if jobStage(t, st, user.ID, job.ID).Stage != api.StageDone {
		t.Fatal("a resumed job should run to done")
	}
}

// TestCancelQueuedCleansInput: cancelling a queued job removes it and deletes
// its staged upload.
func TestCancelQueuedCleansInput(t *testing.T) {
	m, st, _, user := testManager(t)
	if err := m.Pause(); err != nil {
		t.Fatalf("pause: %v", err)
	}
	src := srcFolder(t)
	job, _ := m.Begin(user.ID)
	if err := m.Start(context.Background(), job.ID, src, api.ImportOptions{}); err != nil {
		t.Fatalf("start: %v", err)
	}

	if err := m.Cancel(user.ID, job.ID); err != nil {
		t.Fatalf("cancel queued: %v", err)
	}
	got := jobStage(t, st, user.ID, job.ID)
	if got.Stage != api.StageFailed || got.FinishedAt == 0 {
		t.Fatalf("a cancelled queued job must end, got %+v", got)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("cancelling a queued folder job must clean its staged input, stat err=%v", err)
	}
}

// TestRestartRecovery: a job whose staged input survives is re-queued; one whose
// input is gone is failed.
func TestRestartRecovery(t *testing.T) {
	m, st, _, user := testManager(t)

	// A survivor: an unfinished row whose input dir still exists.
	src := srcFolder(t)
	alive := api.ImportJob{ID: store.NewID(), OwnerID: user.ID, Kind: kindFolder,
		Stage: api.StageReading, StartedAt: 1, QueueSeq: 5}
	if err := st.SaveImportJob(alive); err != nil {
		t.Fatalf("save alive: %v", err)
	}
	if err := st.SetImportJobInput(alive.ID, src, "{}"); err != nil {
		t.Fatalf("set input: %v", err)
	}
	// A lost one: unfinished, input path gone.
	lost := api.ImportJob{ID: store.NewID(), OwnerID: user.ID, Kind: kindFolder,
		Stage: api.StageReading, StartedAt: 1, QueueSeq: 6}
	if err := st.SaveImportJob(lost); err != nil {
		t.Fatalf("save lost: %v", err)
	}
	if err := st.SetImportJobInput(lost.ID, filepath.Join(t.TempDir(), "gone"), "{}"); err != nil {
		t.Fatalf("set input: %v", err)
	}

	runWorkers(t, m) // Run's first act is recovery
	waitFor(t, "recovery to resolve both jobs", func() bool {
		return jobStage(t, st, user.ID, alive.ID).FinishedAt != 0 &&
			jobStage(t, st, user.ID, lost.ID).FinishedAt != 0
	})
	if got := jobStage(t, st, user.ID, alive.ID); got.Stage != api.StageDone {
		t.Fatalf("a job with surviving input must resume and finish, got %q msg=%q", got.Stage, got.Message)
	}
	if got := jobStage(t, st, user.ID, lost.ID); got.Stage != api.StageFailed {
		t.Fatalf("a job whose input is gone must be failed, got %q", got.Stage)
	}
}

// TestLibraryPDFConversion: a library-pdf job writes its CBZ to the uploads dir,
// never touches the read-only library folder, leaves the source PDF in place,
// files an ownerless server-wide comic, and fans out to admins only.
func TestLibraryPDFConversion(t *testing.T) {
	m, st, rec, _ := testManager(t)
	runWorkers(t, m)

	libDir := t.TempDir()
	pdfPath := filepath.Join(libDir, "Some Book.pdf")
	buildPDF(t, pdfPath, [][]byte{makeJPEG(t, 1), makeJPEG(t, 2)})

	m.EnqueueLibraryPDF(pdfPath)

	var job api.ImportJob
	waitFor(t, "the conversion to file a comic", func() bool {
		jobs, err := st.ListAllImportJobs(20)
		if err != nil || len(jobs) != 1 {
			return false
		}
		job = jobs[0]
		return job.Stage == api.StageDone && job.ComicID != ""
	})
	if job.Kind != kindLibraryPDF {
		t.Fatalf("job kind = %q, want %q", job.Kind, kindLibraryPDF)
	}

	// The library folder is read-only by contract: the source PDF stays put and no
	// CBZ is ever written beside it.
	if _, err := os.Stat(pdfPath); err != nil {
		t.Fatalf("the source PDF must be left in place, stat err=%v", err)
	}
	if entries, _ := os.ReadDir(libDir); len(entries) != 1 {
		t.Fatalf("nothing but the PDF may appear in the library folder, got %d entries", len(entries))
	}

	// The comic is server-wide and ownerless, and its CBZ lives in the uploads dir.
	row, err := st.ComicRowByID(job.ComicID)
	if err != nil {
		t.Fatalf("the importer must file a comic row: %v", err)
	}
	if row.Source != store.SourceLibraryPDF {
		t.Fatalf("source = %q, want %q", row.Source, store.SourceLibraryPDF)
	}
	if row.OwnerID != "" {
		t.Fatalf("a library-pdf comic must be ownerless, got owner %q", row.OwnerID)
	}
	if _, err := os.Stat(filepath.Join(m.cfg.UploadsDir, row.Path)); err != nil {
		t.Fatalf("the CBZ must live in the uploads dir: %v", err)
	}

	// The job reached admins, and never an owner (it has none).
	found := false
	for _, j := range rec.adminJobs() {
		if j.Kind == kindLibraryPDF {
			found = true
		}
	}
	if !found {
		t.Fatal("an ownerless library-pdf job must fan out to admins")
	}
	if len(rec.recipients()) != 0 {
		t.Fatalf("an ownerless job must not be addressed to any user, got %v", rec.recipients())
	}
}

// TestLibraryPDFReimportDeduped: the source PDF is never deleted, so a later
// hand-off of the same file (a restart re-scanning the folder) must not produce a
// second comic. The enqueue-time skip covers the common case; here the job
// history is wiped first to force the content-hash backstop at filing time.
func TestLibraryPDFReimportDeduped(t *testing.T) {
	m, st, _, _ := testManager(t)
	runWorkers(t, m)

	libDir := t.TempDir()
	pdfPath := filepath.Join(libDir, "Some Book.pdf")
	buildPDF(t, pdfPath, [][]byte{makeJPEG(t, 1), makeJPEG(t, 2)})

	m.EnqueueLibraryPDF(pdfPath)
	var firstID string
	waitFor(t, "the first conversion to finish", func() bool {
		jobs, _ := st.ListAllImportJobs(20)
		if len(jobs) == 1 && jobs[0].Stage == api.StageDone && jobs[0].ComicID != "" {
			firstID = jobs[0].ComicID
			return true
		}
		return false
	})

	// Clear the job history so the enqueue-time skip cannot see the earlier import;
	// only the content-hash check at filing time can now catch the duplicate.
	if err := st.DeleteFinishedImportJobs("", true); err != nil {
		t.Fatalf("clear jobs: %v", err)
	}

	m.EnqueueLibraryPDF(pdfPath)
	waitFor(t, "the second conversion to finish", func() bool {
		jobs, _ := st.ListAllImportJobs(20)
		return len(jobs) == 1 && jobs[0].Stage == api.StageDone
	})
	jobs, _ := st.ListAllImportJobs(20)
	if got := jobs[0].ComicID; got != firstID {
		t.Fatalf("the re-import must point at the existing comic %q, got %q", firstID, got)
	}
	comics, err := st.ListComics("")
	if err != nil {
		t.Fatalf("list comics: %v", err)
	}
	if len(comics) != 1 {
		t.Fatalf("re-importing an unchanged PDF must not duplicate the comic, got %d", len(comics))
	}
}

// TestDrainLeavesJobUnfinished: when the base context is cancelled (shutdown),
// an in-flight or queued job is never marked failed — it is left for a restart
// to resume. This is the opposite of a user cancel, which is terminal (failed),
// as TestManagerCancel covers.
func TestDrainLeavesJobUnfinished(t *testing.T) {
	m, st, _, user := testManager(t)
	job, _ := m.Begin(user.ID)
	if err := m.Start(context.Background(), job.ID, srcFolder(t), api.ImportOptions{}); err != nil {
		t.Fatalf("start: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go m.Run(ctx)
	// Cancel almost immediately: the job is either still queued or mid-flight,
	// both of which drain rather than fail.
	cancel()

	// Let the worker unwind.
	waitFor(t, "the queue to settle after drain", func() bool {
		g := jobStage(t, st, user.ID, job.ID)
		return g.Stage == api.StageQueued || g.Stage == api.StageDone
	})
	got := jobStage(t, st, user.ID, job.ID)
	if got.Stage == api.StageFailed {
		t.Fatalf("a drained job must not be failed; failing is the user-cancel outcome, got %+v", got)
	}
	if got.Stage == api.StageQueued && got.FinishedAt != 0 {
		t.Fatal("a drained-but-queued job must stay unfinished so a restart resumes it")
	}
}

// TestEnqueueLibraryPDFDedupes: the same path handed off twice queues one job.
func TestEnqueueLibraryPDFDedupes(t *testing.T) {
	m, st, _, _ := testManager(t)
	if err := m.Pause(); err != nil { // hold it queued so both hand-offs race the same state
		t.Fatalf("pause: %v", err)
	}
	pdfPath := filepath.Join(t.TempDir(), "book.pdf")
	if err := os.WriteFile(pdfPath, []byte("%PDF-1.4"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.EnqueueLibraryPDF(pdfPath)
	m.EnqueueLibraryPDF(pdfPath)
	all, _ := st.ListAllImportJobs(20)
	if len(all) != 1 {
		t.Fatalf("the same PDF handed off twice must queue one job, got %d", len(all))
	}
}
