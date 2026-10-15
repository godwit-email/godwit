import { act, renderHook } from "@testing-library/react";
import type { Runtime } from "mygo-runtime";
import { createCatalog } from "mygo-runtime/i18n";
import * as generated from "@/i18n";
import { makeT, useT } from "./i18n";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

test("uses the generated locale catalog and preserves unknown keys", () => {
  expect(makeT("en")("common.save")).toBe("Save");
  expect(makeT("zh-CN")("common.save")).toBe("保存");
  expect(makeT("zh-Hans")("common.save")).toBe("保存");
  expect(makeT("zh-SG")("common.save")).toBe("保存");
  expect(makeT("en")("missing.key")).toBe("missing.key");
  expect(makeT("en")("toString")).toBe("toString");
});

test("passes named arguments intact to the catalog", () => {
  expect(makeT("en")("error.send_blocked", "%s", "100% full")).toBe(
    "Extension “%s” blocked sending: 100% full",
  );
});

test("the catalog chooses plural forms", () => {
  const t = makeT("en");
  expect(t("wit.tools", 0)).toBe("Used 0 tools");
  expect(t("wit.tools", 1)).toBe("Used 1 tool");
  expect(t("wit.tools", 2)).toBe("Used 2 tools");
  expect(makeT("zh-CN")("wit.tools", 1)).toBe("调用了 1 个工具");
});

test("literal percent signs come from the catalog", () => {
  expect(makeT("zh-CN")("wit.compose.rewrite", 62)).toBe("已整体重写，改动约 62%");
});

test("a page loaded after a locale change uses the current runtime language", () => {
  vi.stubGlobal("mygo", {
    locale: "en",
    on: () => () => {},
  } as unknown as Runtime);
  const { result } = renderHook(() => useT("zh-CN"));
  expect(result.current("common.save")).toBe("Save");
});

test("locale events update the hook", () => {
  let change: (payload: { locale: string; dir: string }) => void = () => {};
  const off = vi.fn();
  vi.stubGlobal("mygo", {
    locale: "zh-CN",
    on: (name: string, fn: typeof change) => {
      expect(name).toBe("mygo:locale");
      change = fn;
      return off;
    },
  } as unknown as Runtime);
  const { result, rerender, unmount } = renderHook(({ locale }) => useT(locale), {
    initialProps: { locale: "zh-CN" },
  });
  expect(result.current("common.save")).toBe("保存");
  act(() => change({ locale: "en", dir: "ltr" }));
  expect(result.current("common.save")).toBe("Save");
  rerender({ locale: "zh-CN" });
  expect(result.current("common.save")).toBe("Save");
  unmount();
  expect(off).toHaveBeenCalledOnce();
});

test("catalog placeholders can reorder named arguments in a translation", () => {
  const catalog = createCatalog({
    source: "en",
    languages: ["en", "zh-CN"],
    fallbacks: { en: ["en"], "zh-CN": ["zh-CN", "en"] },
    params: { test_reordered: ["arg1", "arg2"] },
    messages: {
      en: { test_reordered: "{arg1} then {arg2}" },
      "zh-CN": { test_reordered: "{arg2} 先于 {arg1}" },
    },
  });
  vi.spyOn(generated, "messagesIn").mockImplementation(
    (locale) => catalog.messagesIn(locale) as unknown as generated.Messages,
  );
  expect(makeT("zh-CN")("test.reordered", "A", "B")).toBe("B 先于 A");
});
