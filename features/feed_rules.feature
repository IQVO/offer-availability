Feature: Inventory feed rules from the documentation
  ADR 0002 A8-A12 and A21, the use-cases page and the aggregate canvas of
  InventoryFeed and Listing: the node-level high-water mark, the atomic all-or-nothing
  apply, FullFeedZeroing at its edges, the bounds of a feed and the data its events carry.

  Background:
    Given the node "N1" is ACTIVE
    And the node "N2" is ACTIVE
    And the catalogue item "bottle"
    And the catalogue item "mug"
    And a listing of "bottle" on node "N1"
    And a listing of "mug" on node "N1"
    And a listing of "bottle" on node "N2"

  # ---- the node's high-water mark (G23) ----------------------------------------

  Scenario: A feed one second older than the node's last applied feed is superseded
    Given I apply the feed "f1" for node "N1" as of "2026-10-23T11:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 50  | PHYSICAL |
    When I apply the feed "f2" for node "N1" as of "2026-10-23T10:59:59Z" with lines:
      | listing | qty | basis    |
      | mug@N1  | 7   | PHYSICAL |
    Then the feed "f2" ends SUPERSEDED
    And the listing "mug@N1" has availability 0 UNKNOWN and no asOf
    And the last action raised no events

  Scenario Outline: A feed as old as, or newer than, the node's last applied feed is applied
    Given I apply the feed "f1" for node "N1" as of "2026-10-23T11:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 50  | PHYSICAL |
    When I apply the feed "f2" for node "N1" as of "<asOf>" with lines:
      | listing | qty | basis    |
      | mug@N1  | 7   | PHYSICAL |
    Then the feed "f2" is APPLIED with 1 applied, 0 stale and 0 zeroed
    And the listing "mug@N1" has availability 7 PHYSICAL as of "<asOf>"

    Examples:
      | asOf                 |
      | 2026-10-23T11:00:00Z |
      | 2026-10-23T11:00:01Z |

  Scenario: The high-water mark belongs to the node, not to the network
    Given I apply the feed "f1" for node "N1" as of "2026-10-23T11:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 50  | PHYSICAL |
    When I apply the feed "f2" for node "N2" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N2 | 8   | PHYSICAL |
    Then the feed "f2" is APPLIED with 1 applied, 0 stale and 0 zeroed

  Scenario: A FAILED feed does not advance the high-water mark
    Given I apply the feed "f1" for node "N1" as of "2026-10-23T12:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N2 | 5   | PHYSICAL |
    And the feed "f1" is FAILED with ErrListingNotOnNode
    When I apply the feed "f2" for node "N1" as of "2026-10-23T11:00:00Z" with lines:
      | listing | qty | basis    |
      | mug@N1  | 7   | PHYSICAL |
    Then the feed "f2" is APPLIED with 1 applied, 0 stale and 0 zeroed

  # ---- stale lines (A9) ----------------------------------------------------------

  Scenario: A feed whose lines are all stale is still APPLIED and raises only InventoryFeedApplied
    Given I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 120 | PHYSICAL |
    When I apply the feed "f2" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 5   | PHYSICAL |
    Then the feed "f2" is APPLIED with 0 applied, 1 stale and 0 zeroed
    And the last events are "InventoryFeedApplied"
    And the listing "bottle@N1" has availability 120 PHYSICAL as of "2026-10-23T10:00:00Z"

  Scenario: A negative quantity fails the feed even on a stale line
    Given I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 120 | PHYSICAL |
    When I apply the feed "f2" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | -1  | PHYSICAL |
    Then the request fails with "ErrNegativeAvailability"
    And the feed "f2" is FAILED with ErrNegativeAvailability
    And the listing "bottle@N1" has availability 120 PHYSICAL as of "2026-10-23T10:00:00Z"

  Scenario: An applied line raises ListingAvailabilityChanged even when only asOf and feedId changed
    Given I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 120 | PHYSICAL |
    When I apply the feed "f2" for node "N1" as of "2026-10-23T11:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 120 | PHYSICAL |
    Then the feed "f2" is APPLIED with 1 applied, 0 stale and 0 zeroed
    And the last events are "ListingAvailabilityChanged, InventoryFeedApplied"
    And the listing "bottle@N1" has availability 120 PHYSICAL as of "2026-10-23T11:00:00Z"
    And the last "ListingAvailabilityChanged" event has "FeedID" "f2"

  Scenario Outline: An asOf is an instant: less than a microsecond later, or the same instant in another zone, is stale
    Given I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 120 | PHYSICAL |
    When I apply the feed "f2" for node "N1" as of "<asOf>" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 5   | PHYSICAL |
    Then the feed "f2" is APPLIED with <applied> applied, <stale> stale and 0 zeroed

    Examples:
      | asOf                          | applied | stale |
      | 2026-10-23T10:00:00.0000004Z  | 0       | 1     |
      | 2026-10-23T07:00:00-03:00     | 0       | 1     |
      | 2026-10-23T10:00:00.000001Z   | 1       | 0     |
      | 2026-10-23T10:00:01Z          | 1       | 0     |

  # ---- quantities, bases and the shape of a feed (A2, A12) -----------------------

  Scenario Outline: A quantity from 0 up to 2147483647 is applied and leaves the listing LIVE
    When I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty   | basis    |
      | bottle@N1 | <qty> | PHYSICAL |
    Then the feed "f1" is APPLIED with 1 applied, 0 stale and 0 zeroed
    And the listing "bottle@N1" is LIVE
    And the listing "bottle@N1" has availability <qty> PHYSICAL as of "2026-10-23T10:00:00Z"

    Examples:
      | qty        |
      | 0          |
      | 2147483647 |

  Scenario: A quantity above 2147483647 is a validation error and records nothing
    When I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty        | basis    |
      | bottle@N1 | 2147483648 | PHYSICAL |
    Then the request fails with "ErrValidation"
    And the feed "f1" was never stored
    And the last action raised no events

  Scenario Outline: A feed of 10000 lines passes validation, one of 10001 is refused
    When I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with <lines> lines naming listings that do not exist
    Then the request fails with "ErrListingNotOnNode"
    And the feed "f1" is FAILED with ErrListingNotOnNode

    Examples:
      | lines |
      | 1     |
      | 10000 |

  Scenario: A feed of 10001 lines is a validation error and records nothing
    When I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with 10001 lines naming listings that do not exist
    Then the request fails with "ErrValidation"
    And the feed "f1" was never stored

  Scenario: Two lines for one listing are a validation error and apply nothing
    When I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | mug@N1    | 4   | PHYSICAL |
      | bottle@N1 | 5   | PHYSICAL |
      | bottle@N1 | 6   | PHYSICAL |
    Then the request fails with "ErrValidation"
    And the feed "f1" was never stored
    And the listing "mug@N1" has availability 0 UNKNOWN and no asOf

  Scenario Outline: A basis that is not PHYSICAL, THROUGHPUT_CONSTRAINED or UNKNOWN is a validation error
    When I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis   |
      | bottle@N1 | 5   | <basis> |
    Then the request fails with "ErrValidation"
    And the feed "f1" was never stored

    Examples:
      | basis    |
      | BOGUS    |
      | physical |
      |          |

  Scenario: A partial feed needs at least one line
    When I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing | qty | basis |
    Then the request fails with "ErrValidation"
    And the feed "f1" was never stored

  Scenario: The first offending line in line order decides how the feed fails
    When I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N2 | 5   | PHYSICAL |
      | mug@N1    | -1  | PHYSICAL |
    Then the request fails with "ErrListingNotOnNode"
    When I apply the feed "f2" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | mug@N1    | -1  | PHYSICAL |
      | bottle@N2 | 5   | PHYSICAL |
    Then the request fails with "ErrNegativeAvailability"

  Scenario: A failed feed id sent again for another node is a validation error
    Given I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N2 | 5   | PHYSICAL |
    When I apply the feed "f1" for node "N2" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N2 | 5   | PHYSICAL |
    Then the request fails with "ErrValidation"
    And the feed "f1" is FAILED with ErrListingNotOnNode

  # ---- FullFeedZeroing at its edges (A10, A24) -------------------------------------

  Scenario: A full feed with no lines zeroes every listing of the node and keeps them LIVE
    Given I apply the feed "f0" for node "N2" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N2 | 9   | PHYSICAL |
    And I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 5   | PHYSICAL |
      | mug@N1    | 7   | PHYSICAL |
    When I apply the full feed "f2" for node "N1" as of "2026-10-23T11:00:00Z" with lines:
      | listing | qty | basis |
    Then the feed "f2" is APPLIED with 0 applied, 0 stale and 2 zeroed
    And the listing "bottle@N1" has availability 0 PHYSICAL as of "2026-10-23T11:00:00Z"
    And the listing "mug@N1" has availability 0 PHYSICAL as of "2026-10-23T11:00:00Z"
    And the listing "bottle@N1" is LIVE
    And the listing "bottle@N2" has availability 9 PHYSICAL as of "2026-10-23T10:00:00Z"

  Scenario: Zeroing leaves a listing that never had availability untouched and uncounted
    Given I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 5   | PHYSICAL |
    When I apply the full feed "f2" for node "N1" as of "2026-10-23T11:00:00Z" with lines:
      | listing | qty | basis |
    Then the feed "f2" is APPLIED with 0 applied, 0 stale and 1 zeroed
    And the listing "mug@N1" has availability 0 UNKNOWN and no asOf
    And the last events are "ListingAvailabilityChanged, InventoryFeedApplied"

  Scenario: A listing fed at the very instant of a full feed is neither zeroed nor counted stale
    Given I apply the feed "f1" for node "N1" as of "2026-10-23T11:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 120 | PHYSICAL |
    When I apply the full feed "f2" for node "N1" as of "2026-10-23T11:00:00Z" with lines:
      | listing | qty | basis    |
      | mug@N1  | 3   | PHYSICAL |
    Then the feed "f2" is APPLIED with 1 applied, 0 stale and 0 zeroed
    And the listing "bottle@N1" has availability 120 PHYSICAL as of "2026-10-23T11:00:00Z"

  Scenario: A suppressed listing is zeroed too and stays suppressed
    Given I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 5   | PHYSICAL |
    And node-topology publishes the event "e1" NodeSuspended for node "N1" at "2026-10-23T10:30:00Z"
    When I apply the full feed "f2" for node "N1" as of "2026-10-23T11:00:00Z" with lines:
      | listing | qty | basis |
    Then the feed "f2" is APPLIED with 0 applied, 0 stale and 1 zeroed
    And the listing "bottle@N1" has availability 0 PHYSICAL as of "2026-10-23T11:00:00Z"
    And the listing "bottle@N1" is SUPPRESSED because NODE_NOT_SELLABLE

  Scenario: A full feed on a node without listings is applied and says so
    When I apply the full feed "f1" for node "N3" as of "2026-10-23T11:00:00Z" with lines:
      | listing | qty | basis |
    Then the feed "f1" is APPLIED with 0 applied, 0 stale and 0 zeroed
    And the last events are "InventoryFeedApplied"

  # ---- availability is per listing, never summed across owners (SegregatedByOwner) --

  Scenario: Availability of an owner's listing is never merged with the operator's
    Given a listing of "bottle" on node "N1" owned by "seller-7"
    And I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 10  | PHYSICAL |
    And the listing "bottle@N1/seller-7" has availability 0 UNKNOWN and no asOf
    When I apply the feed "f2" for node "N1" as of "2026-10-23T11:00:00Z" with lines:
      | listing            | qty | basis                  |
      | bottle@N1/seller-7 | 4   | THROUGHPUT_CONSTRAINED |
    Then the listing "bottle@N1/seller-7" has availability 4 THROUGHPUT_CONSTRAINED as of "2026-10-23T11:00:00Z"
    And the listing "bottle@N1" has availability 10 PHYSICAL as of "2026-10-23T10:00:00Z"

  Scenario: A full feed zeroes the listings of every owner it omits
    Given a listing of "bottle" on node "N1" owned by "seller-7"
    And I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing            | qty | basis    |
      | bottle@N1          | 10  | PHYSICAL |
      | bottle@N1/seller-7 | 4   | PHYSICAL |
    When I apply the full feed "f2" for node "N1" as of "2026-10-23T11:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 10  | PHYSICAL |
    Then the feed "f2" is APPLIED with 1 applied, 0 stale and 1 zeroed
    And the listing "bottle@N1/seller-7" has availability 0 PHYSICAL as of "2026-10-23T11:00:00Z"
    And the last "ListingAvailabilityChanged" event has "OwnerRef" "seller-7"

  # ---- the data the events carry --------------------------------------------------------

  Scenario: ListingAvailabilityChanged carries the model's data fields
    When I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis                  |
      | bottle@N1 | 120 | THROUGHPUT_CONSTRAINED |
    Then the last "ListingAvailabilityChanged" event has "ListingID" "lst-1"
    And the last "ListingAvailabilityChanged" event has "CatalogItemID" "item-1"
    And the last "ListingAvailabilityChanged" event has "NodeID" "N1"
    And the last "ListingAvailabilityChanged" event has "AvailableQty" "120"
    And the last "ListingAvailabilityChanged" event has "Basis" "THROUGHPUT_CONSTRAINED"
    And the last "ListingAvailabilityChanged" event has "AsOf" "2026-10-23T10:00:00Z"
    And the last "ListingAvailabilityChanged" event has "FeedID" "f1"

  Scenario: InventoryFeedApplied counts the lines as received, stale ones included
    Given I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 5   | PHYSICAL |
      | mug@N1    | 7   | PHYSICAL |
    When I apply the feed "f2" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 6   | PHYSICAL |
      | mug@N1    | 8   | PHYSICAL |
    Then the last events are "InventoryFeedApplied"
    And the last "InventoryFeedApplied" event has "LineCount" "2"
    And the last "InventoryFeedApplied" event has "FullUpdate" "false"
    And the last "InventoryFeedApplied" event has "NodeID" "N1"
    And the last "InventoryFeedApplied" event has "FeedID" "f2"

  Scenario: InventoryFeedApplied of a full feed counts only the lines sent, not the listings zeroed
    Given I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 5   | PHYSICAL |
      | mug@N1    | 7   | PHYSICAL |
    When I apply the full feed "f2" for node "N1" as of "2026-10-23T11:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 6   | PHYSICAL |
    Then the last events are "ListingAvailabilityChanged, ListingAvailabilityChanged, InventoryFeedApplied"
    And the last "InventoryFeedApplied" event has "LineCount" "1"
    And the last "InventoryFeedApplied" event has "FullUpdate" "true"

  Scenario: A discontinued item's listing still takes availability and stays suppressed
    Given I discontinue the catalogue item "bottle"
    When I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 5   | PHYSICAL |
    Then the feed "f1" is APPLIED with 1 applied, 0 stale and 0 zeroed
    And the listing "bottle@N1" is SUPPRESSED because DISCONTINUED
    And the listing "bottle@N1" has availability 5 PHYSICAL as of "2026-10-23T10:00:00Z"
