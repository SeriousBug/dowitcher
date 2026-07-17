package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SeriousBug/longbox/internal/api"
	"github.com/SeriousBug/longbox/internal/store"
)

// putProgress is the sync call an offline client replays.
func putProgress(t *testing.T, c *http.Client, ts *httptest.Server, comicID string, req api.ProgressRequest) api.Progress {
	t.Helper()
	resp, body := sendJSON(t, c, http.MethodPut, ts.URL+"/api/comics/"+comicID+"/progress", mustJSON(t, req))
	if resp.StatusCode != 200 {
		t.Fatalf("put progress: %d %s", resp.StatusCode, body)
	}
	var p api.Progress
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode progress: %v (%s)", err, body)
	}
	return p
}

// TestProgressClientTimestampWins is the base case for the offline queue: the
// stored row is ordered by when the client read the page, not when it synced.
func TestProgressClientTimestampWins(t *testing.T) {
	_, ts, st, cfg := libraryServer(t, nil)
	alice := adminClient(t, ts, st)
	row, _ := addComic(t, st, cfg.LibraryRoot, "Series/One.cbz", 10, store.ComicRow{})

	read := time.Now().Unix() - 3600
	p := putProgress(t, alice, ts, row.ID, api.ProgressRequest{Page: 4, UpdatedAt: read})
	if p.Page != 4 {
		t.Fatalf("page = %d, want 4", p.Page)
	}
	if p.UpdatedAt != read {
		t.Fatalf("updatedAt = %d, want the client's %d", p.UpdatedAt, read)
	}
	stored, err := st.GetProgress(userID(t, st, "Alice"), row.ID)
	if err != nil {
		t.Fatalf("get progress: %v", err)
	}
	if stored.UpdatedAt != read {
		t.Fatalf("stored updatedAt = %d, want the client's %d", stored.UpdatedAt, read)
	}
}

// TestProgressStaleWriteLosesPage is the phone-in-a-tunnel case: a queued write
// replayed after the desktop moved on must not drag the reader backwards.
func TestProgressStaleWriteLosesPage(t *testing.T) {
	_, ts, st, cfg := libraryServer(t, nil)
	alice := adminClient(t, ts, st)
	row, _ := addComic(t, st, cfg.LibraryRoot, "Series/One.cbz", 10, store.ComicRow{})

	now := time.Now().Unix()
	putProgress(t, alice, ts, row.ID, api.ProgressRequest{Page: 7, UpdatedAt: now})

	p := putProgress(t, alice, ts, row.ID, api.ProgressRequest{Page: 2, UpdatedAt: now - 3600})
	if p.Page != 7 {
		t.Fatalf("page = %d, want the newer 7: an hour-old write must not clobber the current position", p.Page)
	}
	if p.UpdatedAt != now {
		t.Fatalf("updatedAt = %d, want the newer %d", p.UpdatedAt, now)
	}
	// The reply is the truth the replaying client converges on, so it has to
	// match what is stored.
	stored, err := st.GetProgress(userID(t, st, "Alice"), row.ID)
	if err != nil {
		t.Fatalf("get progress: %v", err)
	}
	if stored.Page != 7 || stored.UpdatedAt != now {
		t.Fatalf("stored = page %d at %d, want page 7 at %d", stored.Page, stored.UpdatedAt, now)
	}
}

// TestProgressStaleWriteStillCompletes: finishing a comic offline is real. The
// stale write loses its page and keeps its completion.
func TestProgressStaleWriteStillCompletes(t *testing.T) {
	_, ts, st, cfg := libraryServer(t, nil)
	alice := adminClient(t, ts, st)
	row, _ := addComic(t, st, cfg.LibraryRoot, "Series/One.cbz", 10, store.ComicRow{})

	now := time.Now().Unix()
	putProgress(t, alice, ts, row.ID, api.ProgressRequest{Page: 3, UpdatedAt: now})

	// Read to the last page on the plane, synced an hour late.
	p := putProgress(t, alice, ts, row.ID, api.ProgressRequest{Page: 9, UpdatedAt: now - 3600})
	if !p.Completed {
		t.Fatal("a stale write that reached the last page must still mark the comic completed")
	}
	if p.Page != 3 {
		t.Fatalf("page = %d, want the newer 3: completion carries, the position does not", p.Page)
	}
	if p.UpdatedAt != now {
		t.Fatalf("updatedAt = %d, want the newer %d: completing must not make the row look older", p.UpdatedAt, now)
	}

	// An explicit completed=true from a stale write lands the same way.
	putProgress(t, alice, ts, row.ID, api.ProgressRequest{Page: 3, UpdatedAt: now})
	p = putProgress(t, alice, ts, row.ID, api.ProgressRequest{Page: 1, Completed: true, UpdatedAt: now - 60})
	if !p.Completed || p.Page != 3 {
		t.Fatalf("stale explicit completion: page %d completed %v, want page 3 completed true", p.Page, p.Completed)
	}

	// And a stale write that claims nothing does not un-finish it.
	p = putProgress(t, alice, ts, row.ID, api.ProgressRequest{Page: 0, UpdatedAt: now - 60})
	if !p.Completed {
		t.Fatal("a stale write must not clear completion")
	}
}

// TestProgressZeroTimestamp pins the pre-offline behaviour every existing client
// still relies on: no claim means the server stamps now and the write applies.
func TestProgressZeroTimestamp(t *testing.T) {
	_, ts, st, cfg := libraryServer(t, nil)
	alice := adminClient(t, ts, st)
	row, _ := addComic(t, st, cfg.LibraryRoot, "Series/One.cbz", 10, store.ComicRow{})

	before := time.Now().Unix()
	putProgress(t, alice, ts, row.ID, api.ProgressRequest{Page: 5, UpdatedAt: before + 10})

	// A timestamp-less write is happening now, so it beats the row above.
	p := putProgress(t, alice, ts, row.ID, api.ProgressRequest{Page: 2})
	if p.Page != 2 {
		t.Fatalf("page = %d, want 2: a client making no claim must not be treated as stale", p.Page)
	}
	if p.UpdatedAt < before {
		t.Fatalf("updatedAt = %d, want the server clock at or after %d", p.UpdatedAt, before)
	}
}

// TestProgressFutureTimestampClamped: a client with a fast clock must not be
// able to store a timestamp no honest write can beat.
func TestProgressFutureTimestampClamped(t *testing.T) {
	_, ts, st, cfg := libraryServer(t, nil)
	alice := adminClient(t, ts, st)
	row, _ := addComic(t, st, cfg.LibraryRoot, "Series/One.cbz", 10, store.ComicRow{})

	future := time.Now().Unix() + 86400*365
	p := putProgress(t, alice, ts, row.ID, api.ProgressRequest{Page: 4, UpdatedAt: future})
	if p.UpdatedAt >= future {
		t.Fatalf("updatedAt = %d, want it clamped below the claimed %d", p.UpdatedAt, future)
	}

	// Clamped, so an honest write from any other device still wins.
	p = putProgress(t, alice, ts, row.ID, api.ProgressRequest{Page: 6, UpdatedAt: time.Now().Unix()})
	if p.Page != 6 {
		t.Fatalf("page = %d, want 6: a future-dated write must not pin progress", p.Page)
	}
}
