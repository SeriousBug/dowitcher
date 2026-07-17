package library

import (
	"archive/zip"
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/store"
)

// fixture builds real CBZs in the temp dir rather than copying anything from a
// real library: the committed tests have to run on a fresh clone with no
// comics on the machine.

// jpegBytes encodes a small solid-colour JPEG. The pages have to be real images
// because the cover pass decodes one.
func jpegBytes(t *testing.T, shade uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 24, 32))
	for y := range 32 {
		for x := range 24 {
			img.Set(x, y, color.RGBA{R: shade, G: uint8(x * 3), B: uint8(y * 2), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode fixture jpeg: %v", err)
	}
	return buf.Bytes()
}

// cbzBytes builds a CBZ holding pages images, all derived from shade. Two calls
// with the same shade and count produce the same content hash; a different
// shade produces a different one.
func cbzBytes(t *testing.T, pages int, shade uint8) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := range pages {
		w, err := zw.Create(pageName(i))
		if err != nil {
			t.Fatalf("create zip entry: %v", err)
		}
		if _, err := w.Write(jpegBytes(t, shade+uint8(i))); err != nil {
			t.Fatalf("write zip entry: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func pageName(i int) string {
	return string(rune('0'+i/10)) + string(rune('0'+i%10)) + ".jpg"
}

// writeCBZ writes a CBZ at a root-relative path, creating parent directories.
func writeCBZ(t *testing.T, root, rel string, pages int, shade uint8) {
	t.Helper()
	writeRaw(t, root, rel, cbzBytes(t, pages, shade))
}

func writeRaw(t *testing.T, root, rel string, b []byte) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// statusRecorder captures the status callback for assertions about progress
// reporting.
type statusRecorder struct {
	mu   sync.Mutex
	msgs []api.LibraryStatus
}

func (r *statusRecorder) fn(s api.LibraryStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, s)
}

func (r *statusRecorder) all() []api.LibraryStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]api.LibraryStatus(nil), r.msgs...)
}

// newTestLib brings up a real SQLite store and a Library over two temp dirs.
func newTestLib(t *testing.T) (*Library, *store.Store, string, *statusRecorder) {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "library")
	data := filepath.Join(dir, "data")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	rec := &statusRecorder{}
	l := New(st, Config{Root: root, DataDir: data}, rec.fn)
	return l, st, root, rec
}

// mustUser creates a user to hang tags and progress off.
func mustUser(t *testing.T, st *store.Store) api.User {
	t.Helper()
	u, err := st.CreateUser(store.NewID(), "reader", false)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

// comicAt finds the single comic at a path, failing if it is not there.
func comicAt(t *testing.T, st *store.Store, rel string) store.ComicRow {
	t.Helper()
	row, err := st.ComicRowByPath(rel)
	if err != nil {
		t.Fatalf("no comic at %q: %v", rel, err)
	}
	return row
}
