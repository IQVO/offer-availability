import { OFFER_API_BASE } from "./config";
import type { CatalogItem, CatalogPage, InventoryFeed, ListingPage } from "./types";

/** RFC 9457 problem+json body every error response from offer-availability
 *  returns (`type` is a slug such as https://errors.offer-availability.warehouse-systems.dev/not-found). */
export interface ProblemDetails {
  type?: string;
  title?: string;
  status?: number;
  detail?: string;
  instance?: string;
}

/**
 * Every failed call -- an HTTP error with a problem+json body, an HTTP error
 * without one, or a network failure -- surfaces as one ApiError, so a screen
 * has a single shape to render: `title` (the problem's category) and `detail`
 * (what exactly was wrong), plus `slug` to branch on a specific problem
 * (e.g. `not-found`, `validation-failed`).
 */
export class ApiError extends Error {
  readonly status: number;
  readonly problem: ProblemDetails | null;
  readonly title: string;
  readonly detail: string;
  /** Last path segment of problem.type (e.g. "not-found"); "" when unknown. */
  readonly slug: string;

  constructor(status: number, problem: ProblemDetails | null, fallbackTitle: string) {
    const title = problem?.title || fallbackTitle;
    const detail = problem?.detail ?? "";
    super(detail || title);
    this.name = "ApiError";
    this.status = status;
    this.problem = problem;
    this.title = title;
    this.detail = detail;
    this.slug = problem?.type ? (problem.type.split("/").pop() ?? "") : "";
  }
}

/** Normalizes anything thrown by a request into an ApiError. */
export function toApiError(err: unknown): ApiError {
  if (err instanceof ApiError) return err;
  const message = err instanceof Error ? err.message : String(err);
  return new ApiError(0, { detail: message }, "Network error");
}

type Query = Record<string, string | number | boolean | undefined>;

export function withQuery(path: string, query?: Query): string {
  if (!query) return path;
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== "") params.set(key, String(value));
  }
  const qs = params.toString();
  return qs ? `${path}?${qs}` : path;
}

/**
 * The one door to the service. Only this context's own API is ever called
 * (`OFFER_API_BASE` = <apiOrigin>/api/offer-availability): no sibling
 * context, no vendor API. This remote is READ-ONLY: it issues GETs only --
 * creating items and listings and applying feeds are API/vendor operations,
 * not console ones (ADR 0007). GETs send no custom header, so they stay
 * simple CORS requests.
 */
async function get<T>(path: string): Promise<T> {
  let res: Response;
  try {
    res = await fetch(`${OFFER_API_BASE}${path}`, { method: "GET" });
  } catch (err) {
    throw toApiError(err);
  }
  if (!res.ok) {
    let problem: ProblemDetails | null = null;
    try {
      problem = (await res.json()) as ProblemDetails;
    } catch {
      // non-JSON error body -- fall through with problem = null
    }
    throw new ApiError(res.status, problem, `${res.status} ${res.statusText}`.trim());
  }
  return (await res.json()) as T;
}

const seg = encodeURIComponent;

/** GET /catalog?limit=&after= -- one keyset page of catalogue items. */
export async function browseCatalog(q: { limit?: number; after?: string } = {}): Promise<CatalogPage> {
  const res = await get<CatalogPage>(withQuery("/catalog", { limit: q.limit, after: q.after }));
  return { items: res.items ?? [], nextCursor: res.nextCursor || undefined };
}

/** GET /catalog/{catalogItemId} (404 not-found for an unknown id). */
export async function getCatalogItem(catalogItemId: string): Promise<CatalogItem> {
  return get<CatalogItem>(`/catalog/${seg(catalogItemId)}`);
}

/** GET /listings?nodeId=&catalogItemId=&limit=&after= -- the listings of a node and/or item. */
export async function listListings(
  q: { nodeId?: string; catalogItemId?: string; limit?: number; after?: string } = {},
): Promise<ListingPage> {
  const res = await get<ListingPage>(
    withQuery("/listings", { nodeId: q.nodeId, catalogItemId: q.catalogItemId, limit: q.limit, after: q.after }),
  );
  return { listings: res.listings ?? [], nextCursor: res.nextCursor || undefined };
}

/** GET /inventory-feeds/{feedId} (404 not-found for an unknown feed). */
export async function getInventoryFeed(feedId: string): Promise<InventoryFeed> {
  return get<InventoryFeed>(`/inventory-feeds/${seg(feedId)}`);
}
