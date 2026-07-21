package server

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/imports"
)

const (
	// DefaultMaxUploadBytes caps one import when Config.MaxUploadBytes is unset.
	// A folder of raw scans is routinely a few GB, so the cap is high enough to
	// be a backstop against a runaway or hostile upload rather than a quota.
	DefaultMaxUploadBytes = 8 << 30
	// optionsPart is the multipart field carrying api.ImportOptions as JSON.
	optionsPart = "options"
	// maxOptionsBytes bounds the options part. It is a handful of fields; a
	// megabyte of it is not a client we should keep reading.
	maxOptionsBytes = 1 << 20
)

// handleCreateImport accepts a folder of images as multipart and hands it to the
// pipeline.
//
// The parts are streamed to a temp dir one bounded read at a time rather than
// buffered: r.ParseMultipartForm would hold the whole upload, and a folder of
// scans is easily gigabytes. The response returns as soon as the bytes are on
// disk — the import itself reports over the WS.
func (s *Server) handleCreateImport(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	if !s.needImporter(w) {
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "expected a multipart upload")
		return
	}
	job, err := s.importer.Begin(u.ID)
	if err != nil {
		// The concurrency cap is the user's own doing and they can act on it, so
		// it gets its own text rather than flattening to "db error" like a real
		// internal failure. Refusing here, before a byte of a multi-GB upload has
		// been read, is the whole reason the cap lives in Begin.
		if errors.Is(err, imports.ErrTooManyImports) {
			writeErr(w, http.StatusTooManyRequests,
				"you have too many imports queued; wait for some to finish before starting more")
			return
		}
		log.Printf("begin import: %v", err)
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	srcDir, err := os.MkdirTemp(s.cfg.ImportTempDir, "dowitcher-upload-*")
	if err != nil {
		log.Printf("import temp dir: %v", err)
		s.importer.Fail(job.ID, "the server had nowhere to put the upload")
		writeErr(w, http.StatusInternalServerError, "the server had nowhere to put the upload")
		return
	}
	// The pipeline takes the directory over on a successful Start and removes it
	// when it is done; until then it is this handler's to clean up.
	started := false
	defer func() {
		if !started {
			os.RemoveAll(srcDir)
		}
	}()
	fail := func(status int, msg string) {
		s.importer.Fail(job.ID, msg)
		writeErr(w, status, msg)
	}

	var opts api.ImportOptions
	budget := s.cfg.MaxUploadBytes
	if budget <= 0 {
		budget = DefaultMaxUploadBytes
	}
	files := 0
	// A PDF is unpacked into page images and run through the same pipeline, but
	// it is a single self-contained book: it cannot be mixed with loose images,
	// which would be an ambiguous "which is the comic" upload.
	pdfPath := ""
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			fail(http.StatusBadRequest, "the upload ended early or was malformed")
			return
		}
		if part.FormName() == optionsPart {
			if err := json.NewDecoder(io.LimitReader(part, maxOptionsBytes)).Decode(&opts); err != nil {
				part.Close()
				fail(http.StatusBadRequest, "the import options were not valid JSON")
				return
			}
			part.Close()
			continue
		}
		rel, ok := uploadName(part.FileName())
		if !ok {
			part.Close()
			fail(http.StatusBadRequest, "an uploaded file had an unusable name: "+part.FileName())
			return
		}
		if imports.IsPDFName(rel) {
			if files > 0 || pdfPath != "" {
				part.Close()
				fail(http.StatusBadRequest, "a PDF has to be uploaded on its own, not mixed with other files")
				return
			}
			dst := filepath.Join(srcDir, filepath.Base(rel))
			n, err := writeUpload(dst, part, budget)
			part.Close()
			if err != nil {
				if errors.Is(err, errUploadTooBig) {
					fail(http.StatusRequestEntityTooLarge, "this upload is larger than the server allows")
					return
				}
				log.Printf("import upload %s: %v", rel, err)
				fail(http.StatusInternalServerError, "the upload could not be written to disk")
				return
			}
			budget -= n
			pdfPath = dst
			// The count drives the "files uploaded" spinner; a PDF is one file.
			s.importer.Uploaded(job.ID, 1)
			continue
		}
		if !imports.IsImageName(rel) {
			// Refused rather than skipped: a folder full of files the pipeline
			// would ignore means the wrong folder was picked, and saying so beats
			// producing an empty comic twenty minutes later.
			part.Close()
			fail(http.StatusBadRequest, "only image files or a PDF can be imported, got: "+rel)
			return
		}
		if pdfPath != "" {
			part.Close()
			fail(http.StatusBadRequest, "a PDF has to be uploaded on its own, not mixed with other files")
			return
		}
		n, err := writeUpload(filepath.Join(srcDir, filepath.FromSlash(rel)), part, budget)
		part.Close()
		if err != nil {
			if errors.Is(err, errUploadTooBig) {
				fail(http.StatusRequestEntityTooLarge, "this upload is larger than the server allows")
				return
			}
			log.Printf("import upload %s: %v", rel, err)
			fail(http.StatusInternalServerError, "the upload could not be written to disk")
			return
		}
		budget -= n
		files++
		s.importer.Uploaded(job.ID, files)
	}

	if pdfPath != "" {
		if err := s.importer.StartPDF(detached(r), job.ID, pdfPath, opts); err != nil {
			log.Printf("start pdf import %s: %v", job.ID, err)
			fail(http.StatusInternalServerError, "the import could not be started")
			return
		}
		started = true
		writeJSON(w, http.StatusOK, job)
		return
	}

	if files == 0 {
		fail(http.StatusBadRequest, "no images were uploaded")
		return
	}

	if err := s.importer.Start(detached(r), job.ID, srcDir, opts); err != nil {
		log.Printf("start import %s: %v", job.ID, err)
		fail(http.StatusInternalServerError, "the import could not be started")
		return
	}
	started = true
	writeJSON(w, http.StatusOK, job)
}

