import type { ReaderBody, Theme } from "@/mygo";
import frameCSS from "./frame.css?inline";

/**
 * The body iframe's own CSP, the first element of its head: nothing loads but inline styles,
 * data: images and the godwit-img: images Go's image proxy serves (a remote image never reaches
 * the network from the page), on top of the page's CSP (ADR 0013).
 */
export const FRAME_CSP = "default-src 'none'; style-src 'unsafe-inline'; img-src data: godwit-img:";

const escapeText = (s: string) => s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);

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
  const bg = (m: Record<string, string> | undefined) => color(m?.["--background"]);
  const darkBg = bg(t.dark);
  return (
    `html{color-scheme:${scheme};` +
    (darkBg ? `--godwit-bg-dark:${darkBg};` : "") +
    `--godwit-fg-light:${fg(t.light) || "initial"};` +
    `--godwit-fg-dark:${fg(t.dark) || "initial"}}`
  );
};

/** Whether the theme is the dark one; systemDark is the page's appearance, for "system". */
export const isDark = (t: Theme, systemDark: boolean) =>
  t.mode === "dark" || (t.mode === "system" && systemDark);

// Go marks a stylesheet that has styles for the dark scheme, or a mail on a dark canvas (core/mail).
const hasDarkStyles = (b: ReaderBody) => b.html.startsWith('<style data-dark="">');

/**
 * The colour scheme of a body's document, also set on its iframe element (a scheme differing from
 * the iframe's would give it an opaque canvas): HTML mail is dark only in the dark theme and with
 * dark styles of its own (else it is drawn light and inverted); plain text follows the theme's mode
 * ("system": both, by the appearance).
 */
export function frameScheme(b: ReaderBody, t: Theme, systemDark = false): string {
  if (b.html !== "") return isDark(t, systemDark) && hasDarkStyles(b) ? "dark" : "light";
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

const DARK_TABLE =
  "0.0393 0.0393 0.0393 0.0393 0.0393 0.0393 0.0393 0.0393 0.0393 0.0393 0.0393 0.0393 0.0393 0.0393 0.0393 0.0393 0.0393 0.0393 0.0393 0.0393 0.0393 0.0393 0.0393 0.0393 0.0393 0.0393 0.0403 0.0498 0.0593 0.0688 0.0783 0.0878 0.0973 0.1068 0.1163 0.1258 0.1353 0.1448 0.1543 0.1638 0.1733 0.1828 0.1923 0.2018 0.2113 0.2208 0.2303 0.2398 0.2493 0.2588 0.2683 0.2778 0.2873 0.2968 0.3063 0.3158 0.3253 0.3348 0.3443 0.3538 0.3633 0.3728 0.3824 0.3919 0.4014 0.4109 0.4204 0.4299 0.4394 0.4489 0.4584 0.4679 0.4774 0.4869 0.4964 0.5059 0.5154 0.5249 0.5344 0.5439 0.5534 0.5629 0.5724 0.5819 0.5914 0.6009 0.6104 0.6199 0.6294 0.6389 0.6484 0.6579 0.6674 0.6769 0.6864 0.6959 0.7054 0.7149 0.7244 0.7339 0.7434 0.7529 0.7624 0.7719 0.7814 0.7909 0.8005 0.8100 0.8195 0.8290 0.8385 0.8480 0.8575 0.8670 0.8765 0.8860 0.8955 0.9050 0.9145 0.9240 0.9335 0.9430 0.9525 0.9620 0.9715 0.9810 0.9905 1.0000";

const DARK_FILTER_SVG =
  '<svg class="godwit-filter-def" aria-hidden="true">' +
  '<filter id="godwit-dark" color-interpolation-filters="sRGB">' +
  "<feComponentTransfer>" +
  `<feFuncR type="table" tableValues="${DARK_TABLE}" />` +
  `<feFuncG type="table" tableValues="${DARK_TABLE}" />` +
  `<feFuncB type="table" tableValues="${DARK_TABLE}" />` +
  "</feComponentTransfer>" +
  "</filter></svg>";

const LIGHT_TABLE =
  "0.0000 0.0099 0.0198 0.0297 0.0396 0.0495 0.0594 0.0693 0.0792 0.0891 0.0990 0.1089 0.1188 0.1287 0.1386 0.1485 0.1584 0.1683 0.1782 0.1881 0.1980 0.2079 0.2178 0.2277 0.2376 0.2475 0.2574 0.2673 0.2772 0.2871 0.2970 0.3069 0.3168 0.3267 0.3366 0.3465 0.3564 0.3663 0.3762 0.3861 0.3960 0.4059 0.4158 0.4257 0.4356 0.4455 0.4554 0.4653 0.4752 0.4851 0.4950 0.5050 0.5149 0.5248 0.5347 0.5446 0.5545 0.5644 0.5743 0.5842 0.5941 0.6040 0.6139 0.6238 0.6337 0.6436 0.6535 0.6634 0.6733 0.6832 0.6931 0.7030 0.7129 0.7228 0.7327 0.7426 0.7525 0.7624 0.7723 0.7822 0.7921 0.8020 0.8119 0.8218 0.8317 0.8416 0.8515 0.8614 0.8713 0.8812 0.8911 0.9010 0.9109 0.9208 0.9307 0.9406 0.9505 0.9604 0.9703 0.9802 0.9901 1.0000 1.0000 1.0000 1.0000 1.0000 1.0000 1.0000 1.0000 1.0000 1.0000 1.0000 1.0000 1.0000 1.0000 1.0000 1.0000 1.0000 1.0000 1.0000 1.0000 1.0000 1.0000 1.0000 1.0000 1.0000 1.0000 1.0000";

const LIGHT_FILTER_SVG =
  '<svg class="godwit-filter-def" aria-hidden="true">' +
  '<filter id="godwit-light" color-interpolation-filters="sRGB">' +
  "<feComponentTransfer>" +
  `<feFuncR type="table" tableValues="${LIGHT_TABLE}" />` +
  `<feFuncG type="table" tableValues="${LIGHT_TABLE}" />` +
  `<feFuncB type="table" tableValues="${LIGHT_TABLE}" />` +
  "</feComponentTransfer>" +
  "</filter></svg>";

/**
 * The srcdoc of a body's iframe: the CSP meta first, then our style, then the body (already
 * sanitized by Go; plain text escaped here) in #godwit-body. The theme gives plain text its colour
 * and both kinds the body font size.
 */
export function buildSrcdoc(b: ReaderBody, t: Theme, systemDark = false): string {
  const html = b.html !== "";
  const css =
    frameCSS +
    base(size(t.bodyFontSize), html ? "normal" : "1.5") +
    (html ? htmlCSS(t, frameScheme(b, t, systemDark)) : plainCSS(t, frameScheme(b, t)));
  const dark = isDark(t, systemDark);
  const cls = !html
    ? "godwit-plain"
    : dark
      ? hasDarkStyles(b)
        ? "godwit-dark"
        : "godwit-dark godwit-invert"
      : "godwit-light";
  const svg = dark ? DARK_FILTER_SVG : LIGHT_FILTER_SVG;
  return (
    `<!doctype html><html class="${cls}"><head>` +
    `<meta http-equiv="Content-Security-Policy" content="${FRAME_CSP}">` +
    '<meta charset="utf-8">' +
    `<style>${css}</style></head>` +
    `<body>${svg}<div id="godwit-body" class="${cls}">${html ? b.html : escapeText(b.plain)}</div></body></html>`
  );
}
