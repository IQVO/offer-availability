Feature: Return completion restock rules from the documentation
  ADR 0015: a ReturnCompleted raises the available quantity of the owner listing
  of (catalogItemId, nodeId, ownerRef) by qtyRestockable; unsellable units never
  touch a listing, a line without a known listing is counted and skipped, and a
  redelivered CloudEvents id changes nothing.

  Background:
    Given the node "N1" is ACTIVE
    And the catalogue item "bottle"
    And the catalogue item "mug"

  # ---- what a return line restocks ---------------------------------------------

  Scenario: A return line raises the fed owner listing by its restockable units
    Given seller-inbound publishes the event "r0" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item   | gtin           | qty |
      | bottle | 06141410000120 | 10  |
    And I apply the feed "feed-1" for node "N1" as of "2026-11-13T10:00:00Z" with lines:
      | listing          | qty | basis    |
      | bottle@N1/seller-7 | 10 | PHYSICAL |
    When customer-returns publishes the event "r1" ReturnCompleted "ret-1" for node "N1" received at "2026-11-14T12:00:00Z" with lines:
      | item   | owner    | restockable | unsellable |
      | bottle | seller-7 | 2           | 1          |
    Then the return event restocked 1 listing and skipped 0 lines
    And the listing "bottle@N1/seller-7" has availability 12 PHYSICAL as of "2026-11-14T12:00:00Z"
    And the last events are "ListingAvailabilityChanged"
    And no return line was counted as skipped

  Scenario: A never-fed owner listing restocks at basis UNKNOWN and stamps the return
    Given seller-inbound publishes the event "r0" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item   | gtin           | qty |
      | bottle | 06141410000120 | 10  |
    When customer-returns publishes the event "r1" ReturnCompleted "ret-1" for node "N1" received at "2026-11-14T12:00:00Z" with lines:
      | item   | owner    | restockable | unsellable |
      | bottle | seller-7 | 3           | 0          |
    Then the return event restocked 1 listing and skipped 0 lines
    And the listing "bottle@N1/seller-7" has availability 3 UNKNOWN as of "2026-11-14T12:00:00Z"

  Scenario: A return line without a known listing is counted and skipped, never an error
    Given a listing of "bottle" on node "N1"
    When customer-returns publishes the event "r1" ReturnCompleted "ret-1" for node "N1" received at "2026-11-14T12:00:00Z" with lines:
      | item   | owner    | restockable | unsellable |
      | bottle | seller-7 | 2           | 0          |
      | mug    | seller-7 | 4           | 0          |
    Then the return event restocked 0 listings and skipped 2 lines
    And the return lines were counted as skipped with the reasons "listing_unknown, listing_unknown"
    And the listing "bottle@N1" has availability 0 UNKNOWN and no asOf
    And the last action raised no events

  Scenario: A line with only unsellable units never touches a listing
    Given a listing of "bottle" on node "N1"
    When customer-returns publishes the event "r1" ReturnCompleted "ret-1" for node "N1" received at "2026-11-14T12:00:00Z" with lines:
      | item   | owner | restockable | unsellable |
      | bottle |       | 0           | 4          |
    Then the return event restocked 0 listings and skipped 1 lines
    And the return lines were counted as skipped with the reasons "nothing_restockable"
    And the listing "bottle@N1" has availability 0 UNKNOWN and no asOf

  Scenario: A redelivered return event changes nothing
    Given a listing of "bottle" on node "N1"
    When customer-returns publishes the event "r1" ReturnCompleted "ret-1" for node "N1" received at "2026-11-14T12:00:00Z" with lines:
      | item   | owner | restockable | unsellable |
      | bottle |       | 2           | 0          |
    And customer-returns publishes the event "r1" ReturnCompleted "ret-1" for node "N1" received at "2026-11-14T12:00:00Z" with lines:
      | item   | owner | restockable | unsellable |
      | bottle |       | 2           | 0          |
    Then the return event was a duplicate
    And the listing "bottle@N1" has availability 2 UNKNOWN as of "2026-11-14T12:00:00Z"
    And the last action raised no events
