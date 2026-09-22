package server

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/auth"
)

// oidcStateCookieName parks the per-attempt OIDC state while the browser is at
// the provider. Scoped to /auth so it rides only the callback, following the
// ceremony cookie's reasoning: a cookie sent where it is not needed is a cookie
// that can leak. Its value is the whole attempt — state, nonce, PKCE verifier
// and where to land afterwards — because unlike the passkey ceremonies there is
// no server-side row to hang them on, and the provider round-trips through the
// browser rather than a request we answer.
const oidcStateCookieName = "dowitcher_oidc_state"

// oidcStateTTL bounds how long a person has at the provider; five minutes is
// the same window a passkey ceremony gets.
const oidcStateTTL = 5 * time.Minute

type oidcAttempt struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
	ReturnTo string `json:"return_to"`
}

// handleAuthProviders is public: the login page asks which sign-in methods this
// instance offers before anyone has a session. It carries a display label and
// nothing else, because everything readable without a session is readable by
// anyone who knows the URL.
func (s *Server) handleAuthProviders(w http.ResponseWriter, r *http.Request) {
	out := api.AuthProviders{}
	if s.oidc != nil {
		out.OIDC = &api.OIDCProvider{Name: s.oidc.DisplayName()}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleOIDCStart begins a login: it mints the per-attempt secrets, parks them
// in a cookie, and sends the browser to the provider.
func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		http.NotFound(w, r)
		return
	}
	attempt := oidcAttempt{
		State:    auth.RandToken(24),
		Nonce:    auth.RandToken(24),
		Verifier: auth.RandToken(32),
		ReturnTo: safeReturnPath(r.URL.Query().Get("return_to")),
	}
	s.setOIDCStateCookie(w, attempt)
	http.Redirect(w, r, s.oidc.AuthCodeURL(attempt.State, attempt.Nonce, attempt.Verifier), http.StatusFound)
}

// handleOIDCCallback completes a login. It is reached by a top-level GET from
// the provider, so the Lax state cookie is sent; everything it needs to trust
// the result — the state, the nonce, the PKCE verifier — is in that cookie and
// nowhere the browser could have rewritten independently of it.
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		http.NotFound(w, r)
		return
	}
	// The attempt is single-use whether or not it succeeds, so it is cleared
	// before anything can fail: a stale state must not be replayable.
	attempt, ok := s.takeOIDCState(w, r)
	if !ok {
		s.oidcFail(w, "That sign-in attempt expired. Start again from the login page.")
		return
	}
	if state := r.URL.Query().Get("state"); subtle.ConstantTimeCompare([]byte(state), []byte(attempt.State)) != 1 {
		s.oidcFail(w, "That sign-in attempt did not come back the way it started. Start again from the login page.")
		return
	}
	if providerErr := r.URL.Query().Get("error"); providerErr != "" {
		log.Printf("oidc: provider returned error: %s: %s", providerErr, r.URL.Query().Get("error_description"))
		s.oidcFail(w, "The sign-in provider refused the request. Start again from the login page.")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		s.oidcFail(w, "The sign-in provider said nothing to sign in with. Start again from the login page.")
		return
	}

	identity, err := s.oidc.Exchange(r.Context(), code, attempt.Verifier, attempt.Nonce)
	if err != nil {
		log.Printf("oidc: exchange code: %v", err)
		s.oidcFail(w, "Could not finish signing in with the provider. Try again.")
		return
	}
	user, _, err := s.store.ProvisionIdentity(identity.Issuer, identity.Subject, identity.Email, identity.Name, identity.Admin)
	if err != nil {
		log.Printf("oidc: provision identity: %v", err)
		s.oidcFail(w, "Could not open your account. Try again.")
		return
	}
	token, exp, err := auth.NewSession(s.store, user.ID)
	if err != nil {
		log.Printf("oidc: start session: %v", err)
		s.oidcFail(w, "Could not start a session. Try again.")
		return
	}
	s.setSessionCookie(w, token, exp)
	http.Redirect(w, r, attempt.ReturnTo, http.StatusFound)
}

// oidcFail renders the one HTML error page this package already ships. An OIDC
// callback is a browser navigation, so JSON would leave the person on a blank
// document.
func (s *Server) oidcFail(w http.ResponseWriter, msg string) {
	renderErrorPage(w, http.StatusBadRequest, msg)
}

func (s *Server) setOIDCStateCookie(w http.ResponseWriter, attempt oidcAttempt) {
	raw, _ := json.Marshal(attempt)
	http.SetCookie(w, &http.Cookie{
		Name:     oidcStateCookieName,
		Value:    base64.RawURLEncoding.EncodeToString(raw),
		Path:     "/auth",
		MaxAge:   int(oidcStateTTL / time.Second),
		HttpOnly: true,
		Secure:   s.cfg.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// takeOIDCState reads and immediately clears the attempt cookie. Clearing is
// part of reading so no caller can forget it.
func (s *Server) takeOIDCState(w http.ResponseWriter, r *http.Request) (oidcAttempt, bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     oidcStateCookieName,
		Value:    "",
		Path:     "/auth",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.Secure,
		SameSite: http.SameSiteLaxMode,
	})
	c, err := r.Cookie(oidcStateCookieName)
	if err != nil {
		return oidcAttempt{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return oidcAttempt{}, false
	}
	var attempt oidcAttempt
	if err := json.Unmarshal(raw, &attempt); err != nil || attempt.State == "" {
		return oidcAttempt{}, false
	}
	return attempt, true
}

// safeReturnPath keeps the post-login redirect on this server. It accepts only
// a same-origin relative path; anything absolute, protocol-relative, or
// backslash-smuggled falls back to "/". The value rides through the provider in
// the state cookie, so it is validated once on the way in and trusted on the
// way out — which is exactly why it must be validated here.
func safeReturnPath(p string) string {
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return "/"
	}
	// Some browsers read a backslash as a slash, so "/\evil.example" is an
	// off-site redirect that a naive prefix check would pass.
	if strings.ContainsAny(p, "\\\r\n") {
		return "/"
	}
	u, err := url.Parse(p)
	if err != nil || u.IsAbs() || u.Host != "" {
		return "/"
	}
	return p
}
