package server

import (
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/SeriousBug/dowitcher/internal/auth"
	"github.com/SeriousBug/dowitcher/internal/oauth"
	"github.com/SeriousBug/dowitcher/internal/store"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// Dowitcher's OAuth 2.1 authorization server for the MCP endpoint. It exists
// because Claude's connector UI and Claude Code only speak the MCP OAuth flow —
// discovery, dynamic client registration, PKCE authorize, token exchange — with
// no field for a static bearer. The browser login step reuses the passkey
// session; this server never handles a password.
//
// The authorization-server half is hand-rolled to match the repo's stdlib-mux,
// store-backed style rather than pulled in as a framework. The resource-server
// half (the /mcp bearer check and the protected-resource metadata) comes from
// the MCP Go SDK.

// scopeMCP is the single scope. Per-user visibility is already enforced in SQL
// by every tool, so there is no read/write split to express as scopes.
const scopeMCP = "mcp"

// oauthCSRFCookieName parks the anti-CSRF token for the consent form. Scoped to
// /authorize so it rides only the consent POST, following the same reasoning as
// the ceremony cookie.
const oauthCSRFCookieName = "dowitcher_oauth_csrf"

// registerOAuthRoutes wires the authorization-server endpoints. Called only when
// MCP is enabled, so a stock instance advertises nothing. All absolute URLs are
// built from cfg.Origin.
func (s *Server) registerOAuthRoutes() {
	// Protected-resource metadata (RFC 9728), served by the SDK handler with its
	// own CORS. Registered at the base path and the /mcp path-insertion variant a
	// client may probe. Bare pattern (no method) so the handler's own OPTIONS/GET
	// handling applies.
	prm := &oauthex.ProtectedResourceMetadata{
		Resource:               s.cfg.Origin + "/mcp",
		AuthorizationServers:   []string{s.cfg.Origin},
		BearerMethodsSupported: []string{"header"},
		ScopesSupported:        []string{scopeMCP},
	}
	prmHandler := sdkauth.ProtectedResourceMetadataHandler(prm)
	s.mux.Handle("/.well-known/oauth-protected-resource", prmHandler)
	s.mux.Handle("/.well-known/oauth-protected-resource/mcp", prmHandler)

	// Authorization-server metadata (RFC 8414), base path and /mcp variant.
	s.mux.HandleFunc("/.well-known/oauth-authorization-server", s.handleAuthServerMetadata)
	s.mux.HandleFunc("/.well-known/oauth-authorization-server/mcp", s.handleAuthServerMetadata)

	s.mux.HandleFunc("POST /register", s.handleRegister)
	s.mux.HandleFunc("GET /authorize", s.handleAuthorize)
	s.mux.HandleFunc("POST /authorize", s.handleAuthorizeConsent)
	s.mux.HandleFunc("POST /token", s.handleToken)
}

func (s *Server) handleAuthServerMetadata(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	meta := oauthex.AuthServerMeta{
		Issuer:                            s.cfg.Origin,
		AuthorizationEndpoint:             s.cfg.Origin + "/authorize",
		TokenEndpoint:                     s.cfg.Origin + "/token",
		RegistrationEndpoint:              s.cfg.Origin + "/register",
		ResponseTypesSupported:            []string{"code"},
		GrantTypesSupported:               []string{"authorization_code", "refresh_token"},
		CodeChallengeMethodsSupported:     []string{"S256"},
		TokenEndpointAuthMethodsSupported: []string{"none"},
		ScopesSupported:                   []string{scopeMCP},
	}
	writeJSON(w, http.StatusOK, meta)
}

// handleRegister is dynamic client registration (RFC 7591). It stays
// unauthenticated per the MCP spec: a client must be able to register before it
// can obtain any credential. Unused client rows are cheap and are not swept in
// v1.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req oauthex.ClientRegistrationMetadata
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "malformed registration body")
		return
	}
	if len(req.RedirectURIs) == 0 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", "at least one redirect_uri is required")
		return
	}
	for _, u := range req.RedirectURIs {
		if !validRedirectURI(u) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri",
				"redirect_uri must be https or a loopback http address")
			return
		}
	}
	// A public client bound by PKCE is the only shape this server issues, so a
	// client asking for any secret-based method is refused rather than silently
	// downgraded. An omitted method defaults to "none" here (the MCP client
	// default), not RFC 7591's "client_secret_basic".
	if req.TokenEndpointAuthMethod != "" && req.TokenEndpointAuthMethod != "none" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata",
			"only token_endpoint_auth_method=none is supported")
		return
	}
	id := store.NewID()
	if err := s.store.CreateOAuthClient(id, req.ClientName, req.RedirectURIs); err != nil {
		log.Printf("oauth register: %v", err)
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "could not register client")
		return
	}
	resp := oauthex.ClientRegistrationResponse{
		ClientRegistrationMetadata: oauthex.ClientRegistrationMetadata{
			RedirectURIs:            req.RedirectURIs,
			TokenEndpointAuthMethod: "none",
			GrantTypes:              []string{"authorization_code", "refresh_token"},
			ResponseTypes:           []string{"code"},
			ClientName:              req.ClientName,
			Scope:                   scopeMCP,
		},
		ClientID:         id,
		ClientIDIssuedAt: time.Now(),
	}
	writeJSON(w, http.StatusCreated, &resp)
}

