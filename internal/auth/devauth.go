package auth

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/SeriousBug/longbox/internal/api"
	"github.com/SeriousBug/longbox/internal/store"
)

// DevAuthEnv names the env var that disables authentication.
const DevAuthEnv = "LONGBOX_DEV_AUTH"

// ErrDevAuthOnHTTPS refuses the one combination that can only be a mistake.
var ErrDevAuthOnHTTPS = errors.New(DevAuthEnv + " is set but the origin is https:// — " +
	"this bypasses all authentication and must never run on a public deployment")

// DevAuth turns every request into a request from one named user, with no
// WebAuthn ceremony at all. It exists so the frontend can be worked on without
// a passkey in the loop.
//
// It is a hole straight through the auth system, so it is deliberately hard to
// leave on by accident: it only activates from an env var nobody sets casually,
// it prints a banner that is impossible to miss in the logs, and it refuses to
// start at all under an https:// origin. That last check is the real guard —
// nothing else distinguishes "a developer running on localhost" from "someone
// shipped their .env to production", but a TLS origin means the latter, and
// failing to boot is much cheaper than serving an open library.
type DevAuth struct {
	// Name is the user every request resolves to.
	Name string
}

// DevAuthFromEnv reads the bypass config, returning nil when it is not set.
// origin is the configured app origin; an https one is rejected outright.
func DevAuthFromEnv(origin string) (*DevAuth, error) {
	name := strings.TrimSpace(os.Getenv(DevAuthEnv))
	if name == "" {
		return nil, nil
	}
	if strings.HasPrefix(strings.ToLower(origin), "https://") {
		return nil, ErrDevAuthOnHTTPS
	}
	return &DevAuth{Name: name}, nil
}

// Banner is the startup warning. It is multi-line and loud because the whole
// point is that it cannot be scrolled past without being noticed.
func (d *DevAuth) Banner() string {
	line := strings.Repeat("!", 78)
	return fmt.Sprintf("\n%s\n"+
		"!!  %s IS SET.\n"+
		"!!  AUTHENTICATION IS DISABLED. Every request is treated as user %q,\n"+
		"!!  with admin rights, and no passkey is checked.\n"+
		"!!  This is for local development only. Unset %s to restore auth.\n"+
		"%s\n", line, DevAuthEnv, d.Name, DevAuthEnv, line)
}

// User resolves the bypass user, creating it as an admin on first use so a fresh
// dev database needs no enrollment at all.
func (d *DevAuth) User(st *store.Store) (api.User, error) {
	u, err := st.UserByName(d.Name)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return api.User{}, err
	}
	return st.CreateUser(store.NewID(), d.Name, true)
}
