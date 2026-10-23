Feature: Catalogue items
  The network keeps its own item master. Dimensions and weight must be positive,
  and discontinuing an item suppresses every listing of it.

  Background:
    Given the node "N1" is ACTIVE
    And the node "N2" is ACTIVE

  Scenario: Creating an item
    When I create the catalogue item "bottle" of 270 x 75 x 75 mm and 340 g
    Then the catalogue item "bottle" is ACTIVE
    And the outbox holds "CatalogItemCreated"

  Scenario Outline: Dimensions and weight must be positive
    When I create the catalogue item "bad" of <length> x <width> x <height> mm and <weight> g
    Then the request fails with "ErrInvalidItemDimensions"
    And the outbox holds nothing

    Examples:
      | length | width | height | weight |
      | 0      | 75    | 75     | 340    |
      | 270    | 0     | 75     | 340    |
      | 270    | 75    | -1     | 340    |
      | 270    | 75    | 75     | 0      |

  Scenario: Discontinuing an item suppresses every listing of it
    Given the catalogue item "bottle"
    And a listing of "bottle" on node "N1"
    And a listing of "bottle" on node "N2"
    When I discontinue the catalogue item "bottle"
    Then the catalogue item "bottle" is DISCONTINUED
    And the listing "bottle@N1" is SUPPRESSED for DISCONTINUED
    And the listing "bottle@N2" is SUPPRESSED for DISCONTINUED
    And the outbox holds "CatalogItemCreated, ListingCreated, ListingCreated, CatalogItemDiscontinued, ListingSuppressed, ListingSuppressed"

  Scenario: Discontinuing twice changes nothing
    Given the catalogue item "bottle"
    And I discontinue the catalogue item "bottle"
    When I discontinue the catalogue item "bottle"
    Then the catalogue item "bottle" is DISCONTINUED
    And the outbox holds "CatalogItemCreated, CatalogItemDiscontinued"
