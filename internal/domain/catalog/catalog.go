// Package catalog is the CatalogItem aggregate: the network's own item master
// entry (title, dimensions, weight, handling flags), independent of who ships it.
//
// Two states, ACTIVE (initial) and DISCONTINUED (terminal), and one transition
// ACTIVE -> DISCONTINUED (DiscontinueItem). Invariant: title valid and
// dimensions and weight positive (ErrInvalidItemDimensions). Discontinuing a
// DISCONTINUED item is a no-op that raises nothing (ADR 0002, A3). What
// discontinuing does to the item's listings is a use-case concern (A4).
package catalog

import (
	"errors"
	"fmt"
	"time"

	"github.com/claudioed/offer-availability/internal/domain/shared"
	"github.com/claudioed/offer-availability/internal/domain/shared/gs1"
)

// ErrInvalidItemDimensions: dimensions and weight are positive (422).
var ErrInvalidItemDimensions = errors.New("invalid item dimensions")

const (
	// MaxTitle is the longest accepted title in characters.
	MaxTitle = 200
	// MaxDimension is the Postgres integer ceiling for a dimension or weight.
	MaxDimension = 2147483647
)

// ID identifies a catalogue item (catalogItemId).
type ID string

// Valid reports whether id is a well-formed aggregate id.
func (id ID) Valid() bool { return shared.ValidID(string(id)) }

// State is the lifecycle state of an item.
type State string

// The item states of the model.
const (
	StateActive       State = "ACTIVE"
	StateDiscontinued State = "DISCONTINUED"
)

// Valid reports whether s is one of the model's states.
func (s State) Valid() bool { return s == StateActive || s == StateDiscontinued }

// Spec is what a merchandiser supplies to create an item.
type Spec struct {
	Title    string
	LengthMm int
	WidthMm  int
	HeightMm int
	WeightG  int
	Hazmat   bool
	Fragile  bool
	// GTIN is the optional identifiers.gtin (zero = absent), already parsed.
	GTIN gs1.GTIN
}

// Item is the aggregate root.
type Item struct {
	id        ID
	spec      Spec
	state     State
	version   int64
	gtin      gs1.GTIN
	packaging []PackLevelDef
}

// Snapshot is the persisted form of an item. Restore validates it.
type Snapshot struct {
	ID       ID
	Title    string
	LengthMm int
	WidthMm  int
	HeightMm int
	WeightG  int
	Hazmat   bool
	Fragile  bool
	State    State
	Version  int64
	GTIN     gs1.GTIN
}

// Create builds an ACTIVE item (aggregate version 1) and raises
// CatalogItemCreated.
func Create(id ID, spec Spec, now time.Time) (*Item, []shared.Event, error) {
	if !id.Valid() {
		return nil, nil, shared.Invalid("catalogItemId %q is not a valid id", id)
	}
	if err := checkSpec(spec); err != nil {
		return nil, nil, err
	}
	it := &Item{id: id, spec: spec, state: StateActive, version: 1, gtin: spec.GTIN}
	ev := CatalogItemCreated{
		Header:        shared.Header{Version: it.version, At: shared.UTC(now)},
		CatalogItemID: id, Title: spec.Title,
		LengthMm: spec.LengthMm, WidthMm: spec.WidthMm, HeightMm: spec.HeightMm, WeightG: spec.WeightG,
		Hazmat: spec.Hazmat, Fragile: spec.Fragile, GTIN: spec.GTIN,
	}
	return it, []shared.Event{ev}, nil
}

func checkSpec(s Spec) error {
	if !shared.ValidTextN(s.Title, MaxTitle) {
		return shared.Invalid("title must be 1 to %d characters, trimmed, without control characters", MaxTitle)
	}
	for _, d := range []struct {
		name  string
		value int
	}{{"lengthMm", s.LengthMm}, {"widthMm", s.WidthMm}, {"heightMm", s.HeightMm}, {"weightG", s.WeightG}} {
		if d.value < 1 || d.value > MaxDimension {
			return fmt.Errorf("%w: %s %d must be between 1 and %d", ErrInvalidItemDimensions, d.name, d.value, MaxDimension)
		}
	}
	return nil
}

// Discontinue moves the item ACTIVE -> DISCONTINUED and raises
// CatalogItemDiscontinued. On a DISCONTINUED item it changes nothing and
// returns no event (A3).
func (i *Item) Discontinue(now time.Time) []shared.Event {
	if i.state == StateDiscontinued {
		return nil
	}
	i.state = StateDiscontinued
	i.version++
	return []shared.Event{CatalogItemDiscontinued{
		Header:        shared.Header{Version: i.version, At: shared.UTC(now)},
		CatalogItemID: i.id,
	}}
}

// Restore rebuilds a persisted item that has no pack levels.
func Restore(s Snapshot) (*Item, error) { return RestorePackaged(s, nil) }

// PackLevels returns every pack level ever declared, retired ones included, in
// declaration order: the persisted form of the packaging (the Snapshot stays a
// comparable value).
func (i *Item) PackLevels() []PackLevelDef { return append([]PackLevelDef(nil), i.packaging...) }

// RestorePackaged rebuilds a persisted item with its pack levels, validating
// every invariant.
func RestorePackaged(s Snapshot, defs []PackLevelDef) (*Item, error) {
	if err := checkSnapshot(s); err != nil {
		return nil, shared.Corrupt("catalogue item %q: %v", s.ID, err)
	}
	if err := checkPackSnapshot(defs); err != nil {
		return nil, shared.Corrupt("catalogue item %q: %v", s.ID, err)
	}
	return &Item{
		id: s.ID, state: s.State, version: s.Version, gtin: s.GTIN, packaging: append([]PackLevelDef(nil), defs...),
		spec: Spec{
			Title: s.Title, LengthMm: s.LengthMm, WidthMm: s.WidthMm, HeightMm: s.HeightMm, WeightG: s.WeightG,
			Hazmat: s.Hazmat, Fragile: s.Fragile,
		},
	}, nil
}

func checkSnapshot(s Snapshot) error {
	if !s.ID.Valid() {
		return errors.New("catalogItemId is malformed")
	}
	if !s.State.Valid() {
		return fmt.Errorf("state %q is unknown", s.State)
	}
	if err := checkSpec(Spec{
		Title: s.Title, LengthMm: s.LengthMm, WidthMm: s.WidthMm, HeightMm: s.HeightMm, WeightG: s.WeightG,
	}); err != nil {
		return err
	}
	if s.Version < 1 {
		return errors.New("a version of at least 1 is required")
	}
	return nil
}

// ID returns the catalogItemId.
func (i *Item) ID() ID { return i.id }

// Spec returns the descriptive attributes.
func (i *Item) Spec() Spec { return i.spec }

// State returns the lifecycle state.
func (i *Item) State() State { return i.state }

// Discontinued reports whether the item is DISCONTINUED.
func (i *Item) Discontinued() bool { return i.state == StateDiscontinued }

// Version returns the aggregate version (one per saved change).
func (i *Item) Version() int64 { return i.version }

// Snapshot returns the persisted form.
func (i *Item) Snapshot() Snapshot {
	return Snapshot{
		ID: i.id, Title: i.spec.Title, LengthMm: i.spec.LengthMm, WidthMm: i.spec.WidthMm,
		HeightMm: i.spec.HeightMm, WeightG: i.spec.WeightG, Hazmat: i.spec.Hazmat, Fragile: i.spec.Fragile,
		State: i.state, Version: i.version, GTIN: i.gtin,
	}
}