// authorizeParams is the validated shape of an /authorize request, shared by the
// GET (consent render) and POST (consent submit) halves.
type authorizeParams struct {
	ClientID      string
	RedirectURI   string
	State         string
	CodeChallenge string
	Scope         string
}

// validateAuthorize checks everything about an authorize request except the
// user's session. It returns the params on success. On a client_id or
// redirect_uri problem it returns redirectOK=false: those must render an error
// page and never redirect, because redirecting to an unvalidated URI is the
// open-redirect the exact-match check exists to prevent. Any other problem is
// safe to report to the validated redirect_uri.
func (s *Server) validateAuthorize(r *http.Request) (p authorizeParams, redirectOK bool, errMsg string) {
	q := r.URL.Query()
	clientID := q.Get("client_id")
	redirectURI := q.Get("redirect_uri")
	client, err := s.store.OAuthClient(clientID)
	if err != nil {
		return p, false, "unknown client"
	}
	// Exact string match against the registered set. No normalization, prefix or
	// wildcard: a redirect_uri that is not byte-for-byte one we stored is an
	// attempt to redirect somewhere the client never registered.
	if !exactContains(client.RedirectURIs, redirectURI) {
		return p, false, "redirect_uri does not match a registered value"
	}
	// From here the redirect_uri is trusted, so errors may travel back to it.
	p = authorizeParams{
		ClientID:      clientID,
		RedirectURI:   redirectURI,
		State:         q.Get("state"),
		CodeChallenge: q.Get("code_challenge"),
		Scope:         q.Get("scope"),
	}
	if q.Get("response_type") != "code" {
		return p, true, "unsupported_response_type"
	}
	if p.State == "" {
		return p, true, "state is required"
	}
	// PKCE is mandatory and only S256 is accepted; "plain" defeats the point.
	if p.CodeChallenge == "" {
		return p, true, "code_challenge is required"
	}
	if q.Get("code_challenge_method") != "S256" {
		return p, true, "code_challenge_method must be S256"
	}
	return p, true, ""
}

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	p, redirectOK, errMsg := s.validateAuthorize(r)
	if errMsg != "" {
		s.authorizeError(w, r, p, redirectOK, errMsg)
		return
	}
	// The passkey session is the login. A caller with no session is sent to the
	// SPA login, which returns here once it has one.
	if _, ok := s.currentUser(r); !ok {
		returnTo := "/authorize?" + r.URL.RawQuery
		http.Redirect(w, r, "/login?return_to="+url.QueryEscape(returnTo), http.StatusFound)
		return
	}
	csrf := oauth.NewToken()
	s.setOAuthCSRFCookie(w, csrf)
	s.renderConsent(w, p, csrf)
}

func (s *Server) handleAuthorizeConsent(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	u, ok := s.currentUser(r)
	if !ok {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	// Re-validate from the posted fields, not the trusted GET: the consent POST
	// is a fresh request and must stand on its own.
	client, err := s.store.OAuthClient(r.FormValue("client_id"))
	if err != nil {
		renderErrorPage(w, http.StatusBadRequest, "unknown client")
		return
	}
	redirectURI := r.FormValue("redirect_uri")
	if !exactContains(client.RedirectURIs, redirectURI) {
		renderErrorPage(w, http.StatusBadRequest, "redirect_uri does not match a registered value")
		return
	}
	p := authorizeParams{
		ClientID:      client.ID,
		RedirectURI:   redirectURI,
		State:         r.FormValue("state"),
		CodeChallenge: r.FormValue("code_challenge"),
		Scope:         r.FormValue("scope"),
	}
	if p.CodeChallenge == "" {
		redirectAuthError(w, r, p, "invalid_request")
		return
	}
	// Double-submit CSRF: the token is both a cookie and a form field, and an
	// attacker's cross-site POST can forge one but not read the cookie to match
	// it.
	cookie, err := r.Cookie(oauthCSRFCookieName)
	if err != nil || cookie.Value == "" || cookie.Value != r.FormValue("csrf") {
		renderErrorPage(w, http.StatusBadRequest, "invalid or expired consent form, go back and try again")
		return
	}
	s.clearOAuthCSRFCookie(w)

	if r.FormValue("decision") != "allow" {
		redirectAuthError(w, r, p, "access_denied")
		return
	}
	code := oauth.NewToken()
	exp := time.Now().Add(oauth.CodeTTL).Unix()
	if err := s.store.CreateAuthorizationCode(auth.HashToken(code), p.ClientID, u.user.ID,
		p.RedirectURI, p.CodeChallenge, scopeMCP, exp); err != nil {
		log.Printf("oauth authorize: create code: %v", err)
		redirectAuthError(w, r, p, "server_error")
		return
	}
	redirectWithCode(w, r, p, code)
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "malformed form")
		return
	}
	switch r.FormValue("grant_type") {
	case "authorization_code":
		s.tokenFromCode(w, r)
	case "refresh_token":
		s.tokenFromRefresh(w, r)
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "")
	}
}

