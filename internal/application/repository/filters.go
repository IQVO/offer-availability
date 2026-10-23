// Package repository holds the non-interface vocabulary of the repository
// ports: the list filter and the keyset page. It has no behaviour; the
// interfaces are in internal/application/ports.
package repository

import "github.com/claudioed/offer-availability/internal/domain/listing"

// Page is a keyset page over rows ordered by id ascending: the rows with an id
// greater than After (all rows when After is empty), at most Limit of them.
type Page struct {
	After string
	Limit int
}

// ListingFilter narrows ListListings. A zero field does not filter; the set
// fields combine with AND.
type ListingFilter struct {
	NodeID listing.NodeID
	ItemID listing.ItemID
}
