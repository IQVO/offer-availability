package postgres

import (
	"errors"
	"fmt"
)

// The environment variables the Postgres adapter reads.
const (
	// EnvDatabaseURL is the DSN of the service's pool. Mandatory.
	EnvDatabaseURL = "DATABASE_URL"
	// EnvMigrationsDatabaseURL is an optional DIRECT DSN for the boot
	// migrations. golang-migrate takes a session-level advisory lock, which
	// breaks behind a transaction-pooling PgBouncer, so a deployment that
	// pools DATABASE_URL points this at Postgres itself. It falls back to
	// DATABASE_URL.
	EnvMigrationsDatabaseURL = "MIGRATIONS_DATABASE_URL"
)

// ErrMissingEnv is wrapped by the boot error of a mandatory variable that is
// unset or empty; the message names the variable.
var ErrMissingEnv = errors.New("missing required environment variable")

// RequireEnv returns the value of the named variable, or an error that wraps
// ErrMissingEnv and names it. getenv is os.Getenv in production.
func RequireEnv(getenv func(string) string, name string) (string, error) {
	if v := getenv(name); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("%w: %s", ErrMissingEnv, name)
}

// DatabaseURL returns DATABASE_URL, or a boot error naming it.
func DatabaseURL(getenv func(string) string) (string, error) {
	return RequireEnv(getenv, EnvDatabaseURL)
}

// MigrationsURL returns MIGRATIONS_DATABASE_URL when it is set, otherwise
// databaseURL.
func MigrationsURL(getenv func(string) string, databaseURL string) string {
	if v := getenv(EnvMigrationsDatabaseURL); v != "" {
		return v
	}
	return databaseURL
}
