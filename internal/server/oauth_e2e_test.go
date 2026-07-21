package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/SeriousBug/dowitcher/internal/auth"
	"github.com/SeriousBug/dowitcher/internal/store"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// oauthServer brings up a full server with MCP (and so OAuth) enabled, plus a
// seeded user and library comic. The returned client does not follow redirects,
// so a test can inspect each 302 in the authorization-code flow. A session row
// is planted straight into the cookie jar: a passkey login only ever produces a
// session row, so standing one up directly is the same starting state without
// the WebAuthn ceremony.
func oauthServer(t *testing.T) (ts *httptest.Server, st *store.Store, client *http.Client, userID string) {
	t.Helper()
	_, ts, st, client = newTestServer(t, func(c *Config) {
		c.MCPEnabled = true
	})
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	alice, err := st.CreateUser(store.NewID(), "alice", true)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := st.UpsertComic(store.ComicRow{
		ID: store.NewID(), Path: "lib/Public.cbz", Title: "Public",
		Source: store.SourceLibrary, PageCount: 10,
	}); err != nil {
		t.Fatalf("seed comic: %v", err)
	}

	session := store.NewID()
	if err := st.CreateSession(session, alice.ID, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatalf("plant session: %v", err)
	}
	u, _ := url.Parse(ts.URL)
	client.Jar.SetCookies(u, []*http.Cookie{{Name: auth.SessionCookieName, Value: session}})
	return ts, st, client, alice.ID
}

func pkce() (verifier, challenge string) {
	verifier = "verifier-0123456789abcdefghijklmnopqrstuvwxyz"
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge
}

// registerClient runs DCR and returns the issued client_id.
func registerClient(t *testing.T, ts *httptest.Server, client *http.Client, redirectURI string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"redirect_uris":              []string{redirectURI},
		"client_name":                "Test Connector",
		"token_endpoint_auth_method": "none",
	})
	resp, data := post(t, client, ts.URL+"/register", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: status %d, body %s", resp.StatusCode, data)
	}
	var out struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("register decode: %v", err)
	}
	if out.ClientID == "" {
		t.Fatal("register returned no client_id")
	}
	return out.ClientID
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

// authorizeGET hits /authorize and scrapes the CSRF token out of the consent
// page. It fails if the response is not the consent page.
func authorizeGET(t *testing.T, ts *httptest.Server, client *http.Client, q url.Values) string {
	t.Helper()
	resp, data := getReq(t, client, ts.URL+"/authorize?"+q.Encode())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorize GET: status %d, body %s", resp.StatusCode, data)
	}
	m := csrfRe.FindSubmatch(data)
	if m == nil {
		t.Fatalf("no csrf token in consent page: %s", data)
	}
	return string(m[1])
}

func authorizeParamsFor(clientID, redirectURI, challenge, state string) url.Values {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("scope", "mcp")
	q.Set("state", state)
	return q
}

// consentAllow posts the consent form with decision=allow and returns the code
// captured from the redirect back to the client. It asserts the state echoes.
func consentAllow(t *testing.T, ts *httptest.Server, client *http.Client, clientID, redirectURI, challenge, state, csrf string) string {
	t.Helper()
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("redirect_uri", redirectURI)
	form.Set("state", state)
	form.Set("code_challenge", challenge)
	form.Set("scope", "mcp")
	form.Set("csrf", csrf)
	form.Set("decision", "allow")
	resp, err := client.PostForm(ts.URL+"/authorize", form)
	if err != nil {
		t.Fatalf("consent POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("consent should redirect, got %d", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("bad redirect: %v", err)
	}
	if loc.Query().Get("state") != state {
		t.Errorf("state not echoed: got %q, want %q", loc.Query().Get("state"), state)
	}
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatalf("no code in redirect: %s", resp.Header.Get("Location"))
	}
	return code
}

