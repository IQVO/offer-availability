import { describe, expect, it } from "vitest";
import { dimensions, grams, idProblem, optionalIdProblem, utcLabel, wholeNumber } from "./lib";

describe("utcLabel", () => {
  it("formats an instant to the second in UTC", () => {
    expect(utcLabel("2026-10-24T09:00:30+00:00")).toBe("2026-10-24 09:00:30Z");
    expect(utcLabel("2026-10-24T06:00:30-03:00")).toBe("2026-10-24 09:00:30Z");
  });
  it("shows a dash for nothing and the raw text for garbage", () => {
    expect(utcLabel(undefined)).toBe("—");
    expect(utcLabel("soon")).toBe("soon");
  });
});

describe("units", () => {
  it("shows grams with a separator", () => {
    expect(grams(900)).toBe("900 g");
    expect(grams(12345)).toBe("12,345 g");
  });
  it("joins the dimensions", () => {
    expect(dimensions({ lengthMm: 220, widthMm: 160, heightMm: 180 })).toBe("220 × 160 × 180 mm");
  });
});

describe("idProblem", () => {
  it("accepts a plain id", () => {
    expect(idProblem("catalogue item", "item-1")).toBeNull();
    expect(idProblem("node", "a".repeat(64))).toBeNull();
  });
  it("names what is wrong", () => {
    expect(idProblem("feed", "")).toMatch(/Enter a feed id/);
    expect(idProblem("feed", "a".repeat(65))).toMatch(/at most 64/);
    expect(idProblem("node", "a b")).toMatch(/cannot contain/);
    expect(idProblem("node", "a/b")).toMatch(/cannot contain/);
    expect(idProblem("node", "a\tb")).toMatch(/cannot contain/);
  });
  it("lets an optional filter be empty", () => {
    expect(optionalIdProblem("node", "")).toBeNull();
    expect(optionalIdProblem("node", "a/b")).toMatch(/cannot contain/);
  });
});

describe("number parsing", () => {
  it("wholeNumber", () => {
    expect(wholeNumber("90")).toBe(90);
    expect(wholeNumber("0")).toBe(0);
    expect(wholeNumber("12.5")).toBeNull();
    expect(wholeNumber("1e3")).toBeNull();
    expect(wholeNumber("-1")).toBeNull();
    expect(wholeNumber("")).toBeNull();
  });
});
