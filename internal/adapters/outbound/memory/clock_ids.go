package memory

import (
	"fmt"
	"sync"
	"time"

	"github.com/claudioed/offer-availability/internal/domain/catalog"
	"github.com/claudioed/offer-availability/internal/domain/listing"
)

// Clock is a settable ports.Clock.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock returns a clock reading t.
func NewClock(t time.Time) *Clock { return &Clock{now: t} }

// Now returns the current reading.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Set moves the clock to t.
func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// Advance moves the clock forward by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// SequentialIDs is a ports.IDGenerator minting item-1, item-2, lst-1, ... one
// counter per kind.
type SequentialIDs struct {
	mu            sync.Mutex
	items, listed int
}

// NewCatalogItemID returns the next catalogue item id.
func (g *SequentialIDs) NewCatalogItemID() catalog.ID {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.items++
	return catalog.ID(fmt.Sprintf("item-%d", g.items))
}

// NewListingID returns the next listing id.
func (g *SequentialIDs) NewListingID() listing.ID {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.listed++
	return listing.ID(fmt.Sprintf("lst-%d", g.listed))
}
