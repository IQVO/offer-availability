package usecases

import (
	"context"

	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/nodestatus"
	"github.com/claudioed/offer-availability/internal/domain/sellerstatus"
)

// The three causes that can keep a listing off the shelf (ADR 0013) are read
// from the local, event-fed views and the catalogue, never from a sibling.

// nodeActive reports whether the local view says the node is ACTIVE; an
// unknown node is not sellable (ADR 0002, A5).
func (w writer) nodeActive(ctx context.Context, node listing.NodeID) (bool, error) {
	v, err := w.Nodes.Get(ctx, nodestatus.ID(node))
	hit, err := found(err)
	if err != nil || !hit {
		return false, err
	}
	return v.Sellable(), nil
}

// ownerSuspended reports whether the local SellerStatusView says the owner is
// SUSPENDED. An operator-owned listing has no owner and an unknown seller is
// ACTIVE (ADR 0013, rule 2).
func (w writer) ownerSuspended(ctx context.Context, owner listing.OwnerRef) (bool, error) {
	if owner == "" {
		return false, nil
	}
	v, err := w.Sellers.Get(ctx, sellerstatus.ID(owner))
	hit, err := found(err)
	if err != nil || !hit {
		return false, err
	}
	return v.Suspended(), nil
}

// itemDiscontinued reports whether the catalogue item is DISCONTINUED; an item
// that is not stored is not (a listing always names a stored item).
func (w writer) itemDiscontinued(ctx context.Context, item listing.ItemID) (bool, error) {
	i, err := w.Catalog.Get(ctx, catalog.ID(item))
	hit, err := found(err)
	if err != nil || !hit {
		return false, err
	}
	return i.Discontinued(), nil
}

// sellability reads the three causes of one listing.
func (w writer) sellability(ctx context.Context, l *listing.Listing) (listing.Sellability, error) {
	active, err := w.nodeActive(ctx, l.NodeID())
	if err != nil {
		return listing.Sellability{}, err
	}
	discontinued, err := w.itemDiscontinued(ctx, l.ItemID())
	if err != nil {
		return listing.Sellability{}, err
	}
	suspended, err := w.ownerSuspended(ctx, l.OwnerRef())
	if err != nil {
		return listing.Sellability{}, err
	}
	return listing.Sellability{NodeActive: active, ItemDiscontinued: discontinued, OwnerSuspended: suspended}, nil
}
