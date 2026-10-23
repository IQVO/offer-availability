package ports

import "errors"

var (
	// ErrNotFound is what every repository returns for a Get or Find that
	// matches nothing (problem type not-found, 404). An adapter wraps its own
	// miss (pgx.ErrNoRows, a missing map key) into this sentinel, so callers
	// test errors.Is(err, ports.ErrNotFound) and nothing else.
	ErrNotFound = errors.New("not found")
	// ErrConcurrentUpdate is what Save returns when the stored version is not
	// the one the aggregate was loaded at (problem type concurrent-update,
	// 409): the expectation is `stored version == aggregate.Version()-1`, and
	// a create (Version() 1) expects that the id is not stored yet.
	ErrConcurrentUpdate = errors.New("concurrent update")
)
