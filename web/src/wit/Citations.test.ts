import { expect, test } from "vitest";
import { citeDate } from "./Citations";

test("citation dates are formatted according to locale", () => {
  const y = new Date().getFullYear();
  // zh-Hans / en
  expect(citeDate(new Date(y, 9, 3, 9).getTime(), "zh-Hans")).toBe("10/03");
  expect(citeDate(new Date(y, 8, 28, 9).getTime(), "zh-Hans")).toBe("9/28");
  expect(citeDate(new Date(y - 1, 11, 5, 9).getTime(), "zh-Hans")).toBe(`${y - 1}/12/05`);

  // German (DD.MM. and DD.MM.YYYY)
  expect(citeDate(new Date(y, 9, 3, 9).getTime(), "de")).toBe("03.10.");
  expect(citeDate(new Date(y, 8, 28, 9).getTime(), "de")).toBe("28.9.");
  expect(citeDate(new Date(y - 1, 11, 5, 9).getTime(), "de")).toBe(`05.12.${y - 1}`);

  expect(citeDate(0)).toBe("");
});
