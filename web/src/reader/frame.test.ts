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
  locale: "en",
  ...over,
});
const parse = (s: string) => new DOMParser().parseFromString(s, "text/html");
const styles = (doc: Document) =>
  (doc.querySelector("style")?.textContent ?? "")
    .replace(/\s*([{},:;])\s*/g, "$1")
    .replace(/;}/g, "}");

test.each([true, false])("font and theme parse as separate CSS rules (html: %s)", (html) => {
  const t = theme({ mode: "dark", bodyFontSize: 19 });
  const doc = parse(
    buildSrcdoc(body({ html: html ? '<style data-dark=""></style><p>x</p>' : "" }), t),
  );
  const style = document.createElement("style");
  style.textContent = doc.querySelector("style")?.textContent ?? "";
  document.head.append(style);
  try {
    const rules = Array.from(style.sheet?.cssRules ?? []).filter(
      (rule): rule is CSSStyleRule => rule.type === CSSRule.STYLE_RULE,
    );
    const font = rules.find(
      (rule) => rule.selectorText === "html" && rule.style.getPropertyValue("font"),
    );
    expect(font?.style.getPropertyValue("font-size")).toBe("19px");
    expect(font?.style.getPropertyValue("line-height")).toBe(html ? "normal" : "1.5");
    const scheme = rules.find(
      (rule) => rule.selectorText === "html" && rule.style.getPropertyValue("color-scheme"),
    );
    expect(scheme?.style.getPropertyValue("color-scheme")).toBe("dark");
    const property = html ? "--godwit-fg-dark" : "--godwit-plain-fg";
    expect(
      rules.find((rule) => rule.style.getPropertyValue(property))?.style.getPropertyValue(property),
    ).toBe(t.dark?.["--foreground"]);
  } finally {
    style.remove();
  }
});

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

test("the body sits in #godwit-body; html mail is transparent, in the theme's text, with fitted images", () => {
  const doc = parse(buildSrcdoc(body(), theme({ mode: "light" })));
  const wrap = doc.getElementById("godwit-body");
  expect(wrap?.innerHTML).toContain("Hello");
  expect(wrap?.className).toBe("godwit-light");
  const css = styles(doc);
  expect(css).toMatch(/html,body\{[^}]*background:transparent/);
  expect(css).not.toContain("#fff");
  expect(css).toContain("oklch(0.145 0 0)");
  expect(css).toMatch(/img\{[^}]*max-width:100%/);
  expect(css).toContain("break-word");
  expect(css).toContain("px/normal "); // not the reading height: a mail's buttons and footers have none of their own
  // an image with only a height attribute keeps it (height:auto would show it at its natural
  // size); one with a width too shrinks in ratio
  expect(css).toContain("img:not([height]:not([width]))){height:auto}");
});

const render = (over: Partial<ReaderBody>, mode: Theme["mode"], systemDark = false) =>
  parse(buildSrcdoc(body(over), theme({ mode }), systemDark));
const cls = (d: Document) => d.getElementById("godwit-body")?.className;
const aware = {
  html: '<style data-dark="">#godwit-body.godwit-dark .a{color:red}</style><p>x</p>',
};

test("html mail follows the theme: light, dark with its own dark styles, dark inverted without", () => {
  expect(cls(render({}, "light"))).toBe("godwit-light");
  expect(cls(render(aware, "light"))).toBe("godwit-light");
  expect(cls(render(aware, "dark"))).toBe("godwit-dark");
  expect(cls(render({}, "dark"))).toBe("godwit-dark godwit-invert");
  const css = styles(render({}, "dark"));
  expect(css).toMatch(
    /#godwit-body\.godwit-invert,#godwit-body\.godwit-invert img\{filter:invert\(1\)/,
  );
  expect(css).toMatch(/\.godwit-invert img\{filter:invert\(1\)/);
  // a background image is inverted back on its own layer, not with its element's content
  expect(css).toContain('[style*="--godwit-bg"]::before{');
  expect(css).toMatch(/::before\{[^}]*background-image:var\(--godwit-bg\)[^}]*filter:invert\(1\)/);
  expect(css).not.toMatch(/\[style\*="--godwit-bg"\]\{[^}]*filter/);
  expect(css).toContain("oklch(0.985 0 0)");
});

test("html mail in system mode follows the page's appearance", () => {
  expect(cls(render(aware, "system", false))).toBe("godwit-light");
  expect(cls(render(aware, "system", true))).toBe("godwit-dark");
  expect(cls(render({}, "system", true))).toBe("godwit-dark godwit-invert");
});

test("the document's colour scheme: dark only for html mail with dark styles in the dark", () => {
  const scheme = (b: Partial<ReaderBody>, mode: Theme["mode"], sys = false) =>
    frameScheme(body(b), theme({ mode }), sys);
  expect(scheme({}, "dark")).toBe("light");
  expect(scheme(aware, "dark")).toBe("dark");
  expect(scheme(aware, "light")).toBe("light");
  expect(scheme(aware, "system", true)).toBe("dark");
  expect(scheme({}, "system", true)).toBe("light");
  expect(render(aware, "dark").querySelector("style")?.textContent).toContain("color-scheme:dark");
});

test("plain text uses theme and body size, escaped and pre-wrapped", () => {
  const plain = body({ html: "", plain: "a <b> & c\n  d" });
  const doc = parse(buildSrcdoc(plain, theme({ mode: "dark" })));
  const wrap = doc.getElementById("godwit-body");
  expect(wrap?.textContent).toBe("a <b> & c\n  d");
  expect(wrap?.children).toHaveLength(0);
  const css = styles(doc);
  expect(css).toContain("pre-wrap");
  expect(css).toContain("15px/1.5 ");
  expect(css).toContain("oklch(0.985 0 0)");
  expect(css).toContain("--godwit-plain-fg:oklch(0.985 0 0)");
  expect(css).toContain("color:var(--godwit-plain-fg,CanvasText)");
});

test("plain text in system mode follows the appearance", () => {
  const plain = body({ html: "", plain: "x" });
  const css = parse(buildSrcdoc(plain, theme({ mode: "system" }))).querySelector(
    "style",
  )?.textContent;
  expect(css).toContain("oklch(0.145 0 0)");
  expect(css).toMatch(/@media \(prefers-color-scheme: ?dark\)\{[^}]*\{[^}]*oklch\(0\.985 0 0\)/);
});

test("plain text is transparent in the theme's colour scheme", () => {
  const plain = body({ html: "", plain: "x" });
  const css = (m: string) => styles(parse(buildSrcdoc(plain, theme({ mode: m }))));
  expect(css("dark")).toContain("color-scheme:dark");
  expect(css("light")).toContain("color-scheme:light");
  expect(css("system")).toContain("color-scheme:light dark");
  expect(css("dark")).toMatch(/html,body\{[^}]*background:transparent/);
  expect(css("dark")).not.toContain("#fff");
  expect(frameScheme(plain, theme({ mode: "dark" }))).toBe("dark");
  expect(frameScheme(plain, theme({ mode: "system" }))).toBe("light dark");
});
