package usecases_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

func intp(n int) *int { return &n }

func seedItems(e *env, n int) {
	for i := 1; i <= n; i++ {
		e.seedItem(catalogIDf(i))
	}
}

func TestBrowseCatalogPages(t *testing.T) {
	e := newEnv(t)
	seedItems(e, 5)
	q := usecases.NewBrowseCatalog(e.deps.Catalog)
	first, err := q.Execute(context.Background(), usecases.PageQuery{Limit: intp(2)})
	mustNot(t, err)
	if len(first.Items) != 2 || first.Items[0].CatalogItemID != "item-1" || first.NextCursor != "item-2" {
		t.Fatalf("first page = %+v", first)
	}
	second, err := q.Execute(context.Background(), usecases.PageQuery{Limit: intp(2), After: first.NextCursor})
	mustNot(t, err)
	if len(second.Items) != 2 || second.Items[0].CatalogItemID != "item-3" || second.NextCursor != "item-4" {
		t.Fatalf("second page = %+v", second)
	}
	last, err := q.Execute(context.Background(), usecases.PageQuery{Limit: intp(2), After: second.NextCursor})
	mustNot(t, err)
	if len(last.Items) != 1 || last.NextCursor != "" {
		t.Fatalf("last page = %+v", last)
	}
	exact, err := q.Execute(context.Background(), usecases.PageQuery{Limit: intp(5)})
	mustNot(t, err)
	if len(exact.Items) != 5 || exact.NextCursor != "" {
		t.Fatalf("a page that ends exactly has no cursor: %+v", exact)
	}
}

func TestBrowseCatalogDefaultsAndBounds(t *testing.T) {
	e := newEnv(t)
	seedItems(e, 3)
	q := usecases.NewBrowseCatalog(e.deps.Catalog)
	got, err := q.Execute(context.Background(), usecases.PageQuery{})
	mustNot(t, err)
	if len(got.Items) != 3 || got.NextCursor != "" {
		t.Fatalf("default page = %+v", got)
	}
	empty, err := usecases.NewBrowseCatalog(newEnv(t).deps.Catalog).Execute(context.Background(), usecases.PageQuery{})
	mustNot(t, err)
	if empty.Items == nil || len(empty.Items) != 0 {
		t.Fatalf("an empty catalogue is an empty list, got %#v", empty.Items)
	}
	for _, bad := range []int{0, -1, usecases.MaxLimit + 1} {
		_, err := q.Execute(context.Background(), usecases.PageQuery{Limit: intp(bad)})
		wantIs(t, err, shared.ErrValidation)
	}
	for _, ok := range []int{1, usecases.MaxLimit} {
		_, err := q.Execute(context.Background(), usecases.PageQuery{Limit: intp(ok)})
		mustNot(t, err)
	}
	e.catalog.listErr = errBoom
	_, err = q.Execute(context.Background(), usecases.PageQuery{})
	wantIs(t, err, errBoom)
}

func TestGetCatalogItem(t *testing.T) {
	e := newEnv(t)
	e.seedItem("item-1")
	q := usecases.NewGetCatalogItem(e.deps.Catalog)
	got, err := q.Execute(context.Background(), "item-1")
	mustNot(t, err)
	if got.CatalogItemID != "item-1" || got.State != "ACTIVE" {
		t.Fatalf("view = %+v", got)
	}
	_, err = q.Execute(context.Background(), "item-9")
	wantIs(t, err, ports.ErrNotFound)
}

