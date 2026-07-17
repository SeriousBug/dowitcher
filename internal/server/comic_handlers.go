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
	"strconv"
	"strings"
	"time"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/cbz"
	"github.com/SeriousBug/dowitcher/internal/imports"
	"github.com/SeriousBug/dowitcher/internal/library"
	"github.com/SeriousBug/dowitcher/internal/store"
)

const (
	// defaultPageSize and maxPageSize bound a listing. The cap exists so a
	// client cannot ask for the entire library in one response and make the
	// server materialise it.
	defaultPageSize = 100
	maxPageSize     = 500
	// coverWidth is the library grid's thumbnail width. One size, generated
	// once: a self-hosted instance serves a handful of readers, and a
	// responsive image set would cost more cache than it saves.
	coverWidth = 400
	// immutableCache is the caching rule for page and cover bytes. A page's
	// bytes cannot change for a given comic id and index — the id is bound to a
	// file whose contents are hashed, and a file whose contents change gets a
	// new hash and a fresh ETag from the next scan. So the response is cacheable
	// for as long as the browser will hold it, which is what makes flipping back
	// through a comic instant instead of a round trip per page. private, because
	// a page of somebody's private upload must never sit in a shared proxy.
	immutableCache = "private, immutable, max-age=31536000"
)

// handleListComics is the library grid: one filtered, paginated page of what the
// caller may see, plus their progress in those comics.
func (s *Server) handleListComics(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	q := r.URL.Query()
	f := store.ComicFilter{
		Tag:        q.Get("tag"),
		Series:     q.Get("series"),
		Query:      q.Get("q"),
		Collection: q.Get("collection"),
		Limit:      defaultPageSize,
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeErr(w, http.StatusBadRequest, "limit must be a positive number")
			return
		}
		f.Limit = min(n, maxPageSize)
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeErr(w, http.StatusBadRequest, "offset must be a non-negative number")
			return
		}
		f.Offset = n
	}

	comics, total, err := s.store.ListComicsFiltered(u.ID, f)
	if err != nil {
		log.Printf("list comics: %v", err)
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	if comics == nil {
		comics = []api.Comic{}
	}
	progress, err := s.progressFor(u.ID, comics)
	if err != nil {
		log.Printf("list progress: %v", err)
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusOK, api.ComicList{
		Comics: comics, Progress: progress, Total: total, Offset: f.Offset, Limit: f.Limit,
	})
}

