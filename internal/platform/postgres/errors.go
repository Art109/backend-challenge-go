package postgres

import (
	"context"
	"errors"
	"net"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Postgres error codes this package cares about.
// https://www.postgresql.org/docs/current/errcodes-appendix.html
const (
	sqlStateUniqueViolation = "23505"
	sqlStateCheckViolation  = "23514"
)

func isUniqueViolation(err error) bool {
	return pgErrorCode(err) == sqlStateUniqueViolation
}

func isCheckViolation(err error) bool {
	return pgErrorCode(err) == sqlStateCheckViolation
}

func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

// IsUnavailable reports whether err reflects Postgres being transiently
// unreachable (connection refused, DNS failure because the container isn't
// up, a dial/query timeout) rather than a bug or a business-rule
// violation. Callers use this to return 503 instead of 500 - the spec
// requires "indisponibilidade transitória" be distinguishable from other
// failures in the HTTP contract, and 503 is the signal that retrying later
// might succeed, which a generic 500 doesn't communicate.
func IsUnavailable(err error) bool {
	if err == nil {
		return false
	}
	var connErr *pgconn.ConnectError
	if errors.As(err, &connErr) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

func pgErrorCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}
