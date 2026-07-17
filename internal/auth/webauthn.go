package auth

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/SeriousBug/longbox/internal/store"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// Errors surfaced to handlers.
var (
	ErrInvalidInvite   = errors.New("invalid or expired invite")
	ErrCeremonyExpired = errors.New("ceremony expired, try again")
)

// CeremonyTTL bounds how long a half-finished ceremony stays resident. It is a
// user walking to their phone, not a session. The server matches its ceremony
// cookie's MaxAge to it.
const CeremonyTTL = 5 * time.Minute

// Manager runs the WebAuthn ceremonies against the store.
type Manager struct {
	wa *webauthn.WebAuthn
	st *store.Store

	// Ceremonies live in memory and only in memory: they are worthless a few
	// minutes after they are made, and a restart mid-ceremony costs the user one
	// retry rather than anything durable.
	mu         sync.Mutex
	ceremonies map[string]*ceremony
}

type ceremony struct {
	data   *webauthn.SessionData
	userID string
	name   string
	// inviteToken is the invite this ceremony was begun against, and it is the
	// only invite the matching finish will consume.
	//
	// It lives here rather than being handed to FinishRegistration by the caller
	// because isAdmin below is read from the invite at begin time. If the finish
	// could name a different invite, the rights a user is created with and the
	// invite that pays for them would come from two different places, and
	// nothing would check they agree. Keeping both on the ceremony means there
	// is only ever one invite in play.
	inviteToken  string
	isAdmin      bool
	existingUser bool // recovery invite: add a passkey to an existing user, don't create one
	expires      time.Time
}

// Config for the relying party.
type Config struct {
	RPID   string // e.g. "longbox.example.com"
	Origin string // e.g. "https://longbox.example.com"
	RPName string
}

// NewManager constructs a WebAuthn manager.
func NewManager(st *store.Store, cfg Config) (*Manager, error) {
	name := cfg.RPName
	if name == "" {
		name = "Longbox"
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          cfg.RPID,
		RPDisplayName: name,
		RPOrigins:     []string{cfg.Origin},
	})
	if err != nil {
		return nil, fmt.Errorf("webauthn config: %w", err)
	}
	return &Manager{wa: wa, st: st, ceremonies: map[string]*ceremony{}}, nil
}

func (m *Manager) put(c *ceremony) string {
	id := randToken(16)
	c.expires = time.Now().Add(CeremonyTTL)
	m.mu.Lock()
	m.ceremonies[id] = c
	m.gcLocked()
	m.mu.Unlock()
	return id
}

// take is one-shot: the ceremony is removed whether or not it turns out to be
// valid, so a captured challenge cannot be replayed.
func (m *Manager) take(id string) (*ceremony, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.ceremonies[id]
	if ok {
		delete(m.ceremonies, id)
	}
	if ok && time.Now().After(c.expires) {
		return nil, false
	}
	return c, ok
}

func (m *Manager) gcLocked() {
	now := time.Now()
	for k, v := range m.ceremonies {
		if now.After(v.expires) {
			delete(m.ceremonies, k)
		}
	}
}

// registrationOpts are shared by every enrollment path. ResidentKey is required
// because login is usernameless: without a discoverable credential the
// authenticator has nothing to offer when the user has typed no identifier.
func registrationOpts(exclusions []protocol.CredentialDescriptor) []webauthn.RegistrationOption {
	sel := protocol.AuthenticatorSelection{
		ResidentKey:      protocol.ResidentKeyRequirementRequired,
		UserVerification: protocol.VerificationPreferred,
	}
	opts := []webauthn.RegistrationOption{
		webauthn.WithAuthenticatorSelection(sel),
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
	}
	if len(exclusions) > 0 {
		opts = append(opts, webauthn.WithExclusions(exclusions))
	}
	return opts
}

// BeginRegistration validates the invite and starts enrollment, returning the
// creation options JSON and a ceremony id to round-trip.
func (m *Manager) BeginRegistration(inviteToken, name string) (*protocol.CredentialCreation, string, bool, error) {
	inv, err := m.st.GetInvite(inviteToken)
	if err != nil {
		return nil, "", false, ErrInvalidInvite
	}
	if inv.UsedAt != 0 || inv.ExpiresAt < time.Now().Unix() {
		return nil, "", false, ErrInvalidInvite
	}
	// Recovery invite bound to an existing user: enroll an additional passkey
	// onto that user rather than creating a new one.
	if inv.ForUser != "" {
		return m.beginRecoveryRegistration(inv.ForUser, inviteToken)
	}
	if name == "" {
		name = "user"
	}
	userID := randToken(16)
	user := newRegistrationUser(userID, name)
	opts, sessionData, err := m.wa.BeginRegistration(user, registrationOpts(nil)...)
	if err != nil {
		return nil, "", false, err
	}
	cid := m.put(&ceremony{
		data: sessionData, userID: userID, name: name,
		inviteToken: inviteToken, isAdmin: inv.IsAdmin,
	})
	return opts, cid, inv.IsAdmin, nil
}

// beginRecoveryRegistration starts enrollment of an additional passkey for an
// existing user via a bound recovery invite. It reuses the user's WebAuthn id so
// the new passkey lands on the same account.
func (m *Manager) beginRecoveryRegistration(userID, inviteToken string) (*protocol.CredentialCreation, string, bool, error) {
	u, err := m.st.GetUser(userID)
	if err != nil {
		return nil, "", false, ErrInvalidInvite
	}
	user, err := loadAuthUser(m.st, u.ID, u.Name)
	if err != nil {
		return nil, "", false, err
	}
	opts, sessionData, err := m.wa.BeginRegistration(user, registrationOpts(exclusionsFor(user.creds))...)
	if err != nil {
		return nil, "", false, err
	}
	cid := m.put(&ceremony{
		data: sessionData, userID: u.ID, name: u.Name,
		inviteToken: inviteToken, isAdmin: u.IsAdmin, existingUser: true,
	})
	return opts, cid, u.IsAdmin, nil
}