// progressFor narrows the caller's progress to the comics on this page.
func (s *Server) progressFor(userID string, comics []api.Comic) ([]api.Progress, error) {
	all, err := s.store.ListProgress(userID)
	if err != nil {
		return nil, err
	}
	onPage := make(map[string]struct{}, len(comics))
	for _, c := range comics {
		onPage[c.ID] = struct{}{}
	}
	out := []api.Progress{}
	for _, p := range all {
		if _, ok := onPage[p.ComicID]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

// handleUploadComic accepts one ready-made CBZ and files it as a comic.
//
// Separate from handleCreateImport rather than a branch inside it: that endpoint
// exists to run the dedupe pipeline over loose images, and none of what it offers
// — a threshold, a re-encode, a dupe report, a job to watch — means anything for
// an archive that is already packed. The upload is streamed to disk part by part
// for the same reason imports are, since a CBZ is just as easily gigabytes.
//
// The reply is the finished comic rather than a job: the work after the last byte
// lands is a stat and a rename, so there is nothing to watch.
func (s *Server) handleUploadComic(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	if !s.needImporter(w) {
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "expected a multipart upload")
		return
	}
	dir, err := os.MkdirTemp(s.cfg.ImportTempDir, "dowitcher-cbz-*")
	if err != nil {
		log.Printf("cbz temp dir: %v", err)
		writeErr(w, http.StatusInternalServerError, "the server had nowhere to put the upload")
		return
	}
	// Adopt moves the archive out on success; the directory is this handler's
	// either way.
	defer os.RemoveAll(dir)

	budget := s.cfg.MaxUploadBytes
	if budget <= 0 {
		budget = DefaultMaxUploadBytes
	}
	var opts api.ImportOptions
	srcPath := ""
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, "the upload ended early or was malformed")
			return
		}
		if part.FormName() == optionsPart {
			if err := json.NewDecoder(io.LimitReader(part, maxOptionsBytes)).Decode(&opts); err != nil {
				part.Close()
				writeErr(w, http.StatusBadRequest, "the upload options were not valid JSON")
				return
			}
			part.Close()
			continue
		}
		rel, ok := uploadName(part.FileName())
		if !ok {
			part.Close()
			writeErr(w, http.StatusBadRequest, "the uploaded file had an unusable name: "+part.FileName())
			return
		}
		// One file, so any directory part of the name is noise. The base name is
		// kept rather than discarded because Adopt reads the comic's series and
		// number out of it.
		name := path.Base(rel)
		if !imports.IsCBZName(name) {
			part.Close()
			writeErr(w, http.StatusBadRequest, "only a .cbz file can be uploaded here, got: "+name)
			return
		}
		if srcPath != "" {
			part.Close()
			writeErr(w, http.StatusBadRequest, "upload one CBZ at a time")
			return
		}
		dst := filepath.Join(dir, name)
		if _, err := writeUpload(dst, part, budget); err != nil {
			part.Close()
			if errors.Is(err, errUploadTooBig) {
				writeErr(w, http.StatusRequestEntityTooLarge, "this upload is larger than the server allows")
				return
			}
			log.Printf("cbz upload %s: %v", name, err)
			writeErr(w, http.StatusInternalServerError, "the upload could not be written to disk")
			return
		}
		part.Close()
		srcPath = dst
	}
	if srcPath == "" {
		writeErr(w, http.StatusBadRequest, "no CBZ was uploaded")
		return
	}

	comic, err := s.importer.Adopt(u.ID, srcPath, opts)
	switch {
	case err == nil:
	case errors.Is(err, imports.ErrNotCBZ):
		writeErr(w, http.StatusBadRequest, "that file could not be read as a CBZ")
		return
	case errors.Is(err, imports.ErrNoImages):
		writeErr(w, http.StatusBadRequest, "that CBZ has no readable pages in it")
		return
	default:
		log.Printf("adopt cbz: %v", err)
		writeErr(w, http.StatusInternalServerError, "the comic could not be added to the library")
		return
	}
	writeJSON(w, http.StatusOK, comic)
}

