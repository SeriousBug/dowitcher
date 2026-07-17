//go:build !dev

package auth

import (
	"errors"

	"github.com/SeriousBug/longbox/internal/api"
	"github.com/SeriousBug/longbox/internal/store"
)

// This is the default build's dev-auth: nothing at all.
//
// The bypass lives behind a `dev` build tag (see devauth.go) because every other
// guard on it is a check against configuration, and configuration is exactly
// what is wrong when the bypass is dangerous. A guard that reads the config to
// decide whether the config is a mistake fails open the moment the operator's
// mistake is the config itself. A build tag cannot fail open: the code is not in
// the binary, so no env var, header or origin can reach it. Release builds
// (`go build ./...`, and the Dockerfile) never pass the tag, so a shipped
// longbox physically cannot resolve an unauthenticated request to a user.
//
// The type and its methods survive here only so the packages that hold a
// *DevAuth still compile. DevAuthFromEnv always returns nil, so nothing ever
// calls them.
type DevAuth struct {
	Name string
}

// DevAuthFromEnv always returns nil in a non-dev build: there is no bypass to
// configure. The signature matches the dev build's so callers need no tag of
// their own.
func DevAuthFromEnv(origin, addr string) (*DevAuth, error) { return nil, nil }

func (d *DevAuth) Banner() string { return "" }

func (d *DevAuth) User(st *store.Store) (api.User, error) {
	return api.User{}, errors.New("auth: dev bypass is not compiled into this binary")
}
