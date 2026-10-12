import type { ReaderBody, Theme } from "@/mygo";
import { buildSrcdoc, FRAME_CSP, frameScheme } from "./frame";

const theme = (over: Partial<Theme> = {}): Theme => ({
  mode: "light",
  light: { "--foreground": "oklch(0.145 0 0)" },
  dark: { "--foreground": "oklch(0.985 0 0)" },
  fontSize: 14,
  bodyFontSize: 15,
  ...over,
});
const body = (over: Partial<ReaderBody> = {}): ReaderBody => ({
  message: 1,
  version: 1,
  html: '<p>Hello <span data-href="https://example.com">there</span></p>',
  plain: "",
  remote: false,
  blocked: 0,
  cut: false,
  theme: theme(),
  text: {},
  ...over,
});
const parse = (s: string) => new DOMParser().parseFromString(s, "text/html");

test("iframe csp first: the head's first element is the CSP meta, img-src data: and godwit-img: only", () => {
  const s = buildSrcdoc(body(), theme());
  expect(s.startsWith("<!doctype html>")).toBe(true);
  const head = parse(s).head;
  const first = head.firstElementChild;
  expect(first?.tagName).toBe("META");
  expect(first?.getAttribute("http-equiv")).toBe("Content-Security-Policy");
  expect(first?.getAttribute("content")).toBe(FRAME_CSP);
  expect(FRAME_CSP).toContain("img-src data: godwit-img:");
  expect(FRAME_CSP).not.toMatch(/https?:|\*/); // nothing reaches the network
  expect(FRAME_CSP).toContain("default-src 'none'");
  // The CSP precedes every byte of the mail.
  expect(s.indexOf(FRAME_CSP)).toBeLessThan(s.indexOf("Hello"));
});

test("hostile body stays inert: the parent adds no script, handler, javascript: or other meta", () => {
  const hostile = theme({
    light: { "--foreground": "red}</style><script>alert(1)</script>", "x;}": "blue" },
    bodyFontSize: "14px}</style><script>" as unknown as number,
  });
  const s = buildSrcdoc(body({ theme: hostile }), hostile);
  const lower = s.toLowerCase();
  expect(lower).not.toContain("<script");
  expect(lower).not.toContain("javascript:");
  expect(s).not.toMatch(/\son[a-z]+\s*=/i);
  const doc = parse(s);
  const metas = [...doc.querySelectorAll("meta[http-equiv]")];
  expect(metas).toHaveLength(1);
  expect(metas[0].getAttribute("http-equiv")).toBe("Content-Security-Policy");
  expect(doc.querySelectorAll("script, iframe, object, embed, form, base, link")).toHaveLength(0);
});

test("the body sits in #godwit-body; html mail is a white sheet with dark text and fitted images", () => {
  const doc = parse(buildSrcdoc(body(), theme({ mode: "dark" })));
  expect(doc.getElementById("godwit-body")?.innerHTML).toContain("Hello");
  const css = doc.querySelector("style")?.textContent ?? "";
  expect(css).toContain("#fff");
  expect(css).toContain("#0a0a0a");
  expect(css).toMatch(/img\{[^}]*max-width:100%/);
  expect(css).toContain("break-word");
  expect(css).not.toContain("invert");
  expect(css).not.toContain("0.985"); // not the dark theme's text
});

test("plain text uses theme and body size, escaped and pre-wrapped", () => {
  const plain = body({ html: "", plain: "a <b> & c\n  d" });
  const doc = parse(buildSrcdoc(plain, theme({ mode: "dark" })));
  const wrap = doc.getElementById("godwit-body");
  expect(wrap?.textContent).toBe("a <b> & c\n  d");
  expect(wrap?.children).toHaveLength(0);
  const css = doc.querySelector("style")?.textContent ?? "";
  expect(css).toContain("pre-wrap");
  expect(css).toContain("15px");
  expect(css).toContain("oklch(0.985 0 0)");
  expect(css).not.toContain("#0a0a0a");
});

test("plain text in system mode follows the appearance", () => {
  const plain = body({ html: "", plain: "x" });
  const css = parse(buildSrcdoc(plain, theme({ mode: "system" }))).querySelector(
    "style",
  )?.textContent;
  expect(css).toContain("oklch(0.145 0 0)");
  expect(css).toMatch(/@media \(prefers-color-scheme: ?dark\)\{[^}]*\{[^}]*oklch\(0\.985 0 0\)/);
});

test("plain text is transparent in the theme's colour scheme; html mail stays a light sheet", () => {
  const plain = body({ html: "", plain: "x" });
  const css = (m: string) =>
    parse(buildSrcdoc(plain, theme({ mode: m }))).querySelector("style")?.textContent ?? "";
  expect(css("dark")).toContain("color-scheme:dark");
  expect(css("light")).toContain("color-scheme:light");
  expect(css("system")).toContain("color-scheme:light dark");
  expect(css("dark")).toMatch(/html,body\{[^}]*background:transparent/);
  expect(css("dark")).not.toContain("#fff");
  expect(frameScheme(plain, theme({ mode: "dark" }))).toBe("dark");
  expect(frameScheme(plain, theme({ mode: "system" }))).toBe("light dark");
  expect(frameScheme(body(), theme({ mode: "dark" }))).toBe("light");
});
