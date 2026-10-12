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

// line is the line height: plain text reads at 1.5; HTML mail sets its own, and where it does not
// (a button, a footer) it gets the browser's normal, as in any mail client, not our reading height.
const base = (px: number, line: string) =>
  "html,body{margin:0;padding:0}" +
  `html{font:${px}px/${line} -apple-system,BlinkMacSystemFont,"Helvetica Neue",sans-serif;` +
  "-webkit-text-size-adjust:100%}" +
  // Wide content scrolls sideways in the wrapper; vertically it is fitted (the page reports its height).
  "#godwit-body{overflow-x:auto;overflow-y:hidden;overflow-wrap:break-word;word-break:break-word}" +
  ":where([data-href]){cursor:pointer}" +
  "[data-href]:focus-visible{outline:2px solid #0b57d0;outline-offset:1px;border-radius:2px}";

// HTML mail follows the theme: transparent over the app's background, its own colours kept. The
// classes on #godwit-body say which: godwit-dark in the dark theme (the mail's own (prefers-color-
// scheme) styles are scoped to it and godwit-light by Go), and godwit-invert in the dark for a mail
// with no dark styles of its own, which is drawn as itself and inverted (images back again), so it
// reads as dark, not as a sheet. The defaults are :where, so the mail's own colours win.
const INVERT = "invert(1) hue-rotate(180deg)";
const link = (c: string) => `text-decoration:underline;color:${c}`;

const htmlCSS = (t: Theme, scheme: string) => {
  const fg = (m: Record<string, string> | undefined) => color(m?.["--foreground"]);
  return (
    `html,body{background:transparent}html{color-scheme:${scheme}}` +
    `:where(#godwit-body){color:${fg(t.light) || "#0a0a0a"}}` +
    `:where(#godwit-body.godwit-dark){color:${fg(t.dark) || "#fafafa"}}` +
    ":where(#godwit-body.godwit-invert){color:#0a0a0a}" +
    `:where([data-href]){${link("#0b57d0")}}` +
    `:where(#godwit-body.godwit-dark:not(.godwit-invert) [data-href]){${link("#8ab4f8")}}` +
    `#godwit-body.godwit-invert{filter:${INVERT}}` +
    `#godwit-body.godwit-invert img,#godwit-body.godwit-invert [style*="godwit-img"]{filter:${INVERT}}` +
    "#godwit-body img{max-width:100%!important}" +
    // keeps the ratio of an image shrunk to the width; one with a height of its own (an icon) keeps it
    ":where(#godwit-body img:not([height])){height:auto}" +
    "#godwit-body table{max-width:100%}" +
    ":where(#godwit-body td,#godwit-body th){word-break:break-word}"
  );
};

/** Whether the theme is the dark one; systemDark is the page's appearance, for "system". */
export const isDark = (t: Theme, systemDark: boolean) =>
  t.mode === "dark" || (t.mode === "system" && systemDark);

// Go marks a stylesheet that has styles for the dark scheme (core/mail).
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
export function buildSrcdoc(b: ReaderBody, t: Theme, systemDark = false): string {
  const html = b.html !== "";
  const css =
    base(size(t.bodyFontSize), html ? "normal" : "1.5") +
    (html ? htmlCSS(t, frameScheme(b, t, systemDark)) : plainCSS(t, frameScheme(b, t)));
  const dark = isDark(t, systemDark);
  const cls = !html
    ? ""
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
