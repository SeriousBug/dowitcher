//go:build dev

package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/SeriousBug/longbox/internal/api"
	"github.com/SeriousBug/longbox/internal/auth"
	"github.com/SeriousBug/longbox/internal/store"
)

// TestDevAuthBypass: with the bypass on, an unauthenticated client is the named
// admin user everywhere, with no ceremony and no cookie.
func TestDevAuthBypass(t *testing.T) {
	ts, st, _ := testServer(t, func(c *Config) { c.DevAuth = &auth.DevAuth{Name: "dev"} })

	// No jar: nothing is carrying a session.
	client := &http.Client{}
	resp, body := getReq(t, client, ts.URL+"/auth/me")
	if resp.StatusCode != 200 {
		t.Fatalf("/auth/me with dev auth: %d %s", resp.StatusCode, body)
	}
	var me api.Session
	if err := json.Unmarshal(body, &me); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if me.User.Name != "dev" || !me.User.IsAdmin {
		t.Fatalf("dev auth user = %+v, want admin named dev", me.User)
	}
	// Admin routes open too.
	if resp, _ := getReq(t, client, ts.URL+"/api/users"); resp.StatusCode != 200 {
		t.Fatalf("dev auth should reach admin routes, got %d", resp.StatusCode)
	}
	// The user is created once and reused, not re-created per request.
	getReq(t, client, ts.URL+"/auth/me")
	if n, err := st.CountUsers(); err != nil || n != 1 {
		t.Fatalf("users = %d err=%v, want 1", n, err)
	}
}

// TestDevAuthRefusesRequestsBearingTLSEvidence is the finding itself, end to
// end: a TLS proxy in front, LONGBOX_ORIGIN left at its http://localhost
// default, and the bypass on. The boot-time origin check passes because the
// origin genuinely says http, so the only thing left to catch it is the request.
func TestDevAuthRefusesRequestsBearingTLSEvidence(t *testing.T) {
	ts, _, _ := testServer(t, func(c *Config) {
		c.Origin = "http://localhost:8080"
		c.DevAuth = &auth.DevAuth{Name: "dev"}
	})
	client := &http.Client{}

	for _, tc := range []struct{ name, header, value string }{
		{"proxy terminated TLS", "X-Forwarded-Proto", "https"},
		{"request came through a proxy", "X-Forwarded-For", "203.0.113.7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", ts.URL+"/auth/me", nil)
			req.Header.Set(tc.header, tc.value)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("GET /auth/me: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != 401 {
				t.Fatalf("a request bearing %s: %s resolved to a dev user (status %d); "+
					"every unauthenticated internet request would be an admin",
					tc.header, tc.value, resp.StatusCode)
			}
		})
	}

	// The same server still serves a developer's own request, or the bypass
	// would be useless rather than safe.
	if resp, _ := getReq(t, client, ts.URL+"/auth/me"); resp.StatusCode != 200 {
		t.Fatalf("a plain local request should still resolve the dev user, got %d", resp.StatusCode)
	}
}

// TestDevAuthRefusesNonLoopbackAddr: a listener on a routable address is not a
// developer's laptop, whatever the origin claims.
func TestDevAuthRefusesNonLoopbackAddr(t *testing.T) {
	t.Setenv(auth.DevAuthEnv, "dev")

	refused := []string{
		":8080",          // the default, and the dangerous one: binds every interface
		"0.0.0.0:8080",   //
		"[::]:8080",      //
		"192.168.1.5:80", //
	}
	for _, addr := range refused {
		if _, err := auth.DevAuthFromEnv("http://localhost:8080", addr); err == nil {
			t.Errorf("dev auth on addr %q must be refused: it is reachable from off-box", addr)
		}
	}

	allowed := []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080", "127.0.0.1:5173"}
	for _, addr := range allowed {
		d, err := auth.DevAuthFromEnv("http://localhost:8080", addr)
		if err != nil || d == nil {
			t.Errorf("dev auth on loopback addr %q should be allowed: d=%v err=%v", addr, d, err)
		}
	}
}

// TestDevAuthRefusesHTTPSOrigin: the bypass on a TLS origin means it reached
// production, and the process must refuse rather than serve an open library.
func TestDevAuthRefusesHTTPSOrigin(t *testing.T) {
	t.Setenv(auth.DevAuthEnv, "dev")
	if _, err := auth.DevAuthFromEnv("https://longbox.example.com", "127.0.0.1:8080"); err == nil {
		t.Fatal("dev auth on an https origin must be refused")
	}
	if d, err := auth.DevAuthFromEnv("http://localhost:8080", "127.0.0.1:8080"); err != nil || d == nil {
		t.Fatalf("dev auth on http loopback should be allowed: d=%v err=%v", d, err)
	}
}

// TestDevAuthOffByDefault: nothing but the env var turns it on, even in a build
// that has the bypass compiled in.
func TestDevAuthOffByDefault(t *testing.T) {
	t.Setenv(auth.DevAuthEnv, "")
	d, err := auth.DevAuthFromEnv("http://localhost:8080", "127.0.0.1:8080")
	if err != nil || d != nil {
		t.Fatalf("dev auth should be off without the env var: d=%v err=%v", d, err)
	}

	ts, _, _ := testServer(t, nil)
	if resp, _ := getReq(t, &http.Client{}, ts.URL+"/auth/me"); resp.StatusCode != 401 {
		t.Fatalf("/auth/me without a session should be 401, got %d", resp.StatusCode)
	}
}

// TestDevAuthPromotesExistingNonAdmin: Banner promises admin rights
// unconditionally, so an existing account of that name must be made to match it
// rather than silently handing back a non-admin.
func TestDevAuthPromotesExistingNonAdmin(t *testing.T) {
	srv, ts, st, _ := newTestServer(t, func(c *Config) { c.DevAuth = &auth.DevAuth{Name: "dev"} })
	_ = srv
	if _, err := st.CreateUser(store.NewID(), "dev", false); err != nil {
		t.Fatalf("create non-admin dev user: %v", err)
	}

	resp, body := getReq(t, &http.Client{}, ts.URL+"/auth/me")
	if resp.StatusCode != 200 {
		t.Fatalf("/auth/me with dev auth: %d %s", resp.StatusCode, body)
	}
	var me api.Session
	if err := json.Unmarshal(body, &me); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if !me.User.IsAdmin {
		t.Fatal("dev auth reused an existing non-admin user, though the banner promises admin rights")
	}
	if n, err := st.CountUsers(); err != nil || n != 1 {
		t.Fatalf("users = %d err=%v, want the existing user reused rather than a second one", n, err)
	}
}
