package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// spaServer is the SPA half of a Server: serveSPA reads nothing but s.spa, so a
// test can exercise the cache rules without a store or an auth manager.
func spaServer(files fstest.MapFS) *Server {
	return &Server{spa: files}
}

func TestServeSPAHeaders(t *testing.T) {
	s := spaServer(fstest.MapFS{
		"index.html":           {Data: []byte("<html></html>")},
		"sw.js":                {Data: []byte("self.addEventListener('fetch',()=>{})")},
		"manifest.webmanifest": {Data: []byte(`{"name":"Dowitcher"}`)},
		"assets/app-abc123.js": {Data: []byte("console.log(1)")},
	})

	cases := []struct {
		path        string
		cacheHeader string
		contentType string
	}{
		{"/", noCache, "text/html; charset=utf-8"},
		{"/sw.js", noCache, "text/javascript; charset=utf-8"},
		{"/manifest.webmanifest", "", "application/manifest+json"},
		{"/assets/app-abc123.js", immutableCache, "text/javascript; charset=utf-8"},
		// A client route is not a file. It gets the shell, and the shell's rule.
		{"/library/some-comic", noCache, "text/html; charset=utf-8"},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.serveSPA(w, httptest.NewRequest(http.MethodGet, c.path, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			if got := w.Header().Get("Cache-Control"); got != c.cacheHeader {
				t.Errorf("Cache-Control = %q, want %q", got, c.cacheHeader)
			}
			if got := w.Header().Get("Content-Type"); got != c.contentType {
				t.Errorf("Content-Type = %q, want %q", got, c.contentType)
			}
		})
	}
}

// TestServeSPAIndexRedirects pins why the table above has no /index.html row:
// FileServer bounces the literal name to "/", so the shell only ever leaves this
// handler as "/" or as the deep-link fallback, and those are the two paths the
// no-cache rule has to cover.
func TestServeSPAIndexRedirects(t *testing.T) {
	s := spaServer(fstest.MapFS{"index.html": {Data: []byte("<html></html>")}})
	w := httptest.NewRecorder()
	s.serveSPA(w, httptest.NewRequest(http.MethodGet, "/index.html", nil))
	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", w.Code)
	}
}

// TestServeSPAUnbuilt: web/dist is gitignored, so a server built without the
// frontend must say so rather than panic.
func TestServeSPAUnbuilt(t *testing.T) {
	s := spaServer(fstest.MapFS{})
	w := httptest.NewRecorder()
	s.serveSPA(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}
