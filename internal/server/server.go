// Package server wires HTTP handlers, the SPA, auth middleware and the WS hub.
package server

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"

	"github.com/SeriousBug/longbox/internal/api"
	"github.com/SeriousBug/longbox/internal/auth"
	"github.com/SeriousBug/longbox/internal/store"
	"github.com/SeriousBug/longbox/web"
)

// Config holds server-wide settings derived from env.
type Config struct {
	RPID   string
	Origin string
	Secure bool
	// LibraryRoot is the watched folder. A library comic's Comic.Path is
	// relative to it; an upload's is relative to UploadsDir. The pair is how a
	// comic id becomes a file to read pages out of.
	LibraryRoot string
	UploadsDir  string
	// CoverCacheDir holds cover thumbnails named by content hash. The scanner
	// fills it ahead of time; a miss is generated on the fly rather than served
	// as a 404, so a cold cache is slow rather than broken.
	CoverCacheDir string
	// ImportTempDir is where an upload's images land before the pipeline runs.
	// Empty means the OS temp dir, which in a container is usually a small
	// tmpfs — an import is easily gigabytes, so a deployment should point this
	// at real storage.
	ImportTempDir string
	// MaxUploadBytes caps one import upload. Zero means DefaultMaxUploadBytes.
	MaxUploadBytes int64
	// DevAuth, when non-nil, resolves every gated route to one fixed user
	// without a passkey. See auth.DevAuth — it is a development-only hole.
	DevAuth *auth.DevAuth
}

// Server holds shared dependencies for handlers.
type Server struct {
	store *store.Store
	auth  *auth.Manager
	cfg   Config
	spa   fs.FS
	hub   *Hub
	mux   *http.ServeMux

	// Optional collaborators, attached after New via setters so the constructor
	// signature stays stable and tests can bring up a server without them.
	lib      Library
	importer Importer
}

// New builds a Server with routes registered.
func New(st *store.Store, mgr *auth.Manager, cfg Config) *Server {
	s := &Server{
		store: st,
		auth:  mgr,
		cfg:   cfg,
		spa:   web.DistFS(),
		hub:   newHub(),
		mux:   http.NewServeMux(),
	}
	s.routes()
	return s
}

// Hub exposes the WS fan-out hub for producers (scanner, importer).
func (s *Server) Hub() *Hub { return s.hub }

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler { return s.mux }

// Store exposes the store to collaborators wired in by main.
func (s *Server) Store() *store.Store { return s.store }

// Config exposes the server config.
func (s *Server) Cfg() Config { return s.cfg }

// routes is the access-control table: every route names its own gate here, so
// the list reads as who-can-do-what without opening a handler.
func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Auth (public).
	s.mux.HandleFunc("POST /auth/register/begin", s.handleRegisterBegin)
	s.mux.HandleFunc("POST /auth/register/finish", s.handleRegisterFinish)
	// Add-a-device: enroll an extra passkey for the current user (second
	// device), no invite needed.
	s.mux.HandleFunc("POST /auth/register/device/begin", s.requireAuth(s.handleAddDeviceBegin))
	s.mux.HandleFunc("POST /auth/register/device/finish", s.requireAuth(s.handleAddDeviceFinish))
	s.mux.HandleFunc("POST /auth/login/begin", s.handleLoginBegin)
	s.mux.HandleFunc("POST /auth/login/finish", s.handleLoginFinish)
	s.mux.HandleFunc("POST /auth/logout", s.handleLogout)
	s.mux.HandleFunc("GET /auth/me", s.requireAuth(s.handleMe))
	s.mux.HandleFunc("DELETE /auth/credentials/{id}", s.requireAuth(s.handleDeleteCredential))

	// Invites and users (admin).
	s.mux.HandleFunc("GET /api/invites", s.requireAdmin(s.handleListInvites))
	s.mux.HandleFunc("POST /api/invites", s.requireAdmin(s.handleCreateInvite))
	s.mux.HandleFunc("DELETE /api/invites/{token}", s.requireAdmin(s.handleRevokeInvite))
	s.mux.HandleFunc("GET /api/users", s.requireAdmin(s.handleListUsers))
	s.mux.HandleFunc("DELETE /api/users/{id}", s.requireAdmin(s.handleDeleteUser))
	s.mux.HandleFunc("POST /api/users/{id}/reset", s.requireAdmin(s.handleResetUser))

	// --- Library, comics, tags, collections and imports ---
	// Owned by routes_stub.go and its handler files, registered here so the
	// route table stays in one place.
	s.registerLibraryRoutes()
	s.registerImportRoutes()

	// Live push.
	s.mux.HandleFunc("GET /ws", s.handleWS)

	// SPA + static assets fallback. Registered last: "/" matches everything the
	// patterns above did not, so anything added after it would be dead.
	s.mux.HandleFunc("/", s.serveSPA)
}

// serveSPA serves embedded static files, falling back to index.html for client
// routes so deep links work.
func (s *Server) serveSPA(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p == "" {
		p = "index.html"
	}
	if f, err := s.spa.Open(p); err == nil {
		f.Close()
		http.FileServer(http.FS(s.spa)).ServeHTTP(w, r)
		return
	}
	data, err := fs.ReadFile(s.spa, "index.html")
	if err != nil {
		http.Error(w, "SPA not built", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, api.APIError{Error: msg})
}

// writeOK is the success body for actions that have nothing to return.
func writeOK(w http.ResponseWriter) { writeJSON(w, http.StatusOK, map[string]bool{"ok": true}) }

// detached returns a context carrying the request's values but not its
// cancellation, for work that must finish after the response is written. A scan
// or an import reports over the WebSocket, so tying it to the request context
// would abort it the moment the client navigates away.
func detached(r *http.Request) context.Context { return context.WithoutCancel(r.Context()) }

// userCtxKey carries the authenticated user through the request context.
type userCtxKey struct{}

func userFrom(ctx context.Context) (api.User, bool) {
	u, ok := ctx.Value(userCtxKey{}).(api.User)
	return u, ok
}
