package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/auth"
	"github.com/SeriousBug/dowitcher/internal/store"
	"github.com/descope/virtualwebauthn"
)

// testServer spins up a full Server backed by a temp SQLite DB and an httptest
// server.
func testServer(t *testing.T, cfg func(*Config)) (*httptest.Server, *store.Store, *http.Client) {
	t.Helper()
	_, ts, st, client := newTestServer(t, cfg)
	return ts, st, client
}

// newTestServer is testServer plus the Server itself, for tests that attach a
// collaborator to it. The httptest server is started with a nil handler and
// given the real one afterwards, because the RP ID has to be derived from the
// URL the listener picked, which does not exist until it is running.
func newTestServer(t *testing.T, cfg func(*Config)) (*Server, *httptest.Server, *store.Store, *http.Client) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ts := httptest.NewServer(nil)
	t.Cleanup(ts.Close)

	c := Config{RPID: rpIDOf(ts.URL), Origin: ts.URL}
	if cfg != nil {
		cfg(&c)
	}
	mgr, err := auth.NewManager(st, auth.Config{RPID: c.RPID, Origin: c.Origin})
	if err != nil {
		t.Fatalf("auth manager: %v", err)
	}
	srv := New(st, mgr, c)
	ts.Config.Handler = srv.Handler()

	jar, _ := cookiejar.New(nil)
	return srv, ts, st, &http.Client{Jar: jar}
}

// rpIDOf is the host of the test origin, without the port.
func rpIDOf(url string) string {
	host := strings.TrimPrefix(url, "http://")
	return host[:strings.IndexByte(host, ':')]
}

func post(t *testing.T, client *http.Client, url string, body []byte) (*http.Response, []byte) {
	t.Helper()
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, data
}

func getReq(t *testing.T, client *http.Client, url string) (*http.Response, []byte) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, data
}

func doReq(t *testing.T, client *http.Client, method, url string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, data
}

func bootstrapToken(t *testing.T, st *store.Store, origin string) string {
	t.Helper()
	url, err := auth.Bootstrap(st, origin)
	if err != nil || url == "" {
		t.Fatalf("bootstrap: url=%q err=%v", url, err)
	}
	return url[strings.Index(url, "token=")+len("token="):]
}

// passkey is a virtual authenticator bound to one test server.
type passkey struct {
	rp   virtualwebauthn.RelyingParty
	auth virtualwebauthn.Authenticator
	cred virtualwebauthn.Credential
}

func newPasskey(url string) *passkey {
	return &passkey{
		rp:   virtualwebauthn.RelyingParty{Name: "Dowitcher", ID: rpIDOf(url), Origin: url},
		auth: virtualwebauthn.NewAuthenticator(),
		cred: virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2),
	}
}

// enroll runs the full registration ceremony against an invite token.
func (p *passkey) enroll(t *testing.T, client *http.Client, url, token, name string) (*http.Response, []byte) {
	t.Helper()
	req, _ := json.Marshal(api.EnrollRequest{Token: token, Name: name})
	resp, body := post(t, client, url+"/auth/register/begin", req)
	if resp.StatusCode != 200 {
		return resp, body
	}
	opts, err := virtualwebauthn.ParseAttestationOptions(string(body))
	if err != nil {
		t.Fatalf("parse attestation options: %v (body=%s)", err, body)
	}
	// Discoverable login requires the authenticator to return a user handle.
	p.auth.Options.UserHandle = []byte(opts.UserID)
	att := virtualwebauthn.CreateAttestationResponse(p.rp, p.auth, p.cred, *opts)
	resp, body = post(t, client, url+"/auth/register/finish", []byte(att))
	if resp.StatusCode == 200 {
		p.auth.AddCredential(p.cred)
	}
	return resp, body
}

// login runs the usernameless assertion ceremony.
func (p *passkey) login(t *testing.T, client *http.Client, url string) (*http.Response, []byte) {
	t.Helper()
	resp, body := post(t, client, url+"/auth/login/begin", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("login/begin: %d %s", resp.StatusCode, body)
	}
	opts, err := virtualwebauthn.ParseAssertionOptions(string(body))
	if err != nil {
		t.Fatalf("parse assertion options: %v (body=%s)", err, body)
	}
	asr := virtualwebauthn.CreateAssertionResponse(p.rp, p.auth, p.cred, *opts)
	return post(t, client, url+"/auth/login/finish", []byte(asr))
}

