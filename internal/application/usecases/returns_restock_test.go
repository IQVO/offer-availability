package usecases_test

import (
	"context"
	"errors"
	"testing"

	"github.com/claudioed/offer-availability/internal/adapters/outbound/repocontract"
	"github.com/claudioed/offer-availability/internal/application/ports"
	"github.com/claudioed/offer-availability/internal/application/usecases"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/nodestatus"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// ADR 0015: ReturnCompleted restocks the owner listings of a closed return.

const typeReturnCompleted = "com.retailnetwork.commerce.customer-returns.returnauthorization.ReturnCompleted"

// recordedReturnSkips is a ports.ReturnRestockMetrics that keeps the reasons.
type recordedReturnSkips struct {
	reasons []string
}

func (r *recordedReturnSkips) ReturnSkipped(_ context.Context, _, reason string) {
	r.reasons = append(r.reasons, reason)
}

func completion(id, authorization string, node listing.NodeID, lines ...usecases.ReturnLine) usecases.ReturnCompletionEvent {
	return usecases.ReturnCompletionEvent{
		EventID: id, EventType: typeReturnCompleted, AuthorizationID: authorization, NodeID: node,
		ReceivedAt: asOf2, LinesPresent: true, Lines: lines,
	}
}

func returned(item catalog.ID, owner listing.OwnerRef, restockable, unsellable int) usecases.ReturnLine {
	return usecases.ReturnLine{CatalogItemID: item, OwnerRef: owner, QtyRestockable: restockable, QtyUnsellable: unsellable}
}

func handleReturn(e *env, ev usecases.ReturnCompletionEvent) (usecases.ReturnCompletionResult, error) {
	return usecases.NewApplyReturnCompletion(e.deps).Execute(context.Background(), ev)
}

// returnsEnv: node-1 with item-1/item-2; lst-1 is seller-1's fed (10 PHYSICAL)
// listing of item-1 on node-1; lst-2 is the operator's never-fed listing of
// item-2 on node-1; nothing exists on node-2.
func returnsEnv(t *testing.T) (*env, *recordedReturnSkips) {
	t.Helper()
	e := newEnv(t)
	e.seedNode("node-1", nodestatus.StatusActive)
	e.seedItem("item-1")
	e.seedItem("item-2")
	l1 := repocontract.NewOwnedListing(t, "lst-1", "item-1", "node-1", "seller-1", listing.Sellability{NodeActive: true})
	mustNot(t, e.store.Listings().Save(context.Background(), l1))
	mustNot(t, e.store.Listings().Save(context.Background(), repocontract.Fed(t, l1, 10, listing.BasisPhysical, asOf1)))
	l2 := repocontract.NewOwnedListing(t, "lst-2", "item-2", "node-1", "", listing.Sellability{NodeActive: true})
	mustNot(t, e.store.Listings().Save(context.Background(), l2))
	rec := &recordedReturnSkips{}
	e.deps.ReturnMetrics = rec
	return e, rec
}

func TestReturnCompletionRestocksOwnerListings(t *testing.T) {
	e, rec := returnsEnv(t)
	res, err := handleReturn(e, completion("evt-1", "ret-1", "node-1",
		returned("item-1", "seller-1", 2, 1), returned("item-2", "", 3, 0)))
	mustNot(t, err)
	if len(res.Restocked) != 2 || res.Restocked[0].ListingID != "lst-1" || res.Restocked[1].ListingID != "lst-2" ||
		res.Restocked[0].AddedQty != 2 || res.Restocked[1].AddedQty != 3 {
		t.Fatalf("restocked = %+v", res.Restocked)
	}
	if len(res.Skipped) != 0 || len(rec.reasons) != 0 {
		t.Fatalf("skipped = %+v, counted %v", res.Skipped, rec.reasons)
	}
	l1 := e.ownedListing("item-1", "node-1", "seller-1")
	if got := l1.Availability(); got.Qty != 12 || got.Basis != listing.BasisPhysical || got.FeedID != "ret-1" || !got.AsOf.Equal(asOf2) {
		t.Fatalf("lst-1 availability = %+v", got)
	}
	l2 := e.ownedListing("item-2", "node-1", "")
	if got := l2.Availability(); got.Qty != 3 || got.Basis != listing.BasisUnknown {
		t.Fatalf("lst-2 availability = %+v", got)
	}
	e.assertEvents("ListingAvailabilityChanged", "ListingAvailabilityChanged")
}

func TestReturnCompletionIsInboxIdempotent(t *testing.T) {
	e, _ := returnsEnv(t)
	ev := completion("evt-1", "ret-1", "node-1", returned("item-1", "seller-1", 2, 0))
	if _, err := handleReturn(e, ev); err != nil {
		t.Fatal(err)
	}
	res, err := handleReturn(e, ev) // the same CloudEvents id, redelivered
	mustNot(t, err)
	if !res.Duplicate || len(res.Restocked) != 0 {
		t.Fatalf("redelivery = %+v, want the duplicate no-op", res)
	}
	if got := e.ownedListing("item-1", "node-1", "seller-1").Availability().Qty; got != 12 {
		t.Fatalf("qty after replay = %d, want 12", got)
	}
}

func TestReturnCompletionCountsWhatItCannotApply(t *testing.T) {
	e, rec := returnsEnv(t)
	res, err := handleReturn(e, completion("evt-2", "ret-2", "node-1",
		returned("item-9", "seller-1", 2, 0),  // no listing of that triple
		returned("item-1", "seller-9", 2, 0),  // no listing for that owner
		returned("", "seller-1", 2, 0),        // no catalogItemId
		returned("item-1", "seller-1", 0, 4),  // unsellable only
		returned("item-1", "seller-1", 2, 0))) // restocks
	mustNot(t, err)
	want := []string{usecases.SkipListingUnknown, usecases.SkipListingUnknown, usecases.SkipItemAbsent, usecases.SkipNothingRestockable}
	if len(res.Skipped) != len(want) {
		t.Fatalf("skipped = %+v", res.Skipped)
	}
	for i, s := range res.Skipped {
		if s.Reason != want[i] {
			t.Fatalf("skip %d = %s, want %s", i, s.Reason, want[i])
		}
	}
	if len(rec.reasons) != len(want) || len(res.Restocked) != 1 {
		t.Fatalf("counted %v, restocked %+v", rec.reasons, res.Restocked)
	}
	if got := e.ownedListing("item-1", "node-1", "seller-1").Availability().Qty; got != 12 {
		t.Fatalf("qty = %d, want 12 (only the restockable line applied)", got)
	}
}

func TestReturnCompletionWithoutLines(t *testing.T) {
	e, rec := returnsEnv(t)
	res, err := handleReturn(e, usecases.ReturnCompletionEvent{
		EventID: "evt-3", EventType: typeReturnCompleted, AuthorizationID: "ret-3",
		NodeID: "node-1", ReceivedAt: asOf2, LinesPresent: false,
	})
	mustNot(t, err)
	if !res.LinesAbsent || len(res.Restocked) != 0 || len(rec.reasons) != 1 || rec.reasons[0] != usecases.SkipLinesAbsent {
		t.Fatalf("res = %+v, counted %v", res, rec.reasons)
	}
}

func TestReturnCompletionRefusesMalformedEvents(t *testing.T) {
	cases := []struct {
		name string
		ev   usecases.ReturnCompletionEvent
	}{
		{"no CloudEvents id", usecases.ReturnCompletionEvent{EventType: typeReturnCompleted, AuthorizationID: "ret-1", NodeID: "node-1", ReceivedAt: asOf2}},
		{"no type", usecases.ReturnCompletionEvent{EventID: "evt-4", AuthorizationID: "ret-1", NodeID: "node-1", ReceivedAt: asOf2}},
		{"no authorizationId", completion("evt-4", "", "node-1")},
		{"malformed authorizationId", completion("evt-4", "ret 1", "node-1")},
		{"no nodeId", completion("evt-4", "ret-1", "")},
		{"malformed nodeId", completion("evt-4", "ret-1", "no de")},
		{"no instant", usecases.ReturnCompletionEvent{EventID: "evt-4", EventType: typeReturnCompleted, AuthorizationID: "ret-1", NodeID: "node-1", LinesPresent: true}},
	}
	for _, c := range cases {
		e, _ := returnsEnv(t)
		before := e.store.Counts()
		_, err := handleReturn(e, c.ev)
		if !errors.Is(err, shared.ErrValidation) {
			t.Errorf("%s: want ErrValidation, got %v", c.name, err)
		}
		e.assertNothingStored(before)
	}
}

var _ ports.ReturnRestockMetrics = (*recordedReturnSkips)(nil)
