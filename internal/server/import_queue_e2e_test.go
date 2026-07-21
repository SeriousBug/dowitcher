package server

import (
	"testing"

	"github.com/SeriousBug/dowitcher/internal/api"
)

// TestQueueControlsAreAdminOnly: pausing, resuming and reordering the queue act
// on jobs the caller may not own, so they are gated on admin. A member is
// refused; the admin is not.
func TestQueueControlsAreAdminOnly(t *testing.T) {
	ts, st, _ := importServer(t, nil)
	admin := adminClient(t, ts, st)
	member := enrolledUser(t, ts, admin, "member")

	order := mustJSON(t, api.ReorderQueueRequest{JobIDs: []string{}})
	cases := []struct {
		method, path string
		body         []byte
	}{
		{"POST", "/api/imports/queue/pause", nil},
		{"POST", "/api/imports/queue/resume", nil},
		{"PUT", "/api/imports/queue/order", order},
	}
	for _, c := range cases {
		if resp, body := sendJSON(t, member, c.method, ts.URL+c.path, c.body); resp.StatusCode != 403 {
			t.Fatalf("member %s %s = %d %s, want 403", c.method, c.path, resp.StatusCode, body)
		}
		if resp, body := sendJSON(t, admin, c.method, ts.URL+c.path, c.body); resp.StatusCode != 200 {
			t.Fatalf("admin %s %s = %d %s, want 200", c.method, c.path, resp.StatusCode, body)
		}
	}
}

// TestClearFinishedIsPerCaller: any authenticated user may clear finished jobs;
// the store scopes what is deleted to the caller (or all, for an admin).
func TestClearFinishedIsPerCaller(t *testing.T) {
	ts, st, _ := importServer(t, nil)
	admin := adminClient(t, ts, st)
	member := enrolledUser(t, ts, admin, "member")

	// A member's own finished job, and an ownerless one only an admin-all clear
	// removes.
	memberID := userID(t, st, "member")
	mine := api.ImportJob{ID: "m-done", OwnerID: memberID, Stage: api.StageDone, StartedAt: 1, FinishedAt: 2}
	if err := st.SaveImportJob(mine); err != nil {
		t.Fatalf("save member job: %v", err)
	}
	ownerless := api.ImportJob{ID: "lib-done", Kind: "library-pdf", Stage: api.StageDone, StartedAt: 1, FinishedAt: 2}
	if err := st.SaveImportJob(ownerless); err != nil {
		t.Fatalf("save ownerless job: %v", err)
	}

	if resp, body := sendJSON(t, member, "DELETE", ts.URL+"/api/imports/finished", nil); resp.StatusCode != 200 {
		t.Fatalf("member clear = %d %s, want 200", resp.StatusCode, body)
	}
	if jobs, _ := st.ListImportJobs(memberID); len(jobs) != 0 {
		t.Fatalf("member's finished job should be cleared, got %#v", jobs)
	}
	// The member's clear left the ownerless job alone.
	all, _ := st.ListAllImportJobs(20)
	if len(all) != 1 || all[0].ID != "lib-done" {
		t.Fatalf("a member's clear must not touch ownerless jobs, got %#v", all)
	}

	// The admin's clear sweeps everything finished, ownerless included.
	if resp, body := sendJSON(t, admin, "DELETE", ts.URL+"/api/imports/finished", nil); resp.StatusCode != 200 {
		t.Fatalf("admin clear = %d %s, want 200", resp.StatusCode, body)
	}
	if all, _ := st.ListAllImportJobs(20); len(all) != 0 {
		t.Fatalf("admin clear-all must remove ownerless finished jobs too, got %#v", all)
	}
}
