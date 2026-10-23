package repocontract

import (
	"context"
	"errors"
	"testing"

	"github.com/claudioed/offer-availability/internal/domain/catalog"
)

var unitOfWorkCases = []named{
	{"a returned nil commits every save", uowCommit},
	{"a returned error rolls every save back and is returned unchanged", uowRollback},
	{"a panic rolls back and propagates", uowPanic},
	{"a nested Do joins the outer unit", uowNested},
	{"the outbox commits and rolls back with the aggregates", uowOutbox},
}

var errBoom = errors.New("boom")

// twoItems saves two catalogue items through the repository on ctx.
func twoItems(t *testing.T, f Fixture, ctx context.Context) error {
	t.Helper()
	if err := f.Catalog.Save(ctx, NewItem(t, "item-1")); err != nil {
		return err
	}
	return f.Catalog.Save(ctx, NewItem(t, "item-2"))
}

func itemStored(f Fixture, id catalog.ID) bool {
	_, err := f.Catalog.Get(context.Background(), id)
	return err == nil
}

func uowCommit(t *testing.T, f Fixture) {
	noErr(t, f.UoW.Do(context.Background(), func(ctx context.Context) error { return twoItems(t, f, ctx) }))
	if !itemStored(f, "item-1") || !itemStored(f, "item-2") {
		t.Fatal("a committed unit lost a save")
	}
}

func uowRollback(t *testing.T, f Fixture) {
	err := f.UoW.Do(context.Background(), func(ctx context.Context) error {
		noErr(t, twoItems(t, f, ctx))
		return errBoom
	})
	wantErr(t, err, errBoom)
	if itemStored(f, "item-1") || itemStored(f, "item-2") {
		t.Fatal("a failed unit left a save behind")
	}
}

func uowPanic(t *testing.T, f Fixture) {
	defer func() {
		if recover() == nil {
			t.Fatal("the panic was swallowed")
		}
		if itemStored(f, "item-1") {
			t.Fatal("a panicking unit left a save behind")
		}
	}()
	_ = f.UoW.Do(context.Background(), func(ctx context.Context) error {
		noErr(t, f.Catalog.Save(ctx, NewItem(t, "item-1")))
		panic("kaboom")
	})
}

func uowNested(t *testing.T, f Fixture) {
	err := f.UoW.Do(context.Background(), func(ctx context.Context) error {
		noErr(t, f.UoW.Do(ctx, func(ctx context.Context) error {
			return f.Catalog.Save(ctx, NewItem(t, "item-1"))
		}))
		return errBoom
	})
	wantErr(t, err, errBoom)
	if itemStored(f, "item-1") {
		t.Fatal("the inner unit committed on its own")
	}
}

func uowOutbox(t *testing.T, f Fixture) {
	if f.OutboxCount == nil {
		t.Skip("the fixture cannot count its outbox")
	}
	ctx := context.Background()
	item := NewItem(t, "item-1")
	events := SomeEvents(t)
	failed := f.UoW.Do(ctx, func(ctx context.Context) error {
		noErr(t, f.Catalog.Save(ctx, item))
		noErr(t, f.Outbox.Append(ctx, events...))
		return errBoom
	})
	wantErr(t, failed, errBoom)
	assertOutbox(t, f, 0)
	noErr(t, f.UoW.Do(ctx, func(ctx context.Context) error {
		noErr(t, f.Catalog.Save(ctx, item))
		return f.Outbox.Append(ctx, events...)
	}))
	assertOutbox(t, f, len(events))
}

func assertOutbox(t *testing.T, f Fixture, want int) {
	t.Helper()
	got, err := f.OutboxCount(context.Background())
	noErr(t, err)
	if got != want {
		t.Fatalf("outbox holds %d events, want %d", got, want)
	}
}
