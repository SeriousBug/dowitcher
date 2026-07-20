package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/store"
)

// APITokenPrefix marks a dowitcher API token so a leaked one is recognisable in
// a log or a paste and secret scanners can match it. The suffix is the entropy.
const APITokenPrefix = "dwt_"

// HashToken is the one-way transform stored in the database. The token is 32
// bytes of randomness, so a plain SHA-256 is enough: there is no low-entropy
// password to brute force, so no salt or slow KDF buys anything. The prefix is
// hashed along with the rest — it is part of the secret the caller presents.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NewAPIToken mints a token for a user and returns the plain secret, shown to
// the caller exactly once. Only its hash is persisted, so a lost secret cannot
// be recovered — the user revokes and mints a new one.
func NewAPIToken(st *store.Store, userID, name string) (secret string, tok api.APIToken, err error) {
	secret = APITokenPrefix + randToken(32)
	id := store.NewID()
	if err := st.CreateAPIToken(id, userID, name, HashToken(secret)); err != nil {
		return "", api.APIToken{}, err
	}
	return secret, api.APIToken{ID: id, Name: name}, nil
}

// UserForAPIToken resolves a presented secret to its user, or ErrNotFound. A
// value that is not one of our tokens is rejected before touching the database:
// the prefix check is not security, only a fast path that keeps unrelated
// bearer tokens out of the token table lookup.
func UserForAPIToken(st *store.Store, secret string) (api.User, error) {
	if !strings.HasPrefix(secret, APITokenPrefix) {
		return api.User{}, store.ErrNotFound
	}
	return st.APITokenUser(HashToken(secret))
}
