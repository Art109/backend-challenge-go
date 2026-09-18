package postgres

import (
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // registers the "postgres" DSN scheme
	"github.com/golang-migrate/migrate/v4/source/iofs"

	appmigrations "backend-challenge-go/migrations"
)

// RunMigrations applies every pending up migration embedded in
// migrations.FS. It is safe to call from every instance on every startup:
// golang-migrate takes an advisory lock in Postgres for the duration, so
// concurrent instances migrating at once serialize instead of racing.
func RunMigrations(dsn string) error {
	source, err := iofs.New(appmigrations.FS, ".")
	if err != nil {
		return fmt.Errorf("postgres: open migrations source: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", source, dsn)
	if err != nil {
		return fmt.Errorf("postgres: init migrator: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("postgres: apply migrations: %w", err)
	}
	return nil
}
