Feature: Seller-owned listings
  A listing may carry an ownerRef (the sellerId that owns the stock). There is
  one listing per item, node and owner; no ownerRef means operator-owned and
  behaves exactly as before.

  Background:
    Given the node "N1" is ACTIVE
    And the catalogue item "bottle"

  Scenario: Operator-owned and seller-owned listings coexist on one node
    Given a listing of "bottle" on node "N1"
    When I create a listing of "bottle" on node "N1" owned by "seller-7"
    Then the listing "bottle@N1/seller-7" is LIVE
    And the listing "bottle@N1/seller-7" is owned by "seller-7"
    And the listing "bottle@N1" is owned by ""
    And the last events are "ListingCreated"

  Scenario: Another seller has a listing of its own
    Given a listing of "bottle" on node "N1" owned by "seller-7"
    When I create a listing of "bottle" on node "N1" owned by "seller-8"
    Then the listing "bottle@N1/seller-8" is LIVE

  Scenario Outline: One listing per item, node and owner
    Given a listing of "bottle" on node "N1" owned by "<first>"
    When I create a listing of "bottle" on node "N1" owned by "<second>"
    Then the request fails with "ErrDuplicateListing"
    And the last action raised no events

    Examples:
      | first    | second   |
      |          |          |
      | seller-7 | seller-7 |

  Scenario: An invalid ownerRef is refused
    When I create a listing of "bottle" on node "N1" owned by "not valid"
    Then the request fails with "ErrValidation"
