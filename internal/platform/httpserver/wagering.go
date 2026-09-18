package httpserver

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"backend-challenge-go/internal/app"
	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wagertransaction"
)

type submitWagerTransactionRequest struct {
	ProviderID                     string      `json:"providerId"`
	ExternalTransactionID          string      `json:"externalTransactionId"`
	PlayerID                       string      `json:"playerId"`
	WalletID                       string      `json:"walletId"`
	RoundID                        string      `json:"roundId"`
	GameID                         string      `json:"gameId"`
	Kind                           string      `json:"kind"`
	Money                          money.Money `json:"money"`
	ReferenceExternalTransactionID string      `json:"referenceExternalTransactionId,omitempty"`
}

type submitWagerTransactionResponse struct {
	TransactionID    string       `json:"transactionId"`
	Status           string       `json:"status"`
	Balance          *money.Money `json:"balance,omitempty"`
	IdempotentReplay bool         `json:"idempotentReplay"`
	FailureCode      string       `json:"failureCode,omitempty"`
}

func (s *Server) handleSubmitWagerTransaction(w http.ResponseWriter, r *http.Request) {
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "MISSING_IDEMPOTENCY_KEY", "the Idempotency-Key header is required")
		return
	}

	var req submitWagerTransactionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	playerID, err := uuid.Parse(req.PlayerID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PLAYER_ID", "playerId must be a UUID")
		return
	}
	walletID, err := uuid.Parse(req.WalletID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_WALLET_ID", "walletId must be a UUID")
		return
	}

	// The spec is explicit: the authenticated identity determines the
	// authorized providerId, not the request body. A provider's token can
	// only ever act as itself.
	claims, _ := claimsFromContext(r.Context())
	if req.ProviderID != claims.ClientID {
		writeError(w, http.StatusForbidden, "PROVIDER_MISMATCH", "providerId does not match the authenticated client")
		return
	}

	result, err := s.uc.SubmitWagerTransaction(r.Context(), app.SubmitWagerTransactionCommand{
		ProviderID:                     req.ProviderID,
		ExternalTransactionID:          req.ExternalTransactionID,
		IdempotencyKey:                 idempotencyKey,
		PlayerID:                       playerID,
		WalletID:                       walletID,
		RoundID:                        req.RoundID,
		GameID:                         req.GameID,
		Kind:                           wagertransaction.Kind(req.Kind),
		Money:                          req.Money,
		ReferenceExternalTransactionID: req.ReferenceExternalTransactionID,
	})
	if err != nil {
		writeAppError(w, err)
		return
	}

	resp := submitWagerTransactionResponse{
		TransactionID:    result.TransactionID.String(),
		Status:           string(result.Status),
		IdempotentReplay: result.IdempotentReplay,
		FailureCode:      result.FailureCode,
	}
	if result.HasBalance {
		resp.Balance = &result.Balance
	}
	writeJSON(w, statusCodeForSubmitResult(result), resp)
}

// statusCodeForSubmitResult picks the HTTP status the spec requires
// distinguishing: 201 for a brand-new completed operation, 200 for a
// replay or a definitive business rejection (both are "the request was
// understood and fully handled"), 202 for a reversal still waiting on its
// reference.
func statusCodeForSubmitResult(r app.SubmitWagerTransactionResult) int {
	if r.IdempotentReplay {
		return http.StatusOK
	}
	switch r.Status {
	case wagertransaction.StatusProcessed:
		return http.StatusCreated
	case wagertransaction.StatusPendingReference:
		return http.StatusAccepted
	default: // REJECTED, FAILED
		return http.StatusOK
	}
}

func (s *Server) handleGetWagerTransaction(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("transactionId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TRANSACTION_ID", "transactionId must be a UUID")
		return
	}
	t, err := s.uc.GetWagerTransaction(r.Context(), id)
	if err != nil {
		writeAppError(w, err)
		return
	}
	// Isolation holds for replays too: a provider looking up a transaction
	// by internal id must not see another provider's data, so this check
	// runs even though the row was already found.
	claims, _ := claimsFromContext(r.Context())
	if t.ProviderID() != claims.ClientID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "wager transaction not found")
		return
	}
	writeJSON(w, http.StatusOK, toWagerTransactionResponse(t))
}

func (s *Server) handleGetWagerTransactionByExternalID(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("providerId")
	claims, _ := claimsFromContext(r.Context())
	if providerID != claims.ClientID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "wager transaction not found")
		return
	}
	externalID := r.PathValue("externalTransactionId")
	t, err := s.uc.GetWagerTransactionByProviderAndExternalID(r.Context(), providerID, externalID)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toWagerTransactionResponse(t))
}

type wagerTransactionResponse struct {
	TransactionID         string       `json:"transactionId"`
	ExternalTransactionID string       `json:"externalTransactionId,omitempty"`
	ProviderID            string       `json:"providerId,omitempty"`
	Kind                  string       `json:"kind"`
	Status                string       `json:"status"`
	Money                 money.Money  `json:"money"`
	Balance               *money.Money `json:"balance,omitempty"`
	FailureCode           string       `json:"failureCode,omitempty"`
}

func toWagerTransactionResponse(t wagertransaction.WagerTransaction) wagerTransactionResponse {
	resp := wagerTransactionResponse{
		TransactionID:         t.ID().String(),
		ExternalTransactionID: t.ExternalTransactionID(),
		ProviderID:            t.ProviderID(),
		Kind:                  string(t.Kind()),
		Status:                string(t.Status()),
		Money:                 t.Money(),
		FailureCode:           t.FailureCode(),
	}
	if balance, ok := t.ResultBalance(); ok {
		resp.Balance = &balance
	}
	return resp
}