// TestEnrollAndLogin exercises the full passkey ceremony end to end with a
// virtual authenticator: bootstrap invite -> enroll -> session -> logout ->
// usernameless login.
func TestEnrollAndLogin(t *testing.T) {
	ts, st, client := testServer(t, nil)
	token := bootstrapToken(t, st, ts.URL)
	pk := newPasskey(ts.URL)

	if resp, body := pk.enroll(t, client, ts.URL, token, "Alice"); resp.StatusCode != 200 {
		t.Fatalf("enroll: %d %s", resp.StatusCode, body)
	}

	resp, body := getReq(t, client, ts.URL+"/auth/me")
	if resp.StatusCode != 200 || !strings.Contains(string(body), "Alice") {
		t.Fatalf("/auth/me after enroll: %d %s", resp.StatusCode, body)
	}
	// The bootstrap invite is an admin invite, so the first user must be one.
	var me api.Session
	if err := json.Unmarshal(body, &me); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if !me.User.IsAdmin {
		t.Fatal("first enrolled user should be an admin")
	}
	if len(me.Credentials) != 1 {
		t.Fatalf("want 1 credential, got %d", len(me.Credentials))
	}

	post(t, client, ts.URL+"/auth/logout", nil)
	if r, _ := getReq(t, client, ts.URL+"/auth/me"); r.StatusCode != 401 {
		t.Fatalf("/auth/me after logout should be 401, got %d", r.StatusCode)
	}

	if resp, body := pk.login(t, client, ts.URL); resp.StatusCode != 200 {
		t.Fatalf("login/finish: %d %s", resp.StatusCode, body)
	}
	if r, b := getReq(t, client, ts.URL+"/auth/me"); r.StatusCode != 200 || !strings.Contains(string(b), "Alice") {
		t.Fatalf("/auth/me after login: %d %s", r.StatusCode, b)
	}
}

// TestLogoutOthersCutsEveryOtherDevice: the whole point of the button is that
// the device you press it from stays signed in while every other one does not,
// so both halves are pinned here. The passkey is untouched, which is why the
// second client can sign straight back in afterwards.
func TestLogoutOthersCutsEveryOtherDevice(t *testing.T) {
	ts, st, client := testServer(t, nil)
	token := bootstrapToken(t, st, ts.URL)
	pk := newPasskey(ts.URL)
	if resp, body := pk.enroll(t, client, ts.URL, token, "Alice"); resp.StatusCode != 200 {
		t.Fatalf("enroll: %d %s", resp.StatusCode, body)
	}

	// A second jar is a second device: same passkey, its own session cookie.
	jar, _ := cookiejar.New(nil)
	other := &http.Client{Jar: jar}
	if resp, body := pk.login(t, other, ts.URL); resp.StatusCode != 200 {
		t.Fatalf("second device login: %d %s", resp.StatusCode, body)
	}
	if r, _ := getReq(t, other, ts.URL+"/auth/me"); r.StatusCode != 200 {
		t.Fatalf("second device should start signed in, got %d", r.StatusCode)
	}

	resp, body := post(t, client, ts.URL+"/auth/logout/others", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("logout/others: %d %s", resp.StatusCode, body)
	}
	var out api.SignedOutOthers
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Revoked != 1 {
		t.Errorf("revoked = %d, want 1", out.Revoked)
	}

	if r, _ := getReq(t, other, ts.URL+"/auth/me"); r.StatusCode != 401 {
		t.Errorf("the other device must be signed out, got %d", r.StatusCode)
	}
	if r, _ := getReq(t, client, ts.URL+"/auth/me"); r.StatusCode != 200 {
		t.Errorf("the calling device must stay signed in, got %d", r.StatusCode)
	}
	if resp, body := pk.login(t, other, ts.URL); resp.StatusCode != 200 {
		t.Fatalf("revocation must not harm the passkey: %d %s", resp.StatusCode, body)
	}
}

// TestLogoutOthersNeedsASession: an unauthenticated caller must not be able to
// reach a revocation route at all.
func TestLogoutOthersNeedsASession(t *testing.T) {
	ts, _, _ := testServer(t, nil)
	jar, _ := cookiejar.New(nil)
	if resp, _ := post(t, &http.Client{Jar: jar}, ts.URL+"/auth/logout/others", nil); resp.StatusCode != 401 {
		t.Fatalf("want 401 for an anonymous caller, got %d", resp.StatusCode)
	}
}

// TestInviteIsSingleUse: an invite mints exactly one account, and the link is
// dead the moment it has.
func TestInviteIsSingleUse(t *testing.T) {
	ts, st, client := testServer(t, nil)
	token := bootstrapToken(t, st, ts.URL)

	if resp, body := newPasskey(ts.URL).enroll(t, client, ts.URL, token, "Alice"); resp.StatusCode != 200 {
		t.Fatalf("first enroll: %d %s", resp.StatusCode, body)
	}

	jar, _ := cookiejar.New(nil)
	second := &http.Client{Jar: jar}
	resp, body := newPasskey(ts.URL).enroll(t, second, ts.URL, token, "Mallory")
	if resp.StatusCode == 200 {
		t.Fatal("a consumed invite must not enroll a second account")
	}
	if !strings.Contains(string(body), "invite") {
		t.Fatalf("want an invite error, got %d %s", resp.StatusCode, body)
	}
	if n, err := st.CountUsers(); err != nil || n != 1 {
		t.Fatalf("users = %d err=%v, want 1", n, err)
	}
}

// TestLastAdminGuard: the instance must not be able to delete its way out of
// having an administrator.
func TestLastAdminGuard(t *testing.T) {
	ts, st, client := testServer(t, nil)
	token := bootstrapToken(t, st, ts.URL)
	if resp, body := newPasskey(ts.URL).enroll(t, client, ts.URL, token, "Alice"); resp.StatusCode != 200 {
		t.Fatalf("enroll: %d %s", resp.StatusCode, body)
	}
	alice, err := st.UserByName("Alice")
	if err != nil {
		t.Fatalf("lookup alice: %v", err)
	}

	resp, body := doReq(t, client, "DELETE", ts.URL+"/api/users/"+alice.ID)
	if resp.StatusCode != 400 || !strings.Contains(string(body), "last admin") {
		t.Fatalf("deleting the last admin should be refused, got %d %s", resp.StatusCode, body)
	}

	// With a second admin the guard lifts.
	resp, body = post(t, client, ts.URL+"/api/invites", mustJSON(t, api.CreateInviteRequest{IsAdmin: true}))
	if resp.StatusCode != 200 {
		t.Fatalf("create invite: %d %s", resp.StatusCode, body)
	}
	var inv api.Invite
	if err := json.Unmarshal(body, &inv); err != nil {
		t.Fatalf("decode invite: %v", err)
	}
	jar, _ := cookiejar.New(nil)
	if resp, body := newPasskey(ts.URL).enroll(t, &http.Client{Jar: jar}, ts.URL, inv.Token, "Bob"); resp.StatusCode != 200 {
		t.Fatalf("enroll bob: %d %s", resp.StatusCode, body)
	}
	if resp, body := doReq(t, client, "DELETE", ts.URL+"/api/users/"+alice.ID); resp.StatusCode != 200 {
		t.Fatalf("deleting a non-last admin should succeed, got %d %s", resp.StatusCode, body)
	}
}

// TestNonAdminIsRefusedAdminRoutes pins the requireAdmin gate.
func TestNonAdminIsRefusedAdminRoutes(t *testing.T) {
	ts, st, admin := testServer(t, nil)
	if resp, body := newPasskey(ts.URL).enroll(t, admin, ts.URL, bootstrapToken(t, st, ts.URL), "Alice"); resp.StatusCode != 200 {
		t.Fatalf("enroll admin: %d %s", resp.StatusCode, body)
	}
	_, body := post(t, admin, ts.URL+"/api/invites", mustJSON(t, api.CreateInviteRequest{IsAdmin: false}))
	var inv api.Invite
	json.Unmarshal(body, &inv)

	jar, _ := cookiejar.New(nil)
	plain := &http.Client{Jar: jar}
	if resp, body := newPasskey(ts.URL).enroll(t, plain, ts.URL, inv.Token, "Bob"); resp.StatusCode != 200 {
		t.Fatalf("enroll bob: %d %s", resp.StatusCode, body)
	}
	if resp, _ := getReq(t, plain, ts.URL+"/api/users"); resp.StatusCode != 403 {
		t.Fatalf("non-admin on an admin route should be 403, got %d", resp.StatusCode)
	}
	if resp, _ := getReq(t, plain, ts.URL+"/auth/me"); resp.StatusCode != 200 {
		t.Fatalf("non-admin should still reach /auth/me, got %d", resp.StatusCode)
	}
}

func TestHealthzAndSPAFallback(t *testing.T) {
	ts, _, client := testServer(t, nil)
	if resp, body := getReq(t, client, ts.URL+"/healthz"); resp.StatusCode != 200 || !strings.Contains(string(body), "ok") {
		t.Fatalf("/healthz: %d %s", resp.StatusCode, body)
	}
	// A deep link is a client route, so it must fall back to index.html rather
	// than 404.
	resp, _ := getReq(t, client, ts.URL+"/comics/abc123")
	if resp.StatusCode != 200 {
		t.Fatalf("SPA deep link should serve index.html, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("SPA fallback content type = %q", ct)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
