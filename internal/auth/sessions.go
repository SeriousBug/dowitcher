package auth

import (
	"crypto/rand"
	"encoding/base64"
	"time"

	"github.com/SeriousBug/longbox/internal/store"
)

// SessionTTL is how long a session cookie stays valid.
const SessionTTL = 30 * 24 * time.Hour

// SessionCookieName is the HttpOnly session cookie.
const SessionCookieName = "longbox_session"

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

// NewSession creates a session for a user and returns its token.
func NewSession(st *store.Store, userID string) (string, time.Time, error) {
	token := randToken(32)
	exp := time.Now().Add(SessionTTL)
	if err := st.CreateSession(token, userID, exp.Unix()); err != nil {
		return "", time.Time{}, err
	}
	return token, exp, nil
}
