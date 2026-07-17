package server

import (
	"encoding/json"
	"net/http"

	"github.com/SeriousBug/longbox/internal/api"
	"github.com/SeriousBug/longbox/internal/auth"
)

func (s *Server) handleRegisterBegin(w http.ResponseWriter, r *http.Request) {
	var req api.EnrollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	opts, cid, _, err := s.auth.BeginRegistration(req.Token, req.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.setCeremonyCookie(w, cid)
	// Park the invite token so finish can consume it. It rides in a cookie
	// rather than the finish body because that body is the raw attestation the
	// browser produced, which we do not get to add fields to.
	s.setAuthCookie(w, inviteCookieName, req.Token)
	writeJSON(w, http.StatusOK, opts)
}

func (s *Server) handleRegisterFinish(w http.ResponseWriter, r *http.Request) {
	cid, err := s.ceremonyID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	inviteCookie, err := r.Cookie(inviteCookieName)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no invite in progress")
		return
	}
	userID, err := s.auth.FinishRegistration(cid, inviteCookie.Value, r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.issueSession(w, userID)
	writeOK(w)
}

// handleAddDeviceBegin starts enrolling an additional passkey for the logged-in
// user. No invite is needed; the ceremony is bound to the current session user.
func (s *Server) handleAddDeviceBegin(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	creds, err := s.store.CredentialsByUser(u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	opts, cid, err := s.auth.BeginAddDevice(u.ID, u.Name, creds)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.setCeremonyCookie(w, cid)
	writeJSON(w, http.StatusOK, opts)
}

// handleAddDeviceFinish stores the new passkey against the logged-in user.
func (s *Server) handleAddDeviceFinish(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	cid, err := s.ceremonyID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.auth.FinishAddDevice(cid, u.ID, r); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleLoginBegin(w http.ResponseWriter, r *http.Request) {
	opts, cid, err := s.auth.BeginLogin()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.setCeremonyCookie(w, cid)
	writeJSON(w, http.StatusOK, opts)
}

func (s *Server) handleLoginFinish(w http.ResponseWriter, r *http.Request) {
	cid, err := s.ceremonyID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	userID, err := s.auth.FinishLogin(cid, r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	s.issueSession(w, userID)
	writeOK(w)
}

func (s *Server) issueSession(w http.ResponseWriter, userID string) {
	token, exp, err := auth.NewSession(s.store, userID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "session error")
		return
	}
	s.setSessionCookie(w, token, exp)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.SessionCookieName); err == nil {
		s.store.DeleteSession(c.Value)
	}
	s.clearSessionCookie(w)
	writeOK(w)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	creds, err := s.store.CredentialsByUser(u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	out := api.Session{User: u, Credentials: []api.Credential{}}
	for _, c := range creds {
		out.Credentials = append(out.Credentials, api.Credential{
			ID: c.ID, Name: c.Name, CreatedAt: c.CreatedAt, LastUsed: c.LastUsed,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteCredential removes one of the caller's own passkeys. Removing the
// last one is allowed: an admin can always mint a recovery invite, and refusing
// would strand a user who wants to retire a lost device.
func (s *Server) handleDeleteCredential(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	if err := s.store.DeleteCredential(u.ID, r.PathValue("id")); err != nil {
		if isNotFound(err) {
			writeErr(w, http.StatusNotFound, "passkey not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	writeOK(w)
}

func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	var req api.CreateInviteRequest
	json.NewDecoder(r.Body).Decode(&req)
	token, exp, err := auth.NewInvite(s.store, u.ID, "", req.IsAdmin)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusOK, api.Invite{
		Token:     token,
		IsAdmin:   req.IsAdmin,
		CreatedBy: u.ID,
		CreatedAt: exp.Add(-auth.InviteTTL).Unix(),
		ExpiresAt: exp.Unix(),
	})
}

func (s *Server) handleListInvites(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListPendingInvites()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	names := map[string]string{}
	if users, err := s.store.ListUsers(); err == nil {
		for _, u := range users {
			names[u.ID] = u.Name
		}
	}
	out := []api.Invite{}
	for _, iv := range rows {
		out = append(out, api.Invite{
			Token:       iv.Token,
			IsAdmin:     iv.IsAdmin,
			CreatedAt:   iv.CreatedAt,
			ExpiresAt:   iv.ExpiresAt,
			CreatedBy:   iv.CreatedBy,
			ForUser:     iv.ForUser,
			ForUserName: names[iv.ForUser],
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleResetUser mints a single-use recovery invite bound to an existing user.
// Enrolling on the returned link adds a fresh passkey to that user, restoring
// access without changing their identity or admin status.
func (s *Server) handleResetUser(w http.ResponseWriter, r *http.Request) {
	u, err := s.store.GetUser(r.PathValue("id"))
	if err != nil {
		if isNotFound(err) {
			writeErr(w, http.StatusNotFound, "user not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	token, exp, err := auth.NewInvite(s.store, "", u.ID, u.IsAdmin)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusOK, api.Invite{
		Token:       token,
		IsAdmin:     u.IsAdmin,
		CreatedAt:   exp.Add(-auth.InviteTTL).Unix(),
		ExpiresAt:   exp.Unix(),
		ForUser:     u.ID,
		ForUserName: u.Name,
	})
}

func (s *Server) handleRevokeInvite(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteInvite(r.PathValue("token")); err != nil {
		if isNotFound(err) {
			writeErr(w, http.StatusNotFound, "invite not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	writeOK(w)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	target, err := s.store.GetUser(id)
	if err != nil {
		if isNotFound(err) {
			writeErr(w, http.StatusNotFound, "user not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	// Refuse to remove the last admin so the instance can't be locked out of its
	// own user management.
	if target.IsAdmin {
		admins, err := s.store.CountAdmins()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "db error")
			return
		}
		if admins <= 1 {
			writeErr(w, http.StatusBadRequest, "cannot remove the last admin")
			return
		}
	}
	if err := s.store.DeleteUser(id); err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	writeOK(w)
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	if users == nil {
		users = []api.User{}
	}
	writeJSON(w, http.StatusOK, users)
}
