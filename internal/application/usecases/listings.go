package usecases

import (
	"context"

	"github.com/claudioed/offer-availability/internal/application/views"
	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/listing"
	"github.com/claudioed/offer-availability/internal/domain/nodestatus"
	"github.com/claudioed/offer-availability/internal/domain/shared"
)

// CreateListingCommand is the input of CreateListing.
type CreateListingCommand struct {
	CatalogItemID catalog.ID
	NodeID        listing.NodeID
	// OwnerRef is the optional seller that owns the stock; "" = operator-owned.
	OwnerRef listing.OwnerRef
}

// CreateListing creates the listing of one catalogue item on one node.
type CreateListing struct{ writer }

// NewCreateListing builds the use case.
func NewCreateListing(d Deps) *CreateListing { return &CreateListing{writer{d}} }

// Execute loads the item (unknown is ports.ErrNotFound), refuses a second
// listing for the pair (listing.ErrDuplicateListing), reads the node's status
// from the local NodeStatusView (an unknown node is not sellable, ADR 0002, A5)
// and saves the new listing with ListingCreated and, when it starts
// SUPPRESSED, ListingSuppressed.
func (uc *CreateListing) Execute(ctx context.Context, cmd CreateListingCommand) (views.Listing, error) {
	if !cmd.CatalogItemID.Valid() || !cmd.NodeID.Valid() {
		return views.Listing{}, shared.Invalid("catalogItemId and nodeId must be valid ids")
	}
	if !cmd.OwnerRef.Valid() {
		return views.Listing{}, shared.Invalid("ownerRef must be a valid id")
	}
	return transact(ctx, uc.UoW, func(ctx context.Context) (views.Listing, error) {
		item, err := uc.Catalog.Get(ctx, cmd.CatalogItemID)
		if err != nil {
			return views.Listing{}, err
		}
		itemID := listing.ItemID(cmd.CatalogItemID)
		taken, err := uc.pairTaken(ctx, itemID, cmd.NodeID, cmd.OwnerRef)
		if err != nil {
			return views.Listing{}, err
		}
		if taken {
			return views.Listing{}, listing.ErrDuplicateListing
		}
		active, err := uc.nodeActive(ctx, cmd.NodeID)
		if err != nil {
			return views.Listing{}, err
		}
		l, events, err := listing.CreateOwned(uc.IDs.NewListingID(), itemID, cmd.NodeID, cmd.OwnerRef,
			listing.Sellability{NodeActive: active, ItemDiscontinued: item.Discontinued()}, uc.now())
		if err != nil {
			return views.Listing{}, err
		}
		if err := uc.Listings.Save(ctx, l); err != nil {
			return views.Listing{}, err
		}
		if err := uc.publish(ctx, events); err != nil {
			return views.Listing{}, err
		}
		return views.ListingFrom(l), nil
	})
}

// pairTaken reports whether a listing of (item, node, owner) exists: the
// operator-owned pair for an empty owner, the seller's own otherwise.
func (uc *CreateListing) pairTaken(ctx context.Context, item listing.ItemID, node listing.NodeID, owner listing.OwnerRef) (bool, error) {
	var err error
	if owner == "" {
		_, err = uc.Listings.FindByItemAndNode(ctx, item, node)
	} else {
		_, err = uc.Listings.FindByItemNodeOwner(ctx, item, node, owner)
	}
	return found(err)
}

// nodeActive reports whether the local view says the node is ACTIVE.
func (uc *CreateListing) nodeActive(ctx context.Context, node listing.NodeID) (bool, error) {
	v, err := uc.Nodes.Get(ctx, nodestatus.ID(node))
	hit, err := found(err)
	if err != nil || !hit {
		return false, err
	}
	return v.Sellable(), nil
}
