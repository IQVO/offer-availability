package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"unicode"

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
const MaxIdempotencyKeyLength = 200

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
//   - The request is identified by (route, key); its fingerprint is
//     sha256(method, path, body). The same key with the same fingerprint replays
//     the FIRST response (status and body, header Idempotency-Replayed: true);
//     the same key with another fingerprint is 422 idempotency-key-reused.
//   - Two concurrent requests with the same key race on INSERT ... ON CONFLICT
//     DO NOTHING. Postgres serialises them on the primary-key index: the second
//     statement blocks until the first transaction resolves, so a reader that
//     sees "0 rows inserted" can rely on the original being finished (and if it
//     rolled back, the blocked insert succeeds instead and does the work).
//   - The handler runs inside a SAVEPOINT of that transaction, bound into its
//     context (TxBinder), so the key, the aggregates and the outbox rows of a
//     2xx commit together. A 4xx rolls the handler's writes back to the
//     savepoint but is still stored (a repeat answers the same). A 5xx or a panic
//     rolls everything back and is never stored, so the same key can retry the
//     real work.
//   - Keys live 24 hours. Every keyed write first purges the expired rows
//     (indexed on created_at); an expired key is simply a new key.
func RequireIdempotencyKey(pool *pgxpool.Pool, bind TxBinder) Idempotency {
	return func(route string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return &keyed{pool: pool, bind: bind, route: route, next: next}
		}
	}
}

type keyed struct {
	pool  *pgxpool.Pool
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
	if len(key) > MaxIdempotencyKeyLength || hasControl(key) {
		writeProblem(w, r, idempotencyKeyInvalid, "the Idempotency-Key must be at most 200 printable characters")
		return "", false
	}
	return key, true
}

func hasControl(s string) bool {
	for _, c := range s {
		if unicode.IsControl(c) {
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
	tag, err := tx.Exec(ctx, `INSERT INTO idempotency_keys (route, key, request_hash) VALUES ($1, $2, $3) ON CONFLICT (route, key) DO NOTHING`,
		k.route, key, hash)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// replay answers a request whose key already has a committed row: the stored
// outcome when the fingerprint matches, 422 when it does not.
func (k *keyed) replay(w http.ResponseWriter, r *http.Request, key, hash string) {
	var (
		storedHash string
		status     *int
		body       []byte
	)
	err := k.pool.QueryRow(r.Context(),
		`SELECT request_hash, response_status, response_body FROM idempotency_keys WHERE route = $1 AND key = $2`,
		k.route, key).Scan(&storedHash, &status, &body)
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

// capture records the handler's response so it can be persisted and committed
// BEFORE anything reaches the client.
type capture struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func (c *capture) Header() http.Header { return c.header }

func (c *capture) WriteHeader(code int) {
	if c.code == 0 {
		c.code = code
	}
}

func (c *capture) Write(b []byte) (int, error) {
	if c.code == 0 {
		c.code = http.StatusOK
	}
	return c.body.Write(b)
}

// runFresh handles a genuinely new key: run the handler in a savepoint, record
// the outcome in the same transaction, commit, and only then write to the
// client.
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
	ctx, kept := withKeepWrites(ctx)
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
	if rec.code >= http.StatusInternalServerError {
		// A server failure is never stored: everything rolls back so the same
		// key can retry the real work.
		flush(w, rec)
		return
	}
	if err := k.settle(ctx, tx, sp, key, rec, kept.Load()); err != nil {
		writeError(w, r, err)
		return
	}
	finished = true
	flush(w, rec)
}

// settle keeps the handler's writes on a success and drops them on a 4xx, unless
// the handler asked to keep them (keepWrites), then stores the outcome and
// commits.
func (k *keyed) settle(ctx context.Context, tx, sp pgx.Tx, key string, rec *capture, keepWrites bool) error {
	var err error
	if rec.code >= http.StatusBadRequest && !keepWrites {
		err = sp.Rollback(ctx)
	} else {
		err = sp.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE idempotency_keys SET response_status = $1, response_body = $2 WHERE route = $3 AND key = $4`,
		rec.code, rec.body.Bytes(), k.route, key); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type keepKey struct{}

// withKeepWrites returns a context carrying the flag a handler raises (keepWrites)
// to have the middleware commit its writes although it answers a 4xx.
func withKeepWrites(ctx context.Context) (context.Context, *atomic.Bool) {
	flag := new(atomic.Bool)
	return context.WithValue(ctx, keepKey{}, flag), flag
}

// keepWrites tells the Idempotency-Key middleware to commit the writes of this
// request although it answers a 4xx: a FAILED feed is a recorded fact (ADR 0002,
// A11) that GET /inventory-feeds/{feedId} must show. Outside the middleware it
// does nothing, the unit of work has committed already.
func keepWrites(ctx context.Context) {
	if flag, ok := ctx.Value(keepKey{}).(*atomic.Bool); ok {
		flag.Store(true)
	}
}

// flush copies the captured response onto the real client connection.
func flush(w http.ResponseWriter, rec *capture) {
	for name, vs := range rec.header {
		for _, v := range vs {
			w.Header().Add(name, v)
		}
	}
	w.WriteHeader(rec.code)
	_, _ = w.Write(rec.body.Bytes())
}
