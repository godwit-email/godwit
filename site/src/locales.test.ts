import { expect, test } from "bun:test";
import { pick } from "./locales";

test("the most preferred language we have wins", () => {
	expect(pick("zh-CN,zh;q=0.9,en;q=0.8")).toBe("zh-Hans");
	expect(pick("en-US,en;q=0.9,zh-CN;q=0.8")).toBe("en");
	expect(pick("ja,zh-CN;q=0.5,en;q=0.3")).toBe("zh-Hans");
});

test("q-values rank, not order", () => {
	expect(pick("en;q=0.4,zh-CN;q=0.9")).toBe("zh-Hans");
});

test("a language refused with q=0 is never picked", () => {
	expect(pick("zh;q=0,en;q=0.1")).toBe("en");
});

test("Traditional Chinese reads Simplified until there is a zh-Hant", () => {
	expect(pick("zh-TW")).toBe("zh-Hans");
	expect(pick("zh-Hant-HK,en;q=0.9")).toBe("zh-Hans");
});

test("browsers with none of ours, or no header, get English", () => {
	expect(pick("de-DE,fr;q=0.8")).toBe("en");
	expect(pick("")).toBe("en");
	expect(pick(null)).toBe("en");
});