// tokenExchange posts to /token and returns the decoded body plus the status.
func tokenExchange(t *testing.T, ts *httptest.Server, client *http.Client, form url.Values) (map[string]any, int) {
	t.Helper()
	resp, err := client.PostForm(ts.URL+"/token", form)
	if err != nil {
		t.Fatalf("token POST: %v", err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var out map[string]any
	json.Unmarshal(data, &out)
	return out, resp.StatusCode
}

func codeGrantForm(clientID, redirectURI, code, verifier string) url.Values {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", clientID)
	form.Set("redirect_uri", redirectURI)
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	return form
}

// TestOAuthFullFlow runs the whole authorization-code flow end to end and then
// makes a real MCP call with the issued access token.
func TestOAuthFullFlow(t *testing.T) {
	ts, _, client, _ := oauthServer(t)
	redirectURI := "http://127.0.0.1:9999/callback"

	// Discovery documents.
	prm, _ := getReq(t, client, ts.URL+"/.well-known/oauth-protected-resource")
	if prm.StatusCode != http.StatusOK {
		t.Fatalf("protected-resource metadata: %d", prm.StatusCode)
	}
	_, prmBody := getReq(t, client, ts.URL+"/.well-known/oauth-protected-resource")
	var prmDoc struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	json.Unmarshal(prmBody, &prmDoc)
	if prmDoc.Resource != ts.URL+"/mcp" {
		t.Errorf("resource = %q, want %q", prmDoc.Resource, ts.URL+"/mcp")
	}
	if len(prmDoc.AuthorizationServers) != 1 || prmDoc.AuthorizationServers[0] != ts.URL {
		t.Errorf("authorization_servers = %v, want [%s]", prmDoc.AuthorizationServers, ts.URL)
	}

	_, asBody := getReq(t, client, ts.URL+"/.well-known/oauth-authorization-server")
	var asDoc struct {
		Issuer                        string   `json:"issuer"`
		AuthorizationEndpoint         string   `json:"authorization_endpoint"`
		TokenEndpoint                 string   `json:"token_endpoint"`
		RegistrationEndpoint          string   `json:"registration_endpoint"`
		CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
	}
	json.Unmarshal(asBody, &asDoc)
	if asDoc.Issuer != ts.URL || asDoc.AuthorizationEndpoint != ts.URL+"/authorize" ||
		asDoc.TokenEndpoint != ts.URL+"/token" || asDoc.RegistrationEndpoint != ts.URL+"/register" {
		t.Errorf("auth-server metadata endpoints wrong: %+v", asDoc)
	}
	if len(asDoc.CodeChallengeMethodsSupported) != 1 || asDoc.CodeChallengeMethodsSupported[0] != "S256" {
		t.Errorf("PKCE methods = %v, want [S256]", asDoc.CodeChallengeMethodsSupported)
	}

	clientID := registerClient(t, ts, client, redirectURI)
	verifier, challenge := pkce()
	state := "state-123"

	q := authorizeParamsFor(clientID, redirectURI, challenge, state)
	csrf := authorizeGET(t, ts, client, q)
	code := consentAllow(t, ts, client, clientID, redirectURI, challenge, state, csrf)

	tok, status := tokenExchange(t, ts, client, codeGrantForm(clientID, redirectURI, code, verifier))
	if status != http.StatusOK {
		t.Fatalf("token exchange: status %d, body %v", status, tok)
	}
	access, _ := tok["access_token"].(string)
	if access == "" || tok["token_type"] != "Bearer" || tok["refresh_token"] == "" {
		t.Fatalf("token response missing fields: %v", tok)
	}

	// The issued access token opens a real MCP session and sees the seeded comic.
	sess := mcpConnect(t, ts.URL, access)
	res, err := sess.CallTool(context.Background(), &sdk.CallToolParams{Name: "list_comics", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("list_comics: %v", err)
	}
	if res.IsError {
		t.Fatalf("list_comics errored: %+v", res.Content)
	}
	b, _ := json.Marshal(res.StructuredContent)
	if !regexp.MustCompile(`"Public"`).Match(b) {
		t.Errorf("issued token should see the seeded comic, got %s", b)
	}
}

// TestOAuthReplayedCodeFails: a code is single-use; a second token exchange with
// the same code is invalid_grant.
func TestOAuthReplayedCodeFails(t *testing.T) {
	ts, _, client, _ := oauthServer(t)
	redirectURI := "http://127.0.0.1:9999/callback"
	clientID := registerClient(t, ts, client, redirectURI)
	verifier, challenge := pkce()

	csrf := authorizeGET(t, ts, client, authorizeParamsFor(clientID, redirectURI, challenge, "s"))
	code := consentAllow(t, ts, client, clientID, redirectURI, challenge, "s", csrf)

	if _, status := tokenExchange(t, ts, client, codeGrantForm(clientID, redirectURI, code, verifier)); status != http.StatusOK {
		t.Fatalf("first exchange should succeed, got %d", status)
	}
	out, status := tokenExchange(t, ts, client, codeGrantForm(clientID, redirectURI, code, verifier))
	if status != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Errorf("replayed code should be invalid_grant/400, got %d %v", status, out)
	}
}

// TestOAuthWrongVerifierFails: a code_verifier that does not hash to the stored
// challenge is rejected.
func TestOAuthWrongVerifierFails(t *testing.T) {
	ts, _, client, _ := oauthServer(t)
	redirectURI := "http://127.0.0.1:9999/callback"
	clientID := registerClient(t, ts, client, redirectURI)
	_, challenge := pkce()

	csrf := authorizeGET(t, ts, client, authorizeParamsFor(clientID, redirectURI, challenge, "s"))
	code := consentAllow(t, ts, client, clientID, redirectURI, challenge, "s", csrf)

	out, status := tokenExchange(t, ts, client, codeGrantForm(clientID, redirectURI, code, "not-the-verifier"))
	if status != http.StatusBadRequest || out["error"] != "invalid_grant" {
		t.Errorf("wrong verifier should be invalid_grant/400, got %d %v", status, out)
	}
}

// TestOAuthPlainMethodRejected: only S256 is accepted; a plain challenge method
// is refused at /authorize.
func TestOAuthPlainMethodRejected(t *testing.T) {
	ts, _, client, _ := oauthServer(t)
	redirectURI := "http://127.0.0.1:9999/callback"
	clientID := registerClient(t, ts, client, redirectURI)
	_, challenge := pkce()

	q := authorizeParamsFor(clientID, redirectURI, challenge, "s")
	q.Set("code_challenge_method", "plain")
	resp, _ := getReq(t, client, ts.URL+"/authorize?"+q.Encode())
	// A validated redirect_uri carries the error back as a redirect, not a code.
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("plain method should redirect with an error, got %d", resp.StatusCode)
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if loc.Query().Get("error") == "" || loc.Query().Get("code") != "" {
		t.Errorf("plain method should yield an error and no code, got %s", resp.Header.Get("Location"))
	}
}

// TestOAuthNoSessionRedirectsToLogin: an unauthenticated /authorize is sent to
// the SPA login with a return_to pointing back at /authorize.
func TestOAuthNoSessionRedirectsToLogin(t *testing.T) {
	ts, _, client, _ := oauthServer(t)
	redirectURI := "http://127.0.0.1:9999/callback"
	clientID := registerClient(t, ts, client, redirectURI)
	_, challenge := pkce()

	// Drop the planted session so the caller is anonymous.
	u, _ := url.Parse(ts.URL)
	client.Jar.SetCookies(u, []*http.Cookie{{Name: auth.SessionCookieName, Value: "", MaxAge: -1}})

	q := authorizeParamsFor(clientID, redirectURI, challenge, "s")
	resp, _ := getReq(t, client, ts.URL+"/authorize?"+q.Encode())
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("no session should redirect, got %d", resp.StatusCode)
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if loc.Path != "/login" {
		t.Fatalf("should redirect to /login, got %s", loc.Path)
	}
	rt := loc.Query().Get("return_to")
	if rt == "" || rt[:len("/authorize")] != "/authorize" {
		t.Errorf("return_to should point back at /authorize, got %q", rt)
	}
}

// TestOAuthMismatchedRedirectURIErrorPage: a redirect_uri that was not
// registered renders an error page and never redirects — the open-redirect
// guard.
func TestOAuthMismatchedRedirectURIErrorPage(t *testing.T) {
	ts, _, client, _ := oauthServer(t)
	registered := "http://127.0.0.1:9999/callback"
	clientID := registerClient(t, ts, client, registered)
	_, challenge := pkce()

	q := authorizeParamsFor(clientID, "http://evil.test/steal", challenge, "s")
	resp, _ := getReq(t, client, ts.URL+"/authorize?"+q.Encode())
	if resp.StatusCode == http.StatusFound {
		t.Fatalf("a mismatched redirect_uri must not redirect; got a 302 to %s", resp.Header.Get("Location"))
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("mismatched redirect_uri should render an error page (400), got %d", resp.StatusCode)
	}
}

// TestOAuthRefreshRotation: a refresh token mints a new pair, and the old
// refresh token is dead after use.
func TestOAuthRefreshRotation(t *testing.T) {
	ts, _, client, _ := oauthServer(t)
	redirectURI := "http://127.0.0.1:9999/callback"
	clientID := registerClient(t, ts, client, redirectURI)
	verifier, challenge := pkce()

	csrf := authorizeGET(t, ts, client, authorizeParamsFor(clientID, redirectURI, challenge, "s"))
	code := consentAllow(t, ts, client, clientID, redirectURI, challenge, "s", csrf)
	tok, _ := tokenExchange(t, ts, client, codeGrantForm(clientID, redirectURI, code, verifier))
	refresh, _ := tok["refresh_token"].(string)

	refreshForm := func() url.Values {
		f := url.Values{}
		f.Set("grant_type", "refresh_token")
		f.Set("client_id", clientID)
		f.Set("refresh_token", refresh)
		return f
	}
	out, status := tokenExchange(t, ts, client, refreshForm())
	if status != http.StatusOK || out["access_token"] == "" {
		t.Fatalf("refresh should mint a fresh pair, got %d %v", status, out)
	}
	// The old refresh token is now spent.
	out2, status2 := tokenExchange(t, ts, client, refreshForm())
	if status2 != http.StatusBadRequest || out2["error"] != "invalid_grant" {
		t.Errorf("replayed refresh should be invalid_grant/400, got %d %v", status2, out2)
	}
}

// mcpBearer injects an Authorization header for the MCP client.
type mcpBearer struct {
	token string
	base  http.RoundTripper
}

func (b mcpBearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

func mcpConnect(t *testing.T, base, token string) *sdk.ClientSession {
	t.Helper()
	c := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	tr := &sdk.StreamableClientTransport{
		Endpoint:   base + "/mcp",
		HTTPClient: &http.Client{Transport: mcpBearer{token: token, base: http.DefaultTransport}},
	}
	sess, err := c.Connect(context.Background(), tr, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}
