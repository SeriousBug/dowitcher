package store

import "testing"

func TestProvisionIdentityFirstAccountIsAdmin(t *testing.T) {
	st := testStore(t)

	first, created, err := st.ProvisionIdentity("https://idp.example", "sub-1", "a@example.com", "alice", false)
	if err != nil {
		t.Fatalf("provision first: %v", err)
	}
	if !created {
		t.Fatal("first identity should create an account")
	}
	if !first.IsAdmin {
		t.Error("the first account on the instance must be an admin even without an admin claim")
	}

	second, created, err := st.ProvisionIdentity("https://idp.example", "sub-2", "b@example.com", "bob", false)
	if err != nil {
		t.Fatalf("provision second: %v", err)
	}
	if !created {
		t.Fatal("second identity should create an account")
	}
	if second.IsAdmin {
		t.Error("a later account must not become admin without an admin claim")
	}
}

func TestProvisionIdentityReusesAccountAndPromotes(t *testing.T) {
	st := testStore(t)
	// A passkey-created admin, so the first-account rule is not in play.
	if _, err := st.CreateUser(NewID(), "root", true); err != nil {
		t.Fatalf("create root: %v", err)
	}

	u, created, err := st.ProvisionIdentity("https://idp.example", "sub-9", "c@example.com", "carol", false)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if !created {
		t.Fatal("want a new account")
	}
	if u.IsAdmin {
		t.Fatal("account must start non-admin")
	}

	// The same assertion a second time must resolve to the same account, and an
	// admin claim must promote it.
	again, created, err := st.ProvisionIdentity("https://idp.example", "sub-9", "c@example.com", "carol", true)
	if err != nil {
		t.Fatalf("provision again: %v", err)
	}
	if created {
		t.Error("a known identity must not create a second account")
	}
	if again.ID != u.ID {
		t.Errorf("identity resolved to %s, want %s", again.ID, u.ID)
	}
	if !again.IsAdmin {
		t.Error("an admin claim must promote an existing account")
	}

	// A later login without the claim must not demote; losing a claim is not a
	// deliberate demotion.
	demoted, _, err := st.ProvisionIdentity("https://idp.example", "sub-9", "c@example.com", "carol", false)
	if err != nil {
		t.Fatalf("provision third: %v", err)
	}
	if !demoted.IsAdmin {
		t.Error("an absent admin claim must not strip admin")
	}
}

func TestProvisionIdentityDistinguishesIssuers(t *testing.T) {
	st := testStore(t)
	a, _, err := st.ProvisionIdentity("https://a.example", "shared", "", "a", false)
	if err != nil {
		t.Fatalf("provision a: %v", err)
	}
	b, _, err := st.ProvisionIdentity("https://b.example", "shared", "", "b", false)
	if err != nil {
		t.Fatalf("provision b: %v", err)
	}
	if a.ID == b.ID {
		t.Error("the same subject from two issuers must not share an account")
	}
}

func TestProvisionIdentityNameFallsBackToSubject(t *testing.T) {
	st := testStore(t)
	u, _, err := st.ProvisionIdentity("https://idp.example", "no-name", "", "", false)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if u.Name != "no-name" {
		t.Errorf("name = %q, want the subject as fallback", u.Name)
	}
}
