package catalog

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/claudioed/offer-availability/internal/domain/shared"
	"github.com/claudioed/offer-availability/internal/domain/shared/gs1"
)

// The packaging hierarchy of an item (ADR 0006): the pack levels it is sold,
// shipped and received in, each identified by a GS1 GTIN. It mirrors the
// product-master packaging model: unitsPerPack of a GTIN is immutable (a new
// count is a new GTIN and the old one is retired), the levels are ordered
// EACH < INNER < CASE < PALLET, a level above EACH names the GTIN of an active
// lower level of the same item (the child chain) and every quantity is in
// eaches. Availability stays in eaches; a pack level is a conversion plus an
// identity, never a second ledger.

// MaxPackQuantity bounds unitsPerPack so it stays inside a Postgres INTEGER.
const MaxPackQuantity = 1000000

var (
	// ErrInvalidGTIN: wrong length, wrong mod-10 check digit or a variable measure GTIN (422).
	ErrInvalidGTIN = errors.New("GTIN must be 8, 12, 13 or 14 digits with a valid mod-10 check digit")
	// ErrGtinAlreadyAssigned: a GTIN identifies one item or pack level, forever (409).
	ErrGtinAlreadyAssigned = errors.New("GTIN is already assigned")
	// ErrPackQuantityImmutable: unitsPerPack of an active GTIN cannot change (409).
	ErrPackQuantityImmutable = errors.New("the unitsPerPack of an active pack level cannot change; declare a new GTIN at that level")
	// ErrPackLevelOrder: a level above EACH needs an active lower child level (422).
	ErrPackLevelOrder = errors.New("a pack level needs an active child level that is lower than it")
	// ErrInvalidPackQuantity: unitsPerPack is out of range or disagrees with the chain (422).
	ErrInvalidPackQuantity = errors.New("invalid pack quantity")
	// ErrInvalidPackDimensions: dimsMm and grossWeightG are positive and the weight covers the contents (422).
	ErrInvalidPackDimensions = errors.New("invalid pack dimensions")
	// ErrInvalidPackLevel: a level other than EACH, INNER, CASE or PALLET (422).
	ErrInvalidPackLevel = errors.New("pack level must be EACH, INNER, CASE or PALLET")
	// ErrPackLevelNotFound: the item never declared that level (404).
	ErrPackLevelNotFound = errors.New("pack level not found")
)

// PackLevel is a packaging level.
type PackLevel string

// The packaging levels, lowest first.
const (
	PackLevelEach   PackLevel = "EACH"
	PackLevelInner  PackLevel = "INNER"
	PackLevelCase   PackLevel = "CASE"
	PackLevelPallet PackLevel = "PALLET"
)

func (l PackLevel) rank() int {
	if l == PackLevelEach {
		return 1
	}
	if l == PackLevelInner {
		return 2
	}
	if l == PackLevelCase {
		return 3
	}
	if l == PackLevelPallet {
		return 4
	}
	return 0
}

// Valid reports whether l is one of the four levels.
func (l PackLevel) Valid() bool { return l.rank() != 0 }

// Below reports whether l is a lower level than other.
func (l PackLevel) Below(other PackLevel) bool {
	return l.Valid() && other.Valid() && l.rank() < other.rank()
}

// ParsePackLevel accepts exactly EACH, INNER, CASE or PALLET.
func ParsePackLevel(s string) (PackLevel, error) {
	l := PackLevel(s)
	if !l.Valid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidPackLevel, s)
	}
	return l, nil
}

// ParseGTIN validates a raw GTIN with the template's value object (never
// trusted from a client) and normalises it to 14 digits. An indicator digit 9
// (variable measure) is rejected.
func ParseGTIN(raw string) (gs1.GTIN, error) {
	g, err := gs1.ParseGTIN(raw)
	if err != nil {
		return gs1.GTIN{}, fmt.Errorf("%w: %w", ErrInvalidGTIN, err)
	}
	if gs1.RejectVariableMeasure(g) != nil {
		return gs1.GTIN{}, fmt.Errorf("%w: variable measure GTINs are not supported", ErrInvalidGTIN)
	}
	return g, nil
}

// PackSpec is the input of DeclarePackLevel. Dimensions are all three of
// LengthMm, WidthMm, HeightMm or none (0); GrossWeightG 0 is absent.
type PackSpec struct {
	GTIN         gs1.GTIN
	ChildGTIN    gs1.GTIN
	UnitsPerPack int
	LengthMm     int
	WidthMm      int
	HeightMm     int
	GrossWeightG int
}

// PackLevelDef is one declared pack level. A retired level is kept (Active
// false, RetiredAt set) so an in-flight document naming its GTIN still resolves.
type PackLevelDef struct {
	Level        PackLevel
	GTIN         gs1.GTIN
	ChildGTIN    gs1.GTIN
	UnitsPerPack int
	LengthMm     int
	WidthMm      int
	HeightMm     int
	GrossWeightG int
	Active       bool
	DeclaredAt   time.Time
	RetiredAt    time.Time
}

