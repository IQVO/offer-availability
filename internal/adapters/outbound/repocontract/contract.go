// Package repocontract is the behavioural contract of the repository ports: one
// function, Run, that takes a Factory of a fresh empty fixture and checks every
// promise of internal/application/ports. The in-memory doubles pass it in their
// own tests and the Postgres adapters pass it in the integration tests, so the
// two can never drift. It is test support: cmd/** must never import it.
package repocontract

import (
	"context"
	"errors"
	"testing"

	"github.com/claudioed/offer-availability/internal/application/ports"
)

// Fixture ids and case names shared by more than one contract group: the
// constants keep the literals from drifting (every case seeds the same few
// entities and repeats the same storage promises per aggregate).
const (
	item1 = "item-1"
	item2 = "item-2"
	itemA = "item-a"
	lst1  = "lst-1"
	lst2  = "lst-2"
	lst3  = "lst-3"
	lst4  = "lst-4"
	lstA  = "lst-a"
	lstB  = "lst-b"
	itemB = "item-b"
	itemC = "item-c"
	itemD = "item-d"
	lstC  = "lst-c"
	node1 = "node-1"
	node2 = "node-2"

	caseGetMiss     = "get of an unknown id is ErrNotFound"
	caseDuplicateID = "creating an id that exists is ErrConcurrentUpdate"
	caseVersionRace = "an update expects the stored version to be version-1"
)

// Fixture is one implementation of every port, over ONE empty database.
type Fixture struct {
	Catalog  ports.CatalogRepository
	Listings ports.ListingRepository
	Feeds    ports.FeedRepository
	Marks    ports.NodeFeedMarks
	Nodes    ports.NodeStatusRepository
	Inbox    ports.ProcessedEvents
	Outbox   ports.EventOutbox
	UoW      ports.UnitOfWork
	// OutboxCount returns how many events are stored in the outbox. Nil skips
	// the outbox atomicity checks.
	OutboxCount func(ctx context.Context) (int, error)
}

// Factory returns a fresh, empty Fixture and registers its own cleanup with t.
type Factory func(t *testing.T) Fixture

// named is one contract case.
type named struct {
	name string
	run  func(*testing.T, Fixture)
}

// Run executes the whole contract; every case gets a fresh Fixture.
func Run(t *testing.T, factory Factory) {
	t.Helper()
	groups := []struct {
		name  string
		cases []named
	}{
		{"Catalog", catalogCases},
		{"Packaging", packagingCases},
		{"Listing", listingCases},
		{"Feed", feedCases},
		{"NodeFeedMarks", markCases},
		{"NodeStatus", nodeStatusCases},
		{"Inbox", inboxCases},
		{"UnitOfWork", unitOfWorkCases},
	}
	for _, g := range groups {
		t.Run(g.name, func(t *testing.T) {
			for _, c := range g.cases {
				t.Run(c.name, func(t *testing.T) { c.run(t, factory(t)) })
			}
		})
	}
}

// wantErr fails unless err matches target.
func wantErr(t testing.TB, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want %v", err, target)
	}
}

// noErr fails on err.
func noErr(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
