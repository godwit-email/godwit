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

/** The URL if it is http(s) with a host, or mailto; else null (as the native editor.SetLink). */
export function safeHref(url: string): string | null {
  try {
    const u = new URL(url.trim());
    if (
      u.protocol === "mailto:" ||
      ((u.protocol === "http:" || u.protocol === "https:") && u.host)
    ) {
      return url.trim();
    }
  } catch {}
  return null;
}

const esc = (s: string) =>
  s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");

const TAGS: Record<string, string> = {
  p: "p",
  lic: "p",
  ul: "ul",
  ol: "ol",
  li: "li",
  blockquote: "blockquote",
};

function inline(n: Descendant): string {
  if (isText(n)) {
    let s = esc(n.text).replace(/\n/g, "<br>");
    if (n.italic) s = `<em>${s}</em>`;
    if (n.bold) s = `<strong>${s}</strong>`;
    return s;
  }
  const kids = n.children.map(inline).join("");
  const href = n.type === "a" ? safeHref(String(n.url ?? "")) : null;
  return href ? `<a href="${esc(href)}">${kids}</a>` : kids;
}

function block(n: TElement): string {
  const tag = TAGS[n.type] ?? "p";
  const blocks = n.children.some((c) => !isText(c) && c.type !== "a");
  let body: string;
  if (blocks) {
    body = (n.children as TElement[]).map(block).join("");
  } else {
    body = n.children.map(inline).join("");
    // As richtext.ToHTML: an empty block holds a <br>, and a trailing break needs a second one.
    if (body === "" || lastText(n)?.text.endsWith("\n")) body += "<br>";
  }
  return `<${tag}>${body}</${tag}>`;
}

/** Serializes the editor's value to the format set's HTML; text escaped, soft breaks as <br>. */
export function toHTML(value: Value): string {
  return value.map((n) => block(n as TElement)).join("");
}