var errUploadTooBig = errors.New("upload exceeds the size cap")

// writeUpload streams one part to disk, refusing to write more than budget. The
// cap is enforced against the bytes as they arrive rather than a Content-Length
// header, which a client is free to lie about.
func writeUpload(dst string, src io.Reader, budget int64) (int64, error) {
	if budget <= 0 {
		return 0, errUploadTooBig
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	f, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	// One byte past the budget is enough to know it was exceeded, and stops the
	// read there rather than draining a client that is still sending.
	n, err := io.Copy(f, io.LimitReader(src, budget+1))
	if err != nil {
		return n, err
	}
	if n > budget {
		return n, errUploadTooBig
	}
	return n, nil
}

// uploadName sanitises a part's filename into a slash-separated relative path.
// The path is kept rather than flattened to a base name because the pipeline
// natural-sorts on it, and a folder upload's "ch01/003.jpg" is exactly the order
// the user means.
//
// Anything that could escape the temp dir is refused outright: this is a name a
// client chose, and it is about to be joined onto a path.
func uploadName(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	// A Windows client sends backslashes, and a drive letter would survive
	// path.Clean as an ordinary filename.
	if strings.ContainsAny(name, `\`) || strings.HasPrefix(name, "/") || (len(name) > 1 && name[1] == ':') {
		return "", false
	}
	c := path.Clean(name)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", false
	}
	return c, true
}

func (s *Server) handleListImports(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	jobs, err := s.store.ListImportJobs(u.ID)
	if err != nil {
		log.Printf("list import jobs: %v", err)
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (s *Server) handleCancelImport(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	if !s.needImporter(w) {
		return
	}
	// An admin may cancel any job, including an ownerless library-pdf one; a
	// non-admin may only cancel their own, and CancelAny would skip the ownership
	// check that keeps one user out of another's imports.
	var err error
	if u.IsAdmin {
		err = s.importer.CancelAny(r.PathValue("id"))
	} else {
		err = s.importer.Cancel(u.ID, r.PathValue("id"))
	}
	switch {
	case err == nil:
		writeOK(w)
	case isNotFound(err):
		writeErr(w, http.StatusNotFound, "import not found")
	case errors.Is(err, imports.ErrNotRunning):
		// The job finished, or died with a previous process, between the client
		// deciding to cancel and the request arriving. Nothing is wrong, but the
		// cancel did not happen and the client should stop claiming it did.
		writeErr(w, http.StatusConflict, "that import is not running any more")
	default:
		log.Printf("cancel import: %v", err)
		writeErr(w, http.StatusInternalServerError, "db error")
	}
}

func (s *Server) handlePauseQueue(w http.ResponseWriter, r *http.Request) {
	if !s.needImporter(w) {
		return
	}
	if err := s.importer.Pause(); err != nil {
		log.Printf("pause queue: %v", err)
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	writeOK(w)
}

func (s *Server) handleResumeQueue(w http.ResponseWriter, r *http.Request) {
	if !s.needImporter(w) {
		return
	}
	if err := s.importer.Resume(); err != nil {
		log.Printf("resume queue: %v", err)
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	writeOK(w)
}

func (s *Server) handleReorderQueue(w http.ResponseWriter, r *http.Request) {
	if !s.needImporter(w) {
		return
	}
	var req api.ReorderQueueRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxOptionsBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "expected a JSON body with jobIds")
		return
	}
	if err := s.importer.Reorder(req.JobIDs); err != nil {
		log.Printf("reorder queue: %v", err)
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	writeOK(w)
}

func (s *Server) handleClearFinishedImports(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	// An admin clears every finished job (including ownerless ones); a non-admin
	// clears only their own.
	if err := s.store.DeleteFinishedImportJobs(u.ID, u.IsAdmin); err != nil {
		log.Printf("clear finished imports: %v", err)
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	// A deletion is not a per-job event the WS pushes, so a still-connected client
	// would keep showing the cleared jobs until it reconnected. Push a fresh
	// snapshot to the caller's sockets so they drop out now.
	s.hub.BroadcastTo(u.ID, api.WSMessage{Type: api.WSTypeJobs, Jobs: s.jobSnapshot(u.ID, u.IsAdmin)})
	writeOK(w)
}

// handleImportDupes returns the merge report, which is the only place a user can
// see what the dedupe pass decided on their behalf.
func (s *Server) handleImportDupes(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	if !s.needImporter(w) {
		return
	}
	groups, err := s.importer.Dupes(u.ID, r.PathValue("id"))
	if err != nil {
		if isNotFound(err) {
			writeErr(w, http.StatusNotFound, "no dupe report for that import")
			return
		}
		log.Printf("import dupes: %v", err)
		writeErr(w, http.StatusInternalServerError, "the dupe report could not be read")
		return
	}
	writeJSON(w, http.StatusOK, groups)
}
