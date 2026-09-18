// Package wagertransaction models a single wager operation - either an
// external one (BET, WIN, LOSS, REFUND, ROLLBACK) submitted by a provider
// over HTTP or SQS, or the internal OPENING operation used to fund a wallet
// when it is created.
package wagertransaction

import (
	"errors"
	"time"

	"github.com/google/uuid"

	"backend-challenge-go/internal/domain/money"
)

type Kind string

const (
	KindOpening  Kind = "OPENING"
	KindBet      Kind = "BET"
	KindWin      Kind = "WIN"
	KindLoss     Kind = "LOSS"
	KindRefund   Kind = "REFUND"
	KindRollback Kind = "ROLLBACK"
)

type Status string

const (
	StatusPending          Status = "PENDING"
	StatusPendingReference Status = "PENDING_REFERENCE"
	StatusProcessed        Status = "PROCESSED"
	StatusRejected         Status = "REJECTED"
	StatusFailed           Status = "FAILED"
)

// IsTerminal reports whether a transaction in this status may still
// transition (PENDING and PENDING_REFERENCE) or is final.
func (s Status) IsTerminal() bool {
	return s == StatusProcessed || s == StatusRejected || s == StatusFailed
}

// Stable, documented failure codes. The spec requires every rejection to
// carry one, and specifically requires a *different* code for "reversal
// would overdraw" than for "bet without enough balance" - callers must not
// reuse FailureCodeInsufficientBalance for a REFUND/ROLLBACK rejection.
const (
	FailureCodeInsufficientBalance            = "INSUFFICIENT_BALANCE"
	FailureCodeInsufficientBalanceForReversal = "INSUFFICIENT_BALANCE_FOR_REVERSAL"
	FailureCodeReferenceNotFound              = "REFERENCE_NOT_FOUND"
	FailureCodeReferenceMismatch              = "REFERENCE_MISMATCH"
	FailureCodeReferenceNotProcessable        = "REFERENCE_NOT_PROCESSABLE"
	FailureCodeDuplicateReversal              = "DUPLICATE_REVERSAL"
	FailureCodeInfrastructureUnavailable      = "INFRASTRUCTURE_UNAVAILABLE"
)

var (
	ErrInvalidTransactionID  = errors.New("wagertransaction: invalid transaction id")
	ErrInvalidWalletID       = errors.New("wagertransaction: invalid wallet id")
	ErrInvalidPlayerID       = errors.New("wagertransaction: invalid player id")
	ErrMissingProviderID     = errors.New("wagertransaction: providerId is required")
	ErrMissingExternalID     = errors.New("wagertransaction: externalTransactionId is required")
	ErrMissingIdempotencyKey = errors.New("wagertransaction: idempotencyKey is required")
	ErrMissingPayloadHash    = errors.New("wagertransaction: payloadHash is required")
	ErrMissingRoundID        = errors.New("wagertransaction: roundId is required")
	ErrMissingGameID         = errors.New("wagertransaction: gameId is required")
	ErrInvalidKind           = errors.New("wagertransaction: invalid kind for this constructor")
	ErrNonPositiveAmount     = errors.New("wagertransaction: amount must be positive for this kind")
	ErrNonZeroLossAmount     = errors.New("wagertransaction: LOSS requires money.amount == 0.00")
	ErrMissingReference      = errors.New("wagertransaction: referenceExternalTransactionId is required for this kind")
	ErrUnexpectedReference   = errors.New("wagertransaction: referenceExternalTransactionId is not applicable for this kind")
	ErrTerminalTransaction   = errors.New("wagertransaction: transaction is terminal and cannot transition")
	ErrInvalidTransition     = errors.New("wagertransaction: transition not allowed from current status")
	ErrMissingFailureCode    = errors.New("wagertransaction: failureCode is required")
)

// WagerTransaction is the aggregate for a single wager operation. Fields
// that don't apply to a given Kind (e.g. providerID for OPENING) are left
// at their zero value; NewExternal and NewOpening only ever populate the
// fields relevant to what they construct.
type WagerTransaction struct {
	id                    uuid.UUID
	externalTransactionID string
	providerID            string
	idempotencyKey        string
	payloadHash           string
	walletID              uuid.UUID
	playerID              uuid.UUID
	roundID               string
	gameID                string
	kind                  Kind
	money                 money.Money
	referenceExternalID   string
	resolvedReferenceID   uuid.UUID
	status                Status
	failureCode           string
	resultBalance         *money.Money
	attempts              int
	nextRetryAt           *time.Time
	createdAt             time.Time
	updatedAt             time.Time
}

func (t WagerTransaction) ID() uuid.UUID                             { return t.id }
func (t WagerTransaction) ExternalTransactionID() string             { return t.externalTransactionID }
func (t WagerTransaction) ProviderID() string                        { return t.providerID }
func (t WagerTransaction) IdempotencyKey() string                    { return t.idempotencyKey }
func (t WagerTransaction) PayloadHash() string                       { return t.payloadHash }
func (t WagerTransaction) WalletID() uuid.UUID                       { return t.walletID }
func (t WagerTransaction) PlayerID() uuid.UUID                       { return t.playerID }
func (t WagerTransaction) RoundID() string                           { return t.roundID }
func (t WagerTransaction) GameID() string                            { return t.gameID }
func (t WagerTransaction) Kind() Kind                                { return t.kind }
func (t WagerTransaction) Money() money.Money                        { return t.money }
func (t WagerTransaction) ReferenceExternalTransactionID() string    { return t.referenceExternalID }
func (t WagerTransaction) ResolvedReferenceTransactionID() uuid.UUID { return t.resolvedReferenceID }
func (t WagerTransaction) Status() Status                            { return t.status }
func (t WagerTransaction) FailureCode() string                       { return t.failureCode }
func (t WagerTransaction) Attempts() int                             { return t.attempts }
func (t WagerTransaction) NextRetryAt() *time.Time                   { return t.nextRetryAt }
func (t WagerTransaction) CreatedAt() time.Time                      { return t.createdAt }
func (t WagerTransaction) UpdatedAt() time.Time                      { return t.updatedAt }

// ResultBalance is the wallet balance observed at the moment this
// transaction was processed, persisted so an idempotent replay can return
// it unchanged even if the wallet has since moved further. Nil until the
// transaction reaches PROCESSED.
func (t WagerTransaction) ResultBalance() (money.Money, bool) {
	if t.resultBalance == nil {
		return money.Money{}, false
	}
	return *t.resultBalance, true
}

func (t WagerTransaction) ensureNotTerminal() error {
	if t.status.IsTerminal() {
		return ErrTerminalTransaction
	}
	return nil
}
