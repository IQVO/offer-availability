Feature: Inventory feeds
  A feed is applied in one transaction. A stale line is skipped, an older feed is
  superseded, a broken line fails the whole feed, and a full feed zeroes the
  listings it omits.

  Background:
    Given the node "N1" is ACTIVE
    And the node "N2" is ACTIVE
    And the catalogue item "bottle"
    And the catalogue item "mug"
    And a listing of "bottle" on node "N1"
    And a listing of "mug" on node "N1"
    And a listing of "bottle" on node "N2"

  Scenario: A partial feed replaces the availability and keeps the basis the vendor gave
    When I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis                  |
      | bottle@N1 | 120 | THROUGHPUT_CONSTRAINED |
      | mug@N1    | 7   | PHYSICAL               |
    Then the feed "f1" is APPLIED with 2 applied, 0 stale and 0 zeroed
    And the listing "bottle@N1" has availability 120 THROUGHPUT_CONSTRAINED as of "2026-10-23T10:00:00Z"
    And the listing "mug@N1" has availability 7 PHYSICAL as of "2026-10-23T10:00:00Z"
    And the listing "bottle@N2" has availability 0 UNKNOWN and no asOf
    And the last events are "ListingAvailabilityChanged, ListingAvailabilityChanged, InventoryFeedApplied"

  Scenario: A line that is not newer than the listing is skipped and counted
    Given I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 120 | PHYSICAL |
    When I apply the feed "f2" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 5   | PHYSICAL |
      | mug@N1    | 9   | PHYSICAL |
    Then the feed "f2" is APPLIED with 1 applied, 1 stale and 0 zeroed
    And the listing "bottle@N1" has availability 120 PHYSICAL as of "2026-10-23T10:00:00Z"

  Scenario: A full feed zeroes the listings it omits and keeps their basis
    Given I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis                  |
      | bottle@N1 | 120 | THROUGHPUT_CONSTRAINED |
      | mug@N1    | 7   | PHYSICAL               |
    When I apply the full feed "f2" for node "N1" as of "2026-10-23T11:00:00Z" with lines:
      | listing | qty | basis    |
      | mug@N1  | 3   | PHYSICAL |
    Then the feed "f2" is APPLIED with 1 applied, 0 stale and 1 zeroed
    And the listing "bottle@N1" has availability 0 THROUGHPUT_CONSTRAINED as of "2026-10-23T11:00:00Z"
    And the listing "mug@N1" has availability 3 PHYSICAL as of "2026-10-23T11:00:00Z"
    And the listing "bottle@N2" has availability 0 UNKNOWN and no asOf

  Scenario: A feed older than the last applied feed of the node is superseded
    Given I apply the feed "f2" for node "N1" as of "2026-10-23T11:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 50  | PHYSICAL |
    When I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 99  | PHYSICAL |
    Then the feed "f1" is SUPERSEDED
    And the listing "bottle@N1" has availability 50 PHYSICAL as of "2026-10-23T11:00:00Z"
    And the last action raised no events

  Scenario: A superseded feed id is not applied twice
    Given I apply the feed "f2" for node "N1" as of "2026-10-23T11:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 50  | PHYSICAL |
    And I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 99  | PHYSICAL |
    When I apply the feed "f1" for node "N1" as of "2026-10-23T12:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 99  | PHYSICAL |
    Then the request fails with "ErrFeedAlreadyApplied"

  Scenario: A line naming a listing of another node fails the feed
    When I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | mug@N1    | 4   | PHYSICAL |
      | bottle@N2 | 5   | PHYSICAL |
    Then the request fails with "ErrListingNotOnNode"
    And the feed "f1" is FAILED with ErrListingNotOnNode
    And the listing "mug@N1" has availability 0 UNKNOWN and no asOf
    And the last action raised no events

  Scenario: A negative quantity fails the feed
    When I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing | qty | basis    |
      | mug@N1  | -1  | PHYSICAL |
    Then the request fails with "ErrNegativeAvailability"
    And the feed "f1" is FAILED with ErrNegativeAvailability

  Scenario: A failed feed id may be sent again
    Given I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N2 | 5   | PHYSICAL |
    When I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing | qty | basis    |
      | mug@N1  | 5   | PHYSICAL |
    Then the feed "f1" is APPLIED with 1 applied, 0 stale and 0 zeroed

  Scenario: An applied feed id is not applied twice
    Given I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing | qty | basis    |
      | mug@N1  | 5   | PHYSICAL |
    When I apply the feed "f1" for node "N1" as of "2026-10-23T12:00:00Z" with lines:
      | listing | qty | basis    |
      | mug@N1  | 6   | PHYSICAL |
    Then the request fails with "ErrFeedAlreadyApplied"
    And the listing "mug@N1" has availability 5 PHYSICAL as of "2026-10-23T10:00:00Z"

  Scenario: A suppressed listing still takes availability
    Given node-topology publishes the event "e1" NodeSuspended for node "N1" at "2026-10-23T09:30:00Z"
    When I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing | qty | basis    |
      | mug@N1  | 5   | PHYSICAL |
    Then the listing "mug@N1" is SUPPRESSED for NODE_NOT_SELLABLE
    And the listing "mug@N1" has availability 5 PHYSICAL as of "2026-10-23T10:00:00Z"
