package server

import (
	"context"

	"github.com/SeriousBug/longbox/internal/api"
)

// ---------------------------------------------------------------------------
// PLACEHOLDER. This file holds the seams between the auth/server foundation and
// the library, comic, tag, collection and import handlers. It is meant to be
// replaced wholesale by those handlers; the pieces that must survive are the
// Library and Importer interfaces, the two setters, and the two register hooks
// called from routes() in server.go.
// ---------------------------------------------------------------------------

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

// Importer runs the image-folder import pipeline.
type Importer interface {
	// JobSnapshot is every job the hub should tell userID about: what is running
	// now plus what recently finished.
	JobSnapshot(userID string) []api.ImportJob
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

// registerLibraryRoutes registers the comic, tag, collection and library routes.
// Empty until those handlers land.
func (s *Server) registerLibraryRoutes() {}

// registerImportRoutes registers the import routes. Empty until those handlers
// land.
func (s *Server) registerImportRoutes() {}
