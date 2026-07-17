package auth

import (
	"strings"
	"time"

	"github.com/SeriousBug/dowitcher/internal/store"
)

// InviteTTL is how long an invite link is valid. A link is a bearer token that
// mints an account, so it expires on its own rather than waiting for an admin to
// notice it is still live.
const InviteTTL = 24 * time.Hour

// NewInvite creates a single-use invite and returns its token. forUser binds a
// recovery invite to an existing user (enrolling adds a passkey to that user);
// pass "" for a normal new-user invite.
func NewInvite(st *store.Store, createdBy, forUser string, isAdmin bool) (string, time.Time, error) {
	token := randToken(24)
	exp := time.Now().Add(InviteTTL)
	if err := st.CreateInvite(token, createdBy, forUser, isAdmin, exp.Unix()); err != nil {
		return "", time.Time{}, err
	}
	return token, exp, nil
}

// InviteURL builds the enrollment URL for a token given the app origin.
func InviteURL(origin, token string) string {
	return strings.TrimRight(origin, "/") + "/enroll?token=" + token
}

// Bootstrap mints an admin invite when there are no users yet, returning the
// enrollment URL to print to logs. Returns ("", nil) if users already exist.
func Bootstrap(st *store.Store, origin string) (string, error) {
	n, err := st.CountUsers()
	if err != nil {
		return "", err
	}
	if n > 0 {
		return "", nil
	}
	token, _, err := NewInvite(st, "", "", true)
	if err != nil {
		return "", err
	}
	return InviteURL(origin, token), nil
}