func (s *Server) tokenFromCode(w http.ResponseWriter, r *http.Request) {
	code := r.FormValue("code")
	if code == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "code is required")
		return
	}
	ac, err := s.store.ConsumeAuthorizationCode(auth.HashToken(code))
	if err != nil {
		// A miss covers unknown, already-redeemed and expired alike: all three
		// are an invalid_grant, and distinguishing them would leak which codes
		// ever existed.
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "invalid or expired code")
		return
	}
	if r.FormValue("client_id") != ac.ClientID {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "client mismatch")
		return
	}
	// redirect_uri must match the one the code was issued against (RFC 6749
	// §4.1.3), so a code stolen mid-flight cannot be redeemed toward a different
	// endpoint.
	if r.FormValue("redirect_uri") != ac.RedirectURI {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri mismatch")
		return
	}
	if !oauth.VerifyS256(r.FormValue("code_verifier"), ac.CodeChallenge) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}
	s.issueTokens(w, ac.ClientID, ac.UserID, ac.Scope)
}

func (s *Server) tokenFromRefresh(w http.ResponseWriter, r *http.Request) {
	refresh := r.FormValue("refresh_token")
	if refresh == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "refresh_token is required")
		return
	}
	// Consume rotates: the old token is deleted here, so a replay of it finds
	// nothing and fails.
	rt, err := s.store.ConsumeRefreshToken(auth.HashToken(refresh))
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "invalid or expired refresh token")
		return
	}
	if r.FormValue("client_id") != rt.ClientID {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "client mismatch")
		return
	}
	s.issueTokens(w, rt.ClientID, rt.UserID, rt.Scope)
}

// tokenResponse is the RFC 6749 §5.1 success body.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

