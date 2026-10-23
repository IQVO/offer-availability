package repocontract

import (
	"context"
	"reflect"
	"testing"

	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/application/repository"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
)

var catalogCases = []named{
	{"save then get round-trips the snapshot", catalogRoundTrip},
	{"get of an unknown id is ErrNotFound", catalogGetMiss},
	{"creating an id that exists is ErrConcurrentUpdate", catalogDuplicateID},
	{"an update expects the stored version to be version-1", catalogUpdateVersions},
	{"list is empty, never nil, on an empty database", catalogListEmpty},
	{"list pages by id ascending", catalogListPages},
}

func catalogRoundTrip(t *testing.T, f Fixture) {
	ctx := context.Background()
	it := NewItem(t, "item-1")
	noErr(t, f.Catalog.Save(ctx, it))
	got, err := f.Catalog.Get(ctx, "item-1")
	noErr(t, err)
	if !reflect.DeepEqual(got.Snapshot(), it.Snapshot()) {
		t.Fatalf("snapshot = %+v, want %+v", got.Snapshot(), it.Snapshot())
	}
}

func catalogGetMiss(t *testing.T, f Fixture) {
	_, err := f.Catalog.Get(context.Background(), "nope")
	wantErr(t, err, ports.ErrNotFound)
}

func catalogDuplicateID(t *testing.T, f Fixture) {
	ctx := context.Background()
	noErr(t, f.Catalog.Save(ctx, NewItem(t, "item-1")))
	wantErr(t, f.Catalog.Save(ctx, NewItem(t, "item-1")), ports.ErrConcurrentUpdate)
}

func catalogUpdateVersions(t *testing.T, f Fixture) {
	ctx := context.Background()
	noErr(t, f.Catalog.Save(ctx, NewItem(t, "item-1")))
	loaded, err := f.Catalog.Get(ctx, "item-1")
	noErr(t, err)
	stale, err := f.Catalog.Get(ctx, "item-1")
	noErr(t, err)
	loaded.Discontinue(Now)
	noErr(t, f.Catalog.Save(ctx, loaded))
	stale.Discontinue(Now)
	wantErr(t, f.Catalog.Save(ctx, stale), ports.ErrConcurrentUpdate)
	got, err := f.Catalog.Get(ctx, "item-1")
	noErr(t, err)
	if got.State() != catalog.StateDiscontinued || got.Version() != 2 {
		t.Fatalf("stored item = %s v%d", got.State(), got.Version())
	}
	ghost := NewItem(t, "item-ghost")
	ghost.Discontinue(Now)
	wantErr(t, f.Catalog.Save(ctx, ghost), ports.ErrConcurrentUpdate)
}

func catalogListEmpty(t *testing.T, f Fixture) {
	got, err := f.Catalog.List(context.Background(), repository.Page{Limit: 10})
	noErr(t, err)
	if got == nil || len(got) != 0 {
		t.Fatalf("list = %#v, want a non-nil empty list", got)
	}
}

func catalogListPages(t *testing.T, f Fixture) {
	ctx := context.Background()
	for _, id := range []catalog.ID{"item-c", "item-a", "item-d", "item-b"} {
		noErr(t, f.Catalog.Save(ctx, NewItem(t, id)))
	}
	ids := func(p repository.Page) []catalog.ID {
		rows, err := f.Catalog.List(ctx, p)
		noErr(t, err)
		out := []catalog.ID{}
		for _, r := range rows {
			out = append(out, r.ID())
		}
		return out
	}
	if got, want := ids(repository.Page{Limit: 10}), []catalog.ID{"item-a", "item-b", "item-c", "item-d"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("all = %v, want %v", got, want)
	}
	if got, want := ids(repository.Page{Limit: 2}), []catalog.ID{"item-a", "item-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first page = %v, want %v", got, want)
	}
	if got, want := ids(repository.Page{After: "item-b", Limit: 2}), []catalog.ID{"item-c", "item-d"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second page = %v, want %v", got, want)
	}
	if got := ids(repository.Page{After: "item-d", Limit: 2}); len(got) != 0 {
		t.Fatalf("past the end = %v, want none", got)
	}
}
