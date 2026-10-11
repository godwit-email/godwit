import { expect, test } from "vitest";
import { citeDate } from "./Citations";

test("citation dates are absolute M/DD", () => {
  expect(citeDate(new Date(2026, 9, 3, 9).getTime())).toBe("10/03");
  expect(citeDate(new Date(2026, 8, 28, 9).getTime())).toBe("9/28");
  expect(citeDate(0)).toBe("");
});
