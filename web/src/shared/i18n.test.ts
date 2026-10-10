import { makeT } from "./i18n";

test("substitutes in order", () => {
  const t = makeT({ a: "x %s y %d" });
  expect(t("a", "1", 2)).toBe("x 1 y 2");
  expect(t("missing")).toBe("missing");
});
