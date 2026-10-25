import type { ReactNode } from "react";
import { Link } from "react-router-dom";
import { DataTable, StatusPill } from "@warehouse/ui-kit";
import { grams, utcLabel } from "../lib";
import type { CatalogItem, FeedState, Listing, ListingState, SuppressionReason } from "../types";

export function PageHeader({ title, subtitle }: { title: string; subtitle: ReactNode }) {
  return (
    <div>
      <h1 style={{ fontSize: "var(--wh-font-size-2xl)", margin: 0 }}>{title}</h1>
      <p style={{ color: "var(--wh-color-text-muted)", marginTop: 4 }}>{subtitle}</p>
    </div>
  );
}

export function Stack({ children, gap = 4 }: { children: ReactNode; gap?: 3 | 4 | 5 }) {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: `var(--wh-space-${gap})` }}>
      {children}
    </div>
  );
}

/** A label/value list (dl) for a record's scalar fields. */
export function Facts({ items }: { items: { label: string; value: ReactNode }[] }) {
  return (
    <dl
      style={{
        display: "grid",
        gridTemplateColumns: "max-content 1fr",
        gap: "var(--wh-space-2) var(--wh-space-4)",
        margin: 0,
        fontSize: "var(--wh-font-size-sm)",
      }}
    >
      {items.map((item) => (
        <div key={item.label} style={{ display: "contents" }}>
          <dt style={{ color: "var(--wh-color-text-muted)" }}>{item.label}</dt>
          <dd style={{ margin: 0 }}>{item.value}</dd>
        </div>
      ))}
    </dl>
  );
}

type Tone = "neutral" | "progress" | "success" | "warning" | "danger";

/** A listing's and a feed's lifecycle is offer-availability's own, so
 *  StatusPill's tone override carries it. */
const LISTING_TONE: Record<ListingState, Tone> = { LIVE: "success", SUPPRESSED: "warning" };
const FEED_TONE: Record<FeedState, Tone> = { APPLIED: "success", SUPERSEDED: "neutral", FAILED: "danger" };

export function ListingStatePill({ state }: { state: ListingState }) {
  return <StatusPill status={state} tone={LISTING_TONE[state]} size="sm" />;
}

export function FeedStatePill({ state }: { state: FeedState }) {
  return <StatusPill status={state} tone={FEED_TONE[state]} size="sm" />;
}

/** One listing's availability as the vendor's feed last set it. */
export function availabilityLabel(l: Listing): string {
  const qty = `${l.availability.availableQty} (${l.availability.basis.toLowerCase()})`;
  return l.availability.asOf ? `${qty} at ${utcLabel(l.availability.asOf)}` : qty;
}

/** Why a listing is SUPPRESSED, in the contract's words. */
export function suppressionLabel(reason: SuppressionReason | undefined): string {
  return reason ?? "—";
}

/** The catalogue table of one page: id, title, footprint, flags, state. */
export function CatalogTable({
  items,
  itemTo,
}: {
  items: CatalogItem[];
  /** Relative link target of one item's detail route. */
  itemTo: (id: string) => string;
}) {
  return (
    <DataTable
      rowKey={(i) => i.catalogItemId}
      rows={items}
      columns={[
        { key: "id", header: "Item", render: (i) => <Link to={itemTo(i.catalogItemId)}>{i.catalogItemId}</Link> },
        { key: "title", header: "Title", render: (i) => i.title },
        {
          key: "dims",
          header: "Dimensions",
          render: (i) => `${i.lengthMm} × ${i.widthMm} × ${i.heightMm} mm · ${grams(i.weightG)}`,
        },
        {
          key: "flags",
          header: "Flags",
          render: (i) => [i.hazmat ? "hazmat" : "", i.fragile ? "fragile" : ""].filter(Boolean).join(", ") || "—",
        },
        { key: "state", header: "State", render: (i) => <StatusPill status={i.state} tone={i.state === "ACTIVE" ? "success" : "neutral"} size="sm" /> },
      ]}
    />
  );
}
