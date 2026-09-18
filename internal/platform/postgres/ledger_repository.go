package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"backend-challenge-go/internal/domain/money"
	"backend-challenge-go/internal/domain/wallet"
)

// ErrDuplicateLedgerEntry surfaces the (wallet_id, transaction_id) unique
// constraint: the schema-level half of "no duplicate movement". The
// application layer should normally never hit this - it means the same
// transaction tried to move the same wallet twice.
var ErrDuplicateLedgerEntry = errors.New("postgres: duplicate ledger entry for this wallet and transaction")

type LedgerRepository struct{}

func NewLedgerRepository() *LedgerRepository {
	return &LedgerRepository{}
}

// InsertEntry appends a ledger entry. Call it in the same transaction as
// the matching WalletRepository.UpdateBalance (or Insert, for an OPENING) -
// the two must commit together or not at all.
func (r *LedgerRepository) InsertEntry(ctx context.Context, q Querier, entry wallet.LedgerEntry) error {
	_, err := q.Exec(ctx, `
		INSERT INTO wallet_ledger_entries (
			id, wallet_id, transaction_id, direction,
			amount_minor_units, currency, balance_before_minor_units, balance_after_minor_units, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`,
		entry.ID(), entry.WalletID(), entry.TransactionID(), string(entry.Direction()),
		entry.Amount().MinorUnits(), entry.Amount().Currency(),
		entry.BalanceBefore().MinorUnits(), entry.BalanceAfter().MinorUnits(), entry.CreatedAt(),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicateLedgerEntry
		}
		return fmt.Errorf("postgres: insert ledger entry: %w", err)
	}
	return nil
}

// LedgerCursor is the decoded form of the ledger pagination endpoint's
// opaque cursor: the (createdAt, id) of the last entry the caller already
// saw. (createdAt, id) together give a stable total order even when two
// entries share a timestamp.
type LedgerCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// ListEntries returns up to limit entries for wallet, ordered oldest
// first, strictly after cursor (nil for the first page). It always fetches
// one extra row so the caller can tell whether another page exists without
// a separate count query.
func (r *LedgerRepository) ListEntries(ctx context.Context, q Querier, walletID uuid.UUID, cursor *LedgerCursor, limit int) ([]wallet.LedgerEntry, bool, error) {
	sql := `
		SELECT id, wallet_id, transaction_id, direction, amount_minor_units, currency,
		       balance_before_minor_units, balance_after_minor_units, created_at
		FROM wallet_ledger_entries
		WHERE wallet_id = $1
	`
	args := []any{walletID}
	if cursor != nil {
		sql += ` AND (created_at, id) > ($2, $3) ORDER BY created_at, id LIMIT $4`
		args = append(args, cursor.CreatedAt, cursor.ID, limit+1)
	} else {
		sql += ` ORDER BY created_at, id LIMIT $2`
		args = append(args, limit+1)
	}

	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list ledger entries: %w", err)
	}
	defer rows.Close()

	var entries []wallet.LedgerEntry
	for rows.Next() {
		var (
			id, wID, txID           uuid.UUID
			direction, currency     string
			amountMinorUnits        int64
			balanceBeforeMinorUnits int64
			balanceAfterMinorUnits  int64
			createdAt               time.Time
		)
		if err := rows.Scan(&id, &wID, &txID, &direction, &amountMinorUnits, &currency, &balanceBeforeMinorUnits, &balanceAfterMinorUnits, &createdAt); err != nil {
			return nil, false, fmt.Errorf("postgres: scan ledger entry: %w", err)
		}
		amount, err := money.New(amountMinorUnits, currency)
		if err != nil {
			return nil, false, err
		}
		before, err := money.New(balanceBeforeMinorUnits, currency)
		if err != nil {
			return nil, false, err
		}
		after, err := money.New(balanceAfterMinorUnits, currency)
		if err != nil {
			return nil, false, err
		}
		entry, err := wallet.NewLedgerEntry(id, wID, txID, wallet.Direction(direction), amount, before, after, createdAt)
		if err != nil {
			return nil, false, fmt.Errorf("postgres: rehydrate ledger entry: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("postgres: iterate ledger entries: %w", err)
	}

	hasMore := len(entries) > limit
	if hasMore {
		entries = entries[:limit]
	}
	return entries, hasMore, nil
}

// SumEntries reconstructs a wallet's balance purely from its ledger
// (credits minus debits, including the OPENING credit), for reconciliation
// against the stored balance. currency is the wallet's own currency, since
// an empty ledger has no rows to derive it from.
func (r *LedgerRepository) SumEntries(ctx context.Context, q Querier, walletID uuid.UUID, currency string) (money.Money, int, error) {
	var (
		netMinorUnits int64
		count         int
	)
	err := q.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN direction = 'CREDIT' THEN amount_minor_units ELSE 0 END), 0)
			- COALESCE(SUM(CASE WHEN direction = 'DEBIT' THEN amount_minor_units ELSE 0 END), 0),
			COUNT(*)
		FROM wallet_ledger_entries
		WHERE wallet_id = $1
	`, walletID).Scan(&netMinorUnits, &count)
	if err != nil {
		return money.Money{}, 0, fmt.Errorf("postgres: sum ledger entries: %w", err)
	}
	sum, err := money.New(netMinorUnits, currency)
	if err != nil {
		return money.Money{}, 0, err
	}
	return sum, count, nil
}
