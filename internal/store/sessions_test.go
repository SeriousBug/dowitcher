package store

import (
	"errors"
	"testing"
	"time"
)

// TestDeleteUserSessionsRevokesOnlyThatUser: revocation is the lever the long
// SessionTTL rests on, so it has to cut every session the user holds and no
// session anybody else holds.
func TestDeleteUserSessionsRevokesOnlyThatUser(t *testing.T) {
	st := testStore(t)
	alice, _ := st.CreateUser(NewID(), "alice", false)
	bob, _ := st.CreateUser(NewID(), "bob", false)
	exp := time.Now().Add(time.Hour).Unix()

	for _, tok := range []string{"alice-phone", "alice-laptop"} {
		if err := st.CreateSession(tok, alice.ID, exp); err != nil {
			t.Fatalf("create session %s: %v", tok, err)
		}
	}
	if err := st.CreateSession("bob-phone", bob.ID, exp); err != nil {
		t.Fatalf("create bob session: %v", err)
	}

	if err := st.DeleteUserSessions(alice.ID); err != nil {
		t.Fatalf("delete alice sessions: %v", err)
	}
	for _, tok := range []string{"alice-phone", "alice-laptop"} {
		if _, err := st.SessionUser(tok); !errors.Is(err, ErrNotFound) {
			t.Errorf("session %s still authenticates after revocation: err=%v", tok, err)
		}
	}
	if _, err := st.SessionUser("bob-phone"); err != nil {
		t.Errorf("revoking alice's sessions took bob's with it: %v", err)
	}
}

// TestDeleteUserSessionsExceptKeepsTheCaller: the point of the Except variant is
// that retiring a lost device does not log you out of the device you are doing
// it from.
func TestDeleteUserSessionsExceptKeepsTheCaller(t *testing.T) {
	st := testStore(t)
	alice, _ := st.CreateUser(NewID(), "alice", false)
	exp := time.Now().Add(time.Hour).Unix()
	for _, tok := range []string{"here", "lost", "also-lost"} {
		if err := st.CreateSession(tok, alice.ID, exp); err != nil {
			t.Fatalf("create session %s: %v", tok, err)
		}
	}

	if err := st.DeleteUserSessionsExcept(alice.ID, "here"); err != nil {
		t.Fatalf("delete except: %v", err)
	}
	if _, err := st.SessionUser("here"); err != nil {
		t.Errorf("the kept session must survive: %v", err)
	}
	for _, tok := range []string{"lost", "also-lost"} {
		if _, err := st.SessionUser(tok); !errors.Is(err, ErrNotFound) {
			t.Errorf("session %s survived a revocation that named a different keep token: err=%v", tok, err)
		}
	}
}

// TestDeleteUserSessionsExceptWithNoTokenRevokesAll pins the empty-keepToken
// case: a caller with no session of its own has nothing to preserve, and must
// not accidentally preserve everything.
func TestDeleteUserSessionsExceptWithNoTokenRevokesAll(t *testing.T) {
	st := testStore(t)
	alice, _ := st.CreateUser(NewID(), "alice", false)
	if err := st.CreateSession("only", alice.ID, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := st.DeleteUserSessionsExcept(alice.ID, ""); err != nil {
		t.Fatalf("delete except: %v", err)
	}
	if _, err := st.SessionUser("only"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an empty keep token must revoke everything, got err=%v", err)
	}
}
