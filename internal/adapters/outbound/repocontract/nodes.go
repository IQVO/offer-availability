package repocontract

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/nodestatus"
)

var markCases = []named{
	{"a node without an applied feed has the zero mark", markZero},
	{"advance raises the mark and never lowers it", markAdvance},
	{"marks are per node", markPerNode},
}

var nodeStatusCases = []named{
	{"save then get round-trips the snapshot", nodeRoundTrip},
	{"get of an unknown id is ErrNotFound", nodeGetMiss},
	{"creating an id that exists is ErrConcurrentUpdate", nodeDuplicateID},
	{"an update expects the stored version to be version-1", nodeUpdateVersions},
}

var inboxCases = []named{
	{"the first sight of an id is new, the second is not", inboxOnce},
	{"ids are independent", inboxIndependent},
	{"a rolled back unit forgets the id", inboxRollback},
}

// inUnit runs fn inside a unit of work and fails the test on its error.
func inUnit(t *testing.T, f Fixture, fn func(ctx context.Context) error) {
	t.Helper()
	noErr(t, f.UoW.Do(context.Background(), fn))
}

func lockMark(t *testing.T, f Fixture, node listing.NodeID) time.Time {
	t.Helper()
	var mark time.Time
	inUnit(t, f, func(ctx context.Context) error {
		var err error
		mark, err = f.Marks.Lock(ctx, node)
		return err
	})
	return mark
}

func advanceMark(t *testing.T, f Fixture, node listing.NodeID, asOf time.Time) {
	t.Helper()
	inUnit(t, f, func(ctx context.Context) error {
		if _, err := f.Marks.Lock(ctx, node); err != nil {
			return err
		}
		return f.Marks.Advance(ctx, node, asOf)
	})
}

func markZero(t *testing.T, f Fixture) {
	if mark := lockMark(t, f, "node-1"); !mark.IsZero() {
		t.Fatalf("mark = %v, want the zero time", mark)
	}
}

func markAdvance(t *testing.T, f Fixture) {
	steps := []struct {
		advance time.Time
		want    time.Time
	}{
		{Now, Now},
		{Now.Add(time.Hour), Now.Add(time.Hour)},
		{Now.Add(time.Minute), Now.Add(time.Hour)},
		{Now.Add(time.Hour), Now.Add(time.Hour)},
	}
	for i, s := range steps {
		advanceMark(t, f, "node-1", s.advance)
		if got := lockMark(t, f, "node-1"); !got.Equal(s.want) {
			t.Fatalf("step %d: mark = %v, want %v", i, got, s.want)
		}
	}
}

func markPerNode(t *testing.T, f Fixture) {
	advanceMark(t, f, "node-1", Now.Add(time.Hour))
	if got := lockMark(t, f, "node-2"); !got.IsZero() {
		t.Fatalf("node-2 mark = %v, want the zero time", got)
	}
}

func nodeRoundTrip(t *testing.T, f Fixture) {
	ctx := context.Background()
	v := NewNodeView(t, "node-1", nodestatus.StatusProposed)
	noErr(t, f.Nodes.Save(ctx, v))
	got, err := f.Nodes.Get(ctx, "node-1")
	noErr(t, err)
	if !reflect.DeepEqual(got.Snapshot(), v.Snapshot()) {
		t.Fatalf("snapshot = %+v, want %+v", got.Snapshot(), v.Snapshot())
	}
}

func nodeGetMiss(t *testing.T, f Fixture) {
	_, err := f.Nodes.Get(context.Background(), "nope")
	wantErr(t, err, ports.ErrNotFound)
}

func nodeDuplicateID(t *testing.T, f Fixture) {
	ctx := context.Background()
	noErr(t, f.Nodes.Save(ctx, NewNodeView(t, "node-1", nodestatus.StatusProposed)))
	wantErr(t, f.Nodes.Save(ctx, NewNodeView(t, "node-1", nodestatus.StatusActive)), ports.ErrConcurrentUpdate)
}

func nodeUpdateVersions(t *testing.T, f Fixture) {
	ctx := context.Background()
	noErr(t, f.Nodes.Save(ctx, NewNodeView(t, "node-1", nodestatus.StatusProposed)))
	a, err := f.Nodes.Get(ctx, "node-1")
	noErr(t, err)
	b, err := f.Nodes.Get(ctx, "node-1")
	noErr(t, err)
	_, err = a.Observe(nodestatus.StatusActive, Now.Add(time.Hour))
	noErr(t, err)
	noErr(t, f.Nodes.Save(ctx, a))
	_, err = b.Observe(nodestatus.StatusSuspended, Now.Add(time.Hour))
	noErr(t, err)
	wantErr(t, f.Nodes.Save(ctx, b), ports.ErrConcurrentUpdate)
	got, err := f.Nodes.Get(ctx, "node-1")
	noErr(t, err)
	if got.Status() != nodestatus.StatusActive || got.Version() != 2 {
		t.Fatalf("stored view = %s v%d", got.Status(), got.Version())
	}
}

func markProcessed(t *testing.T, f Fixture, ctx context.Context, id string) bool {
	t.Helper()
	first, err := f.Inbox.MarkProcessed(ctx, id, "com.example.Type", Now)
	noErr(t, err)
	return first
}

func inboxOnce(t *testing.T, f Fixture) {
	inUnit(t, f, func(ctx context.Context) error {
		if !markProcessed(t, f, ctx, "evt-1") {
			t.Error("the first sight must be new")
		}
		if markProcessed(t, f, ctx, "evt-1") {
			t.Error("the second sight must not be new")
		}
		return nil
	})
	inUnit(t, f, func(ctx context.Context) error {
		if markProcessed(t, f, ctx, "evt-1") {
			t.Error("a committed id must stay seen")
		}
		return nil
	})
}

func inboxIndependent(t *testing.T, f Fixture) {
	inUnit(t, f, func(ctx context.Context) error {
		if !markProcessed(t, f, ctx, "evt-1") || !markProcessed(t, f, ctx, "evt-2") {
			t.Error("two different ids must both be new")
		}
		return nil
	})
}

func inboxRollback(t *testing.T, f Fixture) {
	err := f.UoW.Do(context.Background(), func(ctx context.Context) error {
		markProcessed(t, f, ctx, "evt-1")
		return errBoom
	})
	wantErr(t, err, errBoom)
	inUnit(t, f, func(ctx context.Context) error {
		if !markProcessed(t, f, ctx, "evt-1") {
			t.Error("a rolled back id must be new again")
		}
		return nil
	})
}
