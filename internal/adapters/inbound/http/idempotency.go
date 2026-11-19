package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IdempotencyKeyHeader is the request header a caller must supply on every
// state-changing route.
const IdempotencyKeyHeader = "Idempotency-Key"

// IdempotencyReplayedHeader is set to "true" on a response that replays a
// stored outcome instead of running the operation again.
const IdempotencyReplayedHeader = "Idempotency-Replayed"

// MaxIdempotencyKeyLength is the longest accepted key (apis/openapi.yaml).
const MaxIdempotencyKeyLength = 128

// keyTTLInterval is the lifetime of a stored key (a Postgres interval).
const keyTTLInterval = "24 hours"

// TxBinder returns a child context carrying tx, so the use case's own unit of
// work (the Postgres adapter's) JOINS the transaction the middleware opened
// instead of beginning a second one. cmd/ passes the outbound adapter's binder
// in: adapters never import each other.
type TxBinder func(ctx context.Context, tx pgx.Tx) context.Context

// Idempotency builds the per-route Idempotency-Key middleware. route names the
// operation ("POST /nodes", "POST /nodes/{nodeId}:activate", ...) and, with the
// key, is the primary key of idempotency_keys. A nil Idempotency on the Server
// is refused by NewRouter: there is no unkeyed mode.
type Idempotency func(route string) func(http.Handler) http.Handler

// RequireIdempotencyKey is the transactional Idempotency-Key middleware factory
// backed by the idempotency_keys table (migration 0002).
//
// The correctness argument in short:
//
//   - The request is identified by key alone (global); its fingerprint is
//     sha256(method, path, body). The same key with the same fingerprint replays
//     the FIRST response (status and body, header Idempotency-Replayed: true);
//     the same key with another fingerprint is 409 idempotency-key-reused.
//   - Two concurrent requests with the same key race on INSERT ... ON CONFLICT
//     DO NOTHING. Postgres serialises them on the primary-key index: the second
//     statement blocks until the first transaction resolves, so a reader that
//     sees "0 rows inserted" can rely on the original being finished (and if it
//     rolled back, the blocked insert succeeds instead and does the work).
//   - The handler runs inside a SAVEPOINT of that transaction, bound into its
//     context (TxBinder), so the key, the aggregates and the outbox rows of a
//     2xx commit together. A 5xx or a panic rolls everything back and is never
//     stored, so the same key can retry the real work.
//   - A refusal (a non-2xx, non-5xx answer) is decided by the replay class its
//     problem type declares on the catalogue (ADR 0016; the middleware holds no
//     slug and no status range beyond 2xx and 5xx): FINAL rolls the handler's
//     writes back and stores the answer; OUTCOME commits the writes with the
//     stored answer; RETRYABLE is released exactly like a 5xx, key row included,
//     so a retry under the same key executes again (an entry that records a fact,
//     the FAILED feed of listing-not-on-node, commits that fact and still frees
//     the key); an answer with no class is released like RETRYABLE.
//   - A duplicate that arrives while the first request is running waits on the
//     key row. When the first ends RETRYABLE the row is gone, the duplicate's
//     insert succeeds and it executes the work itself: it is neither answered
//     idempotency-key-reused nor replayed.
//   - Keys live 24 hours. Every keyed write first purges the expired rows
//     (indexed on created_at); an expired key is simply a new key.
func RequireIdempotencyKey(pool *pgxpool.Pool, bind TxBinder) Idempotency {
	return requireKey(pool, bind)
}

// txPool is what the middleware needs of the pool: a transaction and the read
// of a committed key row. *pgxpool.Pool is the production one.
type txPool interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func requireKey(pool txPool, bind TxBinder) Idempotency {
	return func(route string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return &keyed{pool: pool, bind: bind, route: route, next: next}
		}
	}
}

type keyed struct {
	pool  txPool
	bind  TxBinder
	route string
	next  http.Handler
}

