Feature: Catalogue item rules from the documentation
  ADR 0002 (A2, A3, A4) and the aggregate canvas of CatalogItem: the bounds of the
  title, dimensions and weight at the edge, the data the creation event carries, and
  what discontinuing does to the listings of the item and of nobody else.

  Background:
    Given the node "N1" is ACTIVE

  Scenario Outline: Dimensions and weight are accepted from 1 up to 2147483647
    When I create the catalogue item "pallet" of <length> x <width> x <height> mm and <weight> g
    Then the catalogue item "pallet" is ACTIVE
    And the last events are "CatalogItemCreated"

    Examples:
      | length     | width      | height     | weight     |
      | 1          | 1          | 1          | 1          |
      | 2147483647 | 75         | 75         | 340        |
      | 270        | 2147483647 | 75         | 340        |
      | 270        | 75         | 2147483647 | 340        |
      | 270        | 75         | 75         | 2147483647 |

  Scenario Outline: A dimension or weight above 2147483647 is refused like a non-positive one
    When I create the catalogue item "pallet" of <length> x <width> x <height> mm and <weight> g
    Then the request fails with "ErrInvalidItemDimensions"
    And the outbox holds nothing

    Examples:
      | length     | width      | height     | weight     |
      | 2147483648 | 75         | 75         | 340        |
      | 270        | 2147483648 | 75         | 340        |
      | 270        | 75         | 2147483648 | 340        |
      | 270        | 75         | 75         | 2147483648 |

  Scenario: A title of 200 characters is accepted
    When I create the catalogue item "long" with a title of 200 characters and 270 x 75 x 75 mm and 340 g
    Then the catalogue item "long" is ACTIVE

  Scenario Outline: A title that is empty or over 200 characters is a validation error
    When I create the catalogue item "x" with a title of <length> characters and 270 x 75 x 75 mm and 340 g
    Then the request fails with "ErrValidation"
    And the outbox holds nothing

    Examples:
      | length |
      | 0      |
      | 201    |

  Scenario Outline: A title with a space at either end is a validation error
    When I create the catalogue item "Bottle" with a <where> space in its title
    Then the request fails with "ErrValidation"
    And the outbox holds nothing

    Examples:
      | where    |
      | leading  |
      | trailing |

  Scenario: The title is checked before the dimensions
    When I create the catalogue item "x" with a title of 0 characters and 0 x 75 x 75 mm and 340 g
    Then the request fails with "ErrValidation"

  Scenario: CatalogItemCreated carries the model's data fields
    When I create the catalogue item "kettle" of 200 x 190 x 250 mm and 900 g with hazmat true and fragile false
    Then the last events are "CatalogItemCreated"
    And the last "CatalogItemCreated" event has "CatalogItemID" "item-1"
    And the last "CatalogItemCreated" event has "Title" "kettle"
    And the last "CatalogItemCreated" event has "LengthMm" "200"
    And the last "CatalogItemCreated" event has "WidthMm" "190"
    And the last "CatalogItemCreated" event has "HeightMm" "250"
    And the last "CatalogItemCreated" event has "WeightG" "900"
    And the last "CatalogItemCreated" event has "Hazmat" "true"
    And the last "CatalogItemCreated" event has "Fragile" "false"

  Scenario: Discontinuing raises one CatalogItemDiscontinued and moves every listing, whatever its suppression, to DISCONTINUED
    Given the node "N3" is SUSPENDED
    And the catalogue item "bottle"
    And a listing of "bottle" on node "N1"
    And a listing of "bottle" on node "N3"
    When I discontinue the catalogue item "bottle"
    Then the last events are "CatalogItemDiscontinued, ListingSuppressed, ListingSuppressed"
    And the last "CatalogItemDiscontinued" event has "CatalogItemID" "item-1"
    And the listing "bottle@N1" is SUPPRESSED because DISCONTINUED
    And the listing "bottle@N3" is SUPPRESSED because DISCONTINUED
    And the last "ListingSuppressed" event has "ListingID" "lst-2"
    And the last "ListingSuppressed" event has "Reason" "DISCONTINUED"

  Scenario: Discontinuing an item leaves the listings of every other item alone
    Given the catalogue item "bottle"
    And the catalogue item "mug"
    And a listing of "bottle" on node "N1"
    And a listing of "mug" on node "N1"
    When I discontinue the catalogue item "bottle"
    Then the listing "bottle@N1" is SUPPRESSED because DISCONTINUED
    And the listing "mug@N1" is LIVE
    And the catalogue item "mug" is ACTIVE
    And the last action raised 1 "ListingSuppressed" event
