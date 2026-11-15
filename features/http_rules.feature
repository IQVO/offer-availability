Feature: REST API rules from the documentation
  The error rows of the use-cases page (ADR 0002 A2, A3, A11, A12, A18, A22 and ADR 0013 rule 7)
  seen through the real router: which status and problem slug each rule answers, what a
  FAILED or SUPERSEDED feed leaves behind, and the owner of a listing in the JSON views.

  Background:
    Given the node "N1" is ACTIVE
    And the node "N2" is ACTIVE
    And the catalogue item "bottle"
    And a listing of "bottle" on node "N1"
    And a listing of "bottle" on node "N2"

  # ---- catalogue ----------------------------------------------------------------------------

  Scenario: Discontinuing a discontinued item over HTTP answers 200 again and raises nothing
    Given a POST request is sent to "/catalog-items/item-1/discontinuations"
    When a POST request is sent to "/catalog-items/item-1/discontinuations"
    Then the response status is 200
    And the response field "state" is "DISCONTINUED"
    And the last action raised no events

  Scenario Outline: A dimension out of bounds is a 422 invalid-item-dimensions problem
    When a POST request is sent to "/catalog-items" with the JSON body:
      """
      {"title":"Bottle","lengthMm":<length>,"widthMm":75,"heightMm":75,"weightG":340,"hazmat":false,"fragile":false}
      """
    Then the response is a 422 "invalid-item-dimensions" problem
    And the last action raised no events

    Examples:
      | length     |
      | 2147483648 |
      | 0          |

  Scenario: A dimension of exactly 2147483647 is accepted over HTTP
    When a POST request is sent to "/catalog-items" with the JSON body:
      """
      {"title":"Pallet","lengthMm":2147483647,"widthMm":75,"heightMm":75,"weightG":340,"hazmat":false,"fragile":false}
      """
    Then the response status is 201
    And the response field "lengthMm" is 2147483647

  Scenario Outline: A page size of 1 or 500 is accepted
    When a GET request is sent to "/catalog?limit=<limit>"
    Then the response status is 200

    Examples:
      | limit |
      | 1     |
      | 500   |

  # ---- listings and their owner ---------------------------------------------------------------

  Scenario: A listing created over HTTP for an owner carries the ownerRef
    When a POST request is sent to "/listings" with the JSON body:
      """
      {"catalogItemId":"item-1","nodeId":"N1","ownerRef":"seller-7"}
      """
    Then the response status is 201
    And the response field "ownerRef" is "seller-7"
    And the response field "state" is "LIVE"

  Scenario: A listing created over HTTP for a suspended owner starts SUPPRESSED for OWNER_SUSPENDED
    Given seller-inbound publishes the event "s1" SellerSuspended for seller "seller-9" at "2026-11-08T10:00:00Z"
    When a POST request is sent to "/listings" with the JSON body:
      """
      {"catalogItemId":"item-1","nodeId":"N1","ownerRef":"seller-9"}
      """
    Then the response status is 201
    And the response field "state" is "SUPPRESSED"
    And the response field "suppressionReason" is "OWNER_SUSPENDED"
    And the last events are "ListingCreated, ListingSuppressed"

  Scenario: The listings list shows the reason OWNER_SUSPENDED once the seller is suspended, and none once reinstated
    Given a POST request is sent to "/listings" with the JSON body:
      """
      {"catalogItemId":"item-1","nodeId":"N1","ownerRef":"seller-7"}
      """
    And seller-inbound publishes the event "s1" SellerSuspended for seller "seller-7" at "2026-11-08T10:00:00Z"
    When a GET request is sent to "/listings?nodeId=N1&catalogItemId=item-1"
    Then the response status is 200
    And the response field "items.1.ownerRef" is "seller-7"
    And the response field "items.1.state" is "SUPPRESSED"
    And the response field "items.1.suppressionReason" is "OWNER_SUSPENDED"
    And the response field "items.0.state" is "LIVE"
    Given seller-inbound publishes the event "s2" SellerReinstated for seller "seller-7" at "2026-11-08T11:00:00Z"
    When a GET request is sent to "/listings?nodeId=N1&catalogItemId=item-1"
    Then the response field "items.1.state" is "LIVE"
    And the response has no field "items.1.suppressionReason"

  Scenario: An invalid ownerRef is a 422 validation-failed problem
    When a POST request is sent to "/listings" with the JSON body:
      """
      {"catalogItemId":"item-1","nodeId":"N1","ownerRef":"not valid"}
      """
    Then the response is a 422 "validation-failed" problem
    And the last action raised no events

  Scenario: A second listing of the same owner is 409 duplicate-listing, another owner's is 201
    Given a POST request is sent to "/listings" with the JSON body:
      """
      {"catalogItemId":"item-1","nodeId":"N1","ownerRef":"seller-7"}
      """
    When a POST request is sent to "/listings" with the JSON body:
      """
      {"catalogItemId":"item-1","nodeId":"N1","ownerRef":"seller-7"}
      """
    Then the response is a 409 "duplicate-listing" problem
    When a POST request is sent to "/listings" with the JSON body:
      """
      {"catalogItemId":"item-1","nodeId":"N1","ownerRef":"seller-8"}
      """
    Then the response status is 201

  # ---- inventory feeds ---------------------------------------------------------------------------

  Scenario: A negative quantity is a 422 negative-availability problem and the FAILED feed is kept
    When a POST request is sent to "/inventory-feeds" with the JSON body:
      """
      {"feedId":"f1","nodeId":"N1","fullUpdate":false,"asOf":"2026-11-09T10:00:00Z","lines":[{"listingId":"lst-1","availableQty":-1,"basis":"PHYSICAL"}]}
      """
    Then the response is a 422 "negative-availability" problem
    And the response problem has the instance "/inventory-feeds/f1"
    And the last action raised no events
    When a GET request is sent to "/inventory-feeds/f1"
    Then the response field "state" is "FAILED"
    And the response field "failureCode" is "ErrNegativeAvailability"

  Scenario: A feed older than the node's last applied feed is answered 201 SUPERSEDED and kept for GET
    Given a POST request is sent to "/inventory-feeds" with the JSON body:
      """
      {"feedId":"f2","nodeId":"N1","fullUpdate":false,"asOf":"2026-11-09T11:00:00Z","lines":[{"listingId":"lst-1","availableQty":50,"basis":"PHYSICAL"}]}
      """
    When a POST request is sent to "/inventory-feeds" with the JSON body:
      """
      {"feedId":"f1","nodeId":"N1","fullUpdate":false,"asOf":"2026-11-09T10:00:00Z","lines":[{"listingId":"lst-1","availableQty":99,"basis":"PHYSICAL"}]}
      """
    Then the response status is 201
    And the response field "state" is "SUPERSEDED"
    And the last action raised no events
    When a GET request is sent to "/inventory-feeds/f1"
    Then the response field "state" is "SUPERSEDED"
    When a GET request is sent to "/listings?nodeId=N1"
    Then the response field "items.0.availability.availableQty" is 50

  Scenario: A SUPERSEDED feed id sent again is 409 feed-already-applied
    Given a POST request is sent to "/inventory-feeds" with the JSON body:
      """
      {"feedId":"f2","nodeId":"N1","fullUpdate":false,"asOf":"2026-11-09T11:00:00Z","lines":[{"listingId":"lst-1","availableQty":50,"basis":"PHYSICAL"}]}
      """
    And a POST request is sent to "/inventory-feeds" with the JSON body:
      """
      {"feedId":"f1","nodeId":"N1","fullUpdate":false,"asOf":"2026-11-09T10:00:00Z","lines":[{"listingId":"lst-1","availableQty":99,"basis":"PHYSICAL"}]}
      """
    When a POST request is sent to "/inventory-feeds" with the JSON body:
      """
      {"feedId":"f1","nodeId":"N1","fullUpdate":false,"asOf":"2026-11-09T12:00:00Z","lines":[{"listingId":"lst-1","availableQty":99,"basis":"PHYSICAL"}]}
      """
    Then the response is a 409 "feed-already-applied" problem

  Scenario: A FAILED feed id sent again with corrected lines is applied
    Given a POST request is sent to "/inventory-feeds" with the JSON body:
      """
      {"feedId":"f1","nodeId":"N1","fullUpdate":false,"asOf":"2026-11-09T10:00:00Z","lines":[{"listingId":"lst-2","availableQty":5,"basis":"PHYSICAL"}]}
      """
    And the response is a 422 "listing-not-on-node" problem
    When a POST request is sent to "/inventory-feeds" with the JSON body:
      """
      {"feedId":"f1","nodeId":"N1","fullUpdate":false,"asOf":"2026-11-09T10:00:00Z","lines":[{"listingId":"lst-1","availableQty":5,"basis":"PHYSICAL"}]}
      """
    Then the response status is 201
    And the response field "state" is "APPLIED"

  Scenario: A FAILED feed id sent again for another node is a 422 validation-failed problem
    Given a POST request is sent to "/inventory-feeds" with the JSON body:
      """
      {"feedId":"f1","nodeId":"N1","fullUpdate":false,"asOf":"2026-11-09T10:00:00Z","lines":[{"listingId":"lst-2","availableQty":5,"basis":"PHYSICAL"}]}
      """
    When a POST request is sent to "/inventory-feeds" with the JSON body:
      """
      {"feedId":"f1","nodeId":"N2","fullUpdate":false,"asOf":"2026-11-09T10:00:00Z","lines":[{"listingId":"lst-2","availableQty":5,"basis":"PHYSICAL"}]}
      """
    Then the response is a 422 "validation-failed" problem

  Scenario: A quantity above 2147483647 is a 422 validation-failed problem and nothing is stored
    When a POST request is sent to "/inventory-feeds" with the JSON body:
      """
      {"feedId":"f1","nodeId":"N1","fullUpdate":false,"asOf":"2026-11-09T10:00:00Z","lines":[{"listingId":"lst-1","availableQty":2147483648,"basis":"PHYSICAL"}]}
      """
    Then the response is a 422 "validation-failed" problem
    When a GET request is sent to "/inventory-feeds/f1"
    Then the response is a 404 "not-found" problem

  Scenario: Two lines for one listing are a 422 validation-failed problem and nothing is stored
    When a POST request is sent to "/inventory-feeds" with the JSON body:
      """
      {"feedId":"f1","nodeId":"N1","fullUpdate":false,"asOf":"2026-11-09T10:00:00Z","lines":[{"listingId":"lst-1","availableQty":5,"basis":"PHYSICAL"},{"listingId":"lst-1","availableQty":6,"basis":"PHYSICAL"}]}
      """
    Then the response is a 422 "validation-failed" problem
    When a GET request is sent to "/inventory-feeds/f1"
    Then the response is a 404 "not-found" problem

  Scenario: A basis the vendor spells in lower case is a 422 validation-failed problem
    When a POST request is sent to "/inventory-feeds" with the JSON body:
      """
      {"feedId":"f1","nodeId":"N1","fullUpdate":false,"asOf":"2026-11-09T10:00:00Z","lines":[{"listingId":"lst-1","availableQty":5,"basis":"physical"}]}
      """
    Then the response is a 422 "validation-failed" problem

  Scenario: A partial feed without lines is a 422 validation-failed problem
    When a POST request is sent to "/inventory-feeds" with the JSON body:
      """
      {"feedId":"f1","nodeId":"N1","fullUpdate":false,"asOf":"2026-11-09T10:00:00Z","lines":[]}
      """
    Then the response is a 422 "validation-failed" problem

  Scenario: A full feed with no lines zeroes the node over HTTP and counts what it zeroed
    Given a POST request is sent to "/inventory-feeds" with the JSON body:
      """
      {"feedId":"f1","nodeId":"N1","fullUpdate":false,"asOf":"2026-11-09T10:00:00Z","lines":[{"listingId":"lst-1","availableQty":5,"basis":"PHYSICAL"}]}
      """
    When a POST request is sent to "/inventory-feeds" with the JSON body:
      """
      {"feedId":"f2","nodeId":"N1","fullUpdate":true,"asOf":"2026-11-09T11:00:00Z","lines":[]}
      """
    Then the response status is 201
    And the response field "state" is "APPLIED"
    And the response field "appliedCount" is 0
    And the response field "zeroedCount" is 1
    When a GET request is sent to "/listings?nodeId=N1"
    Then the response field "items.0.availability.availableQty" is 0
    And the response field "items.0.availability.basis" is "PHYSICAL"
    And the response field "items.0.availability.feedId" is "f2"
