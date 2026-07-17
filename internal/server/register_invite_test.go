package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/SeriousBug/dowitcher/internal/api"
	"github.com/SeriousBug/dowitcher/internal/auth"
	"github.com/descope/virtualwebauthn"
)

// TestRegisterConsumesTheCeremonysOwnInvite: the invite a registration spends
// must be the one it was begun against.
//
// The rights the new user gets are read from the invite at begin time and
// parked on the server-side ceremony. If the finish could name a different
// invite, the invite being spent and the rights being granted would come from
// two different places with nothing checking they agree — so a caller could
// burn a cheap invite while keeping the valuable one unused and reusable.
//
// The client here does exactly that: begins against the admin invite, then
// presents the normal invite's token to the finish.
func TestRegisterConsumesTheCeremonysOwnInvite(t *testing.T) {
	ts, st, client := testServer(t, nil)

	adminToken := bootstrapToken(t, st, ts.URL)
	normalToken, _, err := auth.NewInvite(st, "", "", false)
	if err != nil {
		t.Fatalf("mint normal invite: %v", err)
	}

	// Begin against the admin invite: this is what sets cer.isAdmin.
	req, _ := json.Marshal(api.EnrollRequest{Token: adminToken, Name: "Mallory"})
	resp, body := post(t, client, ts.URL+"/auth/register/begin", req)
	if resp.StatusCode != 200 {
		t.Fatalf("register/begin: %d %s", resp.StatusCode, body)
	}
	opts, err := virtualwebauthn.ParseAttestationOptions(string(body))
	if err != nil {
		t.Fatalf("parse attestation options: %v", err)
	}

	// Point the invite cookie at the *other* invite before finishing. A server
	// that reads the invite from the request rather than from its own ceremony
	// will spend this one.
	u, _ := url.Parse(ts.URL)
	client.Jar.SetCookies(u, []*http.Cookie{{
		Name: "dowitcher_invite", Value: normalToken, Path: "/auth",
	}})

	pk := newPasskey(ts.URL)
	pk.auth.Options.UserHandle = []byte(opts.UserID)
	att := virtualwebauthn.CreateAttestationResponse(pk.rp, pk.auth, pk.cred, *opts)
	if resp, body := post(t, client, ts.URL+"/auth/register/finish", []byte(att)); resp.StatusCode != 200 {
		t.Fatalf("register/finish: %d %s", resp.StatusCode, body)
	}

	// The admin invite paid for this enrollment, so it must be spent...
	adminInv, err := st.GetInvite(adminToken)
	if err != nil {
		t.Fatalf("get admin invite: %v", err)
	}
	if adminInv.UsedAt == 0 {
		t.Error("the invite the ceremony was begun against was not consumed; " +
			"it is still live and can be redeemed again")
	}
	// ...and the invite the client pointed at must be untouched.
	normalInv, err := st.GetInvite(normalToken)
	if err != nil {
		t.Fatalf("get normal invite: %v", err)
	}
	if normalInv.UsedAt != 0 {
		t.Error("a finish-time token consumed an invite the ceremony was never begun against")
	}
}
