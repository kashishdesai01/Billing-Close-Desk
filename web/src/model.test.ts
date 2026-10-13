import { describe, it, expect } from "vitest";
import { cents, money } from "./model";
describe("money at the UI boundary", () => {
  it("parses decimal input without rounding fractional cents", () => {
    expect(cents("12.34")).toBe(1234);
    expect(cents("0.01")).toBe(1);
    expect(cents("12.3")).toBe(1230);
    expect(cents("12")).toBe(1200);
  });
  it("rejects ambiguous, negative and overprecise credits", () => {
    for (const input of ["-1", "0", "0.001", "1e3", "NaN", "1,000.00", " 1"])
      expect(() => cents(input)).toThrow();
  });
  it("refuses unsafe display integers", () => {
    expect(() => money(Number.MAX_SAFE_INTEGER + 1)).toThrow();
    expect(money(1234)).toBe("$12.34");
  });
});
