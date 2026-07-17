package imports

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/SeriousBug/longbox/internal/api"
	"github.com/SeriousBug/longbox/internal/store"
)

// recorder collects what a job pushed to the hub, and who it was addressed to.
type recorder struct {
	mu   sync.Mutex
	msgs []api.WSMessage
	to   []string
}

func (r *recorder) BroadcastTo(userID string, m api.WSMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, m)
	r.to = append(r.to, userID)
}

// recipients is who the hub was asked to deliver to, in order.
func (r *recorder) recipients() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.to...)
}

func (r *recorder) jobs() []api.ImportJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []api.ImportJob
	for _, m := range r.msgs {
		if m.Job != nil {
			out = append(out, *m.Job)
		}
	}
	return out
}

func testManager(t *testing.T) (*Manager, *store.Store, *recorder, api.User) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	user, err := st.CreateUser(store.NewID(), "alice", false)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	rec := &recorder{}
	dir := t.TempDir()
	m, err := NewManager(st, rec, ManagerConfig{
		UploadsDir: filepath.Join(dir, "uploads"),
		ReportDir:  filepath.Join(dir, "reports"),
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return m, st, rec, user
}

// srcFolder writes a folder of images with one exact duplicate pair, so a real
// import has something to report.
func srcFolder(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "My Comic")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, seed := range []int64{1, 2, 3} {
		writePNG(t, filepath.Join(dir, string(rune('a'+i))+"1.png"), synth(64, 96, seed, 0))
	}
	// A byte-identical copy of the first page: the dedupe pass must fold it away.
	writePNG(t, filepath.Join(dir, "a2.png"), synth(64, 96, 1, 0))
	return root
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func jobStage(t *testing.T, st *store.Store, userID, jobID string) api.ImportJob {
	t.Helper()
	j, err := st.GetImportJob(userID, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	return j
}

// TestManagerRunsAnImportEndToEnd: an uploaded folder becomes an owned comic,
// the job says so, and the dupe report survives it.
func TestManagerRunsAnImportEndToEnd(t *testing.T) {
	m, st, rec, user := testManager(t)
	src := srcFolder(t)

	job, err := m.Begin(user.ID)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if job.Stage != api.StageUploading {
		t.Fatalf("a job starts as uploading, got %q", job.Stage)
	}
	if err := m.Start(context.Background(), job.ID, src, api.ImportOptions{}); err != nil {
		t.Fatalf("start: %v", err)
	}

	waitFor(t, "the job to finish", func() bool {
		return jobStage(t, st, user.ID, job.ID).FinishedAt != 0
	})
	done := jobStage(t, st, user.ID, job.ID)
	if done.Stage != api.StageDone {
		t.Fatalf("job stage = %q message=%q, want done", done.Stage, done.Message)
	}
	if done.ComicID == "" {
		t.Fatal("a finished import must name the comic it produced")
	}
	if done.PageCount != 3 || done.SourceCount != 4 || done.ExactDupes != 1 {
		t.Fatalf("job counts = pages %d, sources %d, exact %d; want 3/4/1",
			done.PageCount, done.SourceCount, done.ExactDupes)
	}
	// The name comes from the uploaded folder, not the temp dir it landed in.
	if done.Name != "My Comic" {
		t.Fatalf("job name = %q, want the uploaded folder's name", done.Name)
	}

	comic, err := st.GetComic(user.ID, done.ComicID)
	if err != nil {
		t.Fatalf("the import's comic should be visible to its uploader: %v", err)
	}
	if comic.PageCount != 3 {
		t.Fatalf("comic page count = %d, want 3", comic.PageCount)
	}
	row, err := st.ComicRowByID(done.ComicID)
	if err != nil {
		t.Fatalf("comic row: %v", err)
	}
	if row.Source != store.SourceUpload || row.OwnerID != user.ID {
		t.Fatalf("row = source %q owner %q, want an upload owned by the uploader", row.Source, row.OwnerID)
	}
	if _, err := os.Stat(filepath.Join(m.cfg.UploadsDir, row.Path)); err != nil {
		t.Fatalf("the CBZ should be filed in the uploads dir: %v", err)
	}
	// The uploaded images are the job's to clean up.
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("the upload dir should be gone after the import, got err=%v", err)
	}

	groups, err := m.Dupes(user.ID, job.ID)
	if err != nil {
		t.Fatalf("dupes: %v", err)
	}
	if len(groups) != 1 || len(groups[0].Dropped) != 1 || groups[0].Reason != "exact" {
		t.Fatalf("dupe report = %#v, want one exact group", groups)
	}

	// Progress reached the hub, and the last word on the job is that it is done.
	pushed := rec.jobs()
	if len(pushed) == 0 {
		t.Fatal("the job pushed nothing to the hub")
	}
	if last := pushed[len(pushed)-1]; last.Stage != api.StageDone {
		t.Fatalf("last pushed stage = %q, want done", last.Stage)
	}
}

// TestManagerRecoversOrphanedJobs is the restart case: a row still marked
// running is a lie, because the goroutine behind it died with the process.
func TestManagerRecoversOrphanedJobs(t *testing.T) {
	m, st, _, user := testManager(t)

	orphan := api.ImportJob{
		ID: store.NewID(), OwnerID: user.ID, Name: "half an import",
		Stage: api.StageReading, Done: 12, Total: 300, StartedAt: time.Now().Unix(),
	}
	if err := st.SaveImportJob(orphan); err != nil {
		t.Fatalf("save orphan: %v", err)
	}
	finished := api.ImportJob{
		ID: store.NewID(), OwnerID: user.ID, Name: "a real one",
		Stage: api.StageDone, StartedAt: time.Now().Unix(), FinishedAt: time.Now().Unix(),
	}
	if err := st.SaveImportJob(finished); err != nil {
		t.Fatalf("save finished: %v", err)
	}

	// A second Manager over the same store is what a restart looks like.
	if _, err := NewManager(st, nil, m.cfg); err != nil {
		t.Fatalf("restart: %v", err)
	}

	got := jobStage(t, st, user.ID, orphan.ID)
	if got.Stage != api.StageFailed {
		t.Fatalf("an orphaned job must be failed on startup, got %q", got.Stage)
	}
	if got.FinishedAt == 0 || got.Message == "" {
		t.Fatalf("a recovered job needs an end and a reason, got %+v", got)
	}
	// A job that already ended is history and must not be rewritten.
	if got := jobStage(t, st, user.ID, finished.ID); got.Stage != api.StageDone {
		t.Fatalf("a finished job must survive a restart untouched, got %q", got.Stage)
	}
}

// TestManagerCancel: cancelling reaches the pipeline through the context, and a
// job nobody is running cannot be cancelled twice.
func TestManagerCancel(t *testing.T) {
	m, st, _, user := testManager(t)
	src := srcFolder(t)

	job, err := m.Begin(user.ID)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := m.Start(context.Background(), job.ID, src, api.ImportOptions{}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := m.Cancel(user.ID, job.ID); err != nil && !errors.Is(err, ErrNotRunning) {
		t.Fatalf("cancel: %v", err)
	}
	waitFor(t, "the cancelled job to end", func() bool {
		return jobStage(t, st, user.ID, job.ID).FinishedAt != 0
	})
	// The import is small enough that it may beat the cancel; either outcome is
	// legitimate, but it must end and it must not still claim to be running.
	got := jobStage(t, st, user.ID, job.ID)
	if got.Stage != api.StageFailed && got.Stage != api.StageDone {
		t.Fatalf("job stage = %q, want failed or done", got.Stage)
	}
	if err := m.Cancel(user.ID, job.ID); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("cancelling a finished job should report it is not running, got %v", err)
	}
}

// TestManagerJobsArePrivate: an import is scoped to whoever started it.
func TestManagerJobsArePrivate(t *testing.T) {
	m, st, _, alice := testManager(t)
	bob, err := st.CreateUser(store.NewID(), "bob", false)
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	job, err := m.Begin(alice.ID)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := m.Cancel(bob.ID, job.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("bob must not cancel alice's import, got %v", err)
	}
	if _, err := m.Dupes(bob.ID, job.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("bob must not read alice's dupe report, got %v", err)
	}
	if jobs := m.JobSnapshot(bob.ID); len(jobs) != 0 {
		t.Fatalf("bob's snapshot should be empty, got %#v", jobs)
	}
	if jobs := m.JobSnapshot(alice.ID); len(jobs) != 1 || jobs[0].ID != job.ID {
		t.Fatalf("alice's snapshot = %#v, want her one job", jobs)
	}
}

// TestManagerSnapshotPrefersLiveState: a running job's row is only rewritten on
// a stage change, so the snapshot has to overlay what this process knows.
func TestManagerSnapshotPrefersLiveState(t *testing.T) {
	m, _, _, user := testManager(t)
	job, err := m.Begin(user.ID)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	m.Uploaded(job.ID, 42)
	jobs := m.JobSnapshot(user.ID)
	if len(jobs) != 1 || jobs[0].Done != 42 {
		t.Fatalf("snapshot = %#v, want the live upload count", jobs)
	}
	m.Fail(job.ID, "nope")
	jobs = m.JobSnapshot(user.ID)
	if len(jobs) != 1 || jobs[0].Stage != api.StageFailed || jobs[0].FinishedAt == 0 {
		t.Fatalf("snapshot = %#v, want the failed job", jobs)
	}
}

// TestManagerCapsConcurrentImports: each import fans its decode out over every
// core, so a client looping the upload endpoint would otherwise run as many as
// it liked. The cap is enforced at Begin, before any bytes are uploaded.
func TestManagerCapsConcurrentImports(t *testing.T) {
	m, st, _, alice := testManager(t)
	bob, err := st.CreateUser(store.NewID(), "bob", false)
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}

	var jobs []api.ImportJob
	for i := range maxPerUser {
		j, err := m.Begin(alice.ID)
		if err != nil {
			t.Fatalf("begin %d: %v", i, err)
		}
		jobs = append(jobs, j)
	}
	if _, err := m.Begin(alice.ID); !errors.Is(err, ErrTooManyImports) {
		t.Fatalf("begin past the cap = %v, want ErrTooManyImports", err)
	}
	// The cap is per user, not server-wide.
	if _, err := m.Begin(bob.ID); err != nil {
		t.Fatalf("bob's first import must not be refused for alice's: %v", err)
	}
	// A refused import leaves no history behind: it never started.
	if got := len(m.JobSnapshot(alice.ID)); got != maxPerUser {
		t.Fatalf("alice has %d jobs, want %d — a refused Begin wrote a row", got, maxPerUser)
	}

	// Ending one frees the slot.
	m.Fail(jobs[0].ID, "done with this one")
	if _, err := m.Begin(alice.ID); err != nil {
		t.Fatalf("a finished import must free the slot: %v", err)
	}
}

func TestIsImageName(t *testing.T) {
	for _, name := range []string{"a.png", "A.JPG", "x/y/z.webp", "p.avif"} {
		if !IsImageName(name) {
			t.Errorf("IsImageName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"a.txt", "notes", "cover.psd", "a.png.exe"} {
		if IsImageName(name) {
			t.Errorf("IsImageName(%q) = true, want false", name)
		}
	}
}

// TestJobsAreAddressedToTheirOwner: every frame a job pushes names the user who
// started it. A job carries the uploader's folder name and the comic it made,
// and the hub fans an unaddressed message out to every socket on the server.
func TestJobsAreAddressedToTheirOwner(t *testing.T) {
	m, _, rec, user := testManager(t)
	job, err := m.Begin(user.ID)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	m.Uploaded(job.ID, 3)
	m.Fail(job.ID, "done here")

	got := rec.recipients()
	if len(got) == 0 {
		t.Fatal("the job pushed nothing to the hub")
	}
	for i, to := range got {
		if to != user.ID {
			t.Fatalf("frame %d was addressed to %q, want the job's owner %q; "+
				"an unowned frame reaches every connected client", i, to, user.ID)
		}
	}
}
