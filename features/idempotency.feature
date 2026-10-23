Feature: Idempotency-Key on the state-changing routes
  The OpenAPI parameter Idempotency-Key is required on POST /catalog-items,
  POST /catalog-items/{id}:discontinue, POST /listings and POST /inventory-feeds.
  A repeat with the same key and the same request replays the first answer and
  does the work once; the same key with another request is 422
  idempotency-key-reused (apis/openapi.yaml, .claude/rules/rest-api.md).

  Background:
    Given the node "N1" is ACTIVE

  Scenario: A repeat with the same key and body replays the first answer and raises no second event
    Given the client uses the Idempotency-Key "once"
    When a POST request is sent to "/catalog-items" with the JSON body:
      """
      {"title":"Bottle","lengthMm":270,"widthMm":75,"heightMm":75,"weightG":340,"hazmat":false,"fragile":false}
      """
    Then the response status is 201
    And the response is answered fresh
    When a POST request is sent to "/catalog-items" with the JSON body:
      """
      {"title":"Bottle","lengthMm":270,"widthMm":75,"heightMm":75,"weightG":340,"hazmat":false,"fragile":false}
      """
    Then the response status is 201
    And the response replays the stored answer
    And the response field "catalogItemId" is "item-1"
    And the last action raised no events
    And the outbox holds "CatalogItemCreated"

  Scenario: The same key with another body is 422 idempotency-key-reused
    Given the client uses the Idempotency-Key "once"
    When a POST request is sent to "/catalog-items" with the JSON body:
      """
      {"title":"Bottle","lengthMm":270,"widthMm":75,"heightMm":75,"weightG":340,"hazmat":false,"fragile":false}
      """
    Then the response status is 201
    When a POST request is sent to "/catalog-items" with the JSON body:
      """
      {"title":"Mug","lengthMm":100,"widthMm":100,"heightMm":120,"weightG":400,"hazmat":false,"fragile":false}
      """
    Then the response is a 422 "idempotency-key-reused" problem
    And the last action raised no events
    And the outbox holds "CatalogItemCreated"

  Scenario: The same key on another route is another request
    Given the client uses the Idempotency-Key "shared"
    When a POST request is sent to "/catalog-items" with the JSON body:
      """
      {"title":"Bottle","lengthMm":270,"widthMm":75,"heightMm":75,"weightG":340,"hazmat":false,"fragile":false}
      """
    Then the response status is 201
    When a POST request is sent to "/listings" with the JSON body:
      """
      {"catalogItemId":"item-1","nodeId":"N1"}
      """
    Then the response status is 201
    And the response is answered fresh
    And the response field "state" is "LIVE"

  Scenario: A missing key is 400 idempotency-key-required
    Given the client sends no Idempotency-Key
    When a POST request is sent to "/listings" with the JSON body:
      """
      {"catalogItemId":"item-1","nodeId":"N1"}
      """
    Then the response is a 400 "idempotency-key-required" problem
    And the last action raised no events

  Scenario: A key over 200 characters is 400 idempotency-key-invalid
    Given the client uses a 201-character Idempotency-Key
    When a POST request is sent to "/listings" with the JSON body:
      """
      {"catalogItemId":"item-1","nodeId":"N1"}
      """
    Then the response is a 400 "idempotency-key-invalid" problem
    And the last action raised no events
