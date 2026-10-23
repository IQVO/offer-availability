Feature: REST API (the eight operations of apis/openapi.yaml)
  The writes of merchandising and vendor-connectivity and the Open Host Service
  reads, through the real router: status codes, RFC 9457 problem slugs, the
  JSON views and the keyset pagination. Errors and their slugs are the table of
  .claude/rules/rest-api.md.

  Background:
    Given the node "N1" is ACTIVE

  Scenario: Creating a catalogue item answers 201 with the item and publishes CatalogItemCreated
    When a POST request is sent to "/catalog-items" with the JSON body:
      """
      {"title":"Bottle","lengthMm":270,"widthMm":75,"heightMm":75,"weightG":340,"hazmat":false,"fragile":true}
      """
    Then the response status is 201
    And the response field "catalogItemId" is "item-1"
    And the response field "title" is "Bottle"
    And the response field "state" is "ACTIVE"
    And the response field "fragile" is true
    And the last events are "CatalogItemCreated"
    And the last "CatalogItemCreated" event has "CatalogItemID" "item-1"
    And the last "CatalogItemCreated" event has "Title" "Bottle"

  Scenario: Non-positive dimensions are a 422 invalid-item-dimensions problem
    When a POST request is sent to "/catalog-items" with the JSON body:
      """
      {"title":"Bottle","lengthMm":0,"widthMm":75,"heightMm":75,"weightG":340,"hazmat":false,"fragile":false}
      """
    Then the response is a 422 "invalid-item-dimensions" problem
    And the last action raised no events

  Scenario: A malformed body is a 400 malformed-request-body problem
    When a POST request is sent to "/catalog-items" with the raw body:
      """
      {"title":
      """
    Then the response is a 400 "malformed-request-body" problem

  Scenario: An unknown field is a 400 malformed-request-body problem
    When a POST request is sent to "/catalog-items" with the JSON body:
      """
      {"title":"Bottle","lengthMm":270,"widthMm":75,"heightMm":75,"weightG":340,"hazmat":false,"fragile":false,"colour":"red"}
      """
    Then the response is a 400 "malformed-request-body" problem

  Scenario: An empty required title is a 422 validation-failed problem
    When a POST request is sent to "/catalog-items" with the JSON body:
      """
      {"title":"","lengthMm":270,"widthMm":75,"heightMm":75,"weightG":340,"hazmat":false,"fragile":false}
      """
    Then the response is a 422 "validation-failed" problem

  Scenario: A NUL character in the query string is a 400 malformed-request-query problem
    When a GET request is sent to "/catalog?after=%00"
    Then the response is a 400 "malformed-request-query" problem

  Scenario: Reading one item by id, and the 404 of an unknown one
    Given a POST request is sent to "/catalog-items" with the JSON body:
      """
      {"title":"Bottle","lengthMm":270,"widthMm":75,"heightMm":75,"weightG":340,"hazmat":false,"fragile":false}
      """
    When a GET request is sent to "/catalog/item-1"
    Then the response status is 200
    And the response field "catalogItemId" is "item-1"
    And the response field "state" is "ACTIVE"
    When a GET request is sent to "/catalog/item-404"
    Then the response is a 404 "not-found" problem

  Scenario: Discontinuing over HTTP suppresses the item's listings in the same answer
    Given a POST request is sent to "/catalog-items" with the JSON body:
      """
      {"title":"Bottle","lengthMm":270,"widthMm":75,"heightMm":75,"weightG":340,"hazmat":false,"fragile":false}
      """
    And a POST request is sent to "/listings" with the JSON body:
      """
      {"catalogItemId":"item-1","nodeId":"N1"}
      """
    When a POST request is sent to "/catalog-items/item-1:discontinue"
    Then the response status is 200
    And the response field "state" is "DISCONTINUED"
    And the last events are "CatalogItemDiscontinued, ListingSuppressed"
    When a GET request is sent to "/listings?nodeId=N1"
    Then the response field "listings.0.state" is "SUPPRESSED"
    And the response field "listings.0.suppressionReason" is "DISCONTINUED"
    When a POST request is sent to "/catalog-items/item-404:discontinue"
    Then the response is a 404 "not-found" problem

  Scenario: A listing on a node that is not active starts SUPPRESSED for NODE_NOT_SELLABLE
    Given a POST request is sent to "/catalog-items" with the JSON body:
      """
      {"title":"Bottle","lengthMm":270,"widthMm":75,"heightMm":75,"weightG":340,"hazmat":false,"fragile":false}
      """
    When a POST request is sent to "/listings" with the JSON body:
      """
      {"catalogItemId":"item-1","nodeId":"N2"}
      """
    Then the response status is 201
    And the response field "listingId" is "lst-1"
    And the response field "state" is "SUPPRESSED"
    And the response field "suppressionReason" is "NODE_NOT_SELLABLE"
    And the response field "availability.availableQty" is 0
    And the response field "availability.basis" is "UNKNOWN"
    And the response has no field "availability.asOf"

  Scenario: A listing for an unknown item is 404, a duplicate is 409 duplicate-listing
    Given a POST request is sent to "/catalog-items" with the JSON body:
      """
      {"title":"Bottle","lengthMm":270,"widthMm":75,"heightMm":75,"weightG":340,"hazmat":false,"fragile":false}
      """
    And a POST request is sent to "/listings" with the JSON body:
      """
      {"catalogItemId":"item-1","nodeId":"N1"}
      """
    When a POST request is sent to "/listings" with the JSON body:
      """
      {"catalogItemId":"item-1","nodeId":"N1"}
      """
    Then the response is a 409 "duplicate-listing" problem
    When a POST request is sent to "/listings" with the JSON body:
      """
      {"catalogItemId":"item-404","nodeId":"N1"}
      """
    Then the response is a 404 "not-found" problem

  Scenario: Listing filters combine and an empty result is an empty 200 list
    Given a POST request is sent to "/catalog-items" with the JSON body:
      """
      {"title":"Bottle","lengthMm":270,"widthMm":75,"heightMm":75,"weightG":340,"hazmat":false,"fragile":false}
      """
    And a POST request is sent to "/listings" with the JSON body:
      """
      {"catalogItemId":"item-1","nodeId":"N1"}
      """
    And a POST request is sent to "/listings" with the JSON body:
      """
      {"catalogItemId":"item-1","nodeId":"N2"}
      """
    When a GET request is sent to "/listings"
    Then the response list "listings" has 2 items
    When a GET request is sent to "/listings?nodeId=N1"
    Then the response list "listings" has 1 items
    And the response field "listings.0.listingId" is "lst-1"
    When a GET request is sent to "/listings?nodeId=N1&catalogItemId=item-1"
    Then the response list "listings" has 1 items
    When a GET request is sent to "/listings?nodeId=nowhere"
    Then the response status is 200
    And the response list "listings" has 0 items

  Scenario: Keyset pagination of the catalogue walk the pages by nextCursor
    Given a POST request is sent to "/catalog-items" with the JSON body:
      """
      {"title":"Bottle","lengthMm":270,"widthMm":75,"heightMm":75,"weightG":340,"hazmat":false,"fragile":false}
      """
    And a POST request is sent to "/catalog-items" with the JSON body:
      """
      {"title":"Mug","lengthMm":100,"widthMm":100,"heightMm":120,"weightG":400,"hazmat":false,"fragile":false}
      """
    And a POST request is sent to "/catalog-items" with the JSON body:
      """
      {"title":"Kettle","lengthMm":200,"widthMm":200,"heightMm":250,"weightG":900,"hazmat":false,"fragile":false}
      """
    When a GET request is sent to "/catalog?limit=2"
    Then the response list "items" has 2 items
    And the response field "items.0.catalogItemId" is "item-1"
    And the response field "items.1.catalogItemId" is "item-2"
    And the response field "nextCursor" is "item-2"
    When a GET request is sent to "/catalog?limit=2&after=item-2"
    Then the response list "items" has 1 items
    And the response field "items.0.catalogItemId" is "item-3"
    And the response has no field "nextCursor"
    When a GET request is sent to "/catalog?limit=3"
    Then the response list "items" has 3 items
    And the response has no field "nextCursor"

  Scenario: A page size out of 1 to 500 is a 422 validation-failed problem
    When a GET request is sent to "/catalog?limit=0"
    Then the response is a 422 "validation-failed" problem
    When a GET request is sent to "/catalog?limit=501"
    Then the response is a 422 "validation-failed" problem
    When a GET request is sent to "/catalog?limit=abc"
    Then the response is a 422 "validation-failed" problem

  Scenario: Keyset pagination of the listings list ends without a cursor on the last page
    Given a POST request is sent to "/catalog-items" with the JSON body:
      """
      {"title":"Bottle","lengthMm":270,"widthMm":75,"heightMm":75,"weightG":340,"hazmat":false,"fragile":false}
      """
    And a POST request is sent to "/listings" with the JSON body:
      """
      {"catalogItemId":"item-1","nodeId":"N1"}
      """
    And a POST request is sent to "/listings" with the JSON body:
      """
      {"catalogItemId":"item-1","nodeId":"N2"}
      """
    When a GET request is sent to "/listings?limit=1"
    Then the response list "listings" has 1 items
    And the response field "nextCursor" is "lst-1"
    When a GET request is sent to "/listings?limit=1&after=lst-1"
    Then the response list "listings" has 1 items
    And the response field "listings.0.listingId" is "lst-2"
    And the response has no field "nextCursor"

  Scenario: Applying a feed over HTTP answers 201 APPLIED with the counters
    Given a POST request is sent to "/catalog-items" with the JSON body:
      """
      {"title":"Bottle","lengthMm":270,"widthMm":75,"heightMm":75,"weightG":340,"hazmat":false,"fragile":false}
      """
    And a POST request is sent to "/listings" with the JSON body:
      """
      {"catalogItemId":"item-1","nodeId":"N1"}
      """
    When a POST request is sent to "/inventory-feeds" with the JSON body:
      """
      {"feedId":"f1","nodeId":"N1","fullUpdate":false,"asOf":"2026-10-23T10:00:00Z","lines":[{"listingId":"lst-1","availableQty":120,"basis":"THROUGHPUT_CONSTRAINED"}]}
      """
    Then the response status is 201
    And the response field "feedId" is "f1"
    And the response field "state" is "APPLIED"
    And the response field "appliedCount" is 1
    And the response field "staleCount" is 0
    And the response field "zeroedCount" is 0
    And the last events are "ListingAvailabilityChanged, InventoryFeedApplied"
    And the last "ListingAvailabilityChanged" event has "AvailableQty" "120"
    And the last "ListingAvailabilityChanged" event has "FeedID" "f1"
    And the last "InventoryFeedApplied" event has "FeedID" "f1"
    When a GET request is sent to "/listings?nodeId=N1"
    Then the response field "listings.0.availability.availableQty" is 120
    And the response field "listings.0.availability.basis" is "THROUGHPUT_CONSTRAINED"
    And the response field "listings.0.availability.asOf" is "2026-10-23T10:00:00Z"
    And the response field "listings.0.availability.feedId" is "f1"
    When a GET request is sent to "/inventory-feeds/f1"
    Then the response status is 200
    And the response field "state" is "APPLIED"
    When a GET request is sent to "/inventory-feeds/none"
    Then the response is a 404 "not-found" problem

  Scenario: An applied feed id sent again is 409 feed-already-applied
    Given a POST request is sent to "/catalog-items" with the JSON body:
      """
      {"title":"Bottle","lengthMm":270,"widthMm":75,"heightMm":75,"weightG":340,"hazmat":false,"fragile":false}
      """
    And a POST request is sent to "/listings" with the JSON body:
      """
      {"catalogItemId":"item-1","nodeId":"N1"}
      """
    And a POST request is sent to "/inventory-feeds" with the JSON body:
      """
      {"feedId":"f1","nodeId":"N1","fullUpdate":false,"asOf":"2026-10-23T10:00:00Z","lines":[{"listingId":"lst-1","availableQty":5,"basis":"PHYSICAL"}]}
      """
    When a POST request is sent to "/inventory-feeds" with the JSON body:
      """
      {"feedId":"f1","nodeId":"N1","fullUpdate":false,"asOf":"2026-10-23T12:00:00Z","lines":[{"listingId":"lst-1","availableQty":6,"basis":"PHYSICAL"}]}
      """
    Then the response is a 409 "feed-already-applied" problem
    And the last action raised no events

  Scenario: A FAILED feed answers 422 with its own instance and is kept for GET
    Given a POST request is sent to "/catalog-items" with the JSON body:
      """
      {"title":"Bottle","lengthMm":270,"widthMm":75,"heightMm":75,"weightG":340,"hazmat":false,"fragile":false}
      """
    And a POST request is sent to "/listings" with the JSON body:
      """
      {"catalogItemId":"item-1","nodeId":"N2"}
      """
    When a POST request is sent to "/inventory-feeds" with the JSON body:
      """
      {"feedId":"f9","nodeId":"N1","fullUpdate":false,"asOf":"2026-10-23T10:00:00Z","lines":[{"listingId":"lst-1","availableQty":5,"basis":"PHYSICAL"}]}
      """
    Then the response is a 422 "listing-not-on-node" problem
    And the response problem has the instance "/inventory-feeds/f9"
    And the last action raised no events
    When a GET request is sent to "/inventory-feeds/f9"
    Then the response status is 200
    And the response field "state" is "FAILED"
    And the response field "failureCode" is "ErrListingNotOnNode"

  Scenario: A feed missing a required field is a 422 validation-failed problem
    When a POST request is sent to "/inventory-feeds" with the JSON body:
      """
      {"feedId":"f1","nodeId":"N1","asOf":"2026-10-23T10:00:00Z","lines":[]}
      """
    Then the response is a 422 "validation-failed" problem
