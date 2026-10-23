package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The Postgres-backed paths of idempotency.go (ServeHTTP, replay) are proven on
// a real database by idempotency_integration_test.go. This file unit-tests the
// transactional core over fakeTx — an in-memory pgx.Tx that answers the three
// statements the middleware issues (purge, INSERT ... ON CONFLICT DO NOTHING,
// UPDATE of the outcome) and records the transaction protocol: savepoint
// commit/rollback, outcome storage and the commit order.

type storedOutcome struct {
	status int
	body   []byte
}

type txLog struct {
	mu        sync.Mutex
	failExec  error
	failBegin bool
	claimed   map[string]bool
	outcomes  map[string]storedOutcome
	ops       []string
}

func newTxLog() *txLog {
	return &txLog{claimed: map[string]bool{}, outcomes: map[string]storedOutcome{}}
}

func (l *txLog) op(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ops = append(l.ops, fmt.Sprintf(format, args...))
}

func (l *txLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string{}, l.ops...)
}

type fakeTx struct {
	log   *txLog
	savep bool
}

func (t *fakeTx) Begin(context.Context) (pgx.Tx, error) {
	if t.log.failBegin {
		return nil, errors.New("begin failed")
	}
	t.log.op("begin")
	return &fakeTx{log: t.log, savep: true}, nil
}

func (t *fakeTx) Commit(context.Context) error {
	if t.savep {
		t.log.op("sp-commit")
	} else {
		t.log.op("commit")
	}
	return nil
}

func (t *fakeTx) Rollback(context.Context) error {
	if t.savep {
		t.log.op("sp-rollback")
	} else {
		t.log.op("rollback")
	}
	return nil
}

func (t *fakeTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	// id extracts (route, key): the INSERT passes route,key,hash; the UPDATE
	// passes status,body,route,key.
	id := func(route, key any) string { return fmt.Sprintf("%v\x00%v", route, key) }
	switch {
	case strings.HasPrefix(sql, "DELETE FROM idempotency_keys"):
		t.log.op("purge")
		return pgconn.NewCommandTag("DELETE 0"), nil
	case strings.HasPrefix(sql, "INSERT INTO idempotency_keys"):
		if t.log.failExec != nil {
			return pgconn.NewCommandTag(""), t.log.failExec
		}
		t.log.mu.Lock()
		defer t.log.mu.Unlock()
		if t.log.claimed[id(args[0], args[1])] {
			t.log.ops = append(t.log.ops, "conflict")
			return pgconn.NewCommandTag("INSERT 0 0"), nil // ON CONFLICT DO NOTHING
		}
		t.log.claimed[id(args[0], args[1])] = true
		t.log.ops = append(t.log.ops, "insert "+id(args[0], args[1]))
		return pgconn.NewCommandTag("INSERT 0 1"), nil
	case strings.HasPrefix(sql, "UPDATE idempotency_keys"):
		if t.log.failExec != nil {
			return pgconn.NewCommandTag(""), t.log.failExec
		}
		t.log.mu.Lock()
		defer t.log.mu.Unlock()
		t.log.outcomes[id(args[2], args[3])] = storedOutcome{status: args[0].(int), body: args[1].([]byte)}
		t.log.ops = append(t.log.ops, fmt.Sprintf("store %s %d", id(args[2], args[3]), args[0].(int)))
		return pgconn.NewCommandTag("UPDATE 1"), nil
	}
	return pgconn.NewCommandTag("SELECT 1"), nil
}

func (t *fakeTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("fakeTx: Query is not used by the idempotency middleware")
}

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

func (t *fakeTx) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	return errRow{err: fmt.Errorf("fakeTx: QueryRow is not used by the idempotency middleware: %s", sql)}
}

func (t *fakeTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, errors.New("fakeTx: CopyFrom is not supported")
}

func (t *fakeTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults {
	return nil
}

func (t *fakeTx) LargeObjects() pgx.LargeObjects { return pgx.LargeObjects{} }

func (t *fakeTx) Conn() *pgx.Conn { return nil }

func (t *fakeTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	return nil, errors.New("fakeTx: Prepare is not supported")
}

// ---- the key itself --------------------------------------------------------

func TestKeyFromRequiresAKey(t *testing.T) {
	rec := httptest.NewRecorder()
	if key, ok := keyFrom(rec, httptest.NewRequest(http.MethodPost, "/listings", nil)); ok || key != "" {
		t.Fatalf("keyFrom = %q, %v, want refusal", key, ok)
	}
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "idempotency-key-required") {
		t.Fatalf("response = %d %s, want 400 idempotency-key-required", rec.Code, rec.Body)
	}
}

