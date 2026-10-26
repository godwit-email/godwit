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
  return (
    `html{color-scheme:${scheme};` +
    `--godwit-fg-light:${fg(t.light) || "initial"};` +
    `--godwit-fg-dark:${fg(t.dark) || "initial"}}`
  );
};

/** Whether the theme is the dark one; systemDark is the page's appearance, for "system". */
export const isDark = (t: Theme, systemDark: boolean) =>
  t.mode === "dark" || (t.mode === "system" && systemDark);

// Go marks a stylesheet that has styles for the dark scheme or dark canvas (core/mail).
const hasDarkStyles = (b: ReaderBody) => /^<style\s+data-dark(?:="")?>/i.test(b.html.trimStart());

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
  return (
    "<!doctype html><html><head>" +
    `<meta http-equiv="Content-Security-Policy" content="${FRAME_CSP}">` +
    '<meta charset="utf-8">' +
    `<style>${css}</style></head>` +
    `<body><div id="godwit-body" class="${cls}">${html ? b.html : escapeText(b.plain)}</div></body></html>`
  );
}
