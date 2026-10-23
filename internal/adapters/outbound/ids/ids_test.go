package ids

import (
	"strings"
	"testing"

	"github.com/claudioed/offer-availability/internal/domain/shared"
)

func TestUUIDIdsAreValidPrefixedAndUnique(t *testing.T) {
	g := UUID{}
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		item, lst := string(g.NewCatalogItemID()), string(g.NewListingID())
		if !strings.HasPrefix(item, "item-") || !strings.HasPrefix(lst, "lst-") {
			t.Fatalf("prefixes wrong: %s %s", item, lst)
		}
		for _, id := range []string{item, lst} {
			if !shared.ValidID(id) || seen[id] {
				t.Fatalf("id %q is invalid or repeated", id)
			}
			seen[id] = true
		}
	}
}
