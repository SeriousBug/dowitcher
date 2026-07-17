//go:build dev

package auth

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/store"
)

// Boot-time refusals. Each names a condition under which the bypass could only
// be a mistake.
var (
	ErrDevAuthOnHTTPS = errors.New(DevAuthEnv + " is set but the origin is https:// — " +
		"this bypasses all authentication and must never run on a public deployment")
	ErrDevAuthNotLoopback = errors.New(DevAuthEnv + " is set but DOWITCHER_ADDR does not bind loopback — " +
		"this bypasses all authentication and must never listen on a routable address")
)

// DevAuth turns every request into a request from one named user, with no
// WebAuthn ceremony at all. It exists so the frontend can be worked on without
// a passkey in the loop.
//
// It is a hole straight through the auth system. The guards on it are layered
// because each layer alone has a way of failing open:
//
//   - This file carries a `dev` build tag, so a release binary does not contain
//     the hole at all. That is the only guard that cannot be defeated by
//     configuration, which is why it is the one to rely on. See devauth_stub.go.
//   - DevAuthFromEnv refuses to boot unless DOWITCHER_ADDR binds loopback. The
//     origin check that used to stand alone here was worthless: origin defaults
//     to http://localhost:8080, so a TLS proxy in front with DOWITCHER_ORIGIN
//     unset sailed straight past it. Normally a wrong origin self-enforces —
//     WebAuthn breaks and the Secure cookie is dropped — but dev-auth needs
//     neither, so the check had no teeth in exactly the case that mattered.
//   - The server refuses at request time on evidence that the request crossed
//     TLS or a proxy. See devAuthTLSEvidence in internal/server/middleware.go.
//     Config describes what an operator meant; a request describes what is
//     actually happening, and only the second one is worth trusting here.
//
// It still prints a banner that is impossible to miss in the logs.
type DevAuth struct {
	// Name is the user every request resolves to.
	Name string
}

// DevAuthFromEnv reads the bypass config, returning nil when it is not set.
// origin is the configured app origin and addr is the listen address; both are
// refused when they indicate anything but a developer's own machine.
func DevAuthFromEnv(origin, addr string) (*DevAuth, error) {
	name := strings.TrimSpace(os.Getenv(DevAuthEnv))
	if name == "" {
		return nil, nil
	}
	if strings.HasPrefix(strings.ToLower(origin), "https://") {
		return nil, ErrDevAuthOnHTTPS
	}
	if !addrIsLoopback(addr) {
		return nil, ErrDevAuthNotLoopback
	}
	return &DevAuth{Name: name}, nil
}

// addrIsLoopback reports whether a net/http listen address can only accept
// connections from this machine.
//
// The empty host of ":8080" is the case this exists for: it binds every
// interface, which is the default and is precisely how a dev-auth instance ends
// up reachable from the internet. It is treated as not-loopback rather than as
// unknown, so the refusal is the default rather than the exception.
func addrIsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// Not host:port at all — a bare host, or something malformed. Judge the
		// whole string rather than guessing, and fail closed if it parses as
		// nothing.
		host = addr
	}
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
//
// An existing user of that name is promoted rather than used as-is: Banner
// promises admin rights unconditionally, and a `dev` account that predates the
// bypass (or was demoted by a test) would otherwise hand back a non-admin and
// make the banner a lie, which reads as the admin routes being broken.
func (d *DevAuth) User(st *store.Store) (api.User, error) {
	u, err := st.UserByName(d.Name)
	if err == nil {
		if !u.IsAdmin {
			if err := st.SetUserAdmin(u.ID, true); err != nil {
				return api.User{}, err
			}
			u.IsAdmin = true
		}
		return u, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return api.User{}, err
	}
	return st.CreateUser(store.NewID(), d.Name, true)
}
