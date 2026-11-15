Feature: Received seller stock rules from the documentation
  ADR 0013 rule 8 and ADR 0014 (gap G51): an InboundShipmentReceived creates the owner's
  listing, at availability 0 / UNKNOWN, for each received line that resolves to a
  catalogue item and has no listing yet. It never sets availability, never errors on a
  line it cannot resolve (the line is skipped and counted) and is inbox-idempotent.

  Background:
    Given the node "N1" is ACTIVE
    And the catalogue item "bottle"
    And the catalogue item "mug"
    And the pack chain of "bottle"

  # ---- what a received line creates ---------------------------------------------------

  Scenario: A received line with a catalogItemId creates the seller's listing at availability 0 UNKNOWN
    When seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item   | gtin           | qty |
      | bottle | 06141410000120 | 24  |
    Then the received event created 1 listing, found 0 existing and skipped 0 lines
    And the listing "bottle@N1/seller-7" is LIVE
    And the listing "bottle@N1/seller-7" is owned by "seller-7"
    And the listing "bottle@N1/seller-7" has availability 0 UNKNOWN and no asOf
    And the last events are "ListingCreated"
    And the last "ListingCreated" event has "CatalogItemID" "item-1"
    And the last "ListingCreated" event has "NodeID" "N1"
    And the last "ListingCreated" event has "OwnerRef" "seller-7"
    And no received line was counted as skipped

  Scenario Outline: A line without a catalogItemId resolves its GTIN to the item, whatever the pack level
    When seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item | gtin   | qty |
      |      | <gtin> | 6   |
    Then the received event created 1 listing, found 0 existing and skipped 0 lines
    And the listing "bottle@N1/seller-7" is LIVE
    And no received line was counted as skipped

    Examples:
      | gtin           |
      | 06141410000120 |
      | 06141410000298 |
      | 06141410000366 |

  Scenario: A retired pack level GTIN still resolves to its item
    Given I retire the CASE level of "bottle"
    When seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item | gtin           | qty |
      |      | 06141410000366 | 2   |
    Then the received event created 1 listing, found 0 existing and skipped 0 lines
    And the listing "bottle@N1/seller-7" is LIVE

  Scenario: The catalogItemId of a line wins over its GTIN
    When seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item | gtin           | qty |
      | mug  | 06141410000120 | 5   |
    Then the received event created 1 listing, found 0 existing and skipped 0 lines
    And the listing "mug@N1/seller-7" is LIVE
    And there is no listing of "bottle" on node "N1" owned by "seller-7"

  Scenario: The EACH and the CASE line of one item make one listing
    When seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item | gtin           | qty |
      |      | 06141410000120 | 6   |
      |      | 06141410000366 | 24  |
    Then the received event created 1 listing, found 0 existing and skipped 0 lines
    And the last events are "ListingCreated"

  Scenario: Two items in one shipment make two listings
    When seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item   | gtin           | qty |
      | bottle | 06141410000120 | 24  |
      | mug    | 06141410000502 | 12  |
    Then the received event created 2 listings, found 0 existing and skipped 0 lines
    And the last events are "ListingCreated, ListingCreated"

  Scenario: The received quantity never becomes availability, only the vendor feed sets it
    Given seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item   | gtin           | qty |
      | bottle | 06141410000120 | 24  |
    When I apply the feed "f1" for node "N1" as of "2026-11-09T10:00:00Z" with lines:
      | listing            | qty | basis    |
      | bottle@N1/seller-7 | 20  | PHYSICAL |
    Then the listing "bottle@N1/seller-7" has availability 20 PHYSICAL as of "2026-11-09T10:00:00Z"

  # ---- lines that create nothing ---------------------------------------------------------

  Scenario: A GTIN no catalogue item holds is skipped and counted, and the other lines still create their listings
    When seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item   | gtin           | qty |
      | bottle | 06141410000120 | 24  |
      |        | 06141410000779 | 3   |
    Then the received event created 1 listing, found 0 existing and skipped 1 line
    And the received lines were counted as skipped with the reasons "gtin_unresolved"
    And the listing "bottle@N1/seller-7" is LIVE

  Scenario Outline: A GTIN that is not a well-formed GS1 GTIN is skipped and counted, never an error
    When seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item | gtin   | qty |
      |      | <gtin> | 3   |
    Then the received event created 0 listings, found 0 existing and skipped 1 line
    And the received lines were counted as skipped with the reasons "gtin_unresolved"
    And the last action raised no events

    Examples:
      | gtin           |
      | 12345          |
      | 06141410000121 |
      |                |

  Scenario Outline: A catalogItemId the catalogue does not hold is skipped and counted as item_unknown
    When seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item   | gtin           | qty |
      | <item> | 06141410000120 | 3   |
    Then the received event created 0 listings, found 0 existing and skipped 1 line
    And the received lines were counted as skipped with the reasons "item_unknown"
    And the last action raised no events

    Examples:
      | item       |
      | item-999   |
      | not valid  |

  Scenario Outline: A line of zero or fewer eaches is skipped silently, resolvable or not
    When seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item   | gtin           | qty   |
      | bottle | 06141410000120 | <qty> |
      |        | 06141410000779 | <qty> |
    Then the received event created 0 listings, found 0 existing and skipped 0 lines
    And no received line was counted as skipped
    And the last action raised no events

    Examples:
      | qty |
      | 0   |
      | -1  |

  Scenario: A line of exactly 1 each creates the listing
    When seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item   | gtin           | qty |
      | bottle | 06141410000120 | 1   |
    Then the received event created 1 listing, found 0 existing and skipped 0 lines

  Scenario: Skipped lines are counted in line order, one reason each
    When seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item     | gtin           | qty |
      |          | 06141410000779 | 3   |
      | item-999 | 06141410000120 | 3   |
      |          | 12345          | 3   |
    Then the received event created 0 listings, found 0 existing and skipped 3 lines
    And the received lines were counted as skipped with the reasons "gtin_unresolved, item_unknown, gtin_unresolved"

  # ---- an existing listing is a no-op (the key is item, node and owner) ----------------------

  Scenario: A listing the seller already has is left exactly as it is
    Given a listing of "bottle" on node "N1" owned by "seller-7"
    And I apply the feed "f1" for node "N1" as of "2026-11-09T10:00:00Z" with lines:
      | listing            | qty | basis    |
      | bottle@N1/seller-7 | 40  | PHYSICAL |
    When seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item   | gtin           | qty |
      | bottle | 06141410000120 | 24  |
    Then the received event created 0 listings, found 1 existing and skipped 0 lines
    And the listing "bottle@N1/seller-7" has availability 40 PHYSICAL as of "2026-11-09T10:00:00Z"
    And the last action raised no events

  Scenario: The operator's listing of the same item and node does not stand in for the seller's
    Given a listing of "bottle" on node "N1"
    And I apply the feed "f1" for node "N1" as of "2026-11-09T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 50  | PHYSICAL |
    When seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item   | gtin           | qty |
      | bottle | 06141410000120 | 24  |
    Then the received event created 1 listing, found 0 existing and skipped 0 lines
    And the listing "bottle@N1/seller-7" has availability 0 UNKNOWN and no asOf
    And the listing "bottle@N1" has availability 50 PHYSICAL as of "2026-11-09T10:00:00Z"

  Scenario: Another seller's listing does not stand in for this seller's
    Given a listing of "bottle" on node "N1" owned by "seller-8"
    When seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item   | gtin           | qty |
      | bottle | 06141410000120 | 24  |
    Then the received event created 1 listing, found 0 existing and skipped 0 lines
    And the listing "bottle@N1/seller-7" is LIVE

  Scenario: The seller's listing on another node does not stand in for the receiving node's
    Given the node "N2" is ACTIVE
    And a listing of "bottle" on node "N2" owned by "seller-7"
    When seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item   | gtin           | qty |
      | bottle | 06141410000120 | 24  |
    Then the received event created 1 listing, found 0 existing and skipped 0 lines
    And the listing "bottle@N1/seller-7" is LIVE

  Scenario: A second shipment of the same item finds the listing the first one created
    Given seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item   | gtin           | qty |
      | bottle | 06141410000120 | 24  |
    When seller-inbound publishes the event "r2" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item   | gtin           | qty |
      | bottle | 06141410000120 | 12  |
    Then the received event created 0 listings, found 1 existing and skipped 0 lines
    And the last action raised no events

  # ---- the inbox and the events published before the lines existed --------------------------

  Scenario: A redelivered event id changes nothing and counts nothing a second time
    Given seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item   | gtin           | qty |
      | bottle | 06141410000120 | 24  |
      |        | 06141410000779 | 3   |
    When seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item   | gtin           | qty |
      | bottle | 06141410000120 | 24  |
      |        | 06141410000779 | 3   |
    Then the received event was a duplicate
    And the received event created 0 listings, found 0 existing and skipped 0 lines
    And no received line was counted as skipped
    And the last action raised no events

  Scenario: An event without a received list creates nothing and is counted as received_unknown
    When seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" without a received list
    Then the received event carried no received list
    And the received lines were counted as skipped with the reasons "received_unknown"
    And there is no listing of "bottle" on node "N1" owned by "seller-7"
    And the last action raised no events

  Scenario: An event with an empty received list is not the same as one without a list
    When seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item | gtin | qty |
    Then the received event created 0 listings, found 0 existing and skipped 0 lines
    And no received line was counted as skipped
    And the last action raised no events

  Scenario: An event without a received list still lands in the inbox
    Given seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" without a received list
    When seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item   | gtin           | qty |
      | bottle | 06141410000120 | 24  |
    Then the received event was a duplicate
    And there is no listing of "bottle" on node "N1" owned by "seller-7"

  Scenario Outline: An event without a valid id, sellerId or nodeId is a validation error and creates nothing
    When seller-inbound publishes the event "<event>" InboundShipmentReceived for seller "<seller>" to node "<node>" with received lines:
      | item   | gtin           | qty |
      | bottle | 06141410000120 | 24  |
    Then the request fails with "ErrValidation"
    And no received line was counted as skipped
    And the last action raised no events

    Examples:
      | event | seller     | node     |
      |       | seller-7   | N1       |
      | r1    |            | N1       |
      | r1    | not valid  | N1       |
      | r1    | seller-7   |          |
      | r1    | seller-7   | not valid |

  # ---- the listing starts suppressed for its highest cause (rule 8, A5, A25) ----------------

  Scenario Outline: A listing created on a node that is not active starts SUPPRESSED for NODE_NOT_SELLABLE
    Given the node "N3" is <status>
    When seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N3" with received lines:
      | item   | gtin           | qty |
      | bottle | 06141410000120 | 24  |
    Then the listing "bottle@N3/seller-7" is SUPPRESSED because NODE_NOT_SELLABLE
    And the last events are "ListingCreated, ListingSuppressed"
    And the last "ListingSuppressed" event has "Reason" "NODE_NOT_SELLABLE"

    Examples:
      | status    |
      | PROPOSED  |
      | SUSPENDED |
      | RETIRED   |

  Scenario: A node the view has never heard of is not sellable
    When seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N9" with received lines:
      | item   | gtin           | qty |
      | bottle | 06141410000120 | 24  |
    Then the listing "bottle@N9/seller-7" is SUPPRESSED because NODE_NOT_SELLABLE
    And the last events are "ListingCreated, ListingSuppressed"

  Scenario: A listing created for a discontinued item starts SUPPRESSED for DISCONTINUED
    Given I discontinue the catalogue item "bottle"
    When seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item | gtin           | qty |
      |      | 06141410000366 | 24  |
    Then the listing "bottle@N1/seller-7" is SUPPRESSED because DISCONTINUED
    And the last events are "ListingCreated, ListingSuppressed"
    And the last "ListingSuppressed" event has "Reason" "DISCONTINUED"

  Scenario: A listing created for a suspended seller starts SUPPRESSED for OWNER_SUSPENDED
    Given the seller "seller-7" is SUSPENDED
    When seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item   | gtin           | qty |
      | bottle | 06141410000120 | 24  |
    Then the listing "bottle@N1/seller-7" is SUPPRESSED because OWNER_SUSPENDED
    And the last events are "ListingCreated, ListingSuppressed"
    And the last "ListingSuppressed" event has "Reason" "OWNER_SUSPENDED"

  Scenario: A suspended seller on a node that is not active starts at NODE_NOT_SELLABLE
    Given the node "N3" is SUSPENDED
    And the seller "seller-7" is SUSPENDED
    When seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N3" with received lines:
      | item   | gtin           | qty |
      | bottle | 06141410000120 | 24  |
    Then the listing "bottle@N3/seller-7" is SUPPRESSED because NODE_NOT_SELLABLE
    And the last events are "ListingCreated, ListingSuppressed"

  Scenario: A discontinued item outranks a suspended seller and a node that is not active
    Given the node "N3" is SUSPENDED
    And the seller "seller-7" is SUSPENDED
    And I discontinue the catalogue item "bottle"
    When seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N3" with received lines:
      | item   | gtin           | qty |
      | bottle | 06141410000120 | 24  |
    Then the listing "bottle@N3/seller-7" is SUPPRESSED because DISCONTINUED
    And the last events are "ListingCreated, ListingSuppressed"

  Scenario: A listing created from received stock follows the seller's later suspension and reinstatement
    Given seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item   | gtin           | qty |
      | bottle | 06141410000120 | 24  |
    When seller-inbound publishes the event "s1" SellerSuspended for seller "seller-7" at "2026-11-09T10:00:00Z"
    Then the listing "bottle@N1/seller-7" is SUPPRESSED because OWNER_SUSPENDED
    When seller-inbound publishes the event "s2" SellerReinstated for seller "seller-7" at "2026-11-09T11:00:00Z"
    Then the listing "bottle@N1/seller-7" is LIVE

  Scenario: A listing created from received stock follows the node's later suspension
    Given seller-inbound publishes the event "r1" InboundShipmentReceived for seller "seller-7" to node "N1" with received lines:
      | item   | gtin           | qty |
      | bottle | 06141410000120 | 24  |
    When node-topology publishes the event "n1" NodeSuspended for node "N1" at "2026-11-09T10:00:00Z"
    Then the listing "bottle@N1/seller-7" is SUPPRESSED because NODE_NOT_SELLABLE
