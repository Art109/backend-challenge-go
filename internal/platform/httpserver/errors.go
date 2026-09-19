package httpserver

import (
	"errors"
	"log/slog"
	"net/http"

	"backend-challenge-go/internal/app"
	"backend-challenge-go/internal/platform/postgres"
)

// writeAppError maps an internal/app error to the HTTP status and body the
// spec requires callers be able to distinguish: invalid input, conflict,
// business rejection, pending processing, and transient unavailability.
func writeAppError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, app.ErrWalletConflict):
		writeError(w, http.StatusConflict, "WALLET_ALREADY_EXISTS", err.Error())
	case errors.Is(err, app.ErrWalletNotFound), errors.Is(err, app.ErrWagerTransactionNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.Is(err, app.ErrWalletPlayerMismatch):
		writeError(w, http.StatusForbidden, "WALLET_PLAYER_MISMATCH", err.Error())
	case errors.Is(err, app.ErrIdempotencyKeyConflict), errors.Is(err, app.ErrExternalTransactionKeyMismatch):
		writeError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT", err.Error())
	case errors.Is(err, app.ErrTooManyConflicts):
		writeError(w, http.StatusServiceUnavailable, "TOO_MANY_CONFLICTS", err.Error())
	case errors.Is(err, postgres.ErrWalletNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
	case postgres.IsUnavailable(err):
		// Transient infrastructure failure (Postgres unreachable, dial/query
		// timeout): 503, not 500 - it tells the caller retrying may help,
		// which a generic 500 doesn't. The real error (which can contain a
		// DSN, hostname, or internal path) is logged server-side only,
		// never echoed back to the client.
		slog.Error("transient_infrastructure_failure", "error", err)
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "a dependency is temporarily unavailable; retry later")
	default:
		slog.Error("unhandled_internal_error", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "an unexpected error occurred")
	}
}
