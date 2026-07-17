//go:build !dev

package server

import (
	"net/http"
	"testing"

	"github.com/SeriousBug/dowitcher/internal/auth"
)

// TestDevAuthIsNotCompiledIntoReleaseBuilds is the guard that cannot be argued
// with. Every other check on the bypass reads configuration, and configuration
// is exactly what is wrong when the bypass is dangerous — so the one that
// matters is that a binary built the way releases are built (no -tags dev, as
// in the Dockerfile) has no bypass in it at all.
//
// The env var is set here to the value that would have turned it on, and on the
// origin/addr pair the old guard waved through: origin defaults to
// http://localhost:8080 whether or not there is a TLS proxy in front, and
// :8080 binds every interface.
func TestDevAuthIsNotCompiledIntoReleaseBuilds(t *testing.T) {
	t.Setenv(auth.DevAuthEnv, "admin")

	for _, tc := range []struct{ origin, addr string }{
		{"http://localhost:8080", ":8080"},        // the real finding: proxy in front, origin unset
		{"http://localhost:8080", "127.0.0.1:80"}, // even the shape a developer would use
		{"https://dowitcher.example.com", ":8080"},
	} {
		d, err := auth.DevAuthFromEnv(tc.origin, tc.addr)
		if err != nil {
			t.Fatalf("DevAuthFromEnv(%q, %q) errored in a release build: %v", tc.origin, tc.addr, err)
		}
		if d != nil {
			t.Fatalf("DevAuthFromEnv(%q, %q) = %+v; a release binary must not be able to build a bypass",
				tc.origin, tc.addr, d)
		}
	}

	// And the env var being set changes nothing about a real server.
	ts, _, _ := testServer(t, nil)
	if resp, _ := getReq(t, &http.Client{}, ts.URL+"/auth/me"); resp.StatusCode != 401 {
		t.Fatalf("/auth/me without a session should be 401, got %d", resp.StatusCode)
	}
}
