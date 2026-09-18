// Package keycloak validates OAuth2/OIDC access tokens issued by Keycloak.
// It never issues or stores credentials itself (out of scope per the
// spec) - it only verifies a token's signature (against Keycloak's JWKS,
// fetched via OIDC discovery), expiry and issuer, then exposes the claims
// this application actually needs: the client identity (used as
// providerId) and realm roles.
package keycloak

import (
	"context"
	"fmt"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Claims is the subset of a verified token's content this application
// acts on. ClientID comes from "azp" (falling back to "client_id") - for a
// client_credentials token, that is the calling provider's own identity,
// which is what authorizes which transactions it may touch.
type Claims struct {
	Subject  string
	ClientID string
	Roles    []string
}

// HasRole reports whether the token carries the given realm role.
func (c Claims) HasRole(role string) bool {
	for _, r := range c.Roles {
		if r == role {
			return true
		}
	}
	return false
}

type Verifier struct {
	idTokenVerifier *oidc.IDTokenVerifier
}

// NewVerifier performs OIDC discovery against issuerURL (Keycloak's realm
// URL, e.g. http://keycloak:8080/realms/backend-challenge) to find its
// JWKS endpoint, then builds a verifier around it. Client-ID/audience
// checking is skipped deliberately: Keycloak's client_credentials access
// tokens aren't ID tokens scoped to one audience, and this application
// authorizes by realm role and client identity (see Claims), not by
// audience.
func NewVerifier(ctx context.Context, issuerURL string) (*Verifier, error) {
	provider, err := oidc.NewProvider(ctx, issuerURL)
	if err != nil {
		return nil, fmt.Errorf("keycloak: OIDC discovery against %s: %w", issuerURL, err)
	}
	return &Verifier{
		idTokenVerifier: provider.Verifier(&oidc.Config{SkipClientIDCheck: true}),
	}, nil
}

// rawClaims mirrors just the fields of a Keycloak access token this
// application reads.
type rawClaims struct {
	Azp         string `json:"azp"`
	ClientID    string `json:"client_id"`
	RealmAccess struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

// Verify checks rawToken's signature, issuer and expiry, and returns the
// claims this application cares about. A verification failure (bad
// signature, expired, wrong issuer) is the caller's cue to reject the
// request with 401.
func (v *Verifier) Verify(ctx context.Context, rawToken string) (Claims, error) {
	idToken, err := v.idTokenVerifier.Verify(ctx, rawToken)
	if err != nil {
		return Claims{}, fmt.Errorf("keycloak: verify token: %w", err)
	}
	var raw rawClaims
	if err := idToken.Claims(&raw); err != nil {
		return Claims{}, fmt.Errorf("keycloak: decode claims: %w", err)
	}
	clientID := raw.ClientID
	if clientID == "" {
		clientID = raw.Azp
	}
	return Claims{
		Subject:  idToken.Subject,
		ClientID: clientID,
		Roles:    raw.RealmAccess.Roles,
	}, nil
}