func (d PackLevelDef) sameDims(s PackSpec) bool {
	return d.LengthMm == s.LengthMm && d.WidthMm == s.WidthMm && d.HeightMm == s.HeightMm && d.GrossWeightG == s.GrossWeightG
}

// Packaging returns a copy of every pack level, lowest level first and, within
// a level, in declaration order.
func (i *Item) Packaging() []PackLevelDef {
	out := append([]PackLevelDef(nil), i.packaging...)
	sort.SliceStable(out, func(a, b int) bool { return out[a].Level.rank() < out[b].Level.rank() })
	return out
}

// GTIN returns the item's own identifier (identifiers.gtin); zero when absent.
func (i *Item) GTIN() gs1.GTIN { return i.gtin }

func (i *Item) indexOfGTIN(g gs1.GTIN) int {
	for n, d := range i.packaging {
		if d.GTIN == g {
			return n
		}
	}
	return -1
}

func (i *Item) indexActive(l PackLevel) int {
	for n, d := range i.packaging {
		if d.Level == l && d.Active {
			return n
		}
	}
	return -1
}

func checkPackShape(l PackLevel, s PackSpec) error {
	if !l.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidPackLevel, string(l))
	}
	if s.GTIN.IsZero() {
		return fmt.Errorf("%w: a gtin is required", ErrInvalidGTIN)
	}
	if s.UnitsPerPack < 1 || s.UnitsPerPack > MaxPackQuantity {
		return fmt.Errorf("%w: unitsPerPack must be 1 to %d", ErrInvalidPackQuantity, MaxPackQuantity)
	}
	if l == PackLevelEach {
		if s.UnitsPerPack != 1 || !s.ChildGTIN.IsZero() {
			return fmt.Errorf("%w: an EACH level has no child and exactly 1 unit per pack", ErrInvalidPackQuantity)
		}
	} else {
		if s.UnitsPerPack < 2 {
			return fmt.Errorf("%w: unitsPerPack must be at least 2 for a %s", ErrInvalidPackQuantity, l)
		}
		if s.ChildGTIN.IsZero() {
			return fmt.Errorf("%w: a %s needs the childGtin of its child level", ErrPackLevelOrder, l)
		}
	}
	return checkDims(s)
}

func checkDims(s PackSpec) error {
	dims := []int{s.LengthMm, s.WidthMm, s.HeightMm}
	set := 0
	for _, d := range dims {
		if d < 0 || d > MaxDimension {
			return fmt.Errorf("%w: dimsMm must be between 1 and %d", ErrInvalidPackDimensions, MaxDimension)
		}
		if d > 0 {
			set++
		}
	}
	if set != 0 && set != len(dims) {
		return fmt.Errorf("%w: dimsMm needs lengthMm, widthMm and heightMm together", ErrInvalidPackDimensions)
	}
	if s.GrossWeightG < 0 || s.GrossWeightG > MaxDimension {
		return fmt.Errorf("%w: grossWeightG must be between 1 and %d", ErrInvalidPackDimensions, MaxDimension)
	}
	return nil
}

// checkChain validates a NEW level against its child and answers nothing else:
// the child is an active lower level of this item, unitsPerPack is a multiple
// of the child's (at least twice), and the gross weight covers the contents.
func (i *Item) checkChain(l PackLevel, s PackSpec) error {
	if l == PackLevelEach {
		return nil
	}
	c := i.indexOfGTIN(s.ChildGTIN)
	if c < 0 || !i.packaging[c].Active || !i.packaging[c].Level.Below(l) {
		return fmt.Errorf("%w: child %s is not an active level of this item below %s", ErrPackLevelOrder, s.ChildGTIN, l)
	}
	child := i.packaging[c]
	if s.UnitsPerPack%child.UnitsPerPack != 0 || s.UnitsPerPack/child.UnitsPerPack < 2 {
		return fmt.Errorf("%w: %d eaches is not at least two of the %d-each child %s", ErrInvalidPackQuantity, s.UnitsPerPack, child.UnitsPerPack, child.GTIN)
	}
	if child.GrossWeightG > 0 && s.GrossWeightG > 0 {
		if need := (s.UnitsPerPack / child.UnitsPerPack) * child.GrossWeightG; s.GrossWeightG < need {
			return fmt.Errorf("%w: %d g is below the %d g of its contents", ErrInvalidPackDimensions, s.GrossWeightG, need)
		}
	}
	return nil
}

