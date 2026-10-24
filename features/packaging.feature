Feature: Catalogue identifiers and packaging
  An item may carry a GS1 GTIN and a packaging hierarchy EACH < INNER < CASE
  < PALLET. unitsPerPack (in eaches) never changes for a GTIN: a new count is a
  new GTIN and the old one is retired.

  Background:
    Given the node "N1" is ACTIVE

  Scenario: An item with a valid GTIN
    When I create the catalogue item "bottle" with gtin "6141410000120"
    Then the catalogue item "bottle" is ACTIVE
    And the item "bottle" has the gtin "06141410000120"
    And the outbox holds "CatalogItemCreated"

  Scenario Outline: A GTIN must be well formed and unused
    Given the catalogue item "jar" with gtin "06141410000366"
    When I create the catalogue item "bottle" with gtin "<gtin>"
    Then the request fails with "<error>"

    Examples:
      | gtin           | error                  |
      | 06141410000121 | ErrInvalidGTIN         |
      | 12345          | ErrInvalidGTIN         |
      | 96141410000115 | ErrInvalidGTIN         |
      | 06141410000366 | ErrGtinAlreadyAssigned |

  Scenario: Declaring the chain EACH, INNER, CASE
    Given the catalogue item "bottle"
    When I declare the EACH level of "bottle" with gtin "06141410000120" and 1 units
    And I declare the INNER level of "bottle" with gtin "06141410000298" over "06141410000120" and 6 units
    And I declare the CASE level of "bottle" with gtin "06141410000366" over "06141410000298" and 24 units
    Then the item "bottle" has the active CASE level "06141410000366" of 24 units per pack
    And the outbox holds "CatalogItemCreated, CatalogItemPackagingDeclared, CatalogItemPackagingDeclared, CatalogItemPackagingDeclared"

  Scenario: Declaring the same level twice changes nothing
    Given the catalogue item "bottle"
    And the pack chain of "bottle"
    When I declare the CASE level of "bottle" with gtin "06141410000366" over "06141410000298" and 24 units
    Then the last action raised no events

  Scenario: The units per pack of an active GTIN are immutable
    Given the catalogue item "bottle"
    And the pack chain of "bottle"
    When I declare the CASE level of "bottle" with gtin "06141410000366" over "06141410000298" and 48 units
    Then the request fails with "ErrPackQuantityImmutable"
    And the last action raised no events

  Scenario: A new GTIN supersedes the active level
    Given the catalogue item "bottle"
    And the pack chain of "bottle"
    When I declare the CASE level of "bottle" with gtin "06141410000502" over "06141410000298" and 12 units
    Then the item "bottle" has the active CASE level "06141410000502" of 12 units per pack
    And the item "bottle" has the retired CASE level "06141410000366"
    And the last events are "CatalogItemPackLevelRetired, CatalogItemPackagingDeclared"

  Scenario Outline: Level order and quantities
    Given the catalogue item "bottle"
    And I declare the EACH level of "bottle" with gtin "06141410000120" and 1 units
    When I declare the <level> level of "bottle" with gtin "06141410000298" over "<child>" and <units> units
    Then the request fails with "<error>"
    And the last action raised no events

    Examples:
      | level | child          | units | error                  |
      | CASE  | 06141410000502 | 6     | ErrPackLevelOrder      |
      | INNER | 06141410000298 | 6     | ErrPackLevelOrder      |
      | INNER | 06141410000120 | 1     | ErrInvalidPackQuantity |
      | INNER | 06141410000120 | 1000001 | ErrInvalidPackQuantity |

  Scenario: Retiring a level
    Given the catalogue item "bottle"
    And the pack chain of "bottle"
    When I retire the CASE level of "bottle"
    Then the item "bottle" has the retired CASE level "06141410000366"
    And the last events are "CatalogItemPackLevelRetired"
    When I retire the CASE level of "bottle"
    Then the last action raised no events
    When I retire the PALLET level of "bottle"
    Then the request fails with "ErrPackLevelNotFound"

  Scenario: A retired GTIN is never reused
    Given the catalogue item "bottle"
    And the pack chain of "bottle"
    And I retire the CASE level of "bottle"
    When I declare the CASE level of "bottle" with gtin "06141410000366" over "06141410000298" and 24 units
    Then the request fails with "ErrGtinAlreadyAssigned"

  Scenario: A discontinued item takes no packaging change
    Given the catalogue item "bottle"
    And I discontinue the catalogue item "bottle"
    When I declare the EACH level of "bottle" with gtin "06141410000120" and 1 units
    Then the request fails with "ErrIllegalTransition"
