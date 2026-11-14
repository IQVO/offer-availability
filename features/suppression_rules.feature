Feature: Suppression rules from the documentation
  ADR 0002 A4, A5, A13, A14 and A25, ADR 0013 rules 2 to 7 and the aggregate canvas of
  Listing: a listing shows ONE reason, the highest active cause (DISCONTINUED, then
  NODE_NOT_SELLABLE, then OWNER_SUSPENDED); the node and the seller policies move the
  listings of their node or owner and nobody else's; stale, repeated and redelivered
  events change nothing.

  Background:
    Given the node "N1" is ACTIVE
    And the catalogue item "bottle"
    And the catalogue item "mug"
    And a listing of "bottle" on node "N1"
    And a listing of "bottle" on node "N1" owned by "seller-7"
    And a listing of "mug" on node "N1" owned by "seller-7"
    And a listing of "bottle" on node "N1" owned by "seller-8"

  # ---- SellerSuspended (rule 4) ---------------------------------------------------

  Scenario: A suspended seller loses its live listings and nobody else's
    When seller-inbound publishes the event "s1" SellerSuspended for seller "seller-7" at "2026-11-08T10:00:00Z"
    Then the listing "bottle@N1/seller-7" is SUPPRESSED because OWNER_SUSPENDED
    And the listing "mug@N1/seller-7" is SUPPRESSED because OWNER_SUSPENDED
    And the listing "bottle@N1/seller-8" is LIVE
    And the listing "bottle@N1" is LIVE
    And the last events are "ListingSuppressed, ListingSuppressed"
    And the last "ListingSuppressed" event has "ListingID" "lst-3"
    And the last "ListingSuppressed" event has "NodeID" "N1"
    And the last "ListingSuppressed" event has "Reason" "OWNER_SUSPENDED"

  Scenario: Suppressing for the owner keeps the availability the vendor fed
    Given I apply the feed "f1" for node "N1" as of "2026-11-08T09:00:00Z" with lines:
      | listing            | qty | basis    |
      | bottle@N1/seller-7 | 36  | PHYSICAL |
    When seller-inbound publishes the event "s1" SellerSuspended for seller "seller-7" at "2026-11-08T10:00:00Z"
    Then the listing "bottle@N1/seller-7" has availability 36 PHYSICAL as of "2026-11-08T09:00:00Z"

  Scenario: A listing of a suspended owner still takes availability and stays suppressed
    Given seller-inbound publishes the event "s1" SellerSuspended for seller "seller-7" at "2026-11-08T10:00:00Z"
    When I apply the feed "f1" for node "N1" as of "2026-11-08T11:00:00Z" with lines:
      | listing            | qty | basis    |
      | bottle@N1/seller-7 | 12  | PHYSICAL |
    Then the feed "f1" is APPLIED with 1 applied, 0 stale and 0 zeroed
    And the listing "bottle@N1/seller-7" is SUPPRESSED because OWNER_SUSPENDED
    And the listing "bottle@N1/seller-7" has availability 12 PHYSICAL as of "2026-11-08T11:00:00Z"

  Scenario: A listing already suppressed for a higher cause keeps its reason when its owner is suspended
    Given node-topology publishes the event "n1" NodeSuspended for node "N1" at "2026-11-08T09:00:00Z"
    When seller-inbound publishes the event "s1" SellerSuspended for seller "seller-7" at "2026-11-08T10:00:00Z"
    Then the listing "bottle@N1/seller-7" is SUPPRESSED because NODE_NOT_SELLABLE
    And the last action raised no events

  # ---- SellerReinstated (rule 5) --------------------------------------------------

  Scenario: Reinstating a seller restores exactly its suppressed listings
    Given seller-inbound publishes the event "s1" SellerSuspended for seller "seller-7" at "2026-11-08T10:00:00Z"
    When seller-inbound publishes the event "s2" SellerReinstated for seller "seller-7" at "2026-11-08T12:00:00Z"
    Then the listing "bottle@N1/seller-7" is LIVE
    And the listing "mug@N1/seller-7" is LIVE
    And the last events are "ListingRestored, ListingRestored"
    And the last "ListingRestored" event has "ListingID" "lst-3"
    And the last "ListingRestored" event has "NodeID" "N1"

  Scenario: Reinstating a seller does not restore a listing its item has discontinued
    Given seller-inbound publishes the event "s1" SellerSuspended for seller "seller-7" at "2026-11-08T10:00:00Z"
    And I discontinue the catalogue item "mug"
    When seller-inbound publishes the event "s2" SellerReinstated for seller "seller-7" at "2026-11-08T12:00:00Z"
    Then the listing "bottle@N1/seller-7" is LIVE
    And the listing "mug@N1/seller-7" is SUPPRESSED because DISCONTINUED
    And the last events are "ListingRestored"

  Scenario: Reinstating a seller does not restore a listing its node still holds back
    Given seller-inbound publishes the event "s1" SellerSuspended for seller "seller-7" at "2026-11-08T10:00:00Z"
    And node-topology publishes the event "n1" NodeSuspended for node "N1" at "2026-11-08T11:00:00Z"
    When seller-inbound publishes the event "s2" SellerReinstated for seller "seller-7" at "2026-11-08T12:00:00Z"
    Then the listing "bottle@N1/seller-7" is SUPPRESSED because NODE_NOT_SELLABLE
    And the last action raised no events

  Scenario: Reinstating the seller and then activating the node frees the listing, in either order
    Given seller-inbound publishes the event "s1" SellerSuspended for seller "seller-7" at "2026-11-08T10:00:00Z"
    And node-topology publishes the event "n1" NodeSuspended for node "N1" at "2026-11-08T11:00:00Z"
    And seller-inbound publishes the event "s2" SellerReinstated for seller "seller-7" at "2026-11-08T12:00:00Z"
    When node-topology publishes the event "n2" NodeActivated for node "N1" at "2026-11-08T13:00:00Z"
    Then the listing "bottle@N1/seller-7" is LIVE
    And the listing "mug@N1/seller-7" is LIVE
    And the listing "bottle@N1" is LIVE

  # ---- the node policy meets the owner (rule 6, A14, A25) --------------------------

  Scenario: Activating a node does not restore a listing whose owner is suspended
    Given node-topology publishes the event "n1" NodeSuspended for node "N1" at "2026-11-08T09:00:00Z"
    And seller-inbound publishes the event "s1" SellerSuspended for seller "seller-7" at "2026-11-08T10:00:00Z"
    When node-topology publishes the event "n2" NodeActivated for node "N1" at "2026-11-08T11:00:00Z"
    Then the listing "bottle@N1/seller-7" is SUPPRESSED because OWNER_SUSPENDED
    And the listing "mug@N1/seller-7" is SUPPRESSED because OWNER_SUSPENDED
    And the listing "bottle@N1" is LIVE
    And the listing "bottle@N1/seller-8" is LIVE
    And the last action raised 2 "ListingSuppressed" events
    And the last action raised 2 "ListingRestored" events
    And the last "ListingSuppressed" event has "Reason" "OWNER_SUSPENDED"

  Scenario: Suspending a node outranks the suspended owner and changes the reason
    Given seller-inbound publishes the event "s1" SellerSuspended for seller "seller-7" at "2026-11-08T10:00:00Z"
    When node-topology publishes the event "n1" NodeSuspended for node "N1" at "2026-11-08T11:00:00Z"
    Then the listing "bottle@N1/seller-7" is SUPPRESSED because NODE_NOT_SELLABLE
    And the listing "mug@N1/seller-7" is SUPPRESSED because NODE_NOT_SELLABLE
    And the last action raised 4 "ListingSuppressed" events
    And the last "ListingSuppressed" event has "Reason" "NODE_NOT_SELLABLE"

  Scenario: Discontinuing an item outranks the suspended owner and changes the reason
    Given seller-inbound publishes the event "s1" SellerSuspended for seller "seller-7" at "2026-11-08T10:00:00Z"
    When I discontinue the catalogue item "mug"
    Then the listing "mug@N1/seller-7" is SUPPRESSED because DISCONTINUED
    And the listing "bottle@N1/seller-7" is SUPPRESSED because OWNER_SUSPENDED
    And the last events are "CatalogItemDiscontinued, ListingSuppressed"
    And the last "ListingSuppressed" event has "Reason" "DISCONTINUED"

  Scenario: Suspending a node leaves a discontinued listing at DISCONTINUED and raises no event for it
    Given I discontinue the catalogue item "mug"
    When node-topology publishes the event "n1" NodeSuspended for node "N1" at "2026-11-08T10:00:00Z"
    Then the listing "mug@N1/seller-7" is SUPPRESSED because DISCONTINUED
    And the last action raised 3 "ListingSuppressed" events

  # ---- creating a listing for a suspended owner (rule 7, A5) ---------------------------

  Scenario: A listing created for a suspended owner starts suppressed for OWNER_SUSPENDED
    Given seller-inbound publishes the event "s1" SellerSuspended for seller "seller-9" at "2026-11-08T10:00:00Z"
    When I create a listing of "mug" on node "N1" owned by "seller-9"
    Then the listing "mug@N1/seller-9" is SUPPRESSED because OWNER_SUSPENDED
    And the last events are "ListingCreated, ListingSuppressed"
    And the last "ListingCreated" event has "OwnerRef" "seller-9"
    And the last "ListingSuppressed" event has "Reason" "OWNER_SUSPENDED"

  Scenario: A listing created for a suspended owner on a node that is not active starts at NODE_NOT_SELLABLE
    Given the node "N2" is SUSPENDED
    And seller-inbound publishes the event "s1" SellerSuspended for seller "seller-9" at "2026-11-08T10:00:00Z"
    When I create a listing of "mug" on node "N2" owned by "seller-9"
    Then the listing "mug@N2/seller-9" is SUPPRESSED because NODE_NOT_SELLABLE
    And the last events are "ListingCreated, ListingSuppressed"

  Scenario: A listing created for a suspended owner and a discontinued item starts at DISCONTINUED
    Given the catalogue item "kettle"
    And I discontinue the catalogue item "kettle"
    And seller-inbound publishes the event "s1" SellerSuspended for seller "seller-9" at "2026-11-08T10:00:00Z"
    When I create a listing of "kettle" on node "N1" owned by "seller-9"
    Then the listing "kettle@N1/seller-9" is SUPPRESSED because DISCONTINUED
    And the last events are "ListingCreated, ListingSuppressed"

  Scenario: DISCONTINUED outranks NODE_NOT_SELLABLE on a new listing
    Given the node "N2" is SUSPENDED
    And the catalogue item "kettle"
    And I discontinue the catalogue item "kettle"
    When I create a listing of "kettle" on node "N2"
    Then the listing "kettle@N2" is SUPPRESSED because DISCONTINUED
    And the last events are "ListingCreated, ListingSuppressed"

  Scenario: A reinstated seller gets live listings again
    Given seller-inbound publishes the event "s1" SellerSuspended for seller "seller-9" at "2026-11-08T10:00:00Z"
    And seller-inbound publishes the event "s2" SellerReinstated for seller "seller-9" at "2026-11-08T11:00:00Z"
    When I create a listing of "mug" on node "N1" owned by "seller-9"
    Then the listing "mug@N1/seller-9" is LIVE
    And the last events are "ListingCreated"

  # ---- the SellerStatusView and its ordering (rule 2) ----------------------------------

  Scenario: A seller event older than the status the view holds is ignored whole
    Given seller-inbound publishes the event "s1" SellerSuspended for seller "seller-7" at "2026-11-08T12:00:00Z"
    When seller-inbound publishes the event "s2" SellerReinstated for seller "seller-7" at "2026-11-08T11:59:59Z"
    Then the listing "bottle@N1/seller-7" is SUPPRESSED because OWNER_SUSPENDED
    And the last action raised no events

  Scenario: A seller event at the very instant of the view's status is applied
    Given seller-inbound publishes the event "s1" SellerSuspended for seller "seller-7" at "2026-11-08T12:00:00Z"
    When seller-inbound publishes the event "s2" SellerReinstated for seller "seller-7" at "2026-11-08T12:00:00Z"
    Then the listing "bottle@N1/seller-7" is LIVE
    And the last events are "ListingRestored, ListingRestored"

  Scenario: A SellerSuspended that arrives after a later SellerReinstated of an unknown seller changes nothing
    Given seller-inbound publishes the event "s1" SellerReinstated for seller "seller-7" at "2026-11-08T12:00:00Z"
    When seller-inbound publishes the event "s2" SellerSuspended for seller "seller-7" at "2026-11-08T11:00:00Z"
    Then the listing "bottle@N1/seller-7" is LIVE
    And the listing "mug@N1/seller-7" is LIVE
    And the last action raised no events

  Scenario: A SellerReinstated for a seller the view has never heard of creates its view and touches nothing
    When seller-inbound publishes the event "s1" SellerReinstated for seller "seller-8" at "2026-11-08T12:00:00Z"
    Then the listing "bottle@N1/seller-8" is LIVE
    And the last action raised no events

  Scenario: A seller event whose id was already processed does nothing, whatever it now says
    Given seller-inbound publishes the event "s1" SellerSuspended for seller "seller-7" at "2026-11-08T10:00:00Z"
    When seller-inbound publishes the event "s1" SellerReinstated for seller "seller-7" at "2026-11-08T12:00:00Z"
    Then the listing "bottle@N1/seller-7" is SUPPRESSED because OWNER_SUSPENDED
    And the last action raised no events

  Scenario: A second SellerSuspended with a new id repeats the policy and finds nothing to change
    Given seller-inbound publishes the event "s1" SellerSuspended for seller "seller-7" at "2026-11-08T10:00:00Z"
    When seller-inbound publishes the event "s2" SellerSuspended for seller "seller-7" at "2026-11-08T10:30:00Z"
    Then the listing "bottle@N1/seller-7" is SUPPRESSED because OWNER_SUSPENDED
    And the last action raised no events

  Scenario: A seller event without its CloudEvents id is a validation error and changes nothing
    When seller-inbound publishes the event "" SellerSuspended for seller "seller-7" at "2026-11-08T10:00:00Z"
    Then the request fails with "ErrValidation"
    And the listing "bottle@N1/seller-7" is LIVE
    And the last action raised no events

  Scenario: A seller event for a malformed seller id is a validation error and changes nothing
    When seller-inbound publishes the event "s1" SellerSuspended for seller "not valid" at "2026-11-08T10:00:00Z"
    Then the request fails with "ErrValidation"
    And the last action raised no events

  # ---- the NodeStatusView and its ordering (A5, A13) -----------------------------------

  Scenario: A node event at the very instant of the view's status is applied
    Given node-topology publishes the event "n1" NodeSuspended for node "N1" at "2026-11-08T12:00:00Z"
    When node-topology publishes the event "n2" NodeActivated for node "N1" at "2026-11-08T12:00:00Z"
    Then the listing "bottle@N1" is LIVE
    And the listing "bottle@N1/seller-7" is LIVE

  Scenario: A node event one second older than the view's status is ignored
    Given node-topology publishes the event "n1" NodeSuspended for node "N1" at "2026-11-08T12:00:00Z"
    When node-topology publishes the event "n2" NodeActivated for node "N1" at "2026-11-08T11:59:59Z"
    Then the listing "bottle@N1" is SUPPRESSED because NODE_NOT_SELLABLE
    And the last action raised no events

  Scenario: A retired node ignores every later event, even a newer NodeSuspended
    Given node-topology publishes the event "n1" NodeRetired for node "N1" at "2026-11-08T12:00:00Z"
    When node-topology publishes the event "n2" NodeSuspended for node "N1" at "2026-11-08T13:00:00Z"
    Then the last action raised no events
    And the listing "bottle@N1" is SUPPRESSED because NODE_NOT_SELLABLE

  Scenario: Retiring a node that is already suspended raises nothing more
    Given node-topology publishes the event "n1" NodeSuspended for node "N1" at "2026-11-08T12:00:00Z"
    When node-topology publishes the event "n2" NodeRetired for node "N1" at "2026-11-08T13:00:00Z"
    Then the listing "bottle@N1/seller-7" is SUPPRESSED because NODE_NOT_SELLABLE
    And the last action raised no events

  Scenario: Activating a node that is already active changes nothing
    When node-topology publishes the event "n1" NodeActivated for node "N1" at "2026-11-08T12:00:00Z"
    Then the listing "bottle@N1" is LIVE
    And the last action raised no events

  Scenario: A node event without its CloudEvents id is a validation error and changes nothing
    When node-topology publishes the event "" NodeSuspended for node "N1" at "2026-11-08T12:00:00Z"
    Then the request fails with "ErrValidation"
    And the listing "bottle@N1" is LIVE

  Scenario: A node first heard of by NodeRegistered raises nothing and its listings start suppressed until NodeActivated
    When node-topology publishes the event "n1" NodeRegistered for node "N7" at "2026-11-08T09:00:00Z"
    Then the last action raised no events
    When I create a listing of "mug" on node "N7"
    Then the listing "mug@N7" is SUPPRESSED because NODE_NOT_SELLABLE
    When node-topology publishes the event "n2" NodeActivated for node "N7" at "2026-11-08T10:00:00Z"
    Then the listing "mug@N7" is LIVE
    And the last events are "ListingRestored"