func (s *Server) handleGetComic(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	comic, ok := s.visibleComic(w, r)
	if !ok {
		return
	}
	row, ok := s.comicRow(w, comic.ID)
	if !ok {
		return
	}
	a, err := cbz.Open(s.comicFile(row))
	if err != nil {
		log.Printf("open comic %s (%s): %v", comic.ID, row.Path, err)
		writeErr(w, http.StatusInternalServerError, "this comic's file could not be read")
		return
	}
	defer a.Close()
	pages, err := a.Pages()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "this comic's file could not be read")
		return
	}
	out := api.ComicDetail{Comic: comic, Pages: pages}
	// No progress row simply means unread, which is not an error.
	if p, err := s.store.GetProgress(u.ID, comic.ID); err == nil {
		out.Progress = &p
	} else if !isNotFound(err) {
		log.Printf("get progress %s: %v", comic.ID, err)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleComicPage streams one page's bytes. It is the hot path of the reader: a
// page is copied straight from the zip entry to the socket, never buffered, so
// serving a 20MB scan costs a fixed buffer rather than 20MB of heap per reader.
func (s *Server) handleComicPage(w http.ResponseWriter, r *http.Request) {
	comic, ok := s.visibleComic(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 0 {
		writeErr(w, http.StatusBadRequest, "bad page number")
		return
	}
	row, ok := s.comicRow(w, comic.ID)
	if !ok {
		return
	}

	etag := etagFor(row, strconv.Itoa(n))
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", immutableCache)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	a, err := cbz.Open(s.comicFile(row))
	if err != nil {
		log.Printf("open comic %s (%s): %v", comic.ID, row.Path, err)
		writeErr(w, http.StatusInternalServerError, "this comic's file could not be read")
		return
	}
	defer a.Close()
	rc, ct, err := a.Page(n)
	if err != nil {
		if errors.Is(err, cbz.ErrPageRange) {
			writeErr(w, http.StatusNotFound, "no such page")
			return
		}
		log.Printf("page %d of %s: %v", n, comic.ID, err)
		writeErr(w, http.StatusInternalServerError, "this page could not be read")
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", ct)
	if _, err := io.Copy(w, rc); err != nil {
		// The client hung up or the archive is truncated. The header is long
		// gone, so there is nothing to say to the client; log and move on.
		log.Printf("stream page %d of %s: %v", n, comic.ID, err)
	}
}

// handleComicCover serves the library grid thumbnail. The scanner writes covers
// into the cache dir as it goes; a miss is generated here rather than 404ed, so
// a comic added seconds ago still has a cover and a wiped cache heals itself.
func (s *Server) handleComicCover(w http.ResponseWriter, r *http.Request) {
	comic, ok := s.visibleComic(w, r)
	if !ok {
		return
	}
	row, ok := s.comicRow(w, comic.ID)
	if !ok {
		return
	}
	etag := etagFor(row, "cover")
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", immutableCache)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")

	if p := s.coverCachePath(row); p != "" {
		if f, err := os.Open(p); err == nil {
			defer f.Close()
			io.Copy(w, f)
			return
		}
	}
	data, err := s.generateCover(row)
	if err != nil {
		log.Printf("cover for %s (%s): %v", comic.ID, row.Path, err)
		writeErr(w, http.StatusInternalServerError, "this comic's cover could not be read")
		return
	}
	w.Write(data)
}

// generateCover decodes the cover page and scales it, caching the result when a
// cache dir is configured. A cache write that fails is logged and ignored: the
// user still gets their cover, one decode later than they should have.
func (s *Server) generateCover(row store.ComicRow) ([]byte, error) {
	a, err := cbz.Open(s.comicFile(row))
	if err != nil {
		return nil, err
	}
	defer a.Close()
	rc, err := a.Cover()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := cbz.Thumbnail(rc, coverWidth)
	if err != nil {
		return nil, err
	}
	if p := s.coverCachePath(row); p != "" {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			log.Printf("cover cache dir: %v", err)
		} else if err := os.WriteFile(p, data, 0o644); err != nil {
			log.Printf("cache cover %s: %v", p, err)
		}
	}
	return data, nil
}

// coverCachePath names a comic's cached cover, or "" when there is no cache dir
// or no hash to key it by. The key is the content hash rather than the id so a
// re-scan of an unchanged file finds the cover it already generated, and an
// edited file misses instead of serving the old cover forever.
//
// The layout is the scanner's, deliberately: this handler and the scanner share
// one cache directory, and when they disagreed about its shape the scanner's
// warming was dead work and every cover was decoded twice. library.CoverPathIn
// is the one place the scheme is defined.
func (s *Server) coverCachePath(row store.ComicRow) string {
	return library.CoverPathIn(s.cfg.CoverCacheDir, row.ContentHash)
}

// handleSetProgress is the cross-device sync: the reader PUTs its position, and
// this is the copy every other device reads back.
func (s *Server) handleSetProgress(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	comic, ok := s.visibleComic(w, r)
	if !ok {
		return
	}
	var req api.ProgressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	// Clamp rather than reject: a client that thinks the comic is longer than it
	// is (a file replaced under it, say) should land on the last page, not lose
	// its place to a 400.
	page := req.Page
	if page < 0 {
		page = 0
	}
	if comic.PageCount > 0 && page > comic.PageCount-1 {
		page = comic.PageCount - 1
	}
	// Reaching the last page completes the comic. The rule lives here rather
	// than in the client so every client agrees, and it only ever sets the flag:
	// an explicit completed=true (marked read without opening) survives, and
	// paging backwards through a finished comic does not un-finish it unless the
	// client says so.
	completed := req.Completed
	if comic.PageCount > 0 && page >= comic.PageCount-1 {
		completed = true
	}

	// When the client observed this position, not when it reached us. An offline
	// client replays a queue on reconnect, so arrival order is not reading order.
	observedAt := req.UpdatedAt
	now := time.Now().Unix()
	switch {
	case observedAt == 0:
		// No claim: the client is a plain online reader, so the write is happening
		// now by definition. This is what keeps every existing caller working.
		observedAt = now
	case observedAt > now:
		// Client clocks are not trustworthy. A phone whose clock is a year fast
		// would otherwise store a timestamp no honest write could ever beat and
		// pin its position forever. The server clock is the ceiling: a claim from
		// the future is worth exactly as much as a claim of "now", and that costs
		// a client with a slightly fast clock nothing.
		observedAt = now
	}

	// A stale write is one whose position was read before what we already have.
	// Losing it is the point, but no progress row means nothing to lose.
	if cur, err := s.store.GetProgress(u.ID, comic.ID); err == nil && observedAt < cur.UpdatedAt {
		// Completion is not a position, so it does not lose the same way: finishing
		// a comic on a plane is real, and the page you happen to be on later does
		// not un-finish it. Same rule as above — only ever set, never cleared — so
		// the stale claim can carry its completion in while its page stays out. The
		// stored observation time is kept: the page it describes is still the newer
		// one, and moving it would let the next stale replay win.
		if completed && !cur.Completed {
			cur, err = s.store.SetProgressAt(u.ID, comic.ID, cur.Page, true, cur.UpdatedAt)
			if err != nil {
				log.Printf("set progress %s: %v", comic.ID, err)
				writeErr(w, http.StatusInternalServerError, "db error")
				return
			}
		}
		// 200 with the stored row, not a conflict. Nothing went wrong that the
		// client can act on — the server already holds a newer truth — and a client
		// draining an offline queue needs to converge on that truth and drop the
		// entry, whereas an error would have it retry a write that can never win.
		writeJSON(w, http.StatusOK, cur)
		return
	} else if err != nil && !isNotFound(err) {
		log.Printf("get progress %s: %v", comic.ID, err)
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}

	p, err := s.store.SetProgressAt(u.ID, comic.ID, page, completed, observedAt)
	if err != nil {
		if isNotFound(err) {
			writeErr(w, http.StatusNotFound, "comic not found")
			return
		}
		log.Printf("set progress %s: %v", comic.ID, err)
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleSetTags(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	var req api.SetTagsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	id := r.PathValue("id")
	if err := s.store.SetComicTags(u.ID, id, req.Tags); err != nil {
		if isNotFound(err) {
			writeErr(w, http.StatusNotFound, "comic not found")
			return
		}
		log.Printf("set tags %s: %v", id, err)
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	comic, err := s.store.GetComic(u.ID, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusOK, comic)
}

// handleDeleteComic removes an upload and its file.
func (s *Server) handleDeleteComic(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	comic, ok := s.visibleComic(w, r)
	if !ok {
		return
	}
	row, ok := s.comicRow(w, comic.ID)
	if !ok {
		return
	}
	if row.Source != store.SourceUpload {
		// The library folder is the source of truth for what is in it. Dropping
		// the row would delete the tags and reading progress and then resurrect
		// the comic, stripped, on the next scan. Removing a library comic means
		// removing its file.
		writeErr(w, http.StatusBadRequest, "library comics are managed from the library folder, not here")
		return
	}
	// Visibility got them this far; a shared collection is not a licence to
	// delete somebody's upload.
	if row.OwnerID != u.ID && !u.IsAdmin {
		writeErr(w, http.StatusForbidden, "only the uploader can delete an upload")
		return
	}
	if err := s.store.DeleteComic(comic.ID); err != nil {
		if isNotFound(err) {
			writeErr(w, http.StatusNotFound, "comic not found")
			return
		}
		log.Printf("delete comic %s: %v", comic.ID, err)
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	// The row goes first: a file left behind is dead weight, whereas a row whose
	// file is gone is a comic that opens to an error.
	if err := os.Remove(s.comicFile(row)); err != nil && !os.IsNotExist(err) {
		log.Printf("delete upload file %s: %v", row.Path, err)
	}
	writeOK(w)
}

// handleClaimComic takes a library comic into the caller's own library. Admin
// only: a claim removes a comic from everyone else's view, so it is the same
// kind of server-wide decision as triggering a scan, not a personal preference.
func (s *Server) handleClaimComic(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	comic, ok := s.visibleComic(w, r)
	if !ok {
		return
	}
	err := s.store.ClaimComic(u.ID, comic.ID)
	if isNotFound(err) {
		// visibleComic already passed, so the comic exists and the caller can see
		// it: the only way the update matches nothing is the source guard. Saying
		// so beats a 404 that reads as "no such comic".
		writeErr(w, http.StatusBadRequest, "only comics from the library folder can be claimed")
		return
	}
	if err != nil {
		log.Printf("claim comic %s: %v", comic.ID, err)
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	writeOK(w)
}

// handleUnclaimComic hands a claimed comic back to the server.
func (s *Server) handleUnclaimComic(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	comic, ok := s.visibleComic(w, r)
	if !ok {
		return
	}
	err := s.store.UnclaimComic(u.ID, u.IsAdmin, comic.ID)
	if isNotFound(err) {
		writeErr(w, http.StatusBadRequest, "that comic is not claimed")
		return
	}
	if err != nil {
		log.Printf("unclaim comic %s: %v", comic.ID, err)
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	writeOK(w)
}

func (s *Server) handleListTags(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	tags, err := s.store.ListTags(u.ID)
	if err != nil {
		log.Printf("list tags: %v", err)
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusOK, tags)
}

func (s *Server) handleLibraryStatus(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	st := s.libraryStatus()
	// The scanner counts the files it walked; the card shows what this user can
	// actually open, which for anyone but an admin is a different number.
	if n, err := s.store.CountVisibleComics(u.ID); err == nil {
		st.ComicCount = n
	} else {
		log.Printf("count comics: %v", err)
	}
	writeJSON(w, http.StatusOK, st)
}

// handleLibraryScan kicks off a full rescan and answers immediately: a scan of a
// real library takes minutes, and its progress belongs on the WS where every
// open tab sees it, not in this response.
func (s *Server) handleLibraryScan(w http.ResponseWriter, r *http.Request) {
	if s.lib == nil {
		writeErr(w, http.StatusServiceUnavailable, "no library folder is configured on this server")
		return
	}
	ctx := detached(r)
	go func() {
		if err := s.lib.Scan(ctx); err != nil {
			log.Printf("library scan: %v", err)
		}
	}()
	writeOK(w)
}

// visibleComic resolves {id} to a comic the caller may see, writing the response
// and returning false when they may not. The store decides: a comic that exists
// but is not theirs comes back as ErrNotFound, and it is surfaced as a 404 for
// the same reason — a 403 would confirm the comic exists.
func (s *Server) visibleComic(w http.ResponseWriter, r *http.Request) (api.Comic, bool) {
	u, _ := userFrom(r.Context())
	id := r.PathValue("id")
	c, err := s.store.GetComic(u.ID, id)
	if err != nil {
		if isNotFound(err) {
			writeErr(w, http.StatusNotFound, "comic not found")
			return api.Comic{}, false
		}
		log.Printf("get comic %s: %v", id, err)
		writeErr(w, http.StatusInternalServerError, "db error")
		return api.Comic{}, false
	}
	return c, true
}

// comicRow reaches the ownership and on-disk fields api.Comic does not carry. It
// ignores visibility, so it is only ever called after visibleComic has passed.
func (s *Server) comicRow(w http.ResponseWriter, id string) (store.ComicRow, bool) {
	row, err := s.store.ComicRowByID(id)
	if err != nil {
		if isNotFound(err) {
			writeErr(w, http.StatusNotFound, "comic not found")
			return row, false
		}
		log.Printf("get comic row %s: %v", id, err)
		writeErr(w, http.StatusInternalServerError, "db error")
		return row, false
	}
	return row, true
}

// comicFile turns a stored row into a file to open. Paths are stored relative to
// the root they came from, so a container's mount points can move without
// rewriting the database. A claimed comic's file never moved out of the library
// root, so only an upload resolves anywhere else.
func (s *Server) comicFile(row store.ComicRow) string {
	if row.Source == store.SourceUpload {
		return filepath.Join(s.cfg.UploadsDir, row.Path)
	}
	return filepath.Join(s.cfg.LibraryRoot, row.Path)
}

// etagFor identifies immutable bytes derived from one comic: its content hash
// plus what was derived from it. The hash changes if and only if the archive's
// contents change, which is exactly when a cached page must stop being served.
//
// Rows written before a hash was computed fall back to size and mtime, which is
// weaker (a same-size edit within the mtime's second slips through) but still
// beats serving a page with no validator at all.
func etagFor(row store.ComicRow, part string) string {
	base := row.ContentHash
	if base == "" {
		base = strconv.FormatInt(row.FileSize, 10) + "-" + strconv.FormatInt(row.ModifiedAt, 10)
	}
	return `"` + base + "-" + part + `"`
}

// etagMatches implements If-None-Match for our own strong tags: the wildcard, or
// any member of the list. Weak comparison is not needed — nothing here ever
// issues a W/ tag.
func etagMatches(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" || part == etag {
			return true
		}
	}
	return false
}
