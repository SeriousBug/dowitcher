package store

import (
	"errors"
	"testing"
)

// TestAPITokenResolvesToOwner: a stored token hash authenticates as exactly the
// user it was minted for, carrying that user's admin flag.
func TestAPITokenResolvesToOwner(t *testing.T) {
	st := testStore(t)
	admin, _ := st.CreateUser(NewID(), "admin", true)
	if err := st.CreateAPIToken(NewID(), admin.ID, "laptop", "hash-a"); err != nil {
		t.Fatalf("create token: %v", err)
	}
	u, err := st.APITokenUser("hash-a")
	if err != nil {
		t.Fatalf("resolve token: %v", err)
	}
	if u.ID != admin.ID {
		t.Errorf("token resolved to %s, want %s", u.ID, admin.ID)
	}
	if !u.IsAdmin {
		t.Errorf("admin flag lost through the token: %+v", u)
	}
}

// TestAPITokenUnknownHashIsNotFound: a hash nobody minted must not authenticate.
func TestAPITokenUnknownHashIsNotFound(t *testing.T) {
	st := testStore(t)
	if _, err := st.APITokenUser("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown token hash should be ErrNotFound, got %v", err)
	}
}

// TestAPITokenStampsLastUsed: resolving a token records the moment for the UI.
func TestAPITokenStampsLastUsed(t *testing.T) {
	st := testStore(t)
	alice, _ := st.CreateUser(NewID(), "alice", false)
	if err := st.CreateAPIToken(NewID(), alice.ID, "agent", "hash-b"); err != nil {
		t.Fatalf("create token: %v", err)
	}
	before, _ := st.ListAPITokens(alice.ID)
	if len(before) != 1 || before[0].LastUsed != 0 {
		t.Fatalf("a fresh token should have last_used 0, got %+v", before)
	}
	if _, err := st.APITokenUser("hash-b"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	after, _ := st.ListAPITokens(alice.ID)
	if after[0].LastUsed == 0 {
		t.Errorf("last_used should be stamped after a resolve, got %+v", after[0])
	}
}

// TestListAndDeleteAPITokenScopedToUser: a user only ever sees and revokes their
// own tokens; one user cannot delete another's by guessing its id.
func TestListAndDeleteAPITokenScopedToUser(t *testing.T) {
	st := testStore(t)
	alice, _ := st.CreateUser(NewID(), "alice", false)
	bob, _ := st.CreateUser(NewID(), "bob", false)
	aliceTok := NewID()
	if err := st.CreateAPIToken(aliceTok, alice.ID, "a", "hash-alice"); err != nil {
		t.Fatalf("create alice token: %v", err)
	}
	if err := st.CreateAPIToken(NewID(), bob.ID, "b", "hash-bob"); err != nil {
		t.Fatalf("create bob token: %v", err)
	}

	bobTokens, _ := st.ListAPITokens(bob.ID)
	if len(bobTokens) != 1 || bobTokens[0].Name != "b" {
		t.Errorf("bob should see only his own token, got %+v", bobTokens)
	}

	// Bob cannot delete Alice's token.
	if err := st.DeleteAPIToken(bob.ID, aliceTok); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-user delete should miss, got %v", err)
	}
	if _, err := st.APITokenUser("hash-alice"); err != nil {
		t.Errorf("alice's token must survive bob's attempt: %v", err)
	}

	// Alice can.
	if err := st.DeleteAPIToken(alice.ID, aliceTok); err != nil {
		t.Fatalf("owner delete: %v", err)
	}
	if _, err := st.APITokenUser("hash-alice"); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoked token still authenticates: %v", err)
	}
}

// TestDeleteUserAPITokensCutsEveryToken: signing out other devices has to cut
// every token the user holds and none of anyone else's.
func TestDeleteUserAPITokensCutsEveryToken(t *testing.T) {
	st := testStore(t)
	alice, _ := st.CreateUser(NewID(), "alice", false)
	bob, _ := st.CreateUser(NewID(), "bob", false)
	for _, h := range []string{"a1", "a2"} {
		if err := st.CreateAPIToken(NewID(), alice.ID, h, h); err != nil {
			t.Fatalf("create %s: %v", h, err)
		}
	}
	if err := st.CreateAPIToken(NewID(), bob.ID, "b1", "b1"); err != nil {
		t.Fatalf("create bob token: %v", err)
	}

	n, err := st.DeleteUserAPITokens(alice.ID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n != 2 {
		t.Errorf("revoked count feeds the UI's total, want 2, got %d", n)
	}
	for _, h := range []string{"a1", "a2"} {
		if _, err := st.APITokenUser(h); !errors.Is(err, ErrNotFound) {
			t.Errorf("alice token %s survived: %v", h, err)
		}
	}
	if _, err := st.APITokenUser("b1"); err != nil {
		t.Errorf("bob's token was taken with alice's: %v", err)
	}
}