// FinishRegistration completes enrollment: verifies the attestation, creates the
// user, stores the credential, and consumes the invite. Returns the new user id.
//
// The invite is the ceremony's own, never one the caller names. The rights the
// user is created with were read from that invite at begin time, so letting the
// finish supply a different token would consume one invite and grant another's
// rights, with nothing checking the two matched.
func (m *Manager) FinishRegistration(ceremonyID string, r *http.Request) (string, error) {
	cer, ok := m.take(ceremonyID)
	if !ok {
		return "", ErrCeremonyExpired
	}
	parsed, err := protocol.ParseCredentialCreationResponseBody(r.Body)
	if err != nil {
		return "", err
	}
	user := newRegistrationUser(cer.userID, cer.name)
	cred, err := m.wa.CreateCredential(user, *cer.data, parsed)
	if err != nil {
		return "", err
	}
	// Consume the invite before touching the user: the consume is the atomic
	// single-use gate, so nothing may be created ahead of winning it. The token
	// is the ceremony's own, which is what ties the invite being spent to the
	// invite cer.isAdmin was read from.
	if err := m.st.ConsumeInvite(cer.inviteToken); err != nil {
		return "", ErrInvalidInvite
	}
	if !cer.existingUser {
		if _, err := m.st.CreateUser(cer.userID, cer.name, cer.isAdmin); err != nil {
			return "", err
		}
	}
	if err := m.storeCredential(cer.userID, cred); err != nil {
		return "", err
	}
	return cer.userID, nil
}

// BeginAddDevice starts enrolling an ADDITIONAL passkey for an already
// authenticated user (second device). No invite is involved.
func (m *Manager) BeginAddDevice(userID, userName string, existingCreds []store.StoredCredential) (*protocol.CredentialCreation, string, error) {
	user := &authUser{id: []byte(userID), name: userName}
	for _, c := range existingCreds {
		user.creds = append(user.creds, toWebauthnCredential(c))
	}
	opts, sessionData, err := m.wa.BeginRegistration(user, registrationOpts(exclusionsFor(user.creds))...)
	if err != nil {
		return nil, "", err
	}
	cid := m.put(&ceremony{data: sessionData, userID: userID, name: userName})
	return opts, cid, nil
}

// FinishAddDevice completes an add-device ceremony, storing the new credential
// against the existing user. It creates no user and consumes no invite.
func (m *Manager) FinishAddDevice(ceremonyID, userID string, r *http.Request) error {
	cer, ok := m.take(ceremonyID)
	if !ok {
		return ErrCeremonyExpired
	}
	// The ceremony carries the user it was started for; a session that does not
	// match it is trying to graft a passkey onto someone else's account.
	if cer.userID != userID {
		return errors.New("ceremony does not match session user")
	}
	parsed, err := protocol.ParseCredentialCreationResponseBody(r.Body)
	if err != nil {
		return err
	}
	user := newRegistrationUser(cer.userID, cer.name)
	cred, err := m.wa.CreateCredential(user, *cer.data, parsed)
	if err != nil {
		return err
	}
	return m.storeCredential(cer.userID, cred)
}

func (m *Manager) storeCredential(userID string, cred *webauthn.Credential) error {
	var transports []string
	for _, t := range cred.Transport {
		transports = append(transports, string(t))
	}
	now := time.Now().Unix()
	return m.st.AddCredential(store.StoredCredential{
		ID:             store.NewID(),
		UserID:         userID,
		CredID:         cred.ID,
		PublicKey:      cred.PublicKey,
		SignCount:      cred.Authenticator.SignCount,
		Transports:     transports,
		AAGUID:         cred.Authenticator.AAGUID,
		Name:           "Passkey",
		BackupEligible: cred.Flags.BackupEligible,
		BackupState:    cred.Flags.BackupState,
		CreatedAt:      now,
	})
}

// BeginLogin starts a usernameless (discoverable) login.
func (m *Manager) BeginLogin() (*protocol.CredentialAssertion, string, error) {
	opts, sessionData, err := m.wa.BeginDiscoverableLogin()
	if err != nil {
		return nil, "", err
	}
	cid := m.put(&ceremony{data: sessionData})
	return opts, cid, nil
}

// FinishLogin verifies a discoverable assertion and returns the authenticated
// user id.
func (m *Manager) FinishLogin(ceremonyID string, r *http.Request) (string, error) {
	cer, ok := m.take(ceremonyID)
	if !ok {
		return "", ErrCeremonyExpired
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(r.Body)
	if err != nil {
		return "", err
	}
	var loggedInUserID string
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		sc, err := m.st.CredentialByCredID(rawID)
		if err != nil {
			return nil, errors.New("unknown credential")
		}
		user, err := m.st.GetUser(sc.UserID)
		if err != nil {
			return nil, err
		}
		loggedInUserID = sc.UserID
		return loadAuthUser(m.st, user.ID, user.Name)
	}
	cred, err := m.wa.ValidateDiscoverableLogin(handler, *cer.data, parsed)
	if err != nil {
		return "", err
	}
	// Persist the updated sign counter to detect cloned authenticators.
	if err := m.st.UpdateSignCount(cred.ID, cred.Authenticator.SignCount); err != nil {
		return "", err
	}
	return loggedInUserID, nil
}
