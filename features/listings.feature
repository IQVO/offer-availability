Feature: Listings
  One listing per catalogue item and node. A listing is sellable only while its
  node is ACTIVE.

  Background:
    Given the catalogue item "bottle"

  Scenario: A listing on an active node is live
    Given the node "N1" is ACTIVE
    When I create a listing of "bottle" on node "N1"
    Then the listing "bottle@N1" is LIVE
    And the listing "bottle@N1" has availability 0 UNKNOWN and no asOf
    And the outbox holds "CatalogItemCreated, ListingCreated"

  Scenario Outline: A listing on a node that is not active starts suppressed
    Given the node "N1" is <status>
    When I create a listing of "bottle" on node "N1"
    Then the listing "bottle@N1" is SUPPRESSED for NODE_NOT_SELLABLE
    And the outbox holds "CatalogItemCreated, ListingCreated, ListingSuppressed"

    Examples:
      | status    |
      | PROPOSED  |
      | SUSPENDED |
      | RETIRED   |

  Scenario: A listing on a node nobody has heard of starts suppressed
    When I create a listing of "bottle" on node "GHOST"
    Then the listing "bottle@GHOST" is SUPPRESSED for NODE_NOT_SELLABLE

  Scenario: A second listing for the same item and node is refused
    Given the node "N1" is ACTIVE
    And a listing of "bottle" on node "N1"
    When I create a listing of "bottle" on node "N1"
    Then the request fails with "ErrDuplicateListing"

  Scenario: A listing for a discontinued item starts suppressed for good
    Given the node "N1" is ACTIVE
    And I discontinue the catalogue item "bottle"
    When I create a listing of "bottle" on node "N1"
    Then the listing "bottle@N1" is SUPPRESSED for DISCONTINUED
