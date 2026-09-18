package httpserver

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"backend-challenge-go/internal/app"
	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wallet"
)

type openWalletRequest struct {
	PlayerID       string      `json:"playerId"`
	InitialBalance money.Money `json:"initialBalance"`
}

type walletResponse struct {
	ID       string      `json:"id"`
	PlayerID string      `json:"playerId"`
	Balance  money.Money `json:"balance"`
	Version  int64       `json:"version"`
}

func toWalletResponse(w wallet.Wallet) walletResponse {
	return walletResponse{
		ID:       w.ID().String(),
		PlayerID: w.PlayerID().String(),
		Balance:  w.Balance(),
		Version:  w.Version(),
	}
}

func (s *Server) handleOpenWallet(w http.ResponseWriter, r *http.Request) {
	var req openWalletRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	playerID, err := uuid.Parse(req.PlayerID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PLAYER_ID", "playerId must be a UUID")
		return
	}

	result, err := s.uc.OpenWallet(r.Context(), app.OpenWalletCommand{
		PlayerID:       playerID,
		InitialBalance: req.InitialBalance,
	})
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toWalletResponse(result.Wallet))
}

func (s *Server) handleGetWallet(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("walletId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_WALLET_ID", "walletId must be a UUID")
		return
	}
	result, err := s.uc.GetWallet(r.Context(), id)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toWalletResponse(result))
}
