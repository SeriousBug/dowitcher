package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"time"

	"github.com/SeriousBug/dowitcher/internal/store"
)

// SessionTTL is how long a session cookie stays valid. It is long because the
// installed web app must survive being offline for extended stretches, and a
// reader that logs itself out mid-flight cannot re-authenticate: the passkey
// ceremony needs the server. A long TTL is only tenable because sessions are
// revocable rows rather than signed claims — see randToken and
// store.DeleteUserSessions.
const SessionTTL = 365 * 24 * time.Hour

// SessionCookieName is the HttpOnly session cookie.
const SessionCookieName = "dowitcher_session"

// randToken returns a URL-safe random token with n bytes of entropy. Session
// tokens are opaque random bytes rather than signed claims: revoking a session
// then means deleting a row, with no signing key to rotate or leak.
func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// RandToken is the exported form of randToken, for the oauth package to mint
// codes and access/refresh tokens with the same entropy and encoding the rest
// of the server uses for opaque credentials.
func RandToken(n int) string { return randToken(n) }

// HashToken is the one-way transform stored in the database for opaque
// credentials (OAuth codes and tokens). The token is high-entropy random, so a
// plain SHA-256 is enough: there is no low-entropy password to brute force, so
// no salt or slow KDF buys anything. Storing only the hash means a leaked
// database cannot hand over a live credential.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NewSession creates a session for a user and returns its token.
func NewSession(st *store.Store, userID string) (string, time.Time, error) {
	token := randToken(32)
	exp := time.Now().Add(SessionTTL)
	if err := st.CreateSession(token, userID, exp.Unix()); err != nil {
		return "", time.Time{}, err
	}
	return token, exp, nil
}
