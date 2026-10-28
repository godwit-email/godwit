import type { ReaderBody, Theme } from "@/mygo";
import frameCSS from "./frame.css?inline";

/**
 * The body iframe's own CSP, the first element of its head: nothing loads but inline styles,
 * data: images and the godwit-img: images Go's image proxy serves (a remote image never reaches
 * the network from the page), on top of the page's CSP (ADR 0013).
 */
export const FRAME_CSP = "default-src 'none'; style-src 'unsafe-inline'; img-src data: godwit-img:";

const escapeText = (s: string) => s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);

// A web address in plain text, up to its trailing punctuation.
const URL_RE = /\bhttps?:\/\/[^\s<>"']*[^\s<>"'.,;:!?)\]}]/g;

/**
 * Plain text as HTML: escaped, its web addresses links (data-href, as Go makes an HTML mail's)
 * and its quoted lines ("> …") marked, which the style mutes.
 */
export function plainHTML(text: string): string {
  return text
    .split("\n")
    .map((line) => {
      let html = "";
      let at = 0;
      for (const m of line.matchAll(URL_RE)) {
        const url = escapeText(m[0]);
        html += `${escapeText(line.slice(at, m.index))}<span data-href="${url}">${url}</span>`;
        at = m.index + m[0].length;
      }
      html += escapeText(line.slice(at));
      return /^\s*>/.test(line) ? `<span class="godwit-quote">${html}</span>` : html;
    })
    .join("\n");
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
  const css =
    frameCSS +
    base(size(t.bodyFontSize), html ? "normal" : "1.5") +
    (html ? htmlCSS(t, frameScheme(b, t, systemDark)) : plainCSS(t, frameScheme(b, t)));
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
    `<meta http-equiv="Content-Security-Policy" content="${FRAME_CSP}">` +
    '<meta charset="utf-8">' +
    `<style>${css}</style></head>` +
    `<body><div id="godwit-body" class="${cls}"${auto ? " data-auto-dark" : ""}>${html ? b.html : plainHTML(b.plain)}</div></body></html>`
  );
}
