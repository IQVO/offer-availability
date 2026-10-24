/** Wire types of apis/openapi.yaml (camelCase, as the REST contract names them). */

export const CATALOG_ITEM_STATES = ["ACTIVE", "DISCONTINUED"] as const;
export type CatalogItemState = (typeof CATALOG_ITEM_STATES)[number];

export const LISTING_STATES = ["LIVE", "SUPPRESSED"] as const;
export type ListingState = (typeof LISTING_STATES)[number];

export const SUPPRESSION_REASONS = ["NODE_NOT_SELLABLE", "DISCONTINUED"] as const;
export type SuppressionReason = (typeof SUPPRESSION_REASONS)[number];

export const AVAILABILITY_BASES = ["PHYSICAL", "THROUGHPUT_CONSTRAINED", "UNKNOWN"] as const;
export type AvailabilityBasis = (typeof AVAILABILITY_BASES)[number];

export const FEED_STATES = ["APPLIED", "SUPERSEDED", "FAILED"] as const;
export type FeedState = (typeof FEED_STATES)[number];

/** The CatalogItem schema. */
export interface CatalogItem {
  catalogItemId: string;
  title: string;
  lengthMm: number;
  widthMm: number;
  heightMm: number;
  weightG: number;
  hazmat: boolean;
  fragile: boolean;
  state: CatalogItemState;
}

/** The CatalogPage schema. */
export interface CatalogPage {
  items: CatalogItem[];
  nextCursor?: string;
}

/** The Availability schema (optional fields are omitted, never null). */
export interface Availability {
  availableQty: number;
  basis: AvailabilityBasis;
  asOf?: string;
  feedId?: string;
}

/** The Listing schema. */
export interface Listing {
  listingId: string;
  catalogItemId: string;
  nodeId: string;
  state: ListingState;
  suppressionReason?: SuppressionReason;
  availability: Availability;
}

/** The ListingPage schema. */
export interface ListingPage {
  listings: Listing[];
  nextCursor?: string;
}

/** The InventoryFeed schema. */
export interface InventoryFeed {
  feedId: string;
  nodeId: string;
  fullUpdate: boolean;
  asOf: string;
  state: FeedState;
  lineCount: number;
  appliedCount: number;
  staleCount: number;
  zeroedCount: number;
  receivedAt: string;
  appliedAt?: string;
  failureCode?: string;
}
