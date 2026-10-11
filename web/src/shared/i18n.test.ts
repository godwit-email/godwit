import { makeT } from "./i18n";

test("substitutes in order", () => {
  const t = makeT({ a: "x %s y %d" });
  expect(t("a", "1", 2)).toBe("x 1 y 2");
  expect(t("missing")).toBe("missing");
});

test("%% is a percent sign, as in Go", () => {
  expect(makeT({ a: "约 %d%%" })("a", 62)).toBe("约 62%");
});
