import type { Descendant, SlateEditor, TElement, TText, Value } from "platejs";

// Removed with their content: anything that can load a resource or run, and form controls.
const LOADERS =
  "img,picture,source,video,audio,iframe,frame,frameset,object,embed,link,style,script,svg,math,template,noscript,title,meta,base,textarea,select";
// Blocks outside the format set (richtext's blockish list): a leaf one becomes a paragraph, so
// pasted headings and table cells stay separate lines; one holding blocks is unwrapped.
const BLOCKISH = new Set(
  "DIV CENTER SECTION ARTICLE HEADER FOOTER MAIN NAV ASIDE ADDRESS FIGURE FIGCAPTION DL DT DD FORM FIELDSET DETAILS SUMMARY CAPTION TABLE TBODY THEAD TFOOT TR TD TH H1 H2 H3 H4 H5 H6 PRE".split(
    " ",
  ),
);
const BLOCK = new Set([...BLOCKISH, "P", "UL", "OL", "LI", "BLOCKQUOTE"]);

/**
 * Parses html on an inert DOMParser document (no script runs, nothing loads) and returns its body
 * with every loader element removed and every attribute dropped but <a href>.
 */
export function sanitize(html: string): HTMLElement {
  const body = new DOMParser().parseFromString(html, "text/html").body;
  for (const el of body.querySelectorAll(LOADERS)) el.remove();
  for (const el of [...body.querySelectorAll("*")].reverse()) {
    for (const a of [...el.attributes]) {
      if (!(el.tagName === "A" && a.name === "href")) el.removeAttribute(a.name);
    }
    if (!BLOCKISH.has(el.tagName)) continue;
    if ([...el.children].some((c) => BLOCK.has(c.tagName))) {
      el.replaceWith(...el.childNodes);
    } else {
      const p = el.ownerDocument.createElement("p");
      p.append(...el.childNodes);
      el.replaceWith(p);
    }
  }
  return body;
}

const isText = (n: Descendant): n is TText => "text" in n;

function lastText(n: TElement): TText | undefined {
  const last = n.children.at(-1);
  if (!last) return undefined;
  return isText(last) ? last : last.type === "a" ? lastText(last) : undefined;
}

// A block's last <br> alone does not render a line (richtext.FromComposeHTML drops it too).
function dropTrailingBreak(nodes: Descendant[]) {
  for (const n of nodes) {
    if (isText(n)) continue;
    if (n.children.some((c) => isText(c) || c.type === "a")) {
      const t = lastText(n);
      if (t?.text.endsWith("\n")) t.text = t.text.slice(0, -1);
    } else {
      dropTrailingBreak(n.children);
    }
  }
}

/** Plate's HTML deserializer on sanitize(html); also the paste path. Not normalized. */
export function deserialize(editor: SlateEditor, html: string): Descendant[] {
  const nodes = editor.api.html.deserialize({ element: sanitize(html) }) as Descendant[];
  dropTrailingBreak(nodes);
  return nodes;
}

/** Loads html into editor (normalized, outside undo history) and returns the editor's value. */
export function fromHTML(editor: SlateEditor, html: string): Value {
  const nodes = deserialize(editor, html);
  editor.children = (nodes.length ? nodes : [{ type: "p", children: [{ text: "" }] }]) as Value;
  editor.tf.withoutSaving(() => editor.tf.normalize({ force: true }));
  return editor.children;
}

/**
 * The normalised URL if it is http(s)://host… or mailto:target, else null — the forms the native
 * editor.SetLink accepts.
 */
export function safeHref(url: string): string | null {
  const s = url.trim();
  if (!/^(https?:\/\/|mailto:)/i.test(s)) return null;
  try {
    const u = new URL(s);
    if (u.protocol === "mailto:" ? u.pathname.trim() !== "" : u.hostname !== "") return u.href;
  } catch {}
  return null;
}

const esc = (s: string) =>
  s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");

const isInline = (n: Descendant) => isText(n) || n.type === "a";
const textOf = (n: Descendant): string => (isText(n) ? n.text : n.children.map(textOf).join(""));

function inline(n: Descendant): string {
  if (isText(n)) {
    let s = esc(n.text).replace(/\n/g, "<br>");
    if (n.underline) s = `<u>${s}</u>`; // the signature editor's; compose has no underline
    if (n.italic) s = `<em>${s}</em>`;
    if (n.bold) s = `<strong>${s}</strong>`;
    return s;
  }
  const kids = n.children.map(inline).join("");
  const href = n.type === "a" ? safeHref(String(n.url ?? "")) : null;
  return href ? `<a href="${esc(href)}">${kids}</a>` : kids;
}

// A paragraph of inline nodes. As richtext.ToHTML: an empty one holds a <br>, and a trailing
// break needs a second one.
function para(children: Descendant[]): string {
  let body = children.map(inline).join("");
  if (body === "" || lastText({ type: "p", children })?.text.endsWith("\n")) body += "<br>";
  return `<p>${body}</p>`;
}

const blocks = (children: Descendant[]) => children.map(block).join("");

function item(n: Descendant): string {
  if (isText(n) || n.type !== "li") return `<li>${block(n)}</li>`;
  return `<li>${n.children.every(isInline) ? para(n.children) : blocks(n.children)}</li>`;
}

// Writes only the format set; any other node (or one out of place) becomes a paragraph of its
// text, or nothing when it has none (an image, a rule).
function block(n: Descendant): string {
  if (isText(n)) return para([n]);
  const inlineOnly = n.children.every(isInline);
  switch (n.type) {
    case "p":
    case "lic":
      if (inlineOnly) return para(n.children);
      break;
    case "blockquote":
      return `<blockquote>${inlineOnly ? para(n.children) : blocks(n.children)}</blockquote>`;
    case "ul":
    case "ol":
      return `<${n.type}>${n.children.map(item).join("")}</${n.type}>`;
  }
  const text = textOf(n);
  return text ? para([{ text }]) : "";
}

/** Serializes the editor's value to the format set's HTML; text escaped, soft breaks as <br>. */
export function toHTML(value: Value): string {
  return blocks(value);
}

// A line of the plain text: a block of inline nodes (a paragraph, a list item's text).
const isLine = (n: Descendant) => isText(n) || n.children.every(isInline);

/**
 * The value's plain text, the ⌘K contract webui's plainText keeps: blocks joined by "\n", a soft
 * break "\n" (the trailing one fromHTML already dropped), entities decoded; UTF-16 like JS strings.
 */
export function toText(value: Value): string {
  const lines = (ns: Descendant[]): string[] =>
    ns.flatMap((n) => (isLine(n) ? [textOf(n)] : lines((n as TElement).children)));
  return lines(value).join("\n");
}

/** Where point (a text node's path and offset) falls in toText(value), in UTF-16 code units. */
export function offsetOf(value: Value, point: { path: number[]; offset: number }): number {
  let pos = 0;
  let lines = 0;
  const same = (p: number[]) =>
    p.length === point.path.length && p.every((x, i) => x === point.path[i]);
  const walk = (ns: Descendant[], path: number[], line: boolean): number | undefined => {
    for (const [i, n] of ns.entries()) {
      const at = [...path, i];
      if (!line && isLine(n) && lines++ > 0) pos++; // the "\n" before a line
      if (isText(n)) {
        if (same(at)) return pos + point.offset;
        pos += n.text.length;
        continue;
      }
      const r = walk(n.children, at, line || isLine(n));
      if (r !== undefined) return r;
    }
    return undefined;
  };
  return walk(value, [], false) ?? pos;
}
