package wagertransaction

import (
	"time"

	"github.com/google/uuid"

	"backend-challenge-go/internal/domain/money"
)

// externalKindRules captures, per Kind, whether the movement amount must be
// strictly positive (all external kinds except LOSS, which must be exactly
// zero) and whether referenceExternalTransactionId is required, optional,
// or must be absent. This table is the direct translation of the
// operations table in section 7 of the spec.
type referenceRule int

const (
	referenceForbidden referenceRule = iota
	referenceOptional
	referenceRequired
)

var externalKinds = map[Kind]referenceRule{
	KindBet:      referenceForbidden,
	KindWin:      referenceOptional,
	KindLoss:     referenceForbidden,
	KindRefund:   referenceRequired,
	KindRollback: referenceRequired,
}

// NewExternalParams groups NewExternal's inputs; it exists purely to keep
// the constructor call sites readable given the field count the spec
// requires for an external operation.
type NewExternalParams struct {
	ID                             uuid.UUID
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PayloadHash                    string
	WalletID                       uuid.UUID
	PlayerID                       uuid.UUID
	RoundID                        string
	GameID                         string
	Kind                           Kind
	Money                          money.Money
	ReferenceExternalTransactionID string
	Now                            time.Time
}

// NewExternal validates and builds a fresh, PENDING external transaction
// (BET, WIN, LOSS, REFUND or ROLLBACK). OPENING is rejected here on
// purpose: it is a distinct, internal-only origin with its own
// constructor, and the spec requires it to never be accepted from HTTP or
// SQS input.
func NewExternal(p NewExternalParams) (WagerTransaction, error) {
	rule, isExternalKind := externalKinds[p.Kind]
	if !isExternalKind {
		return WagerTransaction{}, ErrInvalidKind
	}

	if p.ID == uuid.Nil {
		return WagerTransaction{}, ErrInvalidTransactionID
	}
	if p.ProviderID == "" {
		return WagerTransaction{}, ErrMissingProviderID
	}
	if p.ExternalTransactionID == "" {
		return WagerTransaction{}, ErrMissingExternalID
	}
	if p.IdempotencyKey == "" {
		return WagerTransaction{}, ErrMissingIdempotencyKey
	}
	if p.PayloadHash == "" {
		return WagerTransaction{}, ErrMissingPayloadHash
	}
	if p.WalletID == uuid.Nil {
		return WagerTransaction{}, ErrInvalidWalletID
	}
	if p.PlayerID == uuid.Nil {
		return WagerTransaction{}, ErrInvalidPlayerID
	}
	if p.RoundID == "" {
		return WagerTransaction{}, ErrMissingRoundID
	}
	if p.GameID == "" {
		return WagerTransaction{}, ErrMissingGameID
	}

	if p.Kind == KindLoss {
		if !p.Money.IsZero() {
			return WagerTransaction{}, ErrNonZeroLossAmount
		}
	} else if !p.Money.IsPositive() {
		return WagerTransaction{}, ErrNonPositiveAmount
	}

	switch rule {
	case referenceRequired:
		if p.ReferenceExternalTransactionID == "" {
			return WagerTransaction{}, ErrMissingReference
		}
	case referenceForbidden:
		if p.ReferenceExternalTransactionID != "" {
			return WagerTransaction{}, ErrUnexpectedReference
		}
	case referenceOptional:
		// no constraint
	}

	return WagerTransaction{
		id:                    p.ID,
		externalTransactionID: p.ExternalTransactionID,
		providerID:            p.ProviderID,
		idempotencyKey:        p.IdempotencyKey,
		payloadHash:           p.PayloadHash,
		walletID:              p.WalletID,
		playerID:              p.PlayerID,
		roundID:               p.RoundID,
		gameID:                p.GameID,
		kind:                  p.Kind,
		money:                 p.Money,
		referenceExternalID:   p.ReferenceExternalTransactionID,
		status:                StatusPending,
		createdAt:             p.Now,
		updatedAt:             p.Now,
	}, nil
}

// NewOpening builds an already-PROCESSED internal OPENING transaction. It
// only makes sense for a strictly positive amount: a zero initial balance
// produces no OPENING transaction, ledger entry, or event at all (the
// application layer simply never calls this for a zero balance).
// Provider/external id/key/hash/round/game/reference don't apply to this
// origin and are left at their zero value.
func NewOpening(id, walletID, playerID uuid.UUID, amount money.Money, now time.Time) (WagerTransaction, error) {
	if id == uuid.Nil {
		return WagerTransaction{}, ErrInvalidTransactionID
	}
	if walletID == uuid.Nil {
		return WagerTransaction{}, ErrInvalidWalletID
	}
	if playerID == uuid.Nil {
		return WagerTransaction{}, ErrInvalidPlayerID
	}
	if !amount.IsPositive() {
		return WagerTransaction{}, ErrNonPositiveAmount
	}

	return WagerTransaction{
		id:            id,
		walletID:      walletID,
		playerID:      playerID,
		kind:          KindOpening,
		money:         amount,
		status:        StatusProcessed,
		resultBalance: &amount,
		createdAt:     now,
		updatedAt:     now,
	}, nil
}

// RehydrateParams groups Rehydrate's inputs, mirroring exactly what a
// repository reads back from storage - no field is derived or recomputed.
type RehydrateParams struct {
	ID                             uuid.UUID
	ExternalTransactionID          string
	ProviderID                     string
	IdempotencyKey                 string
	PayloadHash                    string
	WalletID                       uuid.UUID
	PlayerID                       uuid.UUID
	RoundID                        string
	GameID                         string
	Kind                           Kind
	Money                          money.Money
	ReferenceExternalTransactionID string
	ResolvedReferenceTransactionID uuid.UUID
	Status                         Status
	FailureCode                    string
	ResultBalance                  *money.Money
	Attempts                       int
	NextRetryAt                    *time.Time
	CreatedAt                      time.Time
	UpdatedAt                      time.Time
}

// Rehydrate reconstructs a WagerTransaction exactly as persisted, without
// reapplying any transition or emitting any event.
func Rehydrate(p RehydrateParams) (WagerTransaction, error) {
	if p.ID == uuid.Nil {
		return WagerTransaction{}, ErrInvalidTransactionID
	}
	if p.WalletID == uuid.Nil {
		return WagerTransaction{}, ErrInvalidWalletID
	}
	if p.PlayerID == uuid.Nil {
		return WagerTransaction{}, ErrInvalidPlayerID
	}
	return WagerTransaction{
		id:                    p.ID,
		externalTransactionID: p.ExternalTransactionID,
		providerID:            p.ProviderID,
		idempotencyKey:        p.IdempotencyKey,
		payloadHash:           p.PayloadHash,
		walletID:              p.WalletID,
		playerID:              p.PlayerID,
		roundID:               p.RoundID,
		gameID:                p.GameID,
		kind:                  p.Kind,
		money:                 p.Money,
		referenceExternalID:   p.ReferenceExternalTransactionID,
		resolvedReferenceID:   p.ResolvedReferenceTransactionID,
		status:                p.Status,
		failureCode:           p.FailureCode,
		resultBalance:         p.ResultBalance,
		attempts:              p.Attempts,
		nextRetryAt:           p.NextRetryAt,
		createdAt:             p.CreatedAt,
		updatedAt:             p.UpdatedAt,
	}, nil
}
