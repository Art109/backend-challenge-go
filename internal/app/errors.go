// Package app holds the use cases that orchestrate the domain packages and
// the postgres repositories into complete, atomic operations. This is
// where "read wallet, debit it, write the ledger entry, stage the outbox
// events" becomes one database transaction - none of the domain or
// repository packages know about that coordination on their own.
package app

import "errors"

var (
	// ErrIdempotencyKeyConflict: the same Idempotency-Key was reused with
	// different business content - the server must not silently accept
	// either version.
	ErrIdempotencyKeyConflict = errors.New("app: idempotency key reused with different content")
	// ErrExternalTransactionKeyMismatch: this exact (providerId,
	// externalTransactionId) operation already exists under a *different*
	// idempotency key than the one just supplied.
	ErrExternalTransactionKeyMismatch = errors.New("app: this operation was already submitted under a different idempotency key")
	// ErrWalletConflict: a wallet already exists for this (playerId, currency).
	ErrWalletConflict = errors.New("app: wallet already exists for this player and currency")
	// ErrWalletNotFound: the referenced wallet doesn't exist.
	ErrWalletNotFound = errors.New("app: wallet not found")
	// ErrWagerTransactionNotFound: no transaction matches the lookup.
	ErrWagerTransactionNotFound = errors.New("app: wager transaction not found")
	// ErrTooManyConflicts: an optimistic-concurrency retry budget was
	// exhausted - the wallet is under unusually heavy contention.
	ErrTooManyConflicts = errors.New("app: exhausted retries on wallet version conflicts")
	// ErrWalletPlayerMismatch: the wallet exists, but not for this player -
	// a provider trying to move someone else's wallet.
	ErrWalletPlayerMismatch = errors.New("app: wallet does not belong to this player")
)