// DeclarePackLevel declares (PUT) the pack level l.
//
//   - A GTIN this item already holds is only re-declarable at its own ACTIVE
//     level, with the same unitsPerPack and child (ErrPackQuantityImmutable
//     otherwise); dimensions and weight may change in place, the identical
//     declaration changes nothing and raises nothing. Any other use of a held
//     GTIN is ErrGtinAlreadyAssigned. Uniqueness across items is the use
//     case's and the repository's check.
//   - A new GTIN at a level that has an active one retires the old level
//     (CatalogItemPackLevelRetired) and declares the new one
//     (CatalogItemPackagingDeclared), in ONE aggregate version.
//
// A DISCONTINUED item takes no packaging change (illegal transition).
func (i *Item) DeclarePackLevel(l PackLevel, s PackSpec, now time.Time) ([]shared.Event, error) {
	if err := checkPackShape(l, s); err != nil {
		return nil, err
	}
	if i.state == StateDiscontinued {
		return nil, fmt.Errorf("%w: item %s is DISCONTINUED", shared.ErrIllegalTransition, i.id)
	}
	if n := i.indexOfGTIN(s.GTIN); n >= 0 {
		return i.redeclare(n, l, s, now)
	}
	if err := i.checkChain(l, s); err != nil {
		return nil, err
	}
	at := shared.UTC(now)
	i.version++
	h := shared.Header{Version: i.version, At: at}
	var events []shared.Event
	if old := i.indexActive(l); old >= 0 {
		events = append(events, i.retireAt(old, h))
	}
	i.packaging = append(i.packaging, PackLevelDef{
		Level: l, GTIN: s.GTIN, ChildGTIN: s.ChildGTIN, UnitsPerPack: s.UnitsPerPack,
		LengthMm: s.LengthMm, WidthMm: s.WidthMm, HeightMm: s.HeightMm, GrossWeightG: s.GrossWeightG,
		Active: true, DeclaredAt: at,
	})
	return append(events, declaredEvent(i.id, h, i.packaging[len(i.packaging)-1])), nil
}

func (i *Item) redeclare(n int, l PackLevel, s PackSpec, now time.Time) ([]shared.Event, error) {
	cur := i.packaging[n]
	if cur.Level != l || !cur.Active {
		return nil, fmt.Errorf("%w: %s is already %s", ErrGtinAlreadyAssigned, s.GTIN, describe(cur))
	}
	if cur.ChildGTIN != s.ChildGTIN || cur.UnitsPerPack != s.UnitsPerPack {
		return nil, fmt.Errorf("%w: %s at level %s", ErrPackQuantityImmutable, s.GTIN, l)
	}
	if cur.sameDims(s) {
		return nil, nil
	}
	if err := i.checkChain(l, s); err != nil && !errors.Is(err, ErrPackLevelOrder) {
		return nil, err
	}
	cur.LengthMm, cur.WidthMm, cur.HeightMm, cur.GrossWeightG = s.LengthMm, s.WidthMm, s.HeightMm, s.GrossWeightG
	i.packaging[n] = cur
	i.version++
	return []shared.Event{declaredEvent(i.id, shared.Header{Version: i.version, At: shared.UTC(now)}, cur)}, nil
}

func describe(d PackLevelDef) string {
	if d.Active {
		return "the active " + string(d.Level) + " level of this item"
	}
	return "a retired " + string(d.Level) + " level of this item"
}

// RetirePackLevel retires (POST :retire) the active definition at l. The
// definition is kept, never deleted. A level with only retired history is no
// change and raises nothing; a level never declared is ErrPackLevelNotFound.
func (i *Item) RetirePackLevel(l PackLevel, now time.Time) ([]shared.Event, error) {
	if !l.Valid() {
		return nil, fmt.Errorf("%w: %q", ErrInvalidPackLevel, string(l))
	}
	n := i.indexActive(l)
	if n < 0 {
		for _, d := range i.packaging {
			if d.Level == l {
				return nil, nil
			}
		}
		return nil, fmt.Errorf("%w: %s", ErrPackLevelNotFound, l)
	}
	i.version++
	return []shared.Event{i.retireAt(n, shared.Header{Version: i.version, At: shared.UTC(now)})}, nil
}

func (i *Item) retireAt(n int, h shared.Header) shared.Event {
	i.packaging[n].Active = false
	i.packaging[n].RetiredAt = h.At
	d := i.packaging[n]
	return CatalogItemPackLevelRetired{Header: h, CatalogItemID: i.id, Level: d.Level, GTIN: d.GTIN, RetiredAt: h.At}
}

func declaredEvent(id ID, h shared.Header, d PackLevelDef) shared.Event {
	return CatalogItemPackagingDeclared{
		Header: h, CatalogItemID: id, Level: d.Level, GTIN: d.GTIN, ChildGTIN: d.ChildGTIN,
		UnitsPerPack: d.UnitsPerPack, DeclaredAt: h.At,
	}
}

// checkPackSnapshot validates persisted pack levels: valid shape, one GTIN
// once, at most one active definition per level.
func checkPackSnapshot(defs []PackLevelDef) error {
	seen := map[gs1.GTIN]bool{}
	active := map[PackLevel]bool{}
	for _, d := range defs {
		if err := checkPackShape(d.Level, PackSpec{
			GTIN: d.GTIN, ChildGTIN: d.ChildGTIN, UnitsPerPack: d.UnitsPerPack,
			LengthMm: d.LengthMm, WidthMm: d.WidthMm, HeightMm: d.HeightMm, GrossWeightG: d.GrossWeightG,
		}); err != nil {
			return err
		}
		if seen[d.GTIN] {
			return fmt.Errorf("GTIN %s appears twice", d.GTIN)
		}
		seen[d.GTIN] = true
		if d.Active {
			if active[d.Level] {
				return fmt.Errorf("two active %s levels", d.Level)
			}
			active[d.Level] = true
		}
	}
	return nil
}
