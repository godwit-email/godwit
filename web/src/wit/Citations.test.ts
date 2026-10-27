import { expect, test } from "vitest";
import { citeDate, formatDate } from "./Citations";

test("formatDate matches Unicode CLDR standard across locales", () => {
  const d = new Date(2026, 9, 19); // Oct 19, 2026
  expect(formatDate(d, "de-DE", false)).toBe("19.10.");
  expect(formatDate(d, "de-DE", true)).toBe("19.10.2026");

  expect(formatDate(d, "fr-FR", false)).toBe("19/10");
  expect(formatDate(d, "fr-FR", true)).toBe("19/10/2026");

  expect(formatDate(d, "es-ES", false)).toBe("19/10");
  expect(formatDate(d, "es-ES", true)).toBe("19/10/2026");

  expect(formatDate(d, "it-IT", false)).toBe("19/10");
  expect(formatDate(d, "it-IT", true)).toBe("19/10/2026");

  expect(formatDate(d, "pt-PT", false)).toBe("19/10");
  expect(formatDate(d, "pt-PT", true)).toBe("19/10/2026");

  expect(formatDate(d, "en-US", false)).toBe("10/19");
  expect(formatDate(d, "en-US", true)).toBe("10/19/2026");

  expect(formatDate(d, "en-GB", false)).toBe("19/10");
  expect(formatDate(d, "en-GB", true)).toBe("19/10/2026");

  expect(formatDate(d, "zh-CN", false)).toBe("10/19");
  expect(formatDate(d, "zh-CN", true)).toBe("2026/10/19");

  expect(formatDate(d, "ja-JP", false)).toBe("10/19");
  expect(formatDate(d, "ja-JP", true)).toBe("2026/10/19");

  expect(formatDate(d, "ko-KR", false)).toBe("10. 19.");
  expect(formatDate(d, "ko-KR", true)).toBe("2026. 10. 19.");
});

test("citation dates are formatted according to locale", () => {
  const y = new Date().getFullYear();
  // zh-Hans / en
  expect(citeDate(new Date(y, 9, 19, 9).getTime(), "zh-Hans")).toBe("10/19");
  expect(citeDate(new Date(y, 8, 28, 9).getTime(), "zh-Hans")).toBe("9/28");
  expect(citeDate(new Date(y - 1, 9, 19, 9).getTime(), "zh-Hans")).toBe(`${y - 1}/10/19`);

  // German (DD.MM. and DD.MM.YYYY)
  expect(citeDate(new Date(y, 9, 19, 9).getTime(), "de")).toBe("19.10.");
  expect(citeDate(new Date(y, 8, 28, 9).getTime(), "de")).toBe("28.9.");
  expect(citeDate(new Date(y - 1, 9, 19, 9).getTime(), "de")).toBe(`19.10.${y - 1}`);

  expect(citeDate(0)).toBe("");
});
