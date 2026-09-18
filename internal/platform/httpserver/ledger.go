package httpserver

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/platform/postgres"
)

const defaultLedgerPageLimit = 50

type ledgerEntryResponse struct {
	ID            string      `json:"id"`
	WalletID      string      `json:"walletId"`
	TransactionID string      `json:"transactionId"`
	Direction     string      `json:"direction"`
	Amount        money.Money `json:"amount"`
	BalanceBefore money.Money `json:"balanceBefore"`
	BalanceAfter  money.Money `json:"balanceAfter"`
	CreatedAt     time.Time   `json:"createdAt"`
}

type ledgerPageResponse struct {
	Entries    []ledgerEntryResponse `json:"entries"`
	NextCursor string                `json:"nextCursor,omitempty"`
}

func (s *Server) handleGetWalletLedger(w http.ResponseWriter, r *http.Request) {
	walletID, err := uuid.Parse(r.PathValue("walletId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_WALLET_ID", "walletId must be a UUID")
		return
	}

	limit := defaultLedgerPageLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 200 {
			writeError(w, http.StatusBadRequest, "INVALID_LIMIT", "limit must be an integer between 1 and 200")
			return
		}
		limit = parsed
	}

	var cursor *postgres.LedgerCursor
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		c, err := decodeLedgerCursor(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "cursor is malformed")
			return
		}
		cursor = &c
	}

	entries, hasMore, err := s.uc.ListLedgerEntries(r.Context(), walletID, cursor, limit)
	if err != nil {
		writeAppError(w, err)
		return
	}

	resp := ledgerPageResponse{Entries: make([]ledgerEntryResponse, len(entries))}
	for i, e := range entries {
		resp.Entries[i] = ledgerEntryResponse{
			ID:            e.ID().String(),
			WalletID:      e.WalletID().String(),
			TransactionID: e.TransactionID().String(),
			Direction:     string(e.Direction()),
			Amount:        e.Amount(),
			BalanceBefore: e.BalanceBefore(),
			BalanceAfter:  e.BalanceAfter(),
			CreatedAt:     e.CreatedAt(),
		}
	}
	if hasMore && len(entries) > 0 {
		last := entries[len(entries)-1]
		resp.NextCursor = encodeLedgerCursor(postgres.LedgerCursor{CreatedAt: last.CreatedAt(), ID: last.ID()})
	}
	writeJSON(w, http.StatusOK, resp)
}

// encodeLedgerCursor/decodeLedgerCursor make the cursor opaque to callers
// (an implementation detail, not a contract) while keeping it a stable,
// simple total order: created_at first, id as a tie-breaker for entries
// sharing a timestamp.
func encodeLedgerCursor(c postgres.LedgerCursor) string {
	raw := fmt.Sprintf("%d|%s", c.CreatedAt.UnixNano(), c.ID.String())
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeLedgerCursor(s string) (postgres.LedgerCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return postgres.LedgerCursor{}, err
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return postgres.LedgerCursor{}, fmt.Errorf("httpserver: malformed cursor")
	}
	nanos, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return postgres.LedgerCursor{}, err
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return postgres.LedgerCursor{}, err
	}
	return postgres.LedgerCursor{CreatedAt: time.Unix(0, nanos).UTC(), ID: id}, nil
}

type reconciliationResponse struct {
	WalletID          string      `json:"walletId"`
	StoredBalance     money.Money `json:"storedBalance"`
	CalculatedBalance money.Money `json:"calculatedBalance"`
	Difference        money.Money `json:"difference"`
	Consistent        bool        `json:"consistent"`
	CheckedEntries    int         `json:"checkedEntries"`
}

func (s *Server) handleReconcileWallet(w http.ResponseWriter, r *http.Request) {
	walletID, err := uuid.Parse(r.PathValue("walletId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_WALLET_ID", "walletId must be a UUID")
		return
	}
	result, err := s.uc.Reconcile(r.Context(), walletID)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, reconciliationResponse{
		WalletID:          result.WalletID.String(),
		StoredBalance:     result.StoredBalance,
		CalculatedBalance: result.CalculatedBalance,
		Difference:        result.Difference,
		Consistent:        result.Consistent,
		CheckedEntries:    result.CheckedEntries,
	})
}
