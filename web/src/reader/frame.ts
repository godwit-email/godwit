import type { ReaderBody, Theme } from "@/mygo";
import { imageSources } from "@/shared/imageURL";
import { parse } from "./dark";
import frameCSS from "./frame.css?inline";

/**
 * The body iframe's own CSP, the first element of its head: nothing loads but inline styles,
 * data: images and the images Go's image proxy serves (godwit-img:, or its loopback http form on
 * Windows: imageURL); a remote image never reaches the network from the page. On top of the page's
 * CSP (ADR 0013).
 */
export const frameCSP = () =>
  `default-src 'none'; style-src 'unsafe-inline'; img-src data: ${imageSources()}`;

const escapeText = (s: string) => s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);

// A web address in plain text, with what may follow it (linkLength cuts that off).
const URL_RE = /\bhttps?:\/\/[^\s<>"']+/g;

/**
 * How much of a matched address is the link, as core/maillinks.go's mailLinks has it: not the
 * sentence punctuation after it, then not an unbalanced ")" ("text (url)"); a ")" that closes a
 * "(" in it is its own, so "Go_(lang)" keeps it and "a!)" its "!".
 */
function linkLength(u: string): number {
  let end = u.length;
  const open = u.split("(").length - 1;
  let closed = u.split(")").length - 1;
  while (end > 0 && ".,;:!?]}".includes(u[end - 1])) end--;
  while (end > 0 && u[end - 1] === ")" && open < closed) {
    closed--;
    end--;
  }
  return end;
}

// A single-line reply lead ("On … wrote:", "于 … 写道：") placed right above a quote by mail clients.
// Uses explicit start/whitespace/comma boundaries instead of \b so Cyrillic and accented Latin match.
const ATTR_RE =
  /(?:^|[\s,，])(?:wrote\s*:|a écrit\s*:|escribió\s*:|escreveu\s*:|ha scritto\s*:|(?:пишет|написал[аи]?)\s*:|yazdı\s*:|đã viết\s*:|(?:schrieb|schreef)(?=\s).*:)|(?:写道|寫道|書きました)\s*[:：]|(?:^|\s)작성\s*:/i;

function isAttribution(text: string): boolean {
  const trimmed = text.trim();
  if (!trimmed || trimmed.includes("\n")) return false;
  const s = trimmed.replace(/\s+/g, " ");
  return s.length <= 240 && /[:：]$/.test(s) && ATTR_RE.test(s);
}

// A quote folded behind a pill and hairline rule: <details> needs no script in the CSP-locked frame;
// the parent wires the left collapse rail and the hover tooltip.
const DOTS_SVG =
  '<svg class="godwit-quote-dots" width="16" height="10" viewBox="0 0 16 10" fill="currentColor" aria-hidden="true">' +
  '<circle cx="3" cy="5" r="1.45"/><circle cx="8" cy="5" r="1.45"/><circle cx="13" cy="5" r="1.45"/></svg>';
const UP_SVG =
  '<svg class="godwit-quote-up" width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" ' +
  'stroke-width="1.65" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
  '<path d="M3 7.5L6 4.5L9 7.5"/></svg>';
const FOLD_SUMMARY =
  `<summary data-godwit-ui><span class="godwit-quote-pill">${DOTS_SVG}${UP_SVG}</span>` +
  '<span class="godwit-quote-line" aria-hidden="true"></span></summary>';
const FOLD_RAIL =
  '<span class="godwit-quote-rail" data-godwit-ui aria-hidden="true">' +
  '<span class="godwit-quote-rail-line"></span></span>';
const FOLD_OPEN = `<details class="godwit-quote-fold">${FOLD_SUMMARY}<div class="godwit-quote-body">${FOLD_RAIL}`;
const FOLD_CLOSE = "</div></details>";

/**
 * Plain text as HTML: escaped, its web addresses links (data-href, as Go makes an HTML mail's)
 * and its quoted lines ("> …") marked, which the style mutes.
 */
export function plainHTML(text: string, fold = false): string {
  const lines = text.split("\n");
  const quoted = (line: string) => /^\s*>/.test(line);
  // With fold: the quoted lines at the end (and a preceding "On … wrote:" lead) fold behind a
  // button, when the message has words of its own above them.
  let from = lines.length;
  if (fold) {
    while (from > 0 && (quoted(lines[from - 1]) || lines[from - 1].trim() === "")) from--;
    while (from < lines.length && !quoted(lines[from])) from++;
    if (from < lines.length) {
      let lead = from - 1;
      while (lead >= 0 && lines[lead].trim() === "") lead--;
      if (lead >= 0 && isAttribution(lines[lead])) from = lead;
    }
    if (
      from === lines.length ||
      !lines.slice(0, from).some((l) => l.trim() !== "" && !quoted(l) && !isAttribution(l))
    )
      from = lines.length;
  }
  const renderLine = (line: string) => {
    let html = "";
    let at = 0;
    for (const m of line.matchAll(URL_RE)) {
      const link = m[0].slice(0, linkLength(m[0]));
      if (!/^https?:\/\/./.test(link)) continue; // nothing but the scheme
      const url = escapeText(link);
      html += `${escapeText(line.slice(at, m.index))}<span data-href="${url}">${url}</span>`;
      at = m.index + link.length;
    }
    html += escapeText(line.slice(at));
    return quoted(line) ? `<span class="godwit-quote">${html}</span>` : html;
  };
  if (from === lines.length) return lines.map(renderLine).join("\n");
  let bodyEnd = from;
  while (bodyEnd > 0 && lines[bodyEnd - 1].trim() === "") bodyEnd--;
  let quoteEnd = lines.length;
  while (quoteEnd > from && lines[quoteEnd - 1].trim() === "") quoteEnd--;
  return (
    lines.slice(0, bodyEnd).map(renderLine).join("\n") +
    FOLD_OPEN +
    lines.slice(from, quoteEnd).map(renderLine).join("\n") +
    FOLD_CLOSE
  );
}

// The rendered document's style: the mail's stylesheet, cascade and inheritance applied.
const style = (el: Element) => (el.ownerDocument.defaultView as Window).getComputedStyle(el);
const px = (v: string) => Number.parseFloat(v);

const isBlockBox = (el: Element) => !/^(?:inline|ruby|contents|none)/.test(style(el).display);

// Irreversible subtree hiding: no descendant override can make anything inside visible.
function isHiddenSubtree(el: Element): boolean {
  const cs = style(el);
  if (cs.display === "none" || px(cs.opacity) === 0) return true;
  // A zero-height box that clips (a preheader) hides its content, unless min-height or vertical
  // padding re-opens it; an inline box ignores height and does not clip.
  return (
    cs.display !== "inline" &&
    cs.display !== "contents" &&
    (px(cs.height) === 0 || px(cs.maxHeight) === 0) &&
    !(px(cs.minHeight) > 0) &&
    !(px(cs.paddingTop) > 0) &&
    !(px(cs.paddingBottom) > 0) &&
    /hidden|clip/.test(cs.overflowY)
  );
}

function isInsideHidden(el: Element): boolean {
  for (let p: Element | null = el; p; p = p.parentElement) {
    if (isHiddenSubtree(p)) return true;
  }
  return false;
}

// Replaced elements that show something without text: they make a block more than an attribution.
const MEDIA_TAGS = new Set([
  "IMG",
  "SVG",
  "VIDEO",
  "IFRAME",
  "CANVAS",
  "PICTURE",
  "OBJECT",
  "EMBED",
]);

// Visible text of root with <br>, block boundaries, and preserved newlines as "\n" (skipping hidden
// subtrees, invisible or zero-size text, and normal source wraps). Not innerText: it keeps
// font-size:0, opacity:0 and clipped preheader text, and jsdom has none.
function renderedText(root: Element, keep: (n: Node) => boolean = () => true): string {
  let out = "";
  const walk = (n: Node) => {
    if (n.nodeType === Node.TEXT_NODE) {
      if (!keep(n)) return;
      const cs = style(n.parentElement as Element);
      if (cs.visibility !== "visible" || px(cs.fontSize) === 0) return;
      const raw = n.textContent ?? "";
      out += /^(?:pre|break-spaces)/.test(cs.whiteSpace)
        ? raw.replace(/\r\n?/g, "\n")
        : raw.replace(/[\r\n]+/g, " ");
      return;
    }
    if (n.nodeType !== Node.ELEMENT_NODE) return;
    const el = n as Element;
    if (isHiddenSubtree(el)) return;
    if (el.tagName === "BR") {
      if (keep(n) && style(el).visibility === "visible") out += "\n";
      return;
    }
    const isBlock = n !== root && isBlockBox(el);
    const beforeLen = out.length;
    if (isBlock && out && !out.endsWith("\n")) out += "\n";
    for (let c = n.firstChild; c; c = c.nextSibling) walk(c);
    if (out.length === beforeLen + 1 && isBlock && out.endsWith("\n")) {
      out = out.slice(0, beforeLen);
    } else if (isBlock && out && !out.endsWith("\n")) {
      out += "\n";
    }
  };
  walk(root);
  return out.trim();
}

function isInlineAttrPart(n: ChildNode): boolean {
  if (n.nodeType === Node.COMMENT_NODE || n.nodeType === Node.TEXT_NODE) return true;
  if (n.nodeType !== Node.ELEMENT_NODE) return false;
  const el = n as Element;
  if (el.tagName === "STYLE" || el.tagName === "SCRIPT") return false;
  return ![el, ...el.querySelectorAll("*")].some(
    (c) => c.tagName === "BR" || isBlockBox(c) || MEDIA_TAGS.has(c.tagName) || hasVisualPaint(c),
  );
}

const QUOTE = "blockquote, div.gmail_quote, div.yahoo_quoted, div.protonmail_quote";

function hasVisualPaint(el: Element): boolean {
  const cs = style(el);
  return (
    el.hasAttribute("data-bg") ||
    el.hasAttribute("bgcolor") ||
    el.hasAttribute("background") ||
    (parse(cs.backgroundColor)?.[3] ?? 0) > 0 ||
    cs.backgroundImage !== "none" ||
    [cs.borderTopWidth, cs.borderRightWidth, cs.borderBottomWidth, cs.borderLeftWidth].some(
      (w) => px(w) > 0,
    )
  );
}

function isBlankSpacer(n: ChildNode): boolean {
  if (n.nodeType === Node.TEXT_NODE) return (n.textContent ?? "").trim() === "";
  if (n.nodeType !== Node.ELEMENT_NODE) return false;
  const el = n as Element;
  if (el.tagName === "BR") return true;
  if ((el.tagName !== "DIV" && el.tagName !== "P") || (el.textContent ?? "").trim() !== "")
    return false;
  return ![el, ...el.querySelectorAll("*")].some(
    (c) => (c !== el && c.tagName !== "BR") || hasVisualPaint(c),
  );
}

// A selector list split at its top-level commas: not those in quotes, [attributes] or :is(…).
function splitSelectorList(list: string): string[] {
  const out: string[] = [];
  let depth = 0;
  let quote = "";
  let from = 0;
  for (let i = 0; i < list.length; i++) {
    const c = list[i];
    if (quote) {
      if (c === "\\") i++;
      else if (c === quote) quote = "";
    } else if (c === '"' || c === "'") quote = c;
    else if (c === "\\") i++;
    else if (c === "(" || c === "[") depth++;
    else if (c === ")" || c === "]") depth--;
    else if (c === "," && depth === 0) {
      out.push(list.slice(from, i));
      from = i + 1;
    }
  }
  out.push(list.slice(from));
  return out;
}

function isEffectivelyInvisible(el: Element): boolean {
  if (isHiddenSubtree(el)) return true;
  if (renderedText(el) !== "") return false;
  return ![el, ...el.querySelectorAll("*")].some(
    (c) =>
      MEDIA_TAGS.has(c.tagName) || c.tagName === "HR" || c.tagName === "TABLE" || hasVisualPaint(c),
  );
}

/**
 * Folds the quote at the end of a rendered HTML body (wrap, #godwit-body) behind a button: the
 * last outermost quote block (plus a single-line "On … wrote:" lead right before a bare
 * blockquote), when the message has words of its own before it and none after. An inline reply
 * and a quote-only mail stay open. It reads the rendered document, so what counts as shown text
 * and as a line follows the mail's stylesheet as the reader shows it.
 */
export function foldQuotes(wrap: HTMLElement): void {
  const doc = wrap.ownerDocument;
  // A visible quote's ancestors are visible too, so the outermost ones are those in no other quote.
  const last = [...wrap.querySelectorAll(QUOTE)]
    .filter((q) => !isInsideHidden(q) && !q.parentElement?.closest(QUOTE))
    .at(-1);
  if (!last) return;
  const after = renderedText(
    wrap,
    (n) =>
      !last.contains(n) && !!(last.compareDocumentPosition(n) & Node.DOCUMENT_POSITION_FOLLOWING),
  );
  if (after !== "") return;

  // Wrapping the quote in <details> changes its parent and siblings: a rule that relies on them
  // (a > child, + or ~ sibling, structural pseudo-class, :has) would stop matching, so leave it.
  const selectors = [...wrap.querySelectorAll("style")]
    .flatMap((s) => [...(s.textContent ?? "").matchAll(/([^{}]+)\{/g)].map((m) => m[1]))
    .filter((sel) => !sel.trim().startsWith("@"));
  if (
    selectors.some((sel) =>
      /[>+~]|(?<!:):(?:nth-|first-child|last-child|first-of-type|last-of-type|only-|has\()/i.test(
        sel,
      ),
    )
  )
    return;

  let first: ChildNode = last;
  if (last.tagName === "BLOCKQUOTE") {
    let cur = last.previousSibling;
    while (
      cur &&
      (isBlankSpacer(cur) ||
        (cur.nodeType === Node.ELEMENT_NODE && isEffectivelyInvisible(cur as Element)))
    ) {
      cur = cur.previousSibling;
    }
    if (
      cur?.nodeType === Node.ELEMENT_NODE &&
      isBlockBox(cur as Element) &&
      ![...(cur as Element).querySelectorAll("*")].some(
        (c) => isBlockBox(c) || MEDIA_TAGS.has(c.tagName),
      ) &&
      !hasVisualPaint(cur as Element) &&
      isAttribution(renderedText(cur as Element))
    ) {
      first = cur;
    } else if (cur && isInlineAttrPart(cur)) {
      let start: ChildNode = cur;
      while (start.previousSibling && isInlineAttrPart(start.previousSibling)) {
        start = start.previousSibling;
      }
      // Rendered as one run under its parent, so a newline between siblings is not trimmed away.
      const run: ChildNode[] = [];
      for (let n: ChildNode | null = start; n; n = n === cur ? null : n.nextSibling) run.push(n);
      const parent = cur.parentElement as Element;
      const inlineText = renderedText(parent, (n) => run.some((r) => r === n || r.contains(n)));
      if (isAttribution(inlineText)) first = start;
    }
  }

  const before = renderedText(
    wrap,
    (n) =>
      !first.contains(n) &&
      n !== first &&
      !n.parentElement?.closest(QUOTE) &&
      !!(first.compareDocumentPosition(n) & Node.DOCUMENT_POSITION_PRECEDING),
  );
  if (
    before === "" ||
    before.split("\n").every((line) => line.trim() === "" || isAttribution(line))
  )
    return;

  // Mark the outer quote wrapper (and any single-child inner quote that only follows an
  // attribution, as in Godwit's or Gmail's wrapper+blockquote pairs) so only the left rail shows.
  const roots: Element[] = [];
  let rootQuote: Element = last;
  while (true) {
    roots.push(rootQuote);
    const directQuotes = [...rootQuote.querySelectorAll(QUOTE)].filter((q) => {
      if (isInsideHidden(q)) return false;
      let p = q.parentElement;
      while (p && p !== rootQuote) {
        if (p.matches(QUOTE)) return false;
        p = p.parentElement;
      }
      return p === rootQuote;
    });
    if (directQuotes.length !== 1) break;
    const inner = directQuotes[0];
    const leadText = renderedText(
      rootQuote,
      (n) =>
        !inner.contains(n) &&
        !!(inner.compareDocumentPosition(n) & Node.DOCUMENT_POSITION_PRECEDING),
    );
    const tailText = renderedText(
      rootQuote,
      (n) =>
        !inner.contains(n) &&
        !!(inner.compareDocumentPosition(n) & Node.DOCUMENT_POSITION_FOLLOWING),
    );
    if (tailText !== "" || (leadText !== "" && !isAttribution(leadText))) break;
    rootQuote = inner;
  }

  const tpl = doc.createElement("div");
  tpl.innerHTML = `${FOLD_OPEN}${FOLD_CLOSE}`;
  const fold = tpl.firstElementChild as HTMLElement;
  const quoteBody = fold.querySelector(".godwit-quote-body") as HTMLElement;
  const rail = fold.querySelector(".godwit-quote-rail") as HTMLElement;
  first.before(fold);
  let node: ChildNode | null = first;
  while (node) {
    const next: ChildNode | null = node === last ? null : node.nextSibling;
    quoteBody.append(node);
    node = next;
  }
  // The mail's stylesheet (scoped under #godwit-body by core/mail) must not reach the controls we
  // added, such as `.mail span{display:none}` hiding the pill: then the quote stays as it came.
  const added = [
    fold,
    quoteBody,
    ...fold.querySelectorAll("summary, summary *, .godwit-quote-rail, .godwit-quote-rail *"),
  ];
  const reaches = (sel: string) => {
    const local = sel
      .trim()
      .replace(/^#godwit-body(?:\.godwit-(?:dark|light))?(?=\s|$)/, "")
      .trim();
    if (!local) return false;
    try {
      return added.some((el) => el.matches(local));
    } catch {
      return false; // not a selector the browser applies either
    }
  };
  if (selectors.some((list) => splitSelectorList(list).some(reaches))) {
    rail.remove();
    fold.replaceWith(...quoteBody.childNodes);
    return;
  }
  for (const q of roots) q.setAttribute("data-quote-root", "");
  while (fold.previousSibling && isBlankSpacer(fold.previousSibling)) {
    fold.previousSibling.remove();
  }
}

// A theme value goes into a <style> only if it can't leave its declaration.
const color = (v: string | undefined) => (v && /^[#\w\s(),.%/+-]+$/.test(v) ? v : "");

const size = (px: unknown) =>
  typeof px === "number" && Number.isFinite(px) && px > 0 && px < 200 ? px : 14;

// line is the line height: plain text reads at 1.5; HTML mail sets its own, and where it does not
// (a button, a footer) it gets the browser's normal, as in any mail client, not our reading height.
const base = (px: number, line: string) =>
  `html{font:${px}px/${line} -apple-system,BlinkMacSystemFont,"Helvetica Neue",sans-serif}`;

const htmlCSS = (t: Theme, scheme: string) => {
  const fg = (m: Record<string, string> | undefined) => color(m?.["--foreground"]);
  return (
    `html{color-scheme:${scheme};` +
    `--godwit-fg-light:${fg(t.light) || "initial"};` +
    `--godwit-fg-dark:${fg(t.dark) || "initial"}}`
  );
};

/** Whether the theme is the dark one; systemDark is the page's appearance, for "system". */
export const isDark = (t: Theme, systemDark: boolean) =>
  t.mode === "dark" || (t.mode === "system" && systemDark);

// What lays a mail out rather than writes it: a table or the like, or a painted background, in a
// tag or in its stylesheet.
const LAYOUT =
  /<(?:table|center|iframe|video|svg)\b|<[^>]*(?:background|bgcolor)[^>]*>|<style\b[^>]*>[^<]*background/i;

// A tracking pixel, hidden or a pixel wide, shows nothing.
const shown = (img: string) => !/display:\s*none|(?:^|[\s;"'])width:\s*1px|\bwidth="1"/i.test(img);

/**
 * Whether an HTML body is text in all but name (a plain message sent as text/html, or one a person
 * wrote: paragraphs, lists, links, its own type sizes, no layout and no picture shown): it reads as
 * plain text does, in the reader's text column.
 */
export const isTextHTML = (html: string) =>
  !LAYOUT.test(html) && ![...html.matchAll(/<img\b[^>]*>/gi)].some((m) => shown(m[0]));

// Go marks a stylesheet with author-provided dark rules (core/mail).
const hasDarkStyles = (b: ReaderBody) => b.html.startsWith('<style data-dark="">');

/**
 * The colour scheme of a body's document, also set on its iframe element (a scheme differing from
 * the iframe's would give it an opaque canvas). HTML follows the selected appearance; plain text
 * can follow the system directly.
 */
export function frameScheme(b: ReaderBody, t: Theme, systemDark = false): string {
  if (b.html !== "") return isDark(t, systemDark) ? "dark" : "light";
  return t.mode === "light" || t.mode === "dark" ? t.mode : "light dark";
}

// Plain text: on the page's background (transparent), in the theme's text colour by its mode.
const plainCSS = (t: Theme, scheme: string) => {
  const fg = (m: Record<string, string> | undefined) => color(m?.["--foreground"]);
  const rule = (c: string) => (c ? `:root{--godwit-plain-fg:${c}}` : "");
  const pre = `html{color-scheme:${scheme}}`;
  if (t.mode === "light") return pre + rule(fg(t.light));
  if (t.mode === "dark") return pre + rule(fg(t.dark));
  const dark = rule(fg(t.dark));
  return `${pre}${rule(fg(t.light))}${dark ? `@media (prefers-color-scheme:dark){${dark}}` : ""}`;
};

/**
 * The srcdoc of a body's iframe: the CSP meta first, then our style, then the body (already
 * sanitized by Go; plain text escaped and linked here, plainHTML) in #godwit-body. The theme gives plain text its colour
 * and both kinds the body font size.
 */
export function buildSrcdoc(b: ReaderBody, t: Theme, systemDark = false): string {
  const html = b.html !== "";
  const scheme = frameScheme(b, t, systemDark);
  const css =
    frameCSS +
    base(size(t.bodyFontSize), html ? "normal" : "1.5") +
    (html ? htmlCSS(t, scheme) : plainCSS(t, scheme));
  const dark = isDark(t, systemDark);
  const auto = html && dark && !hasDarkStyles(b);
  const cls =
    (!html
      ? "godwit-plain"
      : dark
        ? hasDarkStyles(b)
          ? "godwit-dark"
          : "godwit-light godwit-auto"
        : "godwit-light") + (html && isTextHTML(b.html) ? " godwit-text" : "");
  return (
    "<!doctype html><html><head>" +
    `<meta http-equiv="Content-Security-Policy" content="${frameCSP()}">` +
    '<meta charset="utf-8">' +
    `<style>${css}</style></head>` +
    `<body><div id="godwit-body" class="${cls}"${auto ? " data-auto-dark" : ""}>${html ? b.html : plainHTML(b.plain, b.collapseQuotes)}</div></body></html>`
  );
}
