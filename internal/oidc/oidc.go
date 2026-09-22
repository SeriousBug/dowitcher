// Package oidc is a relying party for a generic OpenID Connect provider. It
// discovers the provider from its issuer URL, runs the authorization-code flow
// with PKCE, and verifies the returned ID token. Nothing here is specific to any
// one provider: an operator points it at an issuer and it follows the standard.
package oidc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Config is what the operator configured. Only Issuer and ClientID are
// required; a public client that relies on PKCE leaves ClientSecret empty.
type Config struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	// Scopes defaults to openid, profile, email. openid is always present, so a
	// provider that rejects a scope is the only reason to change this.
	Scopes []string
	// NameClaim is the ID token claim that becomes the account's display name,
	// tried before the built-in preferred_username/name/email/subject fallback.
	NameClaim string
	// GroupsClaim is the claim carrying group membership; AdminGroups are the
	// values in it that grant admin. Empty AdminGroups disables claim-based
	// admin entirely.
	GroupsClaim string
	AdminGroups []string
	// DisplayName labels the button on the login page ("Sign in with <name>").
	DisplayName string
	// RedirectURL is the absolute callback the provider sends the browser to.
	RedirectURL string
}

// Identity is the subset of the ID token this app acts on. Issuer and Subject
// together are the account's external name; everything else is display and
// authorisation.
type Identity struct {
	Issuer  string
	Subject string
	Name    string
	Email   string
	Admin   bool
}

// Provider is a discovered, ready relying party. It is safe to share.
type Provider struct {
	cfg      Config
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// New discovers the issuer and builds the relying party. The issuer must be
// reachable at startup: this is where a typo in the URL or a provider that is
// down is caught, rather than at the first login attempt.
func New(ctx context.Context, cfg Config) (*Provider, error) {
	if cfg.Issuer == "" {
		return nil, errors.New("oidc: issuer is required")
	}
	if cfg.ClientID == "" {
		return nil, errors.New("oidc: client id is required")
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	}
	// openid is not optional for OIDC; adding it back means an operator who
	// trimmed the scope list still gets a working flow.
	if !contains(cfg.Scopes, oidc.ScopeOpenID) {
		cfg.Scopes = append([]string{oidc.ScopeOpenID}, cfg.Scopes...)
	}
	p, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discover issuer: %w", err)
	}
	return &Provider{
		cfg: cfg,
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     p.Endpoint(),
			RedirectURL:  cfg.RedirectURL,
			Scopes:       cfg.Scopes,
		},
		verifier: p.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
	}, nil
}

// DisplayName is the label the login page shows for this provider.
func (p *Provider) DisplayName() string { return p.cfg.DisplayName }

// AuthCodeURL is the provider URL to send the browser to. state and nonce are
// per-attempt random values; verifier is the PKCE secret whose S256 hash rides
// in the request.
func (p *Provider) AuthCodeURL(state, nonce, verifier string) string {
	return p.oauth.AuthCodeURL(state,
		oidc.Nonce(nonce),
		oauth2.S256ChallengeOption(verifier),
	)
}

// Exchange redeems the authorization code, verifies the ID token against the
// provider's keys, audience and nonce, and returns the identity it asserts.
func (p *Provider) Exchange(ctx context.Context, code, verifier, nonce string) (Identity, error) {
	token, err := p.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return Identity{}, fmt.Errorf("exchange code: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return Identity{}, errors.New("provider returned no id_token")
	}
	idToken, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return Identity{}, fmt.Errorf("verify id_token: %w", err)
	}
	// go-oidc deliberately does not check the nonce; binding it here is what
	// stops a token minted for another attempt from being replayed into this one.
	if idToken.Nonce != nonce {
		return Identity{}, errors.New("id_token nonce mismatch")
	}

	var named struct {
		Email             string `json:"email"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
	}
	if err := idToken.Claims(&named); err != nil {
		return Identity{}, fmt.Errorf("read id_token claims: %w", err)
	}
	// A second decode into a generic map for the operator-chosen claims, which
	// have no fixed name.
	var raw map[string]any
	if err := idToken.Claims(&raw); err != nil {
		return Identity{}, fmt.Errorf("read id_token claims: %w", err)
	}

	name := p.displayName(raw,
		named.PreferredUsername, named.Name, named.Email, idToken.Subject)
	return Identity{
		Issuer:  idToken.Issuer,
		Subject: idToken.Subject,
		Name:    name,
		Email:   named.Email,
		Admin:   p.isAdmin(raw),
	}, nil
}

// displayName resolves the account name, preferring the configured claim and
// falling back through the standard ones to the subject, which always exists.
func (p *Provider) displayName(raw map[string]any, fallback ...string) string {
	if p.cfg.NameClaim != "" {
		if s := stringClaim(raw, p.cfg.NameClaim); s != "" {
			return s
		}
	}
	for _, f := range fallback {
		if f != "" {
			return f
		}
	}
	return ""
}

// isAdmin reports whether the groups claim puts this identity in an admin
// group. A provider that never sends the claim therefore never grants admin,
// which is the safe reading of an absent claim.
func (p *Provider) isAdmin(raw map[string]any) bool {
	if len(p.cfg.AdminGroups) == 0 {
		return false
	}
	claim := p.cfg.GroupsClaim
	if claim == "" {
		claim = "groups"
	}
	for _, group := range stringList(raw[claim]) {
		if contains(p.cfg.AdminGroups, group) {
			return true
		}
	}
	return false
}

// stringClaim reads one claim as a string, or "" if it is absent or not a
// string.
func stringClaim(raw map[string]any, key string) string {
	s, _ := raw[key].(string)
	return s
}

// stringList coerces a claim that may be a JSON array or a lone string into a
// list, because providers disagree about how a single value is encoded.
func stringList(v any) []string {
	switch v := v.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	}
	return nil
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if strings.EqualFold(strings.TrimSpace(v), want) {
			return true
		}
	}
	return false
}
