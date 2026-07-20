package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/auth"
)

// handleListTokens returns the caller's own API tokens, metadata only — the
// secret was shown once at creation and is never stored in plain.
func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	tokens, err := s.store.ListAPITokens(u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}

// handleCreateToken mints an API token for the caller and returns the plain
// secret exactly once. The token authenticates the MCP server as this user and
// inherits this user's access; an admin's token can drive the admin-only tools.
func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	var req api.CreateTokenRequest
	json.NewDecoder(r.Body).Decode(&req)
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	secret, tok, err := auth.NewAPIToken(s.store, u.ID, name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusOK, api.CreateTokenResponse{Token: tok, Secret: secret})
}

// handleDeleteToken revokes one of the caller's tokens.
func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	if err := s.store.DeleteAPIToken(u.ID, r.PathValue("id")); err != nil {
		if isNotFound(err) {
			writeErr(w, http.StatusNotFound, "token not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	writeOK(w)
}
