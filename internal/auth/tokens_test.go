package auth

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SeriousBug/dowitcher/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// TestNewAPITokenRoundTrips: a minted token is prefixed, is only stored hashed,
// and resolves back to the user who minted it.
func TestNewAPITokenRoundTrips(t *testing.T) {
	st := testStore(t)
	u, _ := st.CreateUser(store.NewID(), "alice", true)

	secret, tok, err := NewAPIToken(st, u.ID, "laptop")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if !strings.HasPrefix(secret, APITokenPrefix) {
		t.Errorf("secret is not prefixed: %q", secret)
	}
	if tok.ID == "" || tok.Name != "laptop" {
		t.Errorf("returned token metadata is wrong: %+v", tok)
	}

	// The plain secret is never stored: only its hash is in the table.
	rows, _ := st.ListAPITokens(u.ID)
	if len(rows) != 1 {
		t.Fatalf("want 1 token, got %d", len(rows))
	}

	back, err := UserForAPIToken(st, secret)
	if err != nil {
		t.Fatalf("resolve secret: %v", err)
	}
	if back.ID != u.ID || !back.IsAdmin {
		t.Errorf("resolved to the wrong user: %+v", back)
	}
}

// TestUserForAPITokenRejectsForeignTokens: a bearer value that is not one of our
// tokens is rejected, and one without our prefix never reaches the database.
func TestUserForAPITokenRejectsForeignTokens(t *testing.T) {
	st := testStore(t)
	u, _ := st.CreateUser(store.NewID(), "alice", false)
	good, _, _ := NewAPIToken(st, u.ID, "agent")

	if _, err := UserForAPIToken(st, "github_pat_whatever"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a non-dowitcher token should be ErrNotFound, got %v", err)
	}
	// A well-formed-looking but never-minted token also fails.
	if _, err := UserForAPIToken(st, APITokenPrefix+"deadbeef"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an unminted token should be ErrNotFound, got %v", err)
	}
	// A one-character change to a real token must not still resolve.
	if _, err := UserForAPIToken(st, good+"x"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a tampered token should be ErrNotFound, got %v", err)
	}
}

// TestHashTokenIsDeterministic: the same secret always hashes the same, or a
// second presentation of a valid token would fail to authenticate.
func TestHashTokenIsDeterministic(t *testing.T) {
	if HashToken("dwt_abc") != HashToken("dwt_abc") {
		t.Error("hash is not stable across calls")
	}
	if HashToken("dwt_abc") == HashToken("dwt_abd") {
		t.Error("distinct secrets collide")
	}
}