func TestListListings(t *testing.T) {
	e := newEnv(t)
	e.seedListing("lst-1", "item-1", "node-1", 5, repoNow())
	e.seedListing("lst-2", "item-2", "node-1", -1, repoNow())
	e.seedListing("lst-3", "item-1", "node-2", -1, repoNow())
	q := usecases.NewListListings(e.deps.Listings)
	ids := func(query usecases.ListListingsQuery) ([]string, string) {
		page, err := q.Execute(context.Background(), query)
		mustNot(t, err)
		out := []string{}
		for _, l := range page.Listings {
			out = append(out, l.ListingID)
		}
		return out, page.NextCursor
	}
	if got, _ := ids(usecases.ListListingsQuery{}); fmt.Sprint(got) != "[lst-1 lst-2 lst-3]" {
		t.Fatalf("all = %v", got)
	}
	if got, _ := ids(usecases.ListListingsQuery{NodeID: "node-1"}); fmt.Sprint(got) != "[lst-1 lst-2]" {
		t.Fatalf("node = %v", got)
	}
	if got, _ := ids(usecases.ListListingsQuery{CatalogItemID: "item-1", NodeID: "node-2"}); fmt.Sprint(got) != "[lst-3]" {
		t.Fatalf("item and node = %v", got)
	}
	if got, cursor := ids(usecases.ListListingsQuery{Page: usecases.PageQuery{Limit: intp(2)}}); fmt.Sprint(got) != "[lst-1 lst-2]" || cursor != "lst-2" {
		t.Fatalf("first page = %v %q", got, cursor)
	}
	if got, cursor := ids(usecases.ListListingsQuery{Page: usecases.PageQuery{Limit: intp(2), After: "lst-2"}}); fmt.Sprint(got) != "[lst-3]" || cursor != "" {
		t.Fatalf("second page = %v %q", got, cursor)
	}
	if got, _ := ids(usecases.ListListingsQuery{NodeID: "node-9"}); got == nil || len(got) != 0 {
		t.Fatalf("no match = %#v", got)
	}
	_, err := q.Execute(context.Background(), usecases.ListListingsQuery{Page: usecases.PageQuery{Limit: intp(0)}})
	wantIs(t, err, shared.ErrValidation)
	e.listings.listErr = errBoom
	_, err = q.Execute(context.Background(), usecases.ListListingsQuery{})
	wantIs(t, err, errBoom)
}

func TestListingViewCarriesTheAvailability(t *testing.T) {
	e := newEnv(t)
	e.seedListing("lst-1", "item-1", "node-1", 5, repoNow())
	l := e.listing("lst-1")
	_, err := l.Suppress(listing.ReasonDiscontinued, repoNow())
	mustNot(t, err)
	mustNot(t, e.store.Listings().Save(context.Background(), l))
	page, err := usecases.NewListListings(e.deps.Listings).Execute(context.Background(), usecases.ListListingsQuery{})
	mustNot(t, err)
	v := page.Listings[0]
	if v.State != "SUPPRESSED" || v.SuppressionReason != "DISCONTINUED" || v.Availability.AvailableQty != 5 ||
		v.Availability.Basis != "PHYSICAL" || v.Availability.AsOf == nil || !v.Availability.AsOf.Equal(repoNow()) || v.Availability.FeedID != "feed-0" {
		t.Fatalf("view = %+v", v)
	}
}

func TestGetInventoryFeed(t *testing.T) {
	e := feedEnv(t)
	_, err := apply(e, feedCmd("feed-1", false, asOf1, ln("lst-a", 5)))
	mustNot(t, err)
	q := usecases.NewGetInventoryFeed(e.deps.Feeds)
	got, err := q.Execute(context.Background(), "feed-1")
	mustNot(t, err)
	if got.State != "APPLIED" || got.AppliedAt == nil || !got.AppliedAt.Equal(repoNow()) || got.AppliedCount != 1 || got.LineCount != 1 ||
		got.NodeID != "node-1" || !got.AsOf.Equal(asOf1) || !got.ReceivedAt.Equal(repoNow()) {
		t.Fatalf("view = %+v", got)
	}
	_, err = q.Execute(context.Background(), "feed-9")
	wantIs(t, err, ports.ErrNotFound)
}

func catalogIDf(i int) catalog.ID { return catalog.ID(fmt.Sprintf("item-%d", i)) }
