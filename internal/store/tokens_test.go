package store

import (
	"errors"
	"testing"
	"time"
)

// oauthClient creates a registered client so access/refresh token rows have a
// valid FK to reference.
func oauthClient(t *testing.T, st *Store) string {
	t.Helper()
	id := NewID()
	if err := st.CreateOAuthClient(id, "agent", []string{"https://example.test/cb"}); err != nil {
		t.Fatalf("create client: %v", err)
	}
	return id
}

// TestAccessTokenResolvesToOwner: a stored access-token hash authenticates as
// exactly the user it was minted for, carrying that user's admin flag and the
// real stored expiry.
func TestAccessTokenResolvesToOwner(t *testing.T) {
	st := testStore(t)
	admin, _ := st.CreateUser(NewID(), "admin", true)
	client := oauthClient(t, st)
	exp := time.Now().Add(time.Hour).Unix()
	if err := st.CreateAccessToken("hash-a", client, admin.ID, "mcp", exp); err != nil {
		t.Fatalf("create token: %v", err)
	}
	u, gotExp, err := st.AccessTokenUser("hash-a")
	if err != nil {
		t.Fatalf("resolve token: %v", err)
	}
	if u.ID != admin.ID {
		t.Errorf("token resolved to %s, want %s", u.ID, admin.ID)
	}
	if !u.IsAdmin {
		t.Errorf("admin flag lost through the token: %+v", u)
	}
	if gotExp != exp {
		t.Errorf("expiry not returned truthfully: got %d, want %d", gotExp, exp)
	}
}

// TestAccessTokenUnknownHashIsNotFound: a hash nobody minted must not authenticate.
func TestAccessTokenUnknownHashIsNotFound(t *testing.T) {
	st := testStore(t)
	if _, _, err := st.AccessTokenUser("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown token hash should be ErrNotFound, got %v", err)
	}
}

// TestAccessTokenExpiryFilteredInSQL: an expired token does not resolve, and the
// filter is in the WHERE rather than a check on the result — so there is no
// window in which a caller forgets to apply it.
func TestAccessTokenExpiryFilteredInSQL(t *testing.T) {
	st := testStore(t)
	alice, _ := st.CreateUser(NewID(), "alice", false)
	client := oauthClient(t, st)
	past := time.Now().Add(-time.Minute).Unix()
	if err := st.CreateAccessToken("expired", client, alice.ID, "mcp", past); err != nil {
		t.Fatalf("create token: %v", err)
	}
	if _, _, err := st.AccessTokenUser("expired"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired token should be ErrNotFound, got %v", err)
	}
}

// TestConsumeAuthorizationCodeIsSingleUse: a code redeems once and is gone, so a
// replay finds nothing.
func TestConsumeAuthorizationCodeIsSingleUse(t *testing.T) {
	st := testStore(t)
	alice, _ := st.CreateUser(NewID(), "alice", false)
	client := oauthClient(t, st)
	exp := time.Now().Add(time.Minute).Unix()
	if err := st.CreateAuthorizationCode("code-h", client, alice.ID, "https://example.test/cb", "chal", "mcp", exp); err != nil {
		t.Fatalf("create code: %v", err)
	}
	ac, err := st.ConsumeAuthorizationCode("code-h")
	if err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if ac.UserID != alice.ID || ac.ClientID != client || ac.RedirectURI != "https://example.test/cb" || ac.CodeChallenge != "chal" {
		t.Errorf("consumed code lost a binding: %+v", ac)
	}
	if _, err := st.ConsumeAuthorizationCode("code-h"); !errors.Is(err, ErrNotFound) {
		t.Errorf("replayed code should be ErrNotFound, got %v", err)
	}
}

// TestConsumeExpiredAuthorizationCode: an expired code is ErrNotFound.
func TestConsumeExpiredAuthorizationCode(t *testing.T) {
	st := testStore(t)
	alice, _ := st.CreateUser(NewID(), "alice", false)
	client := oauthClient(t, st)
	past := time.Now().Add(-time.Second).Unix()
	if err := st.CreateAuthorizationCode("old", client, alice.ID, "https://example.test/cb", "chal", "mcp", past); err != nil {
		t.Fatalf("create code: %v", err)
	}
	if _, err := st.ConsumeAuthorizationCode("old"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired code should be ErrNotFound, got %v", err)
	}
}

// TestConsumeRefreshTokenRotates: consuming a refresh token deletes it, so a
// replay of the same token fails — rotation on every use.
func TestConsumeRefreshTokenRotates(t *testing.T) {
	st := testStore(t)
	alice, _ := st.CreateUser(NewID(), "alice", false)
	client := oauthClient(t, st)
	exp := time.Now().Add(time.Hour).Unix()
	if err := st.CreateRefreshToken("rt-h", client, alice.ID, "mcp", exp); err != nil {
		t.Fatalf("create refresh: %v", err)
	}
	rt, err := st.ConsumeRefreshToken("rt-h")
	if err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if rt.UserID != alice.ID || rt.ClientID != client {
		t.Errorf("consumed refresh lost a binding: %+v", rt)
	}
	if _, err := st.ConsumeRefreshToken("rt-h"); !errors.Is(err, ErrNotFound) {
		t.Errorf("replayed refresh should be ErrNotFound, got %v", err)
	}
}

// TestDeleteUserOAuthTokensCutsAccessAndRefresh: signing out other devices cuts
// every access and refresh token the user holds and none of anyone else's.
func TestDeleteUserOAuthTokensCutsAccessAndRefresh(t *testing.T) {
	st := testStore(t)
	alice, _ := st.CreateUser(NewID(), "alice", false)
	bob, _ := st.CreateUser(NewID(), "bob", false)
	client := oauthClient(t, st)
	exp := time.Now().Add(time.Hour).Unix()
	must := func(err error) {
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	must(st.CreateAccessToken("a-access", client, alice.ID, "mcp", exp))
	must(st.CreateRefreshToken("a-refresh", client, alice.ID, "mcp", exp))
	must(st.CreateAccessToken("b-access", client, bob.ID, "mcp", exp))

	n, err := st.DeleteUserOAuthTokens(alice.ID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n != 2 {
		t.Errorf("revoked count feeds the UI's total, want 2, got %d", n)
	}
	if _, _, err := st.AccessTokenUser("a-access"); !errors.Is(err, ErrNotFound) {
		t.Errorf("alice access token survived: %v", err)
	}
	if _, err := st.ConsumeRefreshToken("a-refresh"); !errors.Is(err, ErrNotFound) {
		t.Errorf("alice refresh token survived: %v", err)
	}
	if _, _, err := st.AccessTokenUser("b-access"); err != nil {
		t.Errorf("bob's token was taken with alice's: %v", err)
	}
}
