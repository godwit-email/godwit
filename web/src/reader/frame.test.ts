import type { ReaderBody, Theme } from "@/mygo";
import { installFake } from "@/preview/fake";
import { buildSrcdoc, frameCSP, frameScheme, isTextHTML } from "./frame";

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
  // laid out (a table), not text in all but name (isTextHTML)
  html: '<table><tr><td><p>Hello <span data-href="https://example.com">there</span></p></td></tr></table>',
  plain: "",
  remote: false,
  blocked: 0,
  cut: false,
  theme: theme(),
  locale: "en",
  collapseQuotes: false,
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
  expect(first?.getAttribute("content")).toBe(frameCSP());
  expect(frameCSP()).toContain("img-src data: godwit-img:");
  expect(frameCSP()).not.toMatch(/https?:|\*/); // nothing reaches the network
  expect(frameCSP()).toContain("default-src 'none'");
  // The CSP precedes every byte of the mail.
  expect(s.indexOf(frameCSP())).toBeLessThan(s.indexOf("Hello"));
});

test("iframe csp on Windows: the image proxy's loopback http form too, which WebView2 loads it at (#750)", () => {
  installFake({}, "win32");
  try {
    const csp = frameCSP();
    expect(csp).toBe(
      "default-src 'none'; style-src 'unsafe-inline'; img-src data: godwit-img: http://*.godwit-img.localhost",
    );
    expect(
      parse(buildSrcdoc(body(), theme())).head.firstElementChild?.getAttribute("content"),
    ).toBe(csp);
  } finally {
    delete window.mygo;
  }
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
  expect(css).toContain("overflow-wrap:break-word");
  // word-break:break-word counts every letter as a break when sizing a table, so a column of
  // "Status" over an icon would be one letter wide
  expect(css).not.toContain("word-break");
  expect(css).toContain("px/normal "); // not the reading height: a mail's buttons and footers have none of their own
  // an image with only a height attribute keeps it (height:auto would show it at its natural
  // size); one with a width too shrinks in ratio
  expect(css).toContain("img:not([height]:not([width]))){height:auto}");
});

const render = (over: Partial<ReaderBody>, mode: Theme["mode"], systemDark = false) =>
  parse(buildSrcdoc(body(over), theme({ mode }), systemDark));
const cls = (d: Document) => d.getElementById("godwit-body")?.className;
const aware = {
  html: '<style data-dark="">#godwit-body.godwit-dark .a{color:red}</style><table><tr><td>x</td></tr></table>',
};

test("html mail keeps author dark styles, otherwise starts from a hidden light baseline", () => {
  expect(cls(render({}, "light"))).toBe("godwit-light");
  expect(cls(render(aware, "light"))).toBe("godwit-light");
  expect(cls(render(aware, "dark"))).toBe("godwit-dark");
  const automatic = render({}, "dark");
  expect(automatic.getElementById("godwit-body")?.hasAttribute("data-auto-dark")).toBe(true);
  expect(cls(automatic)).toContain("godwit-light");
  const css = styles(render({}, "dark"));
  expect(css).not.toContain("invert(");
  expect(css).not.toContain("::before");
  expect(css).toContain("visibility:hidden");
  expect(css).toContain("oklch(0.985 0 0)");
});

test("html mail in system mode follows the page's appearance", () => {
  expect(cls(render(aware, "system", false))).toBe("godwit-light");
  expect(cls(render(aware, "system", true))).toBe("godwit-dark");
  expect(
    render({}, "system", true).getElementById("godwit-body")?.hasAttribute("data-auto-dark"),
  ).toBe(true);
});

