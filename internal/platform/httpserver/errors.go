package httpserver

import (
	"errors"
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
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
	}
}
