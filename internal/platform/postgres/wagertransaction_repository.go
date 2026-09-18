package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wagertransaction"
)

var (
	// ErrWagerTransactionNotFound is returned by lookups that find no row.
	ErrWagerTransactionNotFound = errors.New("postgres: wager transaction not found")
	// ErrIdempotencyConflict surfaces the (provider_id, external_transaction_id)
	// or (provider_id, idempotency_key) unique constraints: an Insert tried to
	// create a second row for an operation that already has one.
	ErrIdempotencyConflict = errors.New("postgres: an operation with this provider/external id or idempotency key already exists")
	// ErrStatusConflict is WagerTransaction's equivalent of
	// ErrVersionConflict: the row's status changed since the caller read it
	// (e.g. a second worker already transitioned it), so this update didn't
	// apply to anything and the caller must re-read.
	ErrStatusConflict = errors.New("postgres: wager transaction status changed since it was read")
)

type WagerTransactionRepository struct{}

func NewWagerTransactionRepository() *WagerTransactionRepository {
	return &WagerTransactionRepository{}
}

// Insert persists a brand-new transaction (PENDING for an external
// operation, or already-PROCESSED for an OPENING). Call it in the same
// db transaction as any wallet/ledger changes it implies.
func (r *WagerTransactionRepository) Insert(ctx context.Context, q Querier, t wagertransaction.WagerTransaction) error {
	resultBalance, hasResultBalance := t.ResultBalance()

	_, err := q.Exec(ctx, `
		INSERT INTO wager_transactions (
			id, kind, provider_id, external_transaction_id, idempotency_key, payload_hash,
			wallet_id, player_id, round_id, game_id, amount_minor_units, currency,
			reference_external_transaction_id, resolved_reference_transaction_id,
			status, failure_code, result_balance_minor_units, attempts, next_retry_at,
			created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21
		)
	`,
		t.ID(), string(t.Kind()), nullString(t.ProviderID()), nullString(t.ExternalTransactionID()),
		nullString(t.IdempotencyKey()), nullString(t.PayloadHash()),
		t.WalletID(), t.PlayerID(), nullString(t.RoundID()), nullString(t.GameID()),
		t.Money().MinorUnits(), t.Money().Currency(),
		nullString(t.ReferenceExternalTransactionID()), nullUUID(t.ResolvedReferenceTransactionID()),
		string(t.Status()), nullString(t.FailureCode()), nullResultBalance(resultBalance, hasResultBalance),
		t.Attempts(), nullTime(t.NextRetryAt()),
		t.CreatedAt(), t.UpdatedAt(),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrIdempotencyConflict
		}
		return fmt.Errorf("postgres: insert wager transaction: %w", err)
	}
	return nil
}