func (k *keyed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key, ok := keyFrom(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	_ = r.Body.Close()
	if err != nil {
		writeError(w, r, errMalformedRequest)
		return
	}
	if len(body) > maxBodyBytes {
		writeError(w, r, errTooLarge)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	hash := fingerprint(r, body)

	ctx := r.Context()
	tx, err := k.pool.Begin(ctx)
	if err != nil {
		writeError(w, r, err)
		return
	}
	fresh, err := k.claim(ctx, tx, key, hash)
	if err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		writeError(w, r, err)
		return
	}
	if !fresh {
		_ = tx.Rollback(ctx)
		k.replay(w, r, key, hash)
		return
	}
	k.runFresh(ctx, w, r, tx, key)
}

// keyFrom returns the validated Idempotency-Key, or writes the 400 problem.
func keyFrom(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get(IdempotencyKeyHeader)
	if key == "" {
		writeProblem(w, r, idempotencyKeyRequired, "send an Idempotency-Key header with a unique value per logical request")
		return "", false
	}
	if len(key) > MaxIdempotencyKeyLength || notPrintableASCII(key) {
		writeProblem(w, r, idempotencyKeyInvalid, "the Idempotency-Key must be 1 to 128 printable ASCII characters")
		return "", false
	}
	return key, true
}

func notPrintableASCII(s string) bool {
	for _, c := range s {
		if c < 0x20 || c > 0x7e {
			return true
		}
	}
	return false
}

// fingerprint identifies the request apart from its key: the method, the path
// (so the same key on another node is another request) and the body.
func fingerprint(r *http.Request, body []byte) string {
	h := sha256.New()
	h.Write([]byte(r.Method))
	h.Write([]byte{0})
	h.Write([]byte(r.URL.EscapedPath()))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// claim purges expired keys and tries to insert this one: true when the key is
// new, false when a committed row already holds it.
func (k *keyed) claim(ctx context.Context, tx pgx.Tx, key, hash string) (bool, error) {
	if _, err := tx.Exec(ctx, `DELETE FROM idempotency_keys WHERE created_at < now() - interval '`+keyTTLInterval+`'`); err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO idempotency_keys (route, key, request_hash) VALUES ($1, $2, $3) ON CONFLICT (key) DO NOTHING`,
		k.route, key, hash)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// replay answers a request whose key already has a committed row: the stored
// outcome when the fingerprint matches, 409 when it does not.
func (k *keyed) replay(w http.ResponseWriter, r *http.Request, key, hash string) {
	var (
		storedHash string
		status     *int
		body       []byte
	)
	err := k.pool.QueryRow(r.Context(),
		`SELECT request_hash, response_status, response_body FROM idempotency_keys WHERE key = $1`,
		key).Scan(&storedHash, &status, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		// The row expired between the insert attempt and this read: it is a new key now.
		writeProblem(w, r, idempotencyKeyReused, "the Idempotency-Key expired while the request was in flight; retry it")
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	if storedHash != hash {
		writeProblem(w, r, idempotencyKeyReused, "the Idempotency-Key was used with a different request")
		return
	}
	if status == nil {
		// Unreachable (a committed row always has its outcome): a defensive 500
		// rather than a fabricated response.
		writeError(w, r, errors.New("idempotency row committed with no recorded outcome"))
		return
	}
	w.Header().Set("Content-Type", replayContentType(*status))
	w.Header().Set(IdempotencyReplayedHeader, "true")
	w.WriteHeader(*status)
	_, _ = w.Write(body)
}

func replayContentType(status int) string {
	if status >= http.StatusBadRequest {
		return contentProblem
	}
	return contentJSON
}

// runFresh handles a genuinely new key: run the handler in a savepoint, decide
// from the answer what to keep (settlementOf), record the outcome in the same
// transaction, commit, and only then write to the client.
func (k *keyed) runFresh(ctx context.Context, w http.ResponseWriter, r *http.Request, tx pgx.Tx, key string) {
	finished := false
	defer func() {
		if !finished {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	sp, err := tx.Begin(ctx)
	if err != nil {
		writeError(w, r, err)
		return
	}
	rec := &capture{header: http.Header{}}
	func() {
		defer func() {
			if p := recover(); p != nil {
				_ = sp.Rollback(context.WithoutCancel(ctx))
				panic(p)
			}
		}()
		k.next.ServeHTTP(rec, r.WithContext(k.bind(ctx, sp)))
	}()
	if rec.code == 0 {
		rec.code = http.StatusOK
	}
	act := rec.settlement()
	if act == settleRelease {
		// A server failure and a RETRYABLE refusal are never stored: everything
		// rolls back (the deferred rollback) so the same key can retry the work.
		flush(w, rec)
		return
	}
	if err := k.settle(ctx, tx, sp, key, rec, act); err != nil {
		writeError(w, r, err)
		return
	}
	finished = true
	flush(w, rec)
}

// settle ends the savepoint as act says (committed, or rolled back for a FINAL
// refusal), then stores the outcome and commits; a settleRecord answer is not
// stored, its key row is deleted in the same transaction.
func (k *keyed) settle(ctx context.Context, tx, sp pgx.Tx, key string, rec *capture, act settlement) error {
	var err error
	if act == settleFinal {
		err = sp.Rollback(ctx)
	} else {
		err = sp.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if act == settleRecord {
		_, err = tx.Exec(ctx, `DELETE FROM idempotency_keys WHERE key = $1`, key)
	} else {
		_, err = tx.Exec(ctx, `UPDATE idempotency_keys SET response_status = $1, response_body = $2 WHERE key = $3`,
			rec.code, rec.body.Bytes(), key)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
