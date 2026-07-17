package server

import (
	"context"
	"net/http"

	"github.com/SeriousBug/dowitcher/internal/api"
)

// This file holds the seams between the auth/server foundation and the library,
// comic, tag, collection and import handlers: the collaborator interfaces, their
// setters, the nil guards, and the two hooks routes() calls.

// Library is the filesystem scanner and watcher over the library root. The
// server depends on the interface rather than the concrete type so the two can
// be built independently and so a test can bring up a server without a library
// root at all.
type Library interface {
	// Status is what the scanner is doing right now, for the status card and the
	// WS library message.
	Status() api.LibraryStatus
	// Scan walks the library root and reconciles it with the store. It is
	// expected to be long-running and to report progress over the hub, so it is
	// always called with detached(r).
	Scan(ctx context.Context) error
}

// Importer runs the image-folder import pipeline. Reads of a job's history go
// straight to the store; this interface is only the live half — the parts that
// need the goroutine behind a job.
type Importer interface {
	// JobSnapshot is every job the hub should tell userID about: what is running
	// now plus what recently finished.
	JobSnapshot(userID string) []api.ImportJob
	// Begin registers a job before its bytes have arrived, so an upload that
	// takes minutes shows up as an import immediately rather than only once the
	// last byte lands.
	Begin(userID string) (api.ImportJob, error)
	// Uploaded reports how many files have been received so far. It is expected
	// to throttle: the hub drops slow clients, and a message per file of a
	// thousand-image folder would be a flood that says nothing.
	Uploaded(jobID string, files int)
	// Start hands a fully uploaded folder to the pipeline and returns at once.
	// ctx is detached(r), so the import outlives the request that posted it.
	Start(ctx context.Context, jobID, srcDir string, opts api.ImportOptions) error
	// Fail marks a job that died before the pipeline got it, which is how a
	// broken upload stops being a spinner.
	Fail(jobID, msg string)
	// Cancel stops a running job on behalf of its owner.
	Cancel(userID, jobID string) error
	// Dupes is the finished job's merge report.
	Dupes(userID, jobID string) ([]api.DupeGroup, error)
	// Adopt files an already-packed CBZ at srcPath as a comic owned by userID.
	// srcPath must still carry the name the uploader gave it — the comic's
	// series and number are read out of it. It has no job because it has no
	// pipeline: it returns the finished comic instead.
	Adopt(userID, srcPath string, opts api.ImportOptions) (api.Comic, error)
}

// SetLibrary attaches the scanner. Set after New so the constructor signature
// stays stable for tests.
func (s *Server) SetLibrary(l Library) { s.lib = l }

// SetImporter attaches the import pipeline. Set after New, like SetLibrary.
func (s *Server) SetImporter(i Importer) { s.importer = i }

// libraryStatus reports the scanner's state, or a zero status when no library is
// attached. Nil-guarding here rather than at each call site means a server built
// without a scanner (every auth test) degrades to "nothing is scanning" instead
// of panicking.
func (s *Server) libraryStatus() api.LibraryStatus {
	if s.lib == nil {
		return api.LibraryStatus{}
	}
	return s.lib.Status()
}

// jobSnapshot returns userID's import jobs, or none when no importer is
// attached.
func (s *Server) jobSnapshot(userID string) []api.ImportJob {
	if s.importer == nil {
		return []api.ImportJob{}
	}
	jobs := s.importer.JobSnapshot(userID)
	if jobs == nil {
		return []api.ImportJob{}
	}
	return jobs
}

// needImporter fails a request that needs the pipeline when none is wired.
// Imports are the one feature that cannot degrade quietly: silently accepting an
// upload nothing will ever process is worse than refusing it.
func (s *Server) needImporter(w http.ResponseWriter) bool {
	if s.importer == nil {
		writeErr(w, http.StatusServiceUnavailable, "imports are not configured on this server")
		return false
	}
	return true
}

// registerLibraryRoutes registers the comic, tag, collection and library routes.
func (s *Server) registerLibraryRoutes() {
	// Comics and tags.
	s.mux.HandleFunc("GET /api/comics", s.requireAuth(s.handleListComics))
	// Uploading a ready-made CBZ creates a comic outright, so it is a POST to the
	// collection rather than an import: there is no job behind it to look up.
	s.mux.HandleFunc("POST /api/comics", s.requireAuth(s.handleUploadComic))
	s.mux.HandleFunc("GET /api/comics/{id}", s.requireAuth(s.handleGetComic))
	s.mux.HandleFunc("GET /api/comics/{id}/pages/{n}", s.requireAuth(s.handleComicPage))
	s.mux.HandleFunc("GET /api/comics/{id}/cover", s.requireAuth(s.handleComicCover))
	s.mux.HandleFunc("PUT /api/comics/{id}/progress", s.requireAuth(s.handleSetProgress))
	s.mux.HandleFunc("PUT /api/comics/{id}/tags", s.requireAuth(s.handleSetTags))
	s.mux.HandleFunc("DELETE /api/comics/{id}", s.requireAuth(s.handleDeleteComic))
	s.mux.HandleFunc("GET /api/tags", s.requireAuth(s.handleListTags))

	// Collections. Sharing grants read, never write, so every mutation is gated
	// on ownership inside the store rather than by a different route.
	s.mux.HandleFunc("GET /api/collections", s.requireAuth(s.handleListCollections))
	s.mux.HandleFunc("POST /api/collections", s.requireAuth(s.handleCreateCollection))
	s.mux.HandleFunc("GET /api/collections/{id}", s.requireAuth(s.handleGetCollection))
	s.mux.HandleFunc("PUT /api/collections/{id}", s.requireAuth(s.handleUpdateCollection))
	s.mux.HandleFunc("DELETE /api/collections/{id}", s.requireAuth(s.handleDeleteCollection))
	s.mux.HandleFunc("POST /api/collections/{id}/comics", s.requireAuth(s.handleAddToCollection))
	s.mux.HandleFunc("DELETE /api/collections/{id}/comics/{comicId}", s.requireAuth(s.handleRemoveFromCollection))
	s.mux.HandleFunc("PUT /api/collections/{id}/order", s.requireAuth(s.handleReorderCollection))

	// Library. Reading the status is everyone's; triggering a walk of the whole
	// root is an admin's.
	s.mux.HandleFunc("GET /api/library/status", s.requireAuth(s.handleLibraryStatus))
	s.mux.HandleFunc("POST /api/library/scan", s.requireAdmin(s.handleLibraryScan))
}

// registerImportRoutes registers the import routes.
func (s *Server) registerImportRoutes() {
	s.mux.HandleFunc("POST /api/imports", s.requireAuth(s.handleCreateImport))
	s.mux.HandleFunc("GET /api/imports", s.requireAuth(s.handleListImports))
	s.mux.HandleFunc("POST /api/imports/{id}/cancel", s.requireAuth(s.handleCancelImport))
	s.mux.HandleFunc("GET /api/imports/{id}/dupes", s.requireAuth(s.handleImportDupes))
}