// UpdateStatus persists a transitioned WagerTransaction (as produced by one
// of its MarkX methods) with a compare-and-swap on status: the UPDATE only
// matches if the row's status in the database still equals
// expectedPreviousStatus. Zero rows affected means another writer already
// transitioned it - ErrStatusConflict tells the caller to re-read.
func (r *WagerTransactionRepository) UpdateStatus(ctx context.Context, q Querier, t wagertransaction.WagerTransaction, expectedPreviousStatus wagertransaction.Status) error {
	resultBalance, hasResultBalance := t.ResultBalance()

	tag, err := q.Exec(ctx, `
		UPDATE wager_transactions
		SET status = $1,
		    failure_code = $2,
		    result_balance_minor_units = $3,
		    resolved_reference_transaction_id = $4,
		    attempts = $5,
		    next_retry_at = $6,
		    updated_at = $7
		WHERE id = $8 AND status = $9
	`,
		string(t.Status()), nullString(t.FailureCode()), nullResultBalance(resultBalance, hasResultBalance),
		nullUUID(t.ResolvedReferenceTransactionID()), t.Attempts(), nullTime(t.NextRetryAt()), t.UpdatedAt(),
		t.ID(), string(expectedPreviousStatus),
	)
	if err != nil {
		return fmt.Errorf("postgres: update wager transaction status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrStatusConflict
	}
	return nil
}

func (r *WagerTransactionRepository) GetByID(ctx context.Context, q Querier, id uuid.UUID) (wagertransaction.WagerTransaction, error) {
	row := q.QueryRow(ctx, selectWagerTransactionSQL+" WHERE id = $1", id)
	return scanWagerTransaction(row)
}

// GetByProviderAndExternalID is the lookup used to detect an idempotent
// replay of the same financial operation.
func (r *WagerTransactionRepository) GetByProviderAndExternalID(ctx context.Context, q Querier, providerID, externalTransactionID string) (wagertransaction.WagerTransaction, bool, error) {
	row := q.QueryRow(ctx, selectWagerTransactionSQL+" WHERE provider_id = $1 AND external_transaction_id = $2", providerID, externalTransactionID)
	return scanOptionalWagerTransaction(row)
}

// GetByProviderAndIdempotencyKey backs the HTTP Idempotency-Key contract:
// same key + same content -> replay; same key + different content -> conflict.
func (r *WagerTransactionRepository) GetByProviderAndIdempotencyKey(ctx context.Context, q Querier, providerID, idempotencyKey string) (wagertransaction.WagerTransaction, bool, error) {
	row := q.QueryRow(ctx, selectWagerTransactionSQL+" WHERE provider_id = $1 AND idempotency_key = $2", providerID, idempotencyKey)
	return scanOptionalWagerTransaction(row)
}

// ExistsProcessedReversal reports whether a PROCESSED transaction of kind
// already resolved its reference to referenceTransactionID - the check
// behind "a reference may not receive two successful reversals of the same
// type" (a BET can have one PROCESSED REFUND and, separately, one PROCESSED
// ROLLBACK, but never two of the same kind).
func (r *WagerTransactionRepository) ExistsProcessedReversal(ctx context.Context, q Querier, referenceTransactionID uuid.UUID, kind wagertransaction.Kind) (bool, error) {
	var exists bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM wager_transactions
			WHERE resolved_reference_transaction_id = $1 AND kind = $2 AND status = 'PROCESSED'
		)
	`, referenceTransactionID, string(kind)).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("postgres: check existing reversal: %w", err)
	}
	return exists, nil
}

// ListDuePendingReference returns PENDING_REFERENCE rows whose next retry
// is due, for the reference-retry worker to pick up (see ARCHITECTURE.md
// for the backoff/TTL policy).
func (r *WagerTransactionRepository) ListDuePendingReference(ctx context.Context, q Querier, now time.Time, limit int) ([]wagertransaction.WagerTransaction, error) {
	rows, err := q.Query(ctx, selectWagerTransactionSQL+`
		WHERE status = 'PENDING_REFERENCE' AND (next_retry_at IS NULL OR next_retry_at <= $1)
		ORDER BY next_retry_at NULLS FIRST
		LIMIT $2
	`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list due pending-reference transactions: %w", err)
	}
	defer rows.Close()

	var out []wagertransaction.WagerTransaction
	for rows.Next() {
		t, err := scanWagerTransactionRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate due pending-reference transactions: %w", err)
	}
	return out, nil
}

const selectWagerTransactionSQL = `
	SELECT id, kind, provider_id, external_transaction_id, idempotency_key, payload_hash,
	       wallet_id, player_id, round_id, game_id, amount_minor_units, currency,
	       reference_external_transaction_id, resolved_reference_transaction_id,
	       status, failure_code, result_balance_minor_units, attempts, next_retry_at,
	       created_at, updated_at
	FROM wager_transactions
`

// rowScanner is satisfied by both pgx.Row (QueryRow) and pgx.Rows (Query),
// so the same field-mapping code backs both single-row and list lookups.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanOptionalWagerTransaction(row rowScanner) (wagertransaction.WagerTransaction, bool, error) {
	t, err := scanWagerTransaction(row)
	if errors.Is(err, ErrWagerTransactionNotFound) {
		return wagertransaction.WagerTransaction{}, false, nil
	}
	if err != nil {
		return wagertransaction.WagerTransaction{}, false, err
	}
	return t, true, nil
}

func scanWagerTransaction(row rowScanner) (wagertransaction.WagerTransaction, error) {
	return scanWagerTransactionRow(row)
}

func scanWagerTransactionRow(row rowScanner) (wagertransaction.WagerTransaction, error) {
	var (
		id, walletID, playerID            uuid.UUID
		kind, status, currency            string
		providerID, externalTransactionID sql.NullString
		idempotencyKey, payloadHash       sql.NullString
		roundID, gameID                   sql.NullString
		referenceExternalTransactionID    sql.NullString
		resolvedReferenceTransactionID    uuid.NullUUID
		failureCode                       sql.NullString
		amountMinorUnits                  int64
		resultBalanceMinorUnits           sql.NullInt64
		attempts                          int
		nextRetryAt                       sql.NullTime
		createdAt, updatedAt              time.Time
	)

	err := row.Scan(
		&id, &kind, &providerID, &externalTransactionID, &idempotencyKey, &payloadHash,
		&walletID, &playerID, &roundID, &gameID, &amountMinorUnits, &currency,
		&referenceExternalTransactionID, &resolvedReferenceTransactionID,
		&status, &failureCode, &resultBalanceMinorUnits, &attempts, &nextRetryAt,
		&createdAt, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return wagertransaction.WagerTransaction{}, ErrWagerTransactionNotFound
		}
		return wagertransaction.WagerTransaction{}, fmt.Errorf("postgres: scan wager transaction: %w", err)
	}

	amount, err := money.New(amountMinorUnits, currency)
	if err != nil {
		return wagertransaction.WagerTransaction{}, fmt.Errorf("postgres: rehydrate wager transaction amount: %w", err)
	}

	var resultBalance *money.Money
	if resultBalanceMinorUnits.Valid {
		m, err := money.New(resultBalanceMinorUnits.Int64, currency)
		if err != nil {
			return wagertransaction.WagerTransaction{}, fmt.Errorf("postgres: rehydrate wager transaction result balance: %w", err)
		}
		resultBalance = &m
	}

	var nextRetryAtPtr *time.Time
	if nextRetryAt.Valid {
		v := nextRetryAt.Time
		nextRetryAtPtr = &v
	}

	t, err := wagertransaction.Rehydrate(wagertransaction.RehydrateParams{
		ID:                             id,
		ExternalTransactionID:          externalTransactionID.String,
		ProviderID:                     providerID.String,
		IdempotencyKey:                 idempotencyKey.String,
		PayloadHash:                    payloadHash.String,
		WalletID:                       walletID,
		PlayerID:                       playerID,
		RoundID:                        roundID.String,
		GameID:                         gameID.String,
		Kind:                           wagertransaction.Kind(kind),
		Money:                          amount,
		ReferenceExternalTransactionID: referenceExternalTransactionID.String,
		ResolvedReferenceTransactionID: resolvedReferenceTransactionID.UUID,
		Status:                         wagertransaction.Status(status),
		FailureCode:                    failureCode.String,
		ResultBalance:                  resultBalance,
		Attempts:                       attempts,
		NextRetryAt:                    nextRetryAtPtr,
		CreatedAt:                      createdAt,
		UpdatedAt:                      updatedAt,
	})
	if err != nil {
		return wagertransaction.WagerTransaction{}, fmt.Errorf("postgres: rehydrate wager transaction: %w", err)
	}
	return t, nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullUUID(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}

func nullResultBalance(m money.Money, has bool) any {
	if !has {
		return nil
	}
	return m.MinorUnits()
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}
