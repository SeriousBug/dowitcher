package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/SeriousBug/longbox/internal/api"
	"github.com/SeriousBug/longbox/internal/auth"
	"github.com/SeriousBug/longbox/internal/store"
)

type userFromSession struct {
	user  api.User
	token string
}

// currentUser resolves the session cookie to a user, or returns ok=false.
//
// When the dev-auth bypass is active it short-circuits the cookie entirely and
// hands back the configured user. auth.DevAuthFromEnv has already refused to
// build one under an https origin, so reaching this branch at all means the
// operator asked for it on a plaintext origin.
func (s *Server) currentUser(r *http.Request) (u userFromSession, ok bool) {
	if s.cfg.DevAuth != nil {
		dev, err := s.cfg.DevAuth.User(s.store)
		if err != nil {
			log.Printf("dev auth: resolve user: %v", err)
			return u, false
		}
		u.user = dev
		return u, true
	}
	c, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		return u, false
	}
	user, err := s.store.SessionUser(c.Value)
	if err != nil {
		return u, false
	}
	u.user = user
	u.token = c.Value
	return u, true
}

// requireAuth gates a handler behind a valid session.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := s.currentUser(r)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userCtxKey{}, u.user)))
	}
}

// requireAdmin gates a handler behind a valid admin session.
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := s.currentUser(r)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		if !u.user.IsAdmin {
			writeErr(w, http.StatusForbidden, "admin only")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userCtxKey{}, u.user)))
	}
}

// setSessionCookie issues the HttpOnly session cookie. Secure tracks the origin
// scheme rather than being hardcoded, because a Secure cookie is simply dropped
// over plain http and would break every local run.
func (s *Server) setSessionCookie(w http.ResponseWriter, token string, exp time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  exp,
		HttpOnly: true,
		Secure:   s.cfg.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

const (
	ceremonyCookieName = "longbox_ceremony"
	inviteCookieName   = "longbox_invite"
)

// setCeremonyCookie parks the in-flight ceremony id. Path is /auth so it is not
// attached to any other request: it is only ever read by the matching finish
// handler, and a cookie sent where it is not needed is a cookie that can leak.
// MaxAge matches the server-side ceremony TTL.
func (s *Server) setCeremonyCookie(w http.ResponseWriter, id string) {
	s.setAuthCookie(w, ceremonyCookieName, id)
}

func (s *Server) setAuthCookie(w http.ResponseWriter, name, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/auth",
		MaxAge:   int(auth.CeremonyTTL / time.Second),
		HttpOnly: true,
		Secure:   s.cfg.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) ceremonyID(r *http.Request) (string, error) {
	c, err := r.Cookie(ceremonyCookieName)
	if err != nil {
		return "", errors.New("no ceremony in progress")
	}
	return c.Value, nil
}

// isNotFound reports a store miss.
func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }
