package oidc

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeIDP is a minimal OpenID Connect provider: discovery, a JWKS, and a token
// endpoint that signs whatever claims the test put in base. Enough to exercise
// discovery, JWKS verification and claim reading without a network or a fixture
// file.
type fakeIDP struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	base   map[string]any
	issuer string
}

func newFakeIDP(t *testing.T) *fakeIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	idp := &fakeIDP{key: key}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                                idp.issuer,
			"authorization_endpoint":                idp.issuer + "/authorize",
			"token_endpoint":                        idp.issuer + "/token",
			"jwks_uri":                              idp.issuer + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		pub := key.Public().(*rsa.PublicKey)
		writeJSON(w, map[string]any{"keys": []map[string]string{{
			"kty": "RSA",
			"kid": "test",
			"use": "sig",
			"alg": "RS256",
			"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		writeJSON(w, map[string]any{
			"access_token": "access",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     idp.sign(idp.base),
		})
	})

	idp.srv = httptest.NewServer(mux)
	idp.issuer = idp.srv.URL
	t.Cleanup(idp.srv.Close)
	return idp
}

// sign builds and RS256-signs a JWT with the fake provider's key.
func (f *fakeIDP) sign(claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"test","typ":"JWT"}`))
	payload, _ := json.Marshal(claims)
	body := header + "." + base64.RawURLEncoding.EncodeToString(payload)
	sum := sha256.Sum256([]byte(body))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return body + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func newTestProvider(t *testing.T, idp *fakeIDP, cfg Config) *Provider {
	t.Helper()
	cfg.Issuer = idp.issuer
	if cfg.ClientID == "" {
		cfg.ClientID = "client"
	}
	cfg.RedirectURL = "https://dowitcher.example/auth/oidc/callback"
	p, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("build provider: %v", err)
	}
	return p
}

func TestNewRequiresIssuerAndClientID(t *testing.T) {
	if _, err := New(context.Background(), Config{ClientID: "x"}); err == nil {
		t.Error("missing issuer must be rejected")
	}
	if _, err := New(context.Background(), Config{Issuer: "https://idp.example"}); err == nil {
		t.Error("missing client id must be rejected")
	}
}

func TestExchangeReadsIdentityAndGroups(t *testing.T) {
	idp := newFakeIDP(t)
	idp.base = map[string]any{
		"iss":                idp.issuer,
		"aud":                "client",
		"sub":                "subject-1",
		"exp":                time.Now().Add(time.Hour).Unix(),
		"iat":                time.Now().Unix(),
		"nonce":              "the-nonce",
		"email":              "alice@example.com",
		"preferred_username": "alice",
		"groups":             []string{"readers", "comic-admins"},
	}
	p := newTestProvider(t, idp, Config{AdminGroups: []string{"comic-admins"}})

	id, err := p.Exchange(context.Background(), "code", "verifier", "the-nonce")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if id.Issuer != idp.issuer || id.Subject != "subject-1" {
		t.Errorf("issuer/subject = %q/%q", id.Issuer, id.Subject)
	}
	if id.Name != "alice" {
		t.Errorf("name = %q, want alice", id.Name)
	}
	if id.Email != "alice@example.com" {
		t.Errorf("email = %q", id.Email)
	}
	if !id.Admin {
		t.Error("membership in an admin group must grant admin")
	}
}

func TestExchangeNonceMismatch(t *testing.T) {
	idp := newFakeIDP(t)
	idp.base = map[string]any{
		"iss":   idp.issuer,
		"aud":   "client",
		"sub":   "subject-1",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"nonce": "minted-for-another-attempt",
	}
	p := newTestProvider(t, idp, Config{})

	if _, err := p.Exchange(context.Background(), "code", "verifier", "this-attempt"); err == nil {
		t.Fatal("a token for another attempt must be rejected")
	}
}

func TestExchangeNameClaimAndStringGroups(t *testing.T) {
	idp := newFakeIDP(t)
	idp.base = map[string]any{
		"iss":      idp.issuer,
		"aud":      "client",
		"sub":      "subject-2",
		"exp":      time.Now().Add(time.Hour).Unix(),
		"nonce":    "n",
		"username": "custom-name",
		"roles":    "admins", // a lone string, not a list
	}
	p := newTestProvider(t, idp, Config{
		NameClaim:   "username",
		GroupsClaim: "roles",
		AdminGroups: []string{"admins"},
	})

	id, err := p.Exchange(context.Background(), "code", "verifier", "n")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if id.Name != "custom-name" {
		t.Errorf("name = %q, want the configured claim", id.Name)
	}
	if !id.Admin {
		t.Error("a string-valued groups claim must still grant admin")
	}
}

func TestExchangeNoAdminGroups(t *testing.T) {
	idp := newFakeIDP(t)
	idp.base = map[string]any{
		"iss":    idp.issuer,
		"aud":    "client",
		"sub":    "subject-3",
		"exp":    time.Now().Add(time.Hour).Unix(),
		"nonce":  "n",
		"groups": []string{"admins"},
	}
	p := newTestProvider(t, idp, Config{})

	id, err := p.Exchange(context.Background(), "code", "verifier", "n")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if id.Admin {
		t.Error("no configured admin groups must mean no claim-based admin")
	}
}

func TestAuthCodeURLHasPKCEStateAndNonce(t *testing.T) {
	idp := newFakeIDP(t)
	p := newTestProvider(t, idp, Config{})

	raw := p.AuthCodeURL("the-state", "the-nonce", "the-verifier")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	q := u.Query()
	if q.Get("state") != "the-state" || q.Get("nonce") != "the-nonce" {
		t.Errorf("state/nonce missing from %s", raw)
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", q.Get("code_challenge_method"))
	}
	if !strings.HasPrefix(q.Get("scope"), "openid") {
		t.Errorf("scope = %q, want openid present", q.Get("scope"))
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
