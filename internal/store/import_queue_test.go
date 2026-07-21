package store

import (
	"strconv"
	"testing"

	"github.com/SeriousBug/dowitcher/internal/api"
)

// TestImportJobsRebuildKeepsSchemaCurrent proves the owner_id-nullable rebuild
// landed: schema_version equals the migration count, and a job can be saved with
// no owner (a library-pdf job) where the pre-rebuild NOT NULL would have refused
// it. The new columns default to their zero-ish values on a round trip.
func TestImportJobsRebuildKeepsSchemaCurrent(t *testing.T) {
	st := testStore(t)

	var v string
	if err := st.db.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&v); err != nil {
		t.Fatalf("schema_version: %v", err)
	}
	if n, _ := strconv.Atoi(v); n != len(migrations) {
		t.Fatalf("schema_version = %s, want %d", v, len(migrations))
	}

	// An ownerless job — the whole reason for the rebuild.
	own := api.ImportJob{ID: NewID(), Kind: "library-pdf", Stage: api.StageQueued, StartedAt: 1}
	if err := st.SaveImportJob(own); err != nil {
		t.Fatalf("save ownerless job: %v", err)
	}
	got, err := st.getAnyImportJob(own.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.OwnerID != "" || got.Kind != "library-pdf" || got.Stage != api.StageQueued {
		t.Fatalf("round trip = %+v, want ownerless library-pdf queued", got)
	}
}

// getAnyImportJob reads a job regardless of owner, for tests that need the
// ownerless rows no per-user accessor returns.
func (s *Store) getAnyImportJob(id string) (api.ImportJob, error) {
	rows, err := s.db.Query(`SELECT `+jobCols+` FROM import_jobs WHERE id=?`, id)
	if err != nil {
		return api.ImportJob{}, err
	}
	out, err := scanJobs(rows)
	if err != nil {
		return api.ImportJob{}, err
	}
	if len(out) == 0 {
		return api.ImportJob{}, ErrNotFound
	}
	return out[0], nil
}

// TestSetImportJobInputRoundTrips: the server-only input_path/options survive a
// write and come back through the recovery accessor, but never through jobCols.
func TestSetImportJobInputRoundTrips(t *testing.T) {
	st := testStore(t)
	user, _ := st.CreateUser(NewID(), "alice", false)
	j := api.ImportJob{ID: NewID(), OwnerID: user.ID, Kind: "folder", Stage: api.StageQueued, StartedAt: 1}
	if err := st.SaveImportJob(j); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := st.SetImportJobInput(j.ID, "/tmp/staged", `{"name":"X"}`); err != nil {
		t.Fatalf("set input: %v", err)
	}
	// A progress save must not blank the input it does not carry.
	j.Stage = api.StageReading
	if err := st.SaveImportJob(j); err != nil {
		t.Fatalf("re-save: %v", err)
	}

	recs, err := st.ListRecoverableImportJobs()
	if err != nil {
		t.Fatalf("list recoverable: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("recoverable = %d, want 1", len(recs))
	}
	if recs[0].InputPath != "/tmp/staged" || recs[0].Options != `{"name":"X"}` {
		t.Fatalf("input/options = %q/%q, want they survived the re-save", recs[0].InputPath, recs[0].Options)
	}
}

// TestListAllImportJobsSpansOwners: the admin snapshot includes ownerless jobs
// no per-user query returns, newest first.
func TestListAllImportJobsSpansOwners(t *testing.T) {
	st := testStore(t)
	alice, _ := st.CreateUser(NewID(), "alice", false)
	mustSave(t, st, api.ImportJob{ID: NewID(), OwnerID: alice.ID, Stage: api.StageDone, StartedAt: 1, FinishedAt: 2})
	mustSave(t, st, api.ImportJob{ID: NewID(), Kind: "library-pdf", Stage: api.StageDone, StartedAt: 3, FinishedAt: 4})

	all, err := st.ListAllImportJobs(20)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all jobs = %d, want 2 across owners", len(all))
	}
	// Newest first: the library-pdf job started later.
	if all[0].Kind != "library-pdf" {
		t.Fatalf("first = %+v, want the newer library-pdf job", all[0])
	}
	// A per-user query still hides the ownerless one.
	mine, _ := st.ListImportJobs(alice.ID)
	if len(mine) != 1 {
		t.Fatalf("alice's jobs = %d, want only her own", len(mine))
	}
}

// TestDeleteFinishedImportJobs: an owner clears their own finished jobs; an
// unfinished one and another owner's stay unless the admin-all flag is set.
func TestDeleteFinishedImportJobs(t *testing.T) {
	st := testStore(t)
	alice, _ := st.CreateUser(NewID(), "alice", false)
	bob, _ := st.CreateUser(NewID(), "bob", false)
	aDone := api.ImportJob{ID: NewID(), OwnerID: alice.ID, Stage: api.StageDone, StartedAt: 1, FinishedAt: 2}
	aRunning := api.ImportJob{ID: NewID(), OwnerID: alice.ID, Stage: api.StageReading, StartedAt: 1}
	bDone := api.ImportJob{ID: NewID(), OwnerID: bob.ID, Stage: api.StageDone, StartedAt: 1, FinishedAt: 2}
	ownerless := api.ImportJob{ID: NewID(), Kind: "library-pdf", Stage: api.StageDone, StartedAt: 1, FinishedAt: 2}
	for _, j := range []api.ImportJob{aDone, aRunning, bDone, ownerless} {
		mustSave(t, st, j)
	}

	if err := st.DeleteFinishedImportJobs(alice.ID, false); err != nil {
		t.Fatalf("delete alice's finished: %v", err)
	}
	if mine, _ := st.ListImportJobs(alice.ID); len(mine) != 1 || mine[0].ID != aRunning.ID {
		t.Fatalf("alice keeps only her running job, got %#v", mine)
	}
	if theirs, _ := st.ListImportJobs(bob.ID); len(theirs) != 1 {
		t.Fatalf("bob's finished job must survive alice's clear, got %#v", theirs)
	}

	if err := st.DeleteFinishedImportJobs("", true); err != nil {
		t.Fatalf("admin clear all: %v", err)
	}
	all, _ := st.ListAllImportJobs(20)
	if len(all) != 1 || all[0].ID != aRunning.ID {
		t.Fatalf("admin-all clears every finished job incl ownerless, leaving the running one; got %#v", all)
	}
}

// TestQueuePaused: the flag defaults off, persists, and toggles.
func TestQueuePaused(t *testing.T) {
	st := testStore(t)
	if p, err := st.QueuePaused(); err != nil || p {
		t.Fatalf("QueuePaused default = %v err=%v, want false", p, err)
	}
	if err := st.SetQueuePaused(true); err != nil {
		t.Fatalf("set paused: %v", err)
	}
	if p, _ := st.QueuePaused(); !p {
		t.Fatal("QueuePaused = false after SetQueuePaused(true)")
	}
	if err := st.SetQueuePaused(false); err != nil {
		t.Fatalf("unpause: %v", err)
	}
	if p, _ := st.QueuePaused(); p {
		t.Fatal("QueuePaused = true after SetQueuePaused(false)")
	}
}

func mustSave(t *testing.T, st *Store, j api.ImportJob) {
	t.Helper()
	if err := st.SaveImportJob(j); err != nil {
		t.Fatalf("save job: %v", err)
	}
}
