package postgres

import (
	"errors"

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

func pgErrorCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}