// issueTokens mints and stores a fresh access+refresh pair and writes the token
// response. Both are stored hashed; the plain values leave only in this body.
func (s *Server) issueTokens(w http.ResponseWriter, clientID, userID, scope string) {
	if scope == "" {
		scope = scopeMCP
	}
	access := oauth.NewToken()
	refresh := oauth.NewToken()
	accessExp := time.Now().Add(oauth.AccessTTL).Unix()
	refreshExp := time.Now().Add(oauth.RefreshTTL).Unix()
	if err := s.store.CreateAccessToken(auth.HashToken(access), clientID, userID, scope, accessExp); err != nil {
		log.Printf("oauth token: create access: %v", err)
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	if err := s.store.CreateRefreshToken(auth.HashToken(refresh), clientID, userID, scope, refreshExp); err != nil {
		log.Printf("oauth token: create refresh: %v", err)
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	writeJSON(w, http.StatusOK, tokenResponse{
		AccessToken:  access,
		TokenType:    "Bearer",
		ExpiresIn:    int(oauth.AccessTTL / time.Second),
		RefreshToken: refresh,
		Scope:        scope,
	})
}

// --- redirects and error rendering ---

// authorizeError reports a validation failure either back to the client's
// redirect_uri (when it was validated) or as an error page (when the failure was
// the client_id or redirect_uri itself).
func (s *Server) authorizeError(w http.ResponseWriter, r *http.Request, p authorizeParams, redirectOK bool, msg string) {
	if !redirectOK {
		renderErrorPage(w, http.StatusBadRequest, msg)
		return
	}
	redirectAuthError(w, r, p, oauthErrorCode(msg))
}

// oauthErrorCode maps an internal message to the RFC 6749 error code a client
// expects on the redirect.
func oauthErrorCode(msg string) string {
	switch msg {
	case "unsupported_response_type":
		return "unsupported_response_type"
	default:
		return "invalid_request"
	}
}

func redirectWithCode(w http.ResponseWriter, r *http.Request, p authorizeParams, code string) {
	u, err := url.Parse(p.RedirectURI)
	if err != nil {
		renderErrorPage(w, http.StatusBadRequest, "invalid redirect_uri")
		return
	}
	q := u.Query()
	q.Set("code", code)
	q.Set("state", p.State)
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func redirectAuthError(w http.ResponseWriter, r *http.Request, p authorizeParams, code string) {
	u, err := url.Parse(p.RedirectURI)
	if err != nil {
		renderErrorPage(w, http.StatusBadRequest, code)
		return
	}
	q := u.Query()
	q.Set("error", code)
	if p.State != "" {
		q.Set("state", p.State)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// writeOAuthError writes an RFC 6749 error body. The frontend never sees these —
// they go to the OAuth client — so the text is a developer-facing description.
func writeOAuthError(w http.ResponseWriter, status int, code, desc string) {
	w.Header().Set("Cache-Control", "no-store")
	body := map[string]string{"error": code}
	if desc != "" {
		body["error_description"] = desc
	}
	writeJSON(w, status, body)
}

var consentTmpl = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Authorize · Dowitcher</title>
<style>
  body { font-family: system-ui, sans-serif; background: #0a0809; color: #eee;
    display: flex; min-height: 100vh; align-items: center; justify-content: center; margin: 0; }
  .card { max-width: 26rem; padding: 2rem; background: #161314; border: 1px solid #2a2527;
    border-radius: 14px; }
  h1 { font-size: 1.25rem; margin: 0 0 .5rem; }
  p { color: #a89ea2; line-height: 1.6; font-size: .9rem; }
  strong { color: #eee; }
  .row { display: flex; gap: .75rem; margin-top: 1.5rem; }
  button { flex: 1; padding: .7rem; border-radius: 8px; border: 0; font-size: .95rem;
    font-weight: 600; cursor: pointer; }
  .allow { background: #d6336c; color: #fff; }
  .deny { background: transparent; color: #a89ea2; border: 1px solid #2a2527; }
</style></head><body>
<div class="card">
  <h1>Authorize access</h1>
  <p><strong>{{.ClientName}}</strong> wants to connect to your Dowitcher library
  through the MCP server. It will act as you and see only what you can see.</p>
  <form method="post" action="/authorize">
    <input type="hidden" name="client_id" value="{{.ClientID}}">
    <input type="hidden" name="redirect_uri" value="{{.RedirectURI}}">
    <input type="hidden" name="state" value="{{.State}}">
    <input type="hidden" name="code_challenge" value="{{.CodeChallenge}}">
    <input type="hidden" name="scope" value="{{.Scope}}">
    <input type="hidden" name="csrf" value="{{.CSRF}}">
    <div class="row">
      <button class="deny" type="submit" name="decision" value="deny">Deny</button>
      <button class="allow" type="submit" name="decision" value="allow">Allow</button>
    </div>
  </form>
</div></body></html>`))

func (s *Server) renderConsent(w http.ResponseWriter, p authorizeParams, csrf string) {
	client, err := s.store.OAuthClient(p.ClientID)
	if err != nil {
		renderErrorPage(w, http.StatusBadRequest, "unknown client")
		return
	}
	name := client.Name
	if name == "" {
		name = "An application"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	consentTmpl.Execute(w, map[string]string{
		"ClientName":    name,
		"ClientID":      p.ClientID,
		"RedirectURI":   p.RedirectURI,
		"State":         p.State,
		"CodeChallenge": p.CodeChallenge,
		"Scope":         p.Scope,
		"CSRF":          csrf,
	})
}

var errorTmpl = template.Must(template.New("oautherr").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Error · Dowitcher</title>
<style>
  body { font-family: system-ui, sans-serif; background: #0a0809; color: #eee;
    display: flex; min-height: 100vh; align-items: center; justify-content: center; margin: 0; }
  .card { max-width: 26rem; padding: 2rem; background: #161314; border: 1px solid #2a2527;
    border-radius: 14px; }
  h1 { font-size: 1.25rem; margin: 0 0 .5rem; }
  p { color: #a89ea2; line-height: 1.6; font-size: .9rem; }
</style></head><body>
<div class="card"><h1>Could not authorize</h1><p>{{.}}</p></div>
</body></html>`))

func renderErrorPage(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	errorTmpl.Execute(w, msg)
}

func (s *Server) setOAuthCSRFCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     oauthCSRFCookieName,
		Value:    token,
		Path:     "/authorize",
		MaxAge:   int(auth.CeremonyTTL / time.Second),
		HttpOnly: true,
		Secure:   s.cfg.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearOAuthCSRFCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     oauthCSRFCookieName,
		Value:    "",
		Path:     "/authorize",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// validRedirectURI accepts https URLs and loopback http URLs (native clients
// and Claude Code use http://127.0.0.1:<port> callbacks). Everything else is
// rejected at registration so a stored redirect_uri is always safe to redirect
// to once matched.
func validRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		host := u.Hostname()
		return host == "localhost" || host == "127.0.0.1" || host == "::1"
	default:
		return false
	}
}

func exactContains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}
