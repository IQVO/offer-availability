import { NO_VALUE } from "@warehouse/ui-kit";

/** "2026-10-24 09:00:00Z" for an RFC 3339 instant; the raw text when unparseable. */
export function utcLabel(iso: string | undefined): string {
  if (!iso) return NO_VALUE;
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const d = new Date(t).toISOString();
  return `${d.slice(0, 10)} ${d.slice(11, 19)}Z`;
}

/** Grams with a thousands separator: "1,234 g". */
export function grams(g: number): string {
  return `${g.toLocaleString("en-US")} g`;
}

/** Millimetres joined with ×: "220 × 160 × 180 mm". */
export function dimensions(mm: { lengthMm: number; widthMm: number; heightMm: number }): string {
  return `${mm.lengthMm} × ${mm.widthMm} × ${mm.heightMm} mm`;
}

/** The id rule shared by item, node, listing and feed ids: 1..64 characters,
 *  no whitespace, control characters or "/". */
export function idProblem(what: string, id: string): string | null {
  if (id.length === 0) return `Enter a ${what} id.`;
  if (id.length > 64) return `A ${what} id is at most 64 characters.`;
  // eslint-disable-next-line no-control-regex
  if (/[\s/\u0000-\u001f\u007f]/.test(id)) return `A ${what} id cannot contain spaces, control characters or '/'.`;
  return null;
}

/** Like idProblem, but an empty filter is fine ("any"). */
export function optionalIdProblem(what: string, id: string): string | null {
  return id.length === 0 ? null : idProblem(what, id);
}

/** A strictly whole number typed into a field, or null ("12.5", "1e3", "abc", ""). */
export function wholeNumber(raw: string): number | null {
  const s = raw.trim();
  if (!/^\d+$/.test(s)) return null;
  const n = Number(s);
  return Number.isSafeInteger(n) ? n : null;
}
