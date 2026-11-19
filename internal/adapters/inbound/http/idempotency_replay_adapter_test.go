package http

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The adapter of the replay-class suite (idempotency_replay_classes_test.go): the
// REAL Idempotency-Key middleware (requireKey) over memPool, an in-memory pool
// that models what the middleware relies on of Postgres, and nothing else:
//
//   - a transaction and its savepoints see their own writes; only a commit of
//     the outermost transaction makes them visible to others (Committed, KeyRows
//     and the replay read count COMMITTED state only);
//   - INSERT ... ON CONFLICT (key) DO NOTHING against committed and own rows;
//   - a rolled back savepoint or transaction discards what it wrote.
//
// Real Postgres is the integration suite's job (it proves the same classes on
// the router, with the competing writer and the queued duplicate).

type keyRow struct {
	hash   string
	status *int
	body   []byte
}

// memStore is the committed state: key rows and the tagged writes a request made.
type memStore struct {
	mu     sync.Mutex
	keys   map[string]*keyRow
	writes map[string]int
}

type memPool struct{ store *memStore }

func newMemPool() *memPool {
	return &memPool{store: &memStore{keys: map[string]*keyRow{}, writes: map[string]int{}}}
}

func (p *memPool) Begin(context.Context) (pgx.Tx, error) { return newMemTx(p.store, nil), nil }

func (p *memPool) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	row, ok := p.store.keys[args[0].(string)]
	return memRow{row: row, ok: ok}
}

type memRow struct {
	row *keyRow
	ok  bool
}

func (r memRow) Scan(dest ...any) error {
	if !r.ok {
		return pgx.ErrNoRows
	}
	*(dest[0].(*string)) = r.row.hash
	*(dest[1].(**int)) = r.row.status
	*(dest[2].(*[]byte)) = r.row.body
	return nil
}

// memTx is a transaction or a savepoint: its own overlay over its parent (or the
// store). A nil row in keys is a deletion.
type memTx struct {
	pgx.Tx // the methods the middleware never calls
	store  *memStore
	parent *memTx
	keys   map[string]*keyRow
	writes map[string]int
}

func newMemTx(s *memStore, parent *memTx) *memTx {
	return &memTx{store: s, parent: parent, keys: map[string]*keyRow{}, writes: map[string]int{}}
}

func (t *memTx) Begin(context.Context) (pgx.Tx, error) { return newMemTx(t.store, t), nil }

func (t *memTx) lookup(key string) *keyRow {
	for tx := t; tx != nil; tx = tx.parent {
		if row, ok := tx.keys[key]; ok {
			return row
		}
	}
	t.store.mu.Lock()
	defer t.store.mu.Unlock()
	return t.store.keys[key]
}

func (t *memTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	switch {
	case strings.HasPrefix(sql, "DELETE FROM idempotency_keys WHERE created_at"):
		return cmd("DELETE 0"), nil // nothing in the model expires
	case strings.HasPrefix(sql, "DELETE FROM idempotency_keys WHERE key"):
		t.keys[args[0].(string)] = nil
		return cmd("DELETE 1"), nil
	case strings.HasPrefix(sql, "INSERT INTO idempotency_keys"):
		key := args[1].(string)
		if t.lookup(key) != nil {
			return cmd("INSERT 0 0"), nil
		}
		t.keys[key] = &keyRow{hash: args[2].(string)}
		return cmd("INSERT 0 1"), nil
	case strings.HasPrefix(sql, "UPDATE idempotency_keys"):
		status := args[0].(int)
		t.keys[args[2].(string)] = &keyRow{hash: t.lookup(args[2].(string)).hash, status: &status, body: args[1].([]byte)}
		return cmd("UPDATE 1"), nil
	case strings.HasPrefix(sql, "INSERT INTO scratch_writes"):
		t.writes[args[0].(string)]++
		return cmd("INSERT 0 1"), nil
	}
	return cmd(""), errors.New("memTx: unexpected statement " + sql)
}

// Commit merges the overlay into the parent, or into the store for the outermost.
func (t *memTx) Commit(context.Context) error {
	if t.parent != nil {
		for k, v := range t.keys {
			t.parent.keys[k] = v
		}
		for tag, n := range t.writes {
			t.parent.writes[tag] += n
		}
		return nil
	}
	t.store.mu.Lock()
	defer t.store.mu.Unlock()
	for k, v := range t.keys {
		if v == nil {
			delete(t.store.keys, k)
		} else {
			t.store.keys[k] = v
		}
	}
	for tag, n := range t.writes {
		t.store.writes[tag] += n
	}
	return nil
}

func (t *memTx) Rollback(context.Context) error {
	t.keys, t.writes = map[string]*keyRow{}, map[string]int{}
	return nil
}

func cmd(s string) pgconn.CommandTag { return pgconn.NewCommandTag(s) }

type txCtxKey struct{}

// replayAdapter wiring ------------------------------------------------------

type memAdapter struct {
	pool *memPool
	errs map[string]error // slug -> the typed error that produces it
}

func newReplayAdapter(t *testing.T) replayAdapter {
	t.Helper()
	a := &memAdapter{pool: newMemPool(), errs: map[string]error{}}
	for _, e := range problemCatalogue {
		if _, seen := a.errs[e.p.slug]; !seen {
			a.errs[e.p.slug] = e.err
		}
	}
	return a
}

func (a *memAdapter) Catalogue() []problemEntry {
	var out []problemEntry
	seen := map[string]bool{}
	for _, e := range problemCatalogue {
		if !seen[e.p.slug] {
			seen[e.p.slug] = true
			out = append(out, problemEntry{Slug: e.p.slug, Status: e.p.status, Class: e.replay.String()})
		}
	}
	// An unmapped error is the 5xx the idempotent routes can always answer.
	return append(out, problemEntry{Slug: internalProblem.slug, Status: internalProblem.status, Class: classRetryable})
}

func (a *memAdapter) Middleware(_ *testing.T, next http.Handler) http.Handler {
	bind := func(ctx context.Context, tx pgx.Tx) context.Context { return context.WithValue(ctx, txCtxKey{}, tx) }
	return requireKey(a.pool, bind)("POST /inventory-feeds")(next)
}

func (a *memAdapter) Refuse(w http.ResponseWriter, r *http.Request, e problemEntry) {
	err, ok := a.errs[e.Slug]
	if !ok {
		err = errors.New("unmapped") // internal-error
	}
	writeError(w, r, err)
}

func (a *memAdapter) Write(ctx context.Context, tag string) error {
	_, err := ctx.Value(txCtxKey{}).(pgx.Tx).Exec(ctx, "INSERT INTO scratch_writes (tag) VALUES ($1)", tag)
	return err
}

func (a *memAdapter) Committed(_ *testing.T, tag string) int {
	a.pool.store.mu.Lock()
	defer a.pool.store.mu.Unlock()
	return a.pool.store.writes[tag]
}

func (a *memAdapter) KeyRows(_ *testing.T, key string) int {
	a.pool.store.mu.Lock()
	defer a.pool.store.mu.Unlock()
	if _, ok := a.pool.store.keys[key]; ok {
		return 1
	}
	return 0
}
