import { describe, expect, it } from "vitest";
import { resolveOfferApiBase } from "./config";

describe("resolveOfferApiBase", () => {
  it("is this context's own /api/offer-availability under the runtime origin", () => {
    expect(resolveOfferApiBase({ apiOrigin: "http://localhost:8000" }, true)).toBe(
      "http://localhost:8000/api/offer-availability",
    );
  });

  it("drops trailing slashes of the origin", () => {
    expect(resolveOfferApiBase({ apiOrigin: "http://kong.local///" }, false)).toBe(
      "http://kong.local/api/offer-availability",
    );
  });

  it("falls back to the developer port outside production", () => {
    expect(resolveOfferApiBase({}, false)).toBe("http://localhost:8080");
  });

  it("refuses to guess in production", () => {
    expect(() => resolveOfferApiBase({}, true)).toThrow(/apiOrigin is required/);
    expect(() => resolveOfferApiBase({ apiOrigin: "" }, true)).toThrow(/apiOrigin is required/);
  });
});
