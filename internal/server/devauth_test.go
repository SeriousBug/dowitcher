package server

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestDevAuthTLSEvidence pins the request-time half of the dev-auth guard.
//
// It carries no build tag on purpose: the predicate is what stands between the
// bypass and a request that crossed a TLS proxy, and it is worth checking in
// the build everyone actually runs, not only the one that can reach the bypass.
func TestDevAuthTLSEvidence(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*http.Request)
		refused bool
	}{
		{
			name:    "plain loopback request",
			mutate:  func(*http.Request) {},
			refused: false,
		},
		{
			// A developer's own browser sends this on every navigation; it must
			// not be mistaken for proxy evidence.
			name:    "unrelated headers",
			mutate:  func(r *http.Request) { r.Header.Set("User-Agent", "Mozilla/5.0") },
			refused: false,
		},
		{
			name:    "terminated TLS",
			mutate:  func(r *http.Request) { r.TLS = &tls.ConnectionState{} },
			refused: true,
		},
		{
			name:    "proxy reports https",
			mutate:  func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "https") },
			refused: true,
		},
		{
			// Case and padding are the proxy's choice, not ours.
			name:    "proxy reports HTTPS oddly cased",
			mutate:  func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", " HTTPS ") },
			refused: true,
		},
		{
			// A proxy in front at all is enough: this is the shape of the real
			// finding, where TLS terminates upstream and the origin was never
			// configured, so nothing else marks the request as public.
			name:    "forwarded for",
			mutate:  func(r *http.Request) { r.Header.Set("X-Forwarded-For", "203.0.113.7") },
			refused: true,
		},
		{
			name:    "proxy reports plain http",
			mutate:  func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "http") },
			refused: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://localhost:8080/auth/me", nil)
			tc.mutate(r)
			got := devAuthTLSEvidence(r)
			if tc.refused && got == "" {
				t.Fatalf("request must be refused by dev auth, but no evidence was found")
			}
			if !tc.refused && got != "" {
				t.Fatalf("plain local request was refused as %q", got)
			}
		})
	}
}
