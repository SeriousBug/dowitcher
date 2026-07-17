package server

import (
	"encoding/json"
	"testing"

	"github.com/SeriousBug/dowitcher/internal/api"
)

// drain reads everything queued on a client without blocking.
func drain(c *wsClient) []api.WSMessage {
	var out []api.WSMessage
	for {
		select {
		case data := <-c.send:
			var m api.WSMessage
			if err := json.Unmarshal(data, &m); err == nil {
				out = append(out, m)
			}
		default:
			return out
		}
	}
}

func hubClient(h *Hub, userID string) *wsClient {
	c := &wsClient{send: make(chan []byte, 32), userID: userID}
	h.add(c)
	return c
}

// TestImportJobsDoNotReachOtherUsers is the leak itself: the jobs snapshot a
// client gets on connect is per-user, but the live stream was fanned out to
// every socket, so another user's imports appeared in your list — their owner
// id, the comic id, the stage and counts, and the folder name they picked,
// which is usually the sensitive part.
func TestImportJobsDoNotReachOtherUsers(t *testing.T) {
	h := newHub()
	alice := hubClient(h, "alice-id")
	bob := hubClient(h, "bob-id")

	job := api.ImportJob{
		ID: "job-1", OwnerID: "alice-id", Name: "Alice's private folder",
		Stage: api.StageReading, ComicID: "comic-1", Done: 3, Total: 10,
	}
	h.BroadcastTo(job.OwnerID, api.WSMessage{Type: api.WSTypeJob, Job: &job})

	got := drain(alice)
	if len(got) != 1 || got[0].Job == nil || got[0].Job.ID != "job-1" {
		t.Fatalf("the owner must receive their own job, got %#v", got)
	}
	if leaked := drain(bob); len(leaked) != 0 {
		t.Fatalf("another user's import reached bob: %#v — this exposes the "+
			"uploader's folder name, comic id, stage and counts", leaked)
	}
}

// TestLibraryStatusStaysGlobal: the scanner's progress is a property of the
// server, so narrowing the job stream must not have narrowed this too.
func TestLibraryStatusStaysGlobal(t *testing.T) {
	h := newHub()
	alice := hubClient(h, "alice-id")
	bob := hubClient(h, "bob-id")

	h.Broadcast(api.WSMessage{Type: api.WSTypeLibrary, Library: &api.LibraryStatus{Scanning: true}})

	for name, c := range map[string]*wsClient{"alice": alice, "bob": bob} {
		got := drain(c)
		if len(got) != 1 || got[0].Type != api.WSTypeLibrary {
			t.Fatalf("%s should see library status, got %#v", name, got)
		}
	}
}

// TestReplayCacheNeverHoldsPerUserPayloads pins the other half of the hazard.
// The cache is keyed by message type with no per-user dimension, so anything
// that lands in it is handed to whoever connects next.
func TestReplayCacheNeverHoldsPerUserPayloads(t *testing.T) {
	h := newHub()

	job := api.ImportJob{ID: "job-1", OwnerID: "alice-id", Name: "Alice's private folder"}
	h.BroadcastTo("alice-id", api.WSMessage{Type: api.WSTypeJob, Job: &job})
	// A comics payload is filtered by visibility. Nothing produces one today;
	// this pins the rule for whoever writes that producer.
	h.Broadcast(api.WSMessage{Type: api.WSTypeComics, Comics: []api.Comic{{ID: "c1", Title: "Alice's upload"}}})

	// A client connecting afterwards is replayed the cache.
	late := hubClient(h, "bob-id")
	for _, m := range drain(late) {
		if m.Type != api.WSTypeLibrary {
			t.Fatalf("a %q payload was replayed to a user who never asked for it: %#v; "+
				"only state identical for every user may be cached", m.Type, m)
		}
	}
}
