// Package server wires HTTP handlers, the SPA, auth middleware and the WS hub.
package server

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/auth"
	"github.com/SeriousBug/dowitcher/internal/mcp"
	"github.com/SeriousBug/dowitcher/internal/store"
	"github.com/SeriousBug/dowitcher/web"
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
	// MCPEnabled mounts the MCP server at /mcp. Off by default: it is a headless,
	// token-authenticated door into the library, so an operator opts in rather
	// than having it exposed the moment they upgrade.
	MCPEnabled bool
	// Version is the build version reported to MCP clients.
	Version string
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
	s.mux.HandleFunc("POST /auth/logout/others", s.requireAuth(s.handleLogoutOthers))
	s.mux.HandleFunc("GET /auth/me", s.requireAuth(s.handleMe))
	s.mux.HandleFunc("DELETE /auth/credentials/{id}", s.requireAuth(s.handleDeleteCredential))

	// API tokens: a user mints one to authenticate a headless agent (the MCP
	// server) as themselves. Bound to the caller, so gated on a session, not
	// admin — an admin's token merely inherits the admin's own extra reach.
	s.mux.HandleFunc("GET /api/tokens", s.requireAuth(s.handleListTokens))
	s.mux.HandleFunc("POST /api/tokens", s.requireAuth(s.handleCreateToken))
	s.mux.HandleFunc("DELETE /api/tokens/{id}", s.requireAuth(s.handleDeleteToken))

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

	// MCP server, opt-in. Mounted at both the exact path and the subtree so a
	// client that posts to either /mcp or /mcp/ reaches it. Its own bearer-token
	// middleware authenticates every request, so it is registered raw rather than
	// behind requireAuth — the session cookie means nothing to a headless agent.
	if s.cfg.MCPEnabled {
		h := mcp.New(s.store, s.cfg.Version).Handler()
		s.mux.Handle("/mcp", h)
		s.mux.Handle("/mcp/", h)
	}

	// Live push.
	s.mux.HandleFunc("GET /ws", s.handleWS)

	// SPA + static assets fallback. Registered last: "/" matches everything the
	// patterns above did not, so anything added after it would be dead.
	s.mux.HandleFunc("/", s.serveSPA)
}

// noCache is the caching rule for the files that decide which version of the app
// a browser is running: the app shell and the service worker. They keep their
// stable names across every build, so a cached copy is not a copy of an old file
// but the whole old app, still being served after the deploy that replaced it.
// no-cache still allows a conditional request, so the cost of getting this right
// is a 304 per load rather than a download.
const noCache = "no-cache"

// serveSPA serves embedded static files, falling back to index.html for client
// routes so deep links work.
func (s *Server) serveSPA(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p == "" {
		p = "index.html"
	}
	if f, err := s.spa.Open(p); err == nil {
		f.Close()
		spaHeaders(w, p)
		http.FileServer(http.FS(s.spa)).ServeHTTP(w, r)
		return
	}
	data, err := fs.ReadFile(s.spa, "index.html")
	if err != nil {
		http.Error(w, "SPA not built", http.StatusInternalServerError)
		return
	}
	// The deep-link fallback hands out the same shell as "/" and needs the same
	// rule; this is the second of the two ways index.html leaves the server.
	w.Header().Set("Cache-Control", noCache)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

// spaHeaders sets what http.FileServer cannot work out from the embedded FS: how
// long a file may be cached, and the one content type Go's mime table is missing.
func spaHeaders(w http.ResponseWriter, p string) {
	switch {
	case p == "sw.js":
		// The service worker decides what every other request resolves to, so a
		// stale one pins the fleet to the bundle it was built against. Browsers cap
		// worker script freshness at 24h regardless, but a day of serving an old
		// worker to everyone is a day of shipping nothing.
		w.Header().Set("Cache-Control", noCache)
	case p == "index.html":
		w.Header().Set("Cache-Control", noCache)
	case p == "manifest.webmanifest":
		// Go's mime table does not know this extension, so FileServer sniffs and
		// gets it wrong. Chrome ignores a manifest served as anything else, which
		// costs the install prompt.
		w.Header().Set("Content-Type", "application/manifest+json")
	case strings.HasPrefix(p, "assets/"):
		// Vite content-addresses everything under assets/, so the name changes when
		// the bytes do and the old name is never reused. Same reasoning as a comic
		// page: cacheable for as long as the browser will hold it.
		w.Header().Set("Cache-Control", immutableCache)
	}
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
