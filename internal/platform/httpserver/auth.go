package httpserver

import (
	"context"
	"net/http"
	"strings"

	"backend-challenge-go/internal/platform/keycloak"
)

// RoleInternal is granted only to the internal wallet-management service
// account in the provisioned realm (see deploy/keycloak/realm-export.json)
// - never to a provider client. It gates wallet creation/reads and
// reconciliation.
const RoleInternal = "internal"

// RoleProvider is granted to every provider service account. It gates the
// wagering endpoints; which provider's data a request may touch is then
// further scoped by comparing the token's client identity against the
// resource being accessed (see wagering.go).
const RoleProvider = "provider"

type claimsContextKey struct{}

func claimsFromContext(ctx context.Context) (keycloak.Claims, bool) {
	c, ok := ctx.Value(claimsContextKey{}).(keycloak.Claims)
	return c, ok
}

// requireRole wraps a handler so it 401s with no Authorization header or an
// invalid/expired token, and 403s when the token is valid but lacks role.
// This is the "ausência de autenticação efetiva" / "acesso não autorizado"
// gate the spec treats as an automatic-failure condition if missing.
func requireRole(verifier *keycloak.Verifier, role string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := authenticate(w, r, verifier)
		if !ok {
			return
		}
		if !claims.HasRole(role) {
			writeError(w, http.StatusForbidden, "FORBIDDEN", "this credential does not have the required role")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), claimsContextKey{}, claims)))
	}
}

// authenticate validates the Authorization: Bearer <token> header, writing
// a 401 response and returning ok=false on any failure (missing header,
// malformed header, invalid signature, expired token, wrong issuer).
func authenticate(w http.ResponseWriter, r *http.Request, verifier *keycloak.Verifier) (keycloak.Claims, bool) {
	header := r.Header.Get("Authorization")
	token, found := strings.CutPrefix(header, "Bearer ")
	if !found || token == "" {
		writeError(w, http.StatusUnauthorized, "MISSING_CREDENTIALS", "a Bearer token is required")
		return keycloak.Claims{}, false
	}
	claims, err := verifier.Verify(r.Context(), token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "the provided token is invalid or expired")
		return keycloak.Claims{}, false
	}
	return claims, true
}
