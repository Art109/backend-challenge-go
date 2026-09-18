// Package events defines the concrete integration events published through
// the transactional outbox, and the envelope that wraps them. Domain code
// only ever constructs these types; nothing here knows about SQS, an HTTP
// client, or the outbox table - that wiring lives in internal/platform.
package events

import (
	"errors"
	"time"

	"github.com/google/uuid"

	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wagertransaction"
	"backend-challenge-go/internal/domain/wallet"
)

var (
	ErrInvalidEventID       = errors.New("events: invalid event id")
	ErrNilEvent             = errors.New("events: event payload is nil")
	ErrInvalidAggregateID   = errors.New("events: invalid aggregate id")
	ErrInvalidCorrelationID = errors.New("events: invalid correlation id")
)

// Event is implemented by every concrete event payload. Type and Version
// are fixed by each payload's own constructor (per the spec: "Tipo e
// versão devem ser definidos pelo construtor do evento"), not chosen by
// whoever builds the envelope.
type Event interface {
	EventType() string
	EventVersion() int
	AggregateID() uuid.UUID
}

// Envelope is the outbox wire format: a typed payload plus the tracing and
// versioning metadata every event carries regardless of kind. Its JSON tags
// are the actual wire contract published to SQS - this is what a consumer
// downstream of the outbox worker receives, verbatim.
type Envelope struct {
	EventID       uuid.UUID  `json:"eventId"`
	EventType     string     `json:"eventType"`
	AggregateID   uuid.UUID  `json:"aggregateId"`
	CorrelationID uuid.UUID  `json:"correlationId"`
	CausationID   *uuid.UUID `json:"causationId,omitempty"`
	OccurredAt    time.Time  `json:"occurredAt"`
	Version       int        `json:"version"`
	Data          Event      `json:"data"`
}

// NewEnvelope wraps a concrete event payload. eventID is supplied by the
// caller (not generated here) so that a retried publish can be constructed
// deterministically with the same id the spec requires to be preserved
// across republication attempts.
func NewEnvelope(eventID uuid.UUID, payload Event, correlationID uuid.UUID, causationID *uuid.UUID, occurredAt time.Time) (Envelope, error) {
	if eventID == uuid.Nil {
		return Envelope{}, ErrInvalidEventID
	}
	if payload == nil {
		return Envelope{}, ErrNilEvent
	}
	if payload.AggregateID() == uuid.Nil {
		return Envelope{}, ErrInvalidAggregateID
	}
	if correlationID == uuid.Nil {
		return Envelope{}, ErrInvalidCorrelationID
	}
	return Envelope{
		EventID:       eventID,
		EventType:     payload.EventType(),
		AggregateID:   payload.AggregateID(),
		CorrelationID: correlationID,
		CausationID:   causationID,
		OccurredAt:    occurredAt,
		Version:       payload.EventVersion(),
		Data:          payload,
	}, nil
}

// --- WagerTransactionProcessed: successful completion, including LOSS. ---

type WagerTransactionProcessedData struct {
	TransactionID         uuid.UUID
	ExternalTransactionID string
	ProviderID            string
	Kind                  wagertransaction.Kind
	Money                 money.Money
	WalletID              uuid.UUID
	PlayerID              uuid.UUID
}

func (d WagerTransactionProcessedData) EventType() string      { return "WagerTransactionProcessed" }
func (d WagerTransactionProcessedData) EventVersion() int      { return 1 }
func (d WagerTransactionProcessedData) AggregateID() uuid.UUID { return d.TransactionID }

// --- WagerTransactionRejected: definitive business rejection. ---

type WagerTransactionRejectedData struct {
	TransactionID         uuid.UUID
	ExternalTransactionID string
	ProviderID            string
	Kind                  wagertransaction.Kind
	FailureCode           string
	WalletID              uuid.UUID
	PlayerID              uuid.UUID
}

func (d WagerTransactionRejectedData) EventType() string      { return "WagerTransactionRejected" }
func (d WagerTransactionRejectedData) EventVersion() int      { return 1 }
func (d WagerTransactionRejectedData) AggregateID() uuid.UUID { return d.TransactionID }

// --- WalletBalanceChanged: any effective balance change. ---

type WalletBalanceChangedData struct {
	WalletID      uuid.UUID
	TransactionID uuid.UUID
	Direction     wallet.Direction
	Money         money.Money
	BalanceBefore money.Money
	BalanceAfter  money.Money
	WalletVersion int64
}

func (d WalletBalanceChangedData) EventType() string      { return "WalletBalanceChanged" }
func (d WalletBalanceChangedData) EventVersion() int      { return 1 }
func (d WalletBalanceChangedData) AggregateID() uuid.UUID { return d.WalletID }

// --- WagerTransactionPendingReference: waiting on an unresolved reference. ---

type WagerTransactionPendingReferenceData struct {
	TransactionID                  uuid.UUID
	ExternalTransactionID          string
	ProviderID                     string
	Kind                           wagertransaction.Kind
	ReferenceExternalTransactionID string
	WalletID                       uuid.UUID
	PlayerID                       uuid.UUID
}

func (d WagerTransactionPendingReferenceData) EventType() string {
	return "WagerTransactionPendingReference"
}
func (d WagerTransactionPendingReferenceData) EventVersion() int      { return 1 }
func (d WagerTransactionPendingReferenceData) AggregateID() uuid.UUID { return d.TransactionID }
