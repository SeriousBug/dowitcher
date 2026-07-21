// Package oauth holds the pieces of Dowitcher's OAuth 2.1 authorization server
// that are pure logic rather than HTTP: the credential lifetimes, PKCE
// verification, and token minting. The wiring lives in internal/server
// (oauth_handlers.go) and the persistence in internal/store.
//
// Dowitcher is its own authorization server only for the MCP endpoint, because
// the MCP OAuth flow is the only door Claude's connector UI and Claude Code
// offer — there is no field to paste a static token. The browser login step
// reuses the existing passkey session, so this package never touches passwords
// or credentials of its own.
package oauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"time"

	"github.com/SeriousBug/dowitcher/internal/auth"
)

// Credential lifetimes. The authorization code is short because it only has to
// survive the redirect back and the immediate token exchange; anything longer
// is a window for a leaked code to be replayed. The access token is an hour so
// a long-running agent is not re-authorizing constantly, and the refresh token
// is 30 days so a connector left idle over a few weeks still comes back without
// a fresh browser consent.
const (
	CodeTTL    = time.Minute
	AccessTTL  = time.Hour
	RefreshTTL = 30 * 24 * time.Hour
)

// VerifyS256 reports whether verifier hashes to challenge under PKCE's S256
// method: base64url(sha256(verifier)) == challenge. The compare is
// constant-time so a mismatch does not leak how much of the challenge matched.
// Only S256 is accepted anywhere in the flow; the "plain" method is rejected at
// /authorize, so there is no plain branch here.
func VerifyS256(verifier, challenge string) bool {
	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(want), []byte(challenge)) == 1
}

// NewToken mints an opaque credential (an authorization code, access token or
// refresh token) with 32 bytes of entropy. Only its hash is ever stored; the
// plain value lives only in the response to the client.
func NewToken() string { return auth.RandToken(32) }