test("the document colour scheme follows the reader, including automatically adapted HTML", () => {
  const scheme = (b: Partial<ReaderBody>, mode: Theme["mode"], sys = false) =>
    frameScheme(body(b), theme({ mode }), sys);
  expect(scheme({}, "dark")).toBe("dark");
  expect(scheme(aware, "dark")).toBe("dark");
  expect(scheme(aware, "light")).toBe("light");
  expect(scheme(aware, "system", true)).toBe("dark");
  expect(scheme({}, "system", true)).toBe("dark");
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

test("plain text links its web addresses and marks its quoted lines", () => {
  const plain = body({
    html: "",
    plain:
      'Visit https://github.com/contact.\n> see https://x.example/a?b=1&c="2"\nno javascript:alert(1)\n' +
      "Go: https://en.wikipedia.org/wiki/Go_(programming_language). (or https://x.example/b)",
  });
  const wrap = parse(buildSrcdoc(plain, theme({ mode: "light" }))).getElementById("godwit-body");
  const links = [...(wrap?.querySelectorAll("[data-href]") ?? [])].map((e) =>
    e.getAttribute("data-href"),
  );
  expect(links).toEqual([
    "https://github.com/contact",
    "https://x.example/a?b=1&c=",
    "https://en.wikipedia.org/wiki/Go_(programming_language)", // its own ")", not the sentence's "."
    "https://x.example/b", // not the ")" closing the text's "("
  ]);
  expect(wrap?.querySelector(".godwit-quote")?.textContent).toBe(
    '> see https://x.example/a?b=1&c="2"',
  );
  expect(wrap?.textContent).toBe(plain.plain);
  expect(wrap?.querySelector("script,a")).toBeNull();
});

test("HTML of text and line breaks reads as plain text does", () => {
  expect(isTextHTML("Your code: 07698E<br/><br/>\n  这是验证码")).toBe(true);
  // a person's mail: its own type sizes, a list, a hidden tracking pixel, a mobile stylesheet
  expect(
    isTextHTML(
      "<style>@media (max-width:600px){#godwit-body{width:100% !important}}</style>" +
        '<div style="font-size: 13px"><p style="font-size: 13px; line-height: 16px">Hi</p>' +
        '<ul><li><a data-href="https://x.example">a link</a></li></ul><font size="1">bye</font>' +
        '<img style="height: 1px; width: 1px; display: none" alt=""/></div>',
    ),
  ).toBe(true);
  // a GitHub notification: classes, a muted footer, a hidden message id
  expect(
    isTextHTML(
      '<div class="email-fragment">Closed <a class="issue-link" data-href="https://github.com/o/r/pull/1">#1</a>.</div>' +
        '<p style="font-size: small; color: #666">—<br/>Reply to this email directly.' +
        '<span style="color: transparent; font-size: 0; display: none">Message ID: <span>x</span></span></p>',
    ),
  ).toBe(true);
  expect(isTextHTML("<table><tr><td>x</td></tr></table>")).toBe(false);
  expect(isTextHTML('<p style="background: #000">x</p>')).toBe(false);
  expect(isTextHTML('<td bgcolor="#eee">x</td>')).toBe(false);
  expect(isTextHTML("<style>.hero{background:url(x)}</style><p>x</p>")).toBe(false);
  expect(isTextHTML('<img src="data:," width="600">')).toBe(false);
  const text = parse(buildSrcdoc(body({ html: "a<br/>b" }), theme({ mode: "light" })));
  expect(text.getElementById("godwit-body")?.className).toBe("godwit-light godwit-text");
  const designed = parse(
    buildSrcdoc(body({ html: "<table><tr><td>a</td></tr></table>" }), theme({ mode: "light" })),
  );
  expect(designed.getElementById("godwit-body")?.className).toBe("godwit-light");
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

// Quoted text: a reply folds what it replies to behind a button, and keeps what its sender wrote.
const folded = (over: Partial<ReaderBody>) =>
  parse(buildSrcdoc(body({ collapseQuotes: true, ...over }), theme())).getElementById(
    "godwit-body",
  );

test("a reply's quoted text folds behind a pill and hairline rule", () => {
  const w = folded({
    html:
      "<p>Thanks, 8 works.</p>" +
      '<div class="gmail_quote"><div class="gmail_attr">On Tue, Bob wrote:</div>' +
      '<blockquote class="gmail_quote"><p>Dinner?</p></blockquote></div>',
  });
  const fold = w?.querySelector("details.godwit-quote-fold");
  expect(fold).not.toBeNull();
  expect(fold?.hasAttribute("open")).toBe(false);
  expect(fold?.querySelector("summary")?.textContent).toBe(""); // icons only: nothing extra copied with the mail
  expect(fold?.querySelector(".godwit-quote-pill svg.godwit-quote-dots")).not.toBeNull();
  expect(fold?.querySelector(".godwit-quote-line")).not.toBeNull();
  expect(fold?.querySelector(".godwit-quote-rail")).not.toBeNull();
  expect(fold?.textContent).toContain("Dinner?");
  expect(w?.firstElementChild?.textContent).toBe("Thanks, 8 works."); // the sender's own text stays out
});

test("a preceding single-line attribution folds with a bare blockquote, ordinary prose stays out", () => {
  const withAttr = folded({
    html: "<p>Thanks!</p><div><br></div><p>于 2026年11月12日 09:15，周明 写道：</p><blockquote><p>Dinner?</p></blockquote>",
  });
  const fold = withAttr?.querySelector("details.godwit-quote-fold");
  expect(fold?.textContent).toContain("写道：");
  expect(fold?.textContent).toContain("Dinner?");
  expect(withAttr?.firstElementChild?.textContent).toBe("Thanks!");
  expect(withAttr?.children).toHaveLength(2); // blank spacer above the fold was trimmed

  // Image-backed or bordered blocks right above the attribution/quote are sender content, not spacers.
  const withBanner = folded({
    html:
      "<p>Thanks!</p>" +
      '<div id="banner" style="height:40px;background-image:url(godwit-img://m1/sig)"></div>' +
      "<p>On Tue, Bob wrote:</p><blockquote><p>Dinner?</p></blockquote>",
  });
  expect(withBanner?.querySelector("#banner")).not.toBeNull();
  expect(withBanner?.querySelector("details #banner")).toBeNull();
  expect(withBanner?.querySelector("details")?.textContent).toContain("On Tue");

  const prose = folded({
    html: "<p>Hi,</p><p>My notes:</p><blockquote><p>Spec</p></blockquote>",
  });
  expect(prose?.querySelector("details")?.textContent).not.toContain("My notes:");
  expect(prose?.textContent).toContain("My notes:");

  // A multi-line container mixing sender prose with an attribution on a later line stays visible,
  // while a trailing <br> on a genuine single-line attribution still folds.
  const multiLine = folded({
    html: "<p>Hi</p><div>Do not pay yet.<br>On Tue, Bob wrote:</div><blockquote><p>Old request</p></blockquote>",
  });
  expect(multiLine?.querySelector("details")?.textContent).not.toContain("Do not pay yet.");
  expect(multiLine?.textContent).toContain("Do not pay yet.");

  const preWrapMultiLine = folded({
    html: '<p>Hi</p><div style="white-space:pre-wrap">Do not pay yet.\nOn Tue, Bob wrote:</div><blockquote><p>Old request</p></blockquote>',
  });
  expect(preWrapMultiLine?.querySelector("details")?.textContent).not.toContain("Do not pay yet.");
  expect(preWrapMultiLine?.textContent).toContain("Do not pay yet.");

  const inheritedPreMultiLine = folded({
    html: '<div style="white-space:pre-wrap"><p>Hi</p><div>Do not pay yet.\nOn Tue, Bob wrote:</div><blockquote><p>Old request</p></blockquote></div>',
  });
  expect(inheritedPreMultiLine?.querySelector("details")?.textContent).not.toContain(
    "Do not pay yet.",
  );

  const cssBlockSpans = folded({
    html: '<p>Hi</p><div><span style="display:block">Do not pay yet.</span><span style="display:block">On Tue, Bob wrote:</span></div><blockquote><p>Old request</p></blockquote>',
  });
  expect(cssBlockSpans?.querySelector("details")?.textContent).not.toContain("Do not pay yet.");
  expect(cssBlockSpans?.textContent).toContain("Do not pay yet.");

  const trailingBr = folded({
    html: "<p>Hi</p><div>On Tue, Bob wrote:<br></div><blockquote><p>Old request</p></blockquote>",
  });
  expect(trailingBr?.querySelector("details")?.textContent).toContain("On Tue, Bob wrote:");

  // Nothing but an attribution (block, bare text node, or nested wrapper) and a quote stays open.
  expect(
    folded({ html: "<p>On Tue, Bob wrote:</p><blockquote>Forwarded</blockquote>" })?.querySelector(
      "details",
    ),
  ).toBeNull();
  expect(
    folded({
      html: "<div>On Tue, Bob wrote:<br><blockquote>Original</blockquote></div>",
    })?.querySelector("details"),
  ).toBeNull();
  expect(
    folded({
      html: "<div><div><p>On Tue, Bob wrote:</p></div><blockquote>Original</blockquote></div>",
    })?.querySelector("details"),
  ).toBeNull();
  // Hidden preheaders before a quote-only message do not count as sender-written prose.
  expect(
    folded({
      html: '<div style="display:none">preview</div><blockquote>Original</blockquote>',
    })?.querySelector("details"),
  ).toBeNull();
  expect(
    folded({
      html: '<div style="max-height:0;overflow:hidden">preview</div><p>On Tue, Bob wrote:</p><blockquote>Original</blockquote>',
    })?.querySelector("details"),
  ).toBeNull();
  // Conversely, a hidden trailing message ID after a real reply's quote still lets the quote fold.
  expect(
    folded({
      html: '<p>Thanks!</p><blockquote>Original</blockquote><span style="display:none">Message ID: x</span>',
    })?.querySelector("details"),
  ).not.toBeNull();
  // A zero-font-size layout wrapper whose child restores a visible font size stays out of the fold.
  const zeroFontRestored = folded({
    html: '<p>Hi</p><p>On Tue, Bob wrote:</p><div style="font-size:0"><span style="font-size:14px">Do not pay yet.</span></div><blockquote>Old request</blockquote>',
  });
  expect(zeroFontRestored?.querySelector("details")?.textContent).not.toContain("Do not pay yet.");
  expect(zeroFontRestored?.textContent).toContain("Do not pay yet.");

  // Legacy <font size="0"> clamps to size 1 (visible text) rather than hiding like CSS font-size:0.
  const fontZero = folded({
    html: '<p>Hi</p><p>On Tue, Bob wrote:</p><font size="0">Do not pay yet.</font><blockquote>Old request</blockquote>',
  });
  expect(fontZero?.querySelector("details")?.textContent).not.toContain("Do not pay yet.");
  expect(fontZero?.textContent).toContain("Do not pay yet.");

  // <center> is a block boundary, so centered sender text before an inline attribution stays out.
  const centered = folded({
    html: "<p>Hi</p><center>Do not pay yet.</center>On Tue, Bob wrote:<br><blockquote>Old request</blockquote>",
  });
  expect(centered?.querySelector("details")?.textContent).not.toContain("Do not pay yet.");
  expect(centered?.textContent).toContain("Do not pay yet.");
  expect(centered?.querySelector("details")?.textContent).toContain("On Tue, Bob wrote:");

  // A positive min-height overrides height:0, keeping the overflow:hidden block visible outside.
  const minHeightVisible = folded({
    html: '<p>Hi</p><p>On Tue, Bob wrote:</p><div style="height:0;min-height:20px;overflow:hidden">Do not pay yet.</div><blockquote>Old request</blockquote>',
  });
  expect(minHeightVisible?.querySelector("details")?.textContent).not.toContain("Do not pay yet.");
  expect(minHeightVisible?.textContent).toContain("Do not pay yet.");

  // So does bottom padding given through the shorthand's third value.
  const paddedVisible = folded({
    html: '<p>Hi</p><p>On Tue, Bob wrote:</p><div style="height:0;overflow:hidden;padding:0 0 12px">Do not pay yet.</div><blockquote>Old request</blockquote>',
  });
  expect(paddedVisible?.querySelector("details")?.textContent).not.toContain("Do not pay yet.");
  expect(paddedVisible?.textContent).toContain("Do not pay yet.");

  // An inline span ignores height:0, so it does not clip and its text stays visible outside.
  const inlineClip = folded({
    html: '<p>Hi</p><p>On Tue, Bob wrote:</p><span style="height:0;overflow:hidden">Do not pay yet.</span><blockquote>Old request</blockquote>',
  });
  expect(inlineClip?.querySelector("details")?.textContent).not.toContain("Do not pay yet.");
  expect(inlineClip?.textContent).toContain("Do not pay yet.");

  // A preserved newline between sibling spans splits the run into two lines, not one attribution.
  const preSiblings = folded({
    html: '<div style="white-space:pre-wrap"><p>Hi</p><span>Do not pay yet.\n</span><span>On Tue, Bob wrote:</span><blockquote>Old request</blockquote></div>',
  });
  expect(preSiblings?.querySelector("details")?.textContent).not.toContain("Do not pay yet.");
  expect(preSiblings?.textContent).toContain("Do not pay yet.");

  // A stylesheet can make class-styled lines blocks: such a lead stays outside the fold.
  const sheetBlocks = folded({
    html: '<style>.line{display:block}</style><p>Hi</p><div><span class="line">Do not pay yet.</span><span class="line">On Tue, Bob wrote:</span></div><blockquote>Old request</blockquote>',
  });
  expect(sheetBlocks?.querySelector("details")).not.toBeNull();
  expect(sheetBlocks?.querySelector("details")?.textContent).not.toContain("Do not pay yet.");
  expect(sheetBlocks?.textContent).toContain("Do not pay yet.");

  // Rules that depend on the quote's parent or siblings would stop matching inside <details>.
  for (const sheet of [
    ".reply > blockquote{white-space:pre-wrap}",
    "p + blockquote{color:#333}",
    "blockquote:last-child{color:#333}",
  ]) {
    const structural = folded({
      html: `<style>${sheet}</style><div class="reply"><p>Hi</p><blockquote>a\n  b</blockquote></div>`,
    });
    expect(structural?.querySelector("details")).toBeNull();
  }
  // A mail rule that would reach the inserted controls (hiding the pill) leaves the quote unfolded;
  // one that only reaches the mail's own elements does not.
  for (const sheet of [
    ".mail span{display:none}",
    "#godwit-body .mail summary{display:none}",
    "*{visibility:hidden}",
  ]) {
    expect(
      folded({
        html: `<style>${sheet}</style><div class="mail"><p>Reply</p><blockquote>Old</blockquote></div>`,
      })?.querySelector("details"),
    ).toBeNull();
  }
  // A comma inside a quoted attribute value does not split the selector list.
  expect(
    folded({
      html: '<style>.mail[style*=","] span{display:none}</style><div class="mail" style="color:rgb(1,2,3)"><p>Reply</p><blockquote>Old</blockquote></div>',
    })?.querySelector("details"),
  ).toBeNull();
  expect(
    folded({
      html: '<style>#godwit-body .mail p{color:#333}</style><div class="mail"><p>Reply</p><blockquote>Old</blockquote></div>',
    })?.querySelector("details"),
  ).not.toBeNull();
  // A pseudo-element such as ::first-line does not depend on the quote's place.
  expect(
    folded({
      html: "<style>p::first-line{color:red}</style><p>Hi</p><blockquote>Old</blockquote>",
    })?.querySelector("details"),
  ).not.toBeNull();

  // Bare-text/inline attribution inside a reply container folds with the quote while keeping reply text out.
  const inlineAttr = folded({
    html: '<div>Thanks!<br><br>On Tue, <span data-href="mailto:b@x">Bob</span> wrote:<br><blockquote>Original</blockquote></div>',
  });
  expect(inlineAttr?.querySelector("details")?.textContent).toContain("On Tue, Bob wrote:");
  expect(inlineAttr?.querySelector("details")?.textContent).not.toContain("Thanks!");
  expect(inlineAttr?.textContent).toContain("Thanks!");
});

test("a bare blockquote folds once, collapsing wrapper shells while keeping nested quote bars", () => {
  const w = folded({ html: "<p>OK</p><blockquote>one<blockquote>two</blockquote></blockquote>" });
  expect(w?.querySelectorAll("details.godwit-quote-fold")).toHaveLength(1);
  expect(w?.querySelector("details blockquote")?.hasAttribute("data-quote-root")).toBe(true);
  const nested = w?.querySelector("details blockquote blockquote");
  expect(nested?.textContent).toBe("two");
  expect(nested?.hasAttribute("data-quote-root")).toBe(false);

  // Wrapper blockquote holding only an attribution + first quote (e.g. Compose's own reply markup)
  // collapses both into the single outer rail, while an older nested quote keeps its bar.
  const wrapped = folded({
    html:
      "<p>Reply</p>" +
      '<blockquote data-godwit="quote" style="margin:0;padding:0;border:0">' +
      "<p>于 2026-08-12 18:31, A 写道:</p>" +
      '<blockquote id="first"><p>Hello</p><blockquote id="older"><p>Prior</p></blockquote></blockquote>' +
      "</blockquote>",
  });
  expect(wrapped?.querySelector('[data-godwit="quote"]')?.hasAttribute("data-quote-root")).toBe(
    true,
  );
  expect(wrapped?.querySelector("#first")?.hasAttribute("data-quote-root")).toBe(true);
  expect(wrapped?.querySelector("#older")?.hasAttribute("data-quote-root")).toBe(false);
});

test("only a quote at the end folds: an inline reply stays open", () => {
  const w = folded({
    html: "<blockquote>q1</blockquote><p>my answer</p><blockquote>q2</blockquote><p>-- me</p>",
  });
  expect(w?.querySelector("details")).toBeNull();
});

test("a message that is nothing but quotes stays open, including sibling quotes", () => {
  expect(
    folded({ html: "<blockquote>Forwarded text</blockquote>" })?.querySelector("details"),
  ).toBeNull();
  expect(
    folded({
      html: "<blockquote>First</blockquote><blockquote>Second</blockquote>",
    })?.querySelector("details"),
  ).toBeNull();
  expect(
    folded({
      html: "<p>On Mon, A wrote:</p><blockquote>First</blockquote><p>On Tue, B wrote:</p><blockquote>Second</blockquote>",
    })?.querySelector("details"),
  ).toBeNull();
});

test("folded HTML quotes preserve inherited white-space from ancestors", () => {
  const doc = parse(
    buildSrcdoc(
      body({
        collapseQuotes: true,
        html: '<div style="white-space:pre-wrap"><p>Reply</p><blockquote id="q">a\n  b</blockquote></div>',
      }),
      theme(),
    ),
  );
  const style = document.createElement("style");
  style.textContent = doc.querySelector("style")?.textContent ?? "";
  const host = doc.body.cloneNode(true) as HTMLElement;
  document.head.append(style);
  document.body.append(host);
  try {
    const q = host.querySelector("#q") as HTMLElement;
    expect(getComputedStyle(q).whiteSpace).toBe("pre-wrap");
    const summary = host.querySelector("details.godwit-quote-fold > summary") as HTMLElement;
    expect(getComputedStyle(summary).whiteSpace).toBe("normal");
  } finally {
    style.remove();
    host.remove();
  }
});

test("folding is off unless asked, and leaves the markup as it came", () => {
  const html = "<p>OK</p><blockquote>q</blockquote>";
  const w = parse(buildSrcdoc(body({ html, collapseQuotes: false }), theme())).getElementById(
    "godwit-body",
  );
  expect(w?.querySelector("details")).toBeNull();
  expect(w?.innerHTML).toBe(html);
});

test("a leading style stays when a quote folds", () => {
  const w = folded({ html: "<style>p{color:red}</style><p>OK</p><blockquote>q</blockquote>" });
  expect(w?.querySelector("style")?.textContent).toBe("p{color:red}");
  expect(w?.querySelector("details")).not.toBeNull();
});

test("plain text: the trailing '>' lines and preceding attribution fold together", () => {
  const w = folded({
    html: "",
    plain: "Thanks!\n\nOn Tue, Bob wrote:\n> Dinner?\n> At 8?\n",
  });
  const fold = w?.querySelector("details.godwit-quote-fold");
  expect(fold?.textContent).toContain("On Tue, Bob wrote:");
  expect(fold?.textContent).toContain("> Dinner?");
  expect(fold?.textContent).toContain("> At 8?");
  expect(w?.firstChild?.textContent).toBe("Thanks!");
});

test("plain text: nothing but quoted lines (or attribution + quotes) stays open", () => {
  expect(folded({ html: "", plain: "> a\n> b" })?.querySelector("details")).toBeNull();
  expect(
    folded({ html: "", plain: "On Tue, Bob wrote:\n> a" })?.querySelector("details"),
  ).toBeNull();
  expect(
    folded({ html: "", plain: "> First\n\nOn Tue, Bob wrote:\n> Second" })?.querySelector(
      "details",
    ),
  ).toBeNull();
  expect(folded({ html: "", plain: "> First\n\n> Second" })?.querySelector("details")).toBeNull();
  // Non-Latin scripts (Cyrillic, Vietnamese) match without ASCII \b failures.
  expect(
    folded({ html: "", plain: "Боб написал:\n> Original" })?.querySelector("details"),
  ).toBeNull();
  expect(
    folded({ html: "", plain: "Vào Thứ Ba, Bob đã viết:\n> Original" })?.querySelector("details"),
  ).toBeNull();
  const ruReply = folded({ html: "", plain: "Спасибо!\n\n12 нояб., Боб пишет:\n> Ужин?" });
  expect(ruReply?.querySelector("details")?.textContent).toContain("Боб пишет:");
  expect(ruReply?.firstChild?.textContent).toBe("Спасибо!");
});
