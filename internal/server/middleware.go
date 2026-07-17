package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
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
// hands back the configured user. Reaching that branch at all means this binary
// was built with -tags dev and the operator set the env var on a loopback
// listener, but it is still checked against the request itself first — see
// devAuthTLSEvidence.
func (s *Server) currentUser(r *http.Request) (u userFromSession, ok bool) {
	if s.cfg.DevAuth != nil {
		if reason := devAuthTLSEvidence(r); reason != "" {
			// Refusing beats resolving: every other guard on the bypass reads
			// configuration, and configuration is what is wrong when the bypass
			// is dangerous. This is the only check that looks at what is
			// actually reaching the server.
			log.Printf("dev auth: refusing to resolve a user for a request bearing %s; "+
				"the bypass is for local development only", reason)
			return u, false
		}
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

// devAuthTLSEvidence names the evidence, if any, that a request reached this
// server over TLS or through a reverse proxy. It returns "" when there is none.
//
// This is the dev-auth guard that has teeth. The boot-time checks ask the
// operator's configuration whether the operator made a mistake, which is
// circular: LONGBOX_ORIGIN defaults to http://localhost:8080, so a TLS proxy in
// front of an instance whose origin was never set looks exactly like a laptop.
// A request cannot lie in the same direction — a developer's own curl to
// localhost carries none of these, while anything that came through a real
// deployment's proxy carries at least one.
//
// Any of them is disqualifying on its own, and no attempt is made to decide
// whether a header is "trustworthy". An attacker who sets X-Forwarded-Proto by
// hand only turns the bypass off, which is not an attack; the failure worth
// preventing is the opposite one.
func devAuthTLSEvidence(r *http.Request) string {
	switch {
	case r.TLS != nil:
		return "a TLS connection"
	case strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https"):
		return "X-Forwarded-Proto: https"
	case r.Header.Get("X-Forwarded-For") != "":
		return "an X-Forwarded-For header"
	}
	return ""
}

// sessionTokenCtxKey carries the caller's own session token, for the handlers
// that must tell the caller's session apart from the same user's other ones.
//
// It is a second key rather than a wider value under userCtxKey because
// userFrom's type assertion is how every handler reads the user: widening that
// value would not break the build, it would quietly hand all of them a zero
// user.
type sessionTokenCtxKey struct{}

// sessionTokenFrom returns the caller's session token, or "" if the request was
// authenticated without one — which is the dev-auth bypass, since it resolves a
// user with no session behind it.
func sessionTokenFrom(ctx context.Context) string {
	t, _ := ctx.Value(sessionTokenCtxKey{}).(string)
	return t
}

// authed builds the context an authenticated handler runs with.
func authed(r *http.Request, u userFromSession) context.Context {
	ctx := context.WithValue(r.Context(), userCtxKey{}, u.user)
	return context.WithValue(ctx, sessionTokenCtxKey{}, u.token)
}

// requireAuth gates a handler behind a valid session.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := s.currentUser(r)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		next(w, r.WithContext(authed(r, u)))
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
		next(w, r.WithContext(authed(r, u)))
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

const ceremonyCookieName = "longbox_ceremony"

// setCeremonyCookie parks the in-flight ceremony id. Path is /auth so it is not
// attached to any other request: it is only ever read by the matching finish
// handler, and a cookie sent where it is not needed is a cookie that can leak.
// MaxAge matches the server-side ceremony TTL.
//
// The ceremony id is the only thing a registration round-trips through the
// client. Everything the finish decides on — the invite, the rights it carries,
// the user it is for — hangs off the server-side ceremony, so the client cannot
// pair a ceremony with anything other than what began it.
func (s *Server) setCeremonyCookie(w http.ResponseWriter, id string) {
	http.SetCookie(w, &http.Cookie{
		Name:     ceremonyCookieName,
		Value:    id,
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