func TestKeyFromRejectsUnusableKeys(t *testing.T) {
	for name, key := range map[string]string{
		"too long":          strings.Repeat("k", MaxIdempotencyKeyLength+1),
		"control character": "key\u0000with-nul",
		"newline":           "key\n",
	} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/listings", nil)
		r.Header.Set(IdempotencyKeyHeader, key)
		if key, ok := keyFrom(rec, r); ok || key != "" {
			t.Fatalf("%s: keyFrom = %q, %v, want refusal", name, key, ok)
		}
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "idempotency-key-invalid") {
			t.Fatalf("%s: response = %d %s, want 400 idempotency-key-invalid", name, rec.Code, rec.Body)
		}
	}
}

func TestKeyFromAcceptsAUsableKey(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/listings", nil)
	r.Header.Set(IdempotencyKeyHeader, "order-42")
	rec := httptest.NewRecorder()
	key, ok := keyFrom(rec, r)
	if !ok || key != "order-42" || rec.Code != http.StatusOK {
		t.Fatalf("keyFrom = %q, %v (%d %s), want the key accepted", key, ok, rec.Code, rec.Body)
	}
}

func TestHasControl(t *testing.T) {
	for s, want := range map[string]bool{"plain": false, "space key": false, "tab\t": true, "nl\n": true, "del\u007f": true} {
		if got := hasControl(s); got != want {
			t.Errorf("hasControl(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestFingerprintIdentifiesMethodPathAndBody(t *testing.T) {
	base := httptest.NewRequest(http.MethodPost, "/listings", strings.NewReader(`{"a":1}`))
	want := fingerprint(base, []byte(`{"a":1}`))
	same := httptest.NewRequest(http.MethodPost, "/listings", nil)
	if got := fingerprint(same, []byte(`{"a":1}`)); got != want {
		t.Fatalf("the same request fingerprinted differently: %s vs %s", got, want)
	}
	otherMethod := httptest.NewRequest(http.MethodPut, "/listings", nil)
	otherPath := httptest.NewRequest(http.MethodPost, "/catalog-items", nil)
	otherBody := httptest.NewRequest(http.MethodPost, "/listings", nil)
	for name, r := range map[string]*http.Request{"method": otherMethod, "path": otherPath} {
		if got := fingerprint(r, []byte(`{"a":1}`)); got == want {
			t.Errorf("a different %s kept the fingerprint", name)
		}
	}
	if got := fingerprint(otherBody, []byte(`{"a":2}`)); got == want {
		t.Error("a different body kept the fingerprint")
	}
}

func TestReplayContentTypeFollowsTheStatusClass(t *testing.T) {
	for status, want := range map[int]string{200: contentJSON, 201: contentJSON, 399: contentJSON, 400: contentProblem, 422: contentProblem, 500: contentProblem} {
		if got := replayContentType(status); got != want {
			t.Errorf("replayContentType(%d) = %q, want %q", status, got, want)
		}
	}
}

// ---- the capture/flush pair ------------------------------------------------

func TestCaptureRecordsAndFlushReplaysTheResponse(t *testing.T) {
	rec := &capture{header: http.Header{}}
	rec.Header().Set("Content-Type", contentJSON)
	rec.WriteHeader(http.StatusCreated)
	if _, err := rec.Write([]byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	if rec.code != http.StatusCreated {
		t.Fatalf("captured code = %d, want 201", rec.code)
	}

	out := httptest.NewRecorder()
	flush(out, rec)
	if out.Code != http.StatusCreated || out.Header().Get("Content-Type") != contentJSON || out.Body.String() != `{"ok":true}`+"\n" && out.Body.String() != `{"ok":true}` {
		t.Fatalf("flushed = %d %s, want the captured response", out.Code, out.Body)
	}
}

func TestCaptureDefaultsTo200AndKeepsTheFirstWriteHeader(t *testing.T) {
	rec := &capture{header: http.Header{}}
	if _, err := rec.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	rec.WriteHeader(http.StatusTeapot)
	rec.WriteHeader(http.StatusCreated)
	if rec.code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (a Write before WriteHeader)", rec.code)
	}
}

func TestKeepWritesFlagIsOffUnlessTheHandlerRaisesIt(t *testing.T) {
	ctx, flag := withKeepWrites(context.Background())
	if flag.Load() {
		t.Fatal("the flag starts clear")
	}
	keepWrites(context.Background()) // no flag in a plain context: must not panic
	if flag.Load() {
		t.Fatal("keepWrites on a foreign context set the flag")
	}
	keepWrites(ctx)
	if !flag.Load() {
		t.Fatal("keepWrites on the middleware's context did not set the flag")
	}
}

// ---- the transactional core ------------------------------------------------

func TestClaimInsertsAKeyOnce(t *testing.T) {
	log := newTxLog()
	k := &keyed{route: "POST /listings"}
	tx := &fakeTx{log: log}
	fresh, err := k.claim(context.Background(), tx, "k1", "h1")
	if err != nil || !fresh {
		t.Fatalf("claim = %v, %v, want fresh", fresh, err)
	}
	fresh, err = k.claim(context.Background(), tx, "k1", "h1")
	if err != nil || fresh {
		t.Fatalf("claim again = %v, %v, want a conflict", fresh, err)
	}
	if ops := log.snapshot(); !strings.Contains(strings.Join(ops, ","), "insert ") || !strings.Contains(strings.Join(ops, ","), "conflict") {
		t.Fatalf("ops = %v", ops)
	}
}

func TestClaimPurgesExpiredKeysFirst(t *testing.T) {
	log := newTxLog()
	k := &keyed{route: "POST /listings"}
	if _, err := k.claim(context.Background(), &fakeTx{log: log}, "k1", "h1"); err != nil {
		t.Fatal(err)
	}
	ops := strings.Join(log.snapshot(), ",")
	if !strings.Contains(ops, "purge") || strings.Index(ops, "purge") > strings.Index(ops, "insert ") {
		t.Fatalf("ops = %v, want the purge before the insert", ops)
	}
}

func TestClaimPropagatesAStoreFailure(t *testing.T) {
	log := newTxLog()
	log.failExec = errors.New("idempotency_keys is unreachable")
	if _, err := (&keyed{route: "r"}).claim(context.Background(), &fakeTx{log: log}, "k1", "h1"); err == nil {
		t.Fatal("claim = nil error, want the store failure")
	}
}

func newKeyed(route string, next http.Handler) *keyed {
	return &keyed{route: route, next: next, bind: func(ctx context.Context, _ pgx.Tx) context.Context { return ctx }}
}

func TestSettleCommitsASuccessAndStoresItsOutcome(t *testing.T) {
	log := newTxLog()
	tx, sp := &fakeTx{log: log}, &fakeTx{log: log, savep: true}
	rec := &capture{header: http.Header{}}
	rec.WriteHeader(http.StatusCreated)
	if err := (&keyed{route: "POST /listings"}).settle(context.Background(), tx, sp, "k1", rec, false); err != nil {
		t.Fatal(err)
	}
	ops := strings.Join(log.snapshot(), ",")
	for _, want := range []string{"sp-commit", "store ", "commit"} {
		if !strings.Contains(ops, want) {
			t.Fatalf("ops = %v, want %q recorded", ops, want)
		}
	}
	if out := log.outcomes["POST /listings\x00k1"]; out.status != http.StatusCreated {
		t.Fatalf("stored outcome = %+v, want the 201", out)
	}
}

func TestSettleDropsTheWritesOfA4xxButStillStoresIt(t *testing.T) {
	log := newTxLog()
	tx, sp := &fakeTx{log: log}, &fakeTx{log: log, savep: true}
	rec := &capture{header: http.Header{}}
	rec.WriteHeader(http.StatusUnprocessableEntity)
	if err := (&keyed{route: "POST /listings"}).settle(context.Background(), tx, sp, "k1", rec, false); err != nil {
		t.Fatal(err)
	}
	ops := strings.Join(log.snapshot(), ",")
	if !strings.Contains(ops, "sp-rollback") || !strings.Contains(ops, "store ") || strings.Contains(ops, "sp-commit") {
		t.Fatalf("ops = %v, want the savepoint rolled back and the outcome stored", ops)
	}
}

func TestSettleKeepsTheWritesOfA4xxTheHandlerAskedToKeep(t *testing.T) {
	log := newTxLog()
	tx, sp := &fakeTx{log: log}, &fakeTx{log: log, savep: true}
	rec := &capture{header: http.Header{}}
	rec.WriteHeader(http.StatusUnprocessableEntity)
	if err := (&keyed{route: "POST /inventory-feeds"}).settle(context.Background(), tx, sp, "k1", rec, true); err != nil {
		t.Fatal(err)
	}
	if ops := strings.Join(log.snapshot(), ","); !strings.Contains(ops, "sp-commit") {
		t.Fatalf("ops = %v, want the savepoint kept (keepWrites)", ops)
	}
}

func TestSettleRefusesToCommitWhenStoringFails(t *testing.T) {
	log := newTxLog()
	log.failExec = errors.New("lost the connection")
	tx, sp := &fakeTx{log: log}, &fakeTx{log: log, savep: true}
	rec := &capture{header: http.Header{}}
	rec.WriteHeader(http.StatusCreated)
	if err := (&keyed{route: "r"}).settle(context.Background(), tx, sp, "k1", rec, false); err == nil {
		t.Fatal("settle = nil, want the store failure")
	}
	if ops := strings.Join(log.snapshot(), ","); strings.Contains(ops, "commit") && !strings.Contains(ops, "sp-commit") {
		t.Fatalf("ops = %v, the outer transaction must not commit", ops)
	}
}

func TestRunFreshAnswersStoresAndFlushes(t *testing.T) {
	log := newTxLog()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentJSON)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"listingId":"lst-1"}`))
	})
	k := newKeyed("POST /listings", next)
	out := httptest.NewRecorder()
	k.runFresh(context.Background(), out, httptest.NewRequest(http.MethodPost, "/listings", nil), &fakeTx{log: log}, "k1")

	if out.Code != http.StatusCreated || !strings.Contains(out.Body.String(), "lst-1") {
		t.Fatalf("response = %d %s, want the captured 201 flushed", out.Code, out.Body)
	}
	ops := strings.Join(log.snapshot(), ",")
	for _, want := range []string{"begin", "sp-commit", "store POST /listings\x00k1 201", "commit"} {
		if !strings.Contains(ops, want) {
			t.Fatalf("ops = %v, want %q", ops, want)
		}
	}
}

func TestRunFreshKeepsA4xxTheHandlerAskedToKeep(t *testing.T) {
	log := newTxLog()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keepWrites(r.Context()) // what applyInventoryFeed does for a FAILED feed
		w.WriteHeader(http.StatusUnprocessableEntity)
	})
	out := httptest.NewRecorder()
	newKeyed("POST /inventory-feeds", next).runFresh(
		context.Background(), out, httptest.NewRequest(http.MethodPost, "/inventory-feeds", nil), &fakeTx{log: log}, "k1")

	if out.Code != http.StatusUnprocessableEntity {
		t.Fatalf("response = %d, want 422", out.Code)
	}
	ops := strings.Join(log.snapshot(), ",")
	if !strings.Contains(ops, "sp-commit") || !strings.Contains(ops, "store POST /inventory-feeds\x00k1 422") {
		t.Fatalf("ops = %v, want the writes kept and the 422 stored", ops)
	}
}

func TestRunFreshNeverStoresAServerFailure(t *testing.T) {
	log := newTxLog()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	out := httptest.NewRecorder()
	newKeyed("POST /listings", next).runFresh(
		context.Background(), out, httptest.NewRequest(http.MethodPost, "/listings", nil), &fakeTx{log: log}, "k1")

	if out.Code != http.StatusInternalServerError {
		t.Fatalf("response = %d, want the 500 flushed", out.Code)
	}
	ops := strings.Join(log.snapshot(), ",")
	if strings.Contains(ops, "store ") || strings.Contains(ops, "commit") && !strings.Contains(ops, "sp-commit") {
		t.Fatalf("ops = %v, a 5xx must never be stored or committed", ops)
	}
	if !strings.Contains(ops, "rollback") {
		t.Fatalf("ops = %v, want the transaction rolled back", ops)
	}
}

func TestRunFreshPropagatesAPanicAndRollsBack(t *testing.T) {
	log := newTxLog()
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("handler exploded") })
	defer func() {
		if p := recover(); p == nil {
			t.Fatal("the panic did not propagate")
		}
		ops := strings.Join(log.snapshot(), ",")
		if !strings.Contains(ops, "sp-rollback") || !strings.Contains(ops, "rollback") || strings.Contains(ops, "store ") {
			t.Fatalf("ops = %v, want both rollbacks and no stored outcome", ops)
		}
	}()
	newKeyed("POST /listings", next).runFresh(
		context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/listings", nil), &fakeTx{log: log}, "k1")
}

func TestRunFreshAnswers500WhenTheSavepointCannotBeOpened(t *testing.T) {
	log := newTxLog()
	log.failBegin = true
	out := httptest.NewRecorder()
	newKeyed("POST /listings", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("the handler must not run") })).
		runFresh(context.Background(), out, httptest.NewRequest(http.MethodPost, "/listings", nil), &fakeTx{log: log}, "k1")
	if out.Code != http.StatusInternalServerError {
		t.Fatalf("response = %d, want 500", out.Code)
	}
}

func TestRequireIdempotencyKeyBuildsTheMiddlewareWithoutDialling(t *testing.T) {
	for _, route := range []string{"POST /catalog-items", "POST /listings", "POST /inventory-feeds"} {
		h := RequireIdempotencyKey(nil, nil)(route)
		if h == nil {
			t.Fatalf("no middleware for %s", route)
		}
	}
}
