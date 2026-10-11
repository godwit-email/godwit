import { expect, test } from "vitest";
import { citeDate } from "./Citations";

test("citation dates are M/DD this year, YYYY/M/DD another year", () => {
  const y = new Date().getFullYear();
  expect(citeDate(new Date(y, 9, 3, 9).getTime())).toBe("10/03");
  expect(citeDate(new Date(y, 8, 28, 9).getTime())).toBe("9/28");
  expect(citeDate(new Date(y - 1, 11, 5, 9).getTime())).toBe(`${y - 1}/12/05`);
  expect(citeDate(0)).toBe("");
});
