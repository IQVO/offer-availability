package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/offer-availability/internal/adapters/outbound/postgres/pgtx"
	"github.com/claudioed/offer-availability/internal/application/ports"
)

// querier is the subset of pgx shared by *pgxpool.Pool and pgx.Tx that the
// repositories need, so the same SQL runs against either.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// UnitOfWork is the pgx-backed ports.UnitOfWork: Do begins one transaction,
// carries it in the ctx handed to fn (see package pgtx), commits on a nil return
// and rolls back otherwise. If ctx already carries a transaction Do joins it
// instead of beginning a nested one.
type UnitOfWork struct{ pool *pgxpool.Pool }

var _ ports.UnitOfWork = (*UnitOfWork)(nil)

// NewUnitOfWork constructs a UnitOfWork over pool.
func NewUnitOfWork(pool *pgxpool.Pool) *UnitOfWork { return &UnitOfWork{pool: pool} }

// Do implements ports.UnitOfWork.
func (u *UnitOfWork) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, ok := pgtx.From(ctx); ok {
		return fn(ctx)
	}
	tx, err := u.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin unit of work: %w", err)
	}
	// Rollback after a successful Commit is a harmless no-op (pgx.ErrTxClosed);
	// it also covers a panic in fn. Use a context that survives cancellation of
	// ctx so a cancelled message still rolls back.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := fn(pgtx.With(ctx, tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit unit of work: %w", err)
	}
	return nil
}

// queryFor returns the transaction carried by ctx, or pool when there is none.
func queryFor(ctx context.Context, pool *pgxpool.Pool) querier {
	if tx, ok := pgtx.From(ctx); ok {
		return tx
	}
	return pool
}

// inTx runs fn atomically. When ctx already carries a unit-of-work transaction fn
// simply runs on it (the outer unit of work owns commit). When there is none,
// inTx begins, commits and rolls back its own transaction.
func inTx(ctx context.Context, pool *pgxpool.Pool, fn func(q querier) error) error {
	if tx, ok := pgtx.From(ctx); ok {
		return fn(tx)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SQLSTATE codes the repositories translate.
const (
	uniqueViolation = "23505"
)

// constraintOf returns the SQLSTATE and constraint name of a Postgres error.
func constraintOf(err error) (code, constraint string) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code, pgErr.ConstraintName
	}
	return "", ""
}

// notFound maps pgx.ErrNoRows to the port sentinel.
func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrNotFound
	}
	return err
}

// saveResult turns the outcome of one INSERT (version 1) or UPDATE (version>1)
// into the port contract: a unique violation on the primary key and an update
// that matched no row are ports.ErrConcurrentUpdate.
func saveResult(tag pgconn.CommandTag, err error, version int64, primaryKey string) error {
	if err != nil {
		if code, constraint := constraintOf(err); code == uniqueViolation && constraint == primaryKey {
			return fmt.Errorf("%w: id already stored", ports.ErrConcurrentUpdate)
		}
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: stored version is not %d", ports.ErrConcurrentUpdate, version-1)
	}
	return nil
}
