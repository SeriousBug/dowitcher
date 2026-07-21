package server

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/auth"
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
	// The invite is not parked in a cookie: the ceremony holds it server-side,
	// so a finish cannot name a different invite than the begin was authorised
	// against.
	s.setCeremonyCookie(w, cid)
	writeJSON(w, http.StatusOK, opts)
}

func (s *Server) handleRegisterFinish(w http.ResponseWriter, r *http.Request) {
	cid, err := s.ceremonyID(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	userID, err := s.auth.FinishRegistration(cid, r)
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

// handleLogoutOthers revokes every session the caller holds except the one they
// are calling from. It is the lever for "I left myself signed in somewhere I no
// longer control" — the passkey stays valid, so the answer to a borrowed laptop
// is not to retire a credential that is still perfectly good.
//
// The caller's own session is kept for the same reason handleDeleteCredential
// keeps it: signing you out of the device you are securing things from is not
// what the button offers. An empty token would revoke everything, but requireAuth
// guarantees there is one.
func (s *Server) handleLogoutOthers(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	n, err := s.store.DeleteUserSessionsExcept(u.ID, sessionTokenFrom(r.Context()))
	if err != nil {
		log.Printf("logout others: revoke sessions for %s: %v", u.ID, err)
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	// An OAuth grant is a headless session with no cookie to keep, so signing out
	// other devices cuts every access and refresh token too: a leaked token must
	// not survive the rotation the user reached for to contain it. The count
	// folds token revocations in with device ones — from the user's side both are
	// "something else that was signed in".
	tokens, err := s.store.DeleteUserOAuthTokens(u.ID)
	if err != nil {
		log.Printf("logout others: revoke oauth tokens for %s: %v", u.ID, err)
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusOK, api.SignedOutOthers{Revoked: n + tokens})
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
//
// Retiring a lost device means nothing while that device still holds a live
// session cookie, so the caller's other sessions go with the passkey — but not
// the caller's own, because being logged out of the phone you are tidying up
// from is not what anyone asked for.
//
// Revocation runs before the delete so that a failure between the two leaves
// too little access rather than too much: the reverse order can end with the
// passkey gone and the lost device still signed in, which is the one outcome
// this handler exists to prevent. The cost is that a delete which then 404s has
// already cut the other sessions; that is a nuisance for the honest caller and
// the safe side of a decision the security of the flow rests on.
func (s *Server) handleDeleteCredential(w http.ResponseWriter, r *http.Request) {
	u, _ := userFrom(r.Context())
	if _, err := s.store.DeleteUserSessionsExcept(u.ID, sessionTokenFrom(r.Context())); err != nil {
		log.Printf("delete credential: revoke sessions for %s: %v", u.ID, err)
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
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
//
// It also revokes every session the user has. This is the lost-device flow: an
// admin reaches for it precisely when something that should not have access
// still does, and a recovery link that leaves the lost device signed in for the
// rest of the TTL answers the wrong half of the problem. All sessions go, not
// all but the admin's, because the admin is not the user being reset.
//
// Revoking before minting means a revocation failure aborts with nothing done
// and the admin can retry, and a mint failure after it costs only a re-issued
// link — the user is logged out either way, which is the outcome that was
// wanted.
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
	if err := s.store.DeleteUserSessions(u.ID); err != nil {
		log.Printf("reset user: revoke sessions for %s: %v", u.ID, err)
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
