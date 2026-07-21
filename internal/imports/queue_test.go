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

// TestLibraryPDFConversion: a library-pdf job writes a CBZ beside the source,
// deletes the PDF, files no comic row, and fans out to admins only.
func TestLibraryPDFConversion(t *testing.T) {
	m, st, rec, _ := testManager(t)
	runWorkers(t, m)

	libDir := t.TempDir()
	pdfPath := filepath.Join(libDir, "Some Book.pdf")
	buildPDF(t, pdfPath, [][]byte{makeJPEG(t, 1), makeJPEG(t, 2)})

	m.EnqueueLibraryPDF(pdfPath)

	cbzPath := filepath.Join(libDir, "Some Book.cbz")
	waitFor(t, "the CBZ to appear", func() bool {
		_, err := os.Stat(cbzPath)
		return err == nil
	})
	// The source PDF is deleted only after the CBZ is confirmed.
	if _, err := os.Stat(pdfPath); !os.IsNotExist(err) {
		t.Fatalf("the source PDF must be deleted after conversion, stat err=%v", err)
	}
	// No comic row is filed by the importer: the scanner adopts the CBZ.
	comics, err := st.ListAllImportJobs(20)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(comics) != 1 || comics[0].Kind != kindLibraryPDF || comics[0].ComicID != "" {
		t.Fatalf("library-pdf job = %#v, want one ownerless job with no comic id", comics)
	}
	// The row exists in no comics table entry.
	if _, err := st.ComicRowByPath("Some Book.cbz"); err == nil {
		t.Fatal("the importer must not file a comic row; the scanner does that")
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
