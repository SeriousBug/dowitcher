package server

import (
	"testing"

	"github.com/SeriousBug/dowitcher/internal/api"
)

func hubAdminClient(h *Hub, userID string) *wsClient {
	c := &wsClient{send: make(chan []byte, 32), userID: userID, isAdmin: true}
	h.add(c)
	return c
}

// TestOwnerlessJobReachesAdminsOnly: a library-pdf job has no owner, so it is
// fanned out with BroadcastToAdmins. An admin's socket receives it; a non-admin's
// does not — the queue those jobs live in is an admin's to manage.
func TestOwnerlessJobReachesAdminsOnly(t *testing.T) {
	h := newHub()
	admin := hubAdminClient(h, "admin-id")
	member := hubClient(h, "member-id")

	job := api.ImportJob{ID: "lib-1", Kind: "library-pdf", Name: "Some Book", Stage: api.StageExtracting}
	h.BroadcastToAdmins(api.WSMessage{Type: api.WSTypeJob, Job: &job})

	got := drain(admin)
	if len(got) != 1 || got[0].Job == nil || got[0].Job.ID != "lib-1" {
		t.Fatalf("an admin must receive an ownerless job, got %#v", got)
	}
	if leaked := drain(member); len(leaked) != 0 {
		t.Fatalf("an ownerless import reached a non-admin: %#v", leaked)
	}
}

// TestQueuePausedIsCachedAndReplayed: the paused flag is server-wide, so unlike
// job frames it is cached and replayed to whoever connects next.
func TestQueuePausedIsCachedAndReplayed(t *testing.T) {
	h := newHub()
	h.Broadcast(api.WSMessage{Type: api.WSTypeQueue, Queue: &api.QueueState{Paused: true}})

	late := hubClient(h, "someone")
	got := drain(late)
	var seen *bool
	for _, m := range got {
		if m.Type == api.WSTypeQueue && m.Queue != nil {
			p := m.Queue.Paused
			seen = &p
		}
	}
	if seen == nil || !*seen {
		t.Fatalf("a late client must be replayed the cached paused flag, got %#v", got)
	}
}
