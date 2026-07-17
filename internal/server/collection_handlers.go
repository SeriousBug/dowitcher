package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/SeriousBug/longbox/internal/api"
	"github.com/SeriousBug/longbox/internal/store"
)

func (s *Server) handleListCollections(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	cols, err := s.store.ListCollections(u.ID)
	if err != nil {
		log.Printf("list collections: %v", err)
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusOK, cols)
}

func (s *Server) handleGetCollection(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	col, err := s.store.GetCollection(u.ID, r.PathValue("id"))
	if err != nil {
		writeCollectionErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, col)
}

func (s *Server) handleCreateCollection(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	var req api.CreateCollectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "a collection needs a name")
		return
	}
	col, err := s.store.CreateCollection(store.NewID(), u.ID, req.Name, req.Summary, req.Shared)
	if err != nil {
		log.Printf("create collection: %v", err)
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	col.OwnerName = u.Name
	writeJSON(w, http.StatusOK, col)
}

// handleUpdateCollection applies a partial update. Toggling shared is the
// share/unshare action: it is an ordinary field on the collection rather than a
// verb route, because sharing is a property of the collection and the UI shows
// it as a switch.
func (s *Server) handleUpdateCollection(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	var req api.UpdateCollectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "a collection needs a name")
		return
	}
	id := r.PathValue("id")
	if err := s.store.UpdateCollection(u.ID, id, req); err != nil {
		writeCollectionErr(w, err)
		return
	}
	col, err := s.store.GetCollection(u.ID, id)
	if err != nil {
		writeCollectionErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, col)
}

func (s *Server) handleDeleteCollection(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	if err := s.store.DeleteCollection(u.ID, r.PathValue("id")); err != nil {
		writeCollectionErr(w, err)
		return
	}
	writeOK(w)
}

func (s *Server) handleAddToCollection(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	var req api.CollectionComicRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	if req.ComicID == "" {
		writeErr(w, http.StatusBadRequest, "no comic given")
		return
	}
	if err := s.store.AddToCollection(u.ID, r.PathValue("id"), req.ComicID); err != nil {
		writeCollectionErr(w, err)
		return
	}
	writeOK(w)
}

func (s *Server) handleRemoveFromCollection(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	if err := s.store.RemoveFromCollection(u.ID, r.PathValue("id"), r.PathValue("comicId")); err != nil {
		writeCollectionErr(w, err)
		return
	}
	writeOK(w)
}

func (s *Server) handleReorderCollection(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	var req api.ReorderCollectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	if err := s.store.ReorderCollection(u.ID, r.PathValue("id"), req.ComicIDs); err != nil {
		writeCollectionErr(w, err)
		return
	}
	writeOK(w)
}

// writeCollectionErr maps a store error onto a response.
//
// Every collection mutation goes through the store's ownership gate, which
// answers ErrNotFound for both "no such collection" and "not yours" — including
// for a collection the caller can read because it is shared. Sharing grants read
// and nothing more, and 404 rather than 403 is deliberate: a 403 on an id the
// caller cannot see would confirm it exists.
func writeCollectionErr(w http.ResponseWriter, err error) {
	if isNotFound(err) {
		writeErr(w, http.StatusNotFound, "collection not found")
		return
	}
	log.Printf("collection: %v", err)
	writeErr(w, http.StatusInternalServerError, "db error")
}
