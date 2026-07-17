package server

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"testing"

	"github.com/SeriousBug/dowitcher/internal/api"
)

// newClient is a second (or third) browser for the same passkey: a fresh jar
// means a fresh session against the same account.
func newClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

// TestResetUserRevokesEveryTargetSession: an admin reaches for reset when a
// device that should not have access still does. Minting a recovery link while
// leaving that device's cookie live answers the wrong half of the problem — and
// with a year-long SessionTTL, leaves it live for a year.
func TestResetUserRevokesEveryTargetSession(t *testing.T) {
	ts, st, admin := testServer(t, nil)
	if resp, body := newPasskey(ts.URL).enroll(t, admin, ts.URL, bootstrapToken(t, st, ts.URL), "Alice"); resp.StatusCode != 200 {
		t.Fatalf("enroll admin: %d %s", resp.StatusCode, body)
	}

	_, body := post(t, admin, ts.URL+"/api/invites", mustJSON(t, api.CreateInviteRequest{IsAdmin: false}))
	var inv api.Invite
	if err := json.Unmarshal(body, &inv); err != nil {
		t.Fatalf("decode invite: %v", err)
	}

	// Bob signs in on two devices, both of which the reset must cut off.
	bobPhone, bobLaptop := newClient(), newClient()
	pk := newPasskey(ts.URL)
	if resp, body := pk.enroll(t, bobPhone, ts.URL, inv.Token, "Bob"); resp.StatusCode != 200 {
		t.Fatalf("enroll bob: %d %s", resp.StatusCode, body)
	}
	if resp, body := pk.login(t, bobLaptop, ts.URL); resp.StatusCode != 200 {
		t.Fatalf("bob second login: %d %s", resp.StatusCode, body)
	}
	bob, err := st.UserByName("Bob")
	if err != nil {
		t.Fatalf("lookup bob: %v", err)
	}
	for _, c := range []*http.Client{bobPhone, bobLaptop} {
		if resp, _ := getReq(t, c, ts.URL+"/auth/me"); resp.StatusCode != 200 {
			t.Fatalf("bob should be signed in before the reset, got %d", resp.StatusCode)
		}
	}

	if resp, body := post(t, admin, ts.URL+"/api/users/"+bob.ID+"/reset", nil); resp.StatusCode != 200 {
		t.Fatalf("reset: %d %s", resp.StatusCode, body)
	}

	for name, c := range map[string]*http.Client{"phone": bobPhone, "laptop": bobLaptop} {
		if resp, _ := getReq(t, c, ts.URL+"/auth/me"); resp.StatusCode != 401 {
			t.Errorf("bob's %s session survived the reset (%d); a lost device keeps access "+
				"for the whole TTL", name, resp.StatusCode)
		}
	}
	// The reset is about the target, not the admin running it.
	if resp, _ := getReq(t, admin, ts.URL+"/auth/me"); resp.StatusCode != 200 {
		t.Errorf("resetting another user logged the admin out, got %d", resp.StatusCode)
	}
}

// TestDeleteCredentialRevokesOtherSessionsButNotTheCallers: removing a lost
// passkey has to cut the lost device's cookie, or the passkey was the only thing
// removed and the device reads on. It must not cut the session doing the
// removing.
func TestDeleteCredentialRevokesOtherSessionsButNotTheCallers(t *testing.T) {
	ts, st, here := testServer(t, nil)
	pk := newPasskey(ts.URL)
	if resp, body := pk.enroll(t, here, ts.URL, bootstrapToken(t, st, ts.URL), "Alice"); resp.StatusCode != 200 {
		t.Fatalf("enroll: %d %s", resp.StatusCode, body)
	}

	// The same passkey signed in elsewhere: this is the session standing in for
	// the lost device.
	lost := newClient()
	if resp, body := pk.login(t, lost, ts.URL); resp.StatusCode != 200 {
		t.Fatalf("second login: %d %s", resp.StatusCode, body)
	}

	resp, body := getReq(t, here, ts.URL+"/auth/me")
	if resp.StatusCode != 200 {
		t.Fatalf("/auth/me: %d %s", resp.StatusCode, body)
	}
	var me api.Session
	if err := json.Unmarshal(body, &me); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if len(me.Credentials) != 1 {
		t.Fatalf("want 1 credential, got %d", len(me.Credentials))
	}

	if resp, body := doReq(t, here, "DELETE", ts.URL+"/auth/credentials/"+me.Credentials[0].ID); resp.StatusCode != 200 {
		t.Fatalf("delete credential: %d %s", resp.StatusCode, body)
	}

	if resp, _ := getReq(t, lost, ts.URL+"/auth/me"); resp.StatusCode != 401 {
		t.Errorf("the other session survived the passkey it was created with (%d); "+
			"retiring a lost device did not lock it out", resp.StatusCode)
	}
	if resp, _ := getReq(t, here, ts.URL+"/auth/me"); resp.StatusCode != 200 {
		t.Errorf("removing a passkey logged the caller out of the device they did it from, got %d",
			resp.StatusCode)
	}
}
