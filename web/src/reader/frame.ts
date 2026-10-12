import type { ReaderBody, Theme } from "@/mygo";

/**
 * The body iframe's own CSP, the first element of its head: nothing loads but inline styles,
 * data: images and the godwit-img: images Go's image proxy serves (a remote image never reaches
 * the network from the page), on top of the page's CSP (spec §E3).
 */
export const FRAME_CSP = "default-src 'none'; style-src 'unsafe-inline'; img-src data: godwit-img:";

const escapeText = (s: string) => s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);

// A theme value goes into a <style> only if it can't leave its declaration.
const color = (v: string | undefined) => (v && /^[#\w\s(),.%/+-]+$/.test(v) ? v : "");

const size = (px: unknown) =>
  typeof px === "number" && Number.isFinite(px) && px > 0 && px < 200 ? px : 14;

const base = (px: number) =>
  "html,body{margin:0;padding:0}" +
  `html{font:${px}px/1.5 -apple-system,BlinkMacSystemFont,"Helvetica Neue",sans-serif;` +
  "-webkit-text-size-adjust:100%}" +
  // Wide content scrolls sideways in the wrapper; vertically it is fitted (the page reports its height).
  "#godwit-body{overflow-x:auto;overflow-y:hidden;overflow-wrap:break-word;word-break:break-word}" +
  ":where([data-href]){cursor:pointer}" +
  "[data-href]:focus-visible{outline:2px solid #0b57d0;outline-offset:1px;border-radius:2px}";

// HTML mail: a white sheet in either appearance, never inverted.
const htmlCSS =
  "html{background:#fff;color:#0a0a0a;color-scheme:light}" +
  ":where([data-href]){color:#0b57d0;text-decoration:underline}" +
  "#godwit-body img{max-width:100%!important}" +
  ":where(#godwit-body img){height:auto}" +
  "#godwit-body table{max-width:100%}" +
  ":where(#godwit-body td,#godwit-body th){word-break:break-word}";

/**
 * The colour scheme of a body's document, also set on its iframe element (a scheme differing from
 * the iframe's would give it an opaque canvas): HTML mail is a light sheet; plain text follows the
 * theme's mode ("system": both, by the appearance).
 */
export function frameScheme(b: ReaderBody, t: Theme): string {
  if (b.html !== "") return "light";
  return t.mode === "light" || t.mode === "dark" ? t.mode : "light dark";
}

// Plain text: on the page's background (transparent), in the theme's text colour by its mode.
const plainCSS = (t: Theme, scheme: string) => {
  const fg = (m: Record<string, string> | undefined) => color(m?.["--foreground"]);
  const rule = (c: string) => (c ? `:root{color:${c}}` : "");
  const pre =
    `html,body{background:transparent}html{color-scheme:${scheme}}` +
    "#godwit-body{white-space:pre-wrap}";
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
export function buildSrcdoc(b: ReaderBody, t: Theme): string {
  const html = b.html !== "";
  const css = base(size(t.bodyFontSize)) + (html ? htmlCSS : plainCSS(t, frameScheme(b, t)));
  return (
    "<!doctype html><html><head>" +
    `<meta http-equiv="Content-Security-Policy" content="${FRAME_CSP}">` +
    '<meta charset="utf-8">' +
    `<style>${css}</style></head>` +
    `<body><div id="godwit-body">${html ? b.html : escapeText(b.plain)}</div></body></html>`
  );
}
