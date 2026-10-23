Feature: Listings follow the status of their node
  Policy SuppressOnNodeUnavailable: a suspended or retired node suppresses its
  live listings, an activated one restores those suppressed for the node.

  Background:
    Given the node "N1" is ACTIVE
    And the catalogue item "bottle"
    And the catalogue item "mug"
    And a listing of "bottle" on node "N1"
    And a listing of "mug" on node "N1"

  Scenario: A suspended node suppresses its listings, keeping their availability
    Given I apply the feed "f1" for node "N1" as of "2026-10-23T10:00:00Z" with lines:
      | listing   | qty | basis    |
      | bottle@N1 | 8   | PHYSICAL |
    When node-topology publishes the event "e1" NodeSuspended for node "N1" at "2026-10-23T11:00:00Z"
    Then the listing "bottle@N1" is SUPPRESSED for NODE_NOT_SELLABLE
    And the listing "mug@N1" is SUPPRESSED for NODE_NOT_SELLABLE
    And the listing "bottle@N1" has availability 8 PHYSICAL as of "2026-10-23T10:00:00Z"

  Scenario: An activated node restores the listings suppressed for the node
    Given node-topology publishes the event "e1" NodeSuspended for node "N1" at "2026-10-23T11:00:00Z"
    When node-topology publishes the event "e2" NodeActivated for node "N1" at "2026-10-23T12:00:00Z"
    Then the listing "bottle@N1" is LIVE
    And the listing "mug@N1" is LIVE

  Scenario: A discontinued listing is never restored
    Given I discontinue the catalogue item "mug"
    And node-topology publishes the event "e1" NodeSuspended for node "N1" at "2026-10-23T11:00:00Z"
    When node-topology publishes the event "e2" NodeActivated for node "N1" at "2026-10-23T12:00:00Z"
    Then the listing "bottle@N1" is LIVE
    And the listing "mug@N1" is SUPPRESSED for DISCONTINUED

  Scenario: A retired node stays retired
    Given node-topology publishes the event "e1" NodeRetired for node "N1" at "2026-10-23T11:00:00Z"
    When node-topology publishes the event "e2" NodeActivated for node "N1" at "2026-10-23T12:00:00Z"
    Then the listing "bottle@N1" is SUPPRESSED for NODE_NOT_SELLABLE

  Scenario: An event older than the node's status is ignored
    Given node-topology publishes the event "e1" NodeSuspended for node "N1" at "2026-10-23T12:00:00Z"
    When node-topology publishes the event "e2" NodeActivated for node "N1" at "2026-10-23T11:00:00Z"
    Then the listing "bottle@N1" is SUPPRESSED for NODE_NOT_SELLABLE

  Scenario: A redelivered event raises nothing the second time
    Given node-topology publishes the event "e1" NodeSuspended for node "N1" at "2026-10-23T11:00:00Z"
    When node-topology publishes the event "e1" NodeSuspended for node "N1" at "2026-10-23T11:00:00Z"
    Then the last action raised no events

  Scenario: NodeActivated for a node never seen before restores listings created suppressed
    When I create the catalogue item "kettle" of 200 x 200 x 250 mm and 900 g
    And I create a listing of "kettle" on node "N9"
    And node-topology publishes the event "e1" NodeActivated for node "N9" at "2026-10-23T12:00:00Z"
    Then the listing "kettle@N9" is LIVE

  Scenario: NodeRegistered of a known node does not demote it
    When node-topology publishes the event "e1" NodeRegistered for node "N1" at "2026-10-23T12:00:00Z"
    Then the listing "bottle@N1" is LIVE
