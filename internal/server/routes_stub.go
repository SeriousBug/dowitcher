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
	// now plus what recently finished. An admin sees every job, including the
	// ownerless library-pdf ones no per-user query returns.
	JobSnapshot(userID string, isAdmin bool) []api.ImportJob
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
	// StartPDF extracts a fully uploaded PDF into page images and runs them
	// through the same pipeline as a folder import. ctx is detached(r).
	StartPDF(ctx context.Context, jobID, pdfPath string, opts api.ImportOptions) error
	// Fail marks a job that died before the pipeline got it, which is how a
	// broken upload stops being a spinner.
	Fail(jobID, msg string)
	// Cancel stops a job on behalf of its owner: a queued job is removed, a
	// running one is cancelled. The store's ownership check gates it.
	Cancel(userID, jobID string) error
	// CancelAny is Cancel without the ownership check, for an admin, so an admin
	// can remove any job including an ownerless library-pdf one.
	CancelAny(jobID string) error
	// Pause and Resume stop and restart the queue's dequeue. An in-flight job
	// runs to completion; only the picking of the next job is held. Admin-only.
	Pause() error
	Resume() error
	// Paused reports the queue's current paused flag, for the connect snapshot.
	Paused() bool
	// Reorder rewrites the queued order from the full ordered id list. Admin-only.
	Reorder(jobIDs []string) error
	// EnqueueLibraryPDF queues a PDF dropped into the watched library folder for
	// conversion to a server-wide CBZ. The job is ownerless.
	EnqueueLibraryPDF(pdfPath string)
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

// jobSnapshot returns the import jobs the hub should tell this client about, or
// none when no importer is attached. An admin gets the server-wide set.
func (s *Server) jobSnapshot(userID string, isAdmin bool) []api.ImportJob {
	if s.importer == nil {
		return []api.ImportJob{}
	}
	jobs := s.importer.JobSnapshot(userID, isAdmin)
	if jobs == nil {
		return []api.ImportJob{}
	}
	return jobs
}

// queuePaused reports the import queue's paused flag, or false when no importer
// is attached.
func (s *Server) queuePaused() bool {
	if s.importer == nil {
		return false
	}
	return s.importer.Paused()
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
	// Renaming edits a field on the resource rather than running an action, so it
	// is a PATCH on the comic, not a verb sub-path. The store enforces the
	// owner-or-admin rule the handler checks.
	s.mux.HandleFunc("PATCH /api/comics/{id}", s.requireAuth(s.handleRenameComic))
	s.mux.HandleFunc("GET /api/comics/{id}/pages/{n}", s.requireAuth(s.handleComicPage))
	s.mux.HandleFunc("GET /api/comics/{id}/cover", s.requireAuth(s.handleComicCover))
	s.mux.HandleFunc("PUT /api/comics/{id}/progress", s.requireAuth(s.handleSetProgress))
	s.mux.HandleFunc("PUT /api/comics/{id}/tags", s.requireAuth(s.handleSetTags))
	s.mux.HandleFunc("DELETE /api/comics/{id}", s.requireAuth(s.handleDeleteComic))
	// Claiming takes a library comic out of every other user's view, so it is an
	// admin's call rather than a first-come-first-served race between users.
	s.mux.HandleFunc("POST /api/comics/{id}/claim", s.requireAdmin(s.handleClaimComic))
	s.mux.HandleFunc("POST /api/comics/{id}/unclaim", s.requireAdmin(s.handleUnclaimComic))
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
	s.mux.HandleFunc("PUT /api/collections/{id}/cover", s.requireAuth(s.handleSetCollectionCover))

	// Library. Reading the status is everyone's; triggering a walk of the whole
	// root is an admin's.
	s.mux.HandleFunc("GET /api/library/status", s.requireAuth(s.handleLibraryStatus))
	s.mux.HandleFunc("POST /api/library/scan", s.requireAdmin(s.handleLibraryScan))
}

// registerImportRoutes registers the import routes.
func (s *Server) registerImportRoutes() {
	s.mux.HandleFunc("POST /api/imports", s.requireAuth(s.handleCreateImport))
	s.mux.HandleFunc("GET /api/imports", s.requireAuth(s.handleListImports))
	// Clearing finished jobs is scoped in the handler: an owner clears their own,
	// an admin clears everyone's. So it is requireAuth, not requireAdmin.
	s.mux.HandleFunc("DELETE /api/imports/finished", s.requireAuth(s.handleClearFinishedImports))
	// Pausing the queue and reordering it act on jobs the caller may not own, so
	// they are an admin's. Removing a single job stays per-owner (the handler
	// branches on admin), so its cancel route is requireAuth.
	s.mux.HandleFunc("POST /api/imports/queue/pause", s.requireAdmin(s.handlePauseQueue))
	s.mux.HandleFunc("POST /api/imports/queue/resume", s.requireAdmin(s.handleResumeQueue))
	s.mux.HandleFunc("PUT /api/imports/queue/order", s.requireAdmin(s.handleReorderQueue))
	s.mux.HandleFunc("POST /api/imports/{id}/cancel", s.requireAuth(s.handleCancelImport))
	s.mux.HandleFunc("GET /api/imports/{id}/dupes", s.requireAuth(s.handleImportDupes))
}
