import type { SidebarsConfig } from "@docusaurus/plugin-content-docs";

const sidebar: SidebarsConfig = {
  apisidebar: [
    {
      type: "doc",
      id: "api-reference/rest/offer-availability-api",
    },
    {
      type: "category",
      label: "Catalog items",
      link: {
        type: "doc",
        id: "api-reference/rest/catalog-items",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/create-catalog-item",
          label: "Create a catalogue item (CreateCatalogItem)",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "api-reference/rest/discontinue-catalog-item",
          label: "Discontinue a catalogue item (DiscontinueItem)",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "api-reference/rest/declare-pack-level",
          label: "Declare a pack level of a catalogue item (DeclarePackLevel)",
          className: "api-method put",
        },
        {
          type: "doc",
          id: "api-reference/rest/retire-pack-level",
          label: "Retire the active pack level of a catalogue item (RetirePackLevel)",
          className: "api-method post",
        },
      ],
    },
    {
      type: "category",
      label: "Catalog",
      link: {
        type: "doc",
        id: "api-reference/rest/catalog",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/browse-catalog",
          label: "Browse the catalogue (BrowseCatalog)",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/get-catalog-item",
          label: "Get one catalogue item (BrowseCatalog)",
          className: "api-method get",
        },
      ],
    },
    {
      type: "category",
      label: "Listings",
      link: {
        type: "doc",
        id: "api-reference/rest/listings",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/create-listing",
          label: "Create a listing (CreateListing)",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "api-reference/rest/list-listings",
          label: "List listings by node and/or catalogue item",
          className: "api-method get",
        },
      ],
    },
    {
      type: "category",
      label: "Inventory feeds",
      link: {
        type: "doc",
        id: "api-reference/rest/inventory-feeds",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/apply-inventory-feed",
          label: "Apply an inventory feed for one node (ApplyInventoryFeed)",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "api-reference/rest/get-inventory-feed",
          label: "Get one inventory feed and its outcome",
          className: "api-method get",
        },
      ],
    },
  ],
};

export default sidebar.apisidebar;
