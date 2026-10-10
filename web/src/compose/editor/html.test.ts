import type { Descendant, Path, TText, Value } from "platejs";
import { createPlateEditor } from "platejs/react";
// The ⌘K contract's cases, shared with webui's plainText (Go).
import shared from "../../../../webui/testdata/selection.json?raw";
import { fromHTML, offsetOf, safeHref, sanitize, toHTML, toText } from "./html";
import { plugins } from "./plugins";

const editor = () => createPlateEditor({ plugins });
const round = (s: string) => toHTML(fromHTML(editor(), s));

describe("html", () => {
  it("round-trips the allowed formats", () => {
    for (const s of [
      "<p>a</p>",
      "<p><strong>b</strong> <em>i</em></p>",
      '<p><a href="https://x.com/">l</a></p>',
      "<ul><li><p>1</p></li></ul>",
      "<ol><li><p>1</p></li><li><p>2</p></li></ol>",
      "<blockquote><p>q</p></blockquote>",
      "<p>a<br>b</p>",
      // A trailing soft break is written as richtext.ToHTML writes it: a last <br> alone does not render.
      "<p>a<br><br></p>",
      // An empty paragraph holds a <br>, as richtext.ToHTML writes it; FromComposeHTML drops <p></p>.
      "<p><br></p><p>x</p>",
      "<ul><li><p>1</p><ul><li><p>1.1</p></li></ul></li></ul>",
      "<p>&lt;a&gt; &amp; b</p>",
    ]) {
      expect(round(s)).toBe(s);
    }
  });

  it("reads the native page's richtext.ToHTML output, line breaks kept", () => {
    // From internal/richtext TestToHTMLRoundTrip's source, as ToHTML writes it (plus a trailing break).
    const native =
      '<p>Hi <b>bold </b><b><i>both</i></b> <a href="https://x.example/?a=1&amp;b=2">link &lt;3</a></p><p><br></p><p>a<br>b</p><ul><li>one<ul><li>sub</li></ul></li><li>two</li></ul><ol><li>first</li></ol><blockquote><p>q1</p><blockquote><p>q2</p></blockquote><p>q1b</p></blockquote><p>end 中文</p><p>tail<br><br></p>';
    expect(round(native)).toBe(
      '<p>Hi <strong>bold </strong><strong><em>both</em></strong> <a href="https://x.example/?a=1&amp;b=2">link &lt;3</a></p><p><br></p><p>a<br>b</p><ul><li><p>one</p><ul><li><p>sub</p></li></ul></li><li><p>two</p></li></ul><ol><li><p>first</p></li></ol><blockquote><p>q1</p><blockquote><p>q2</p></blockquote><p>q1b</p></blockquote><p>end 中文</p><p>tail<br><br></p>',
    );
  });

  it("empty input is one empty paragraph", () => {
    expect(fromHTML(editor(), "")).toEqual([{ type: "p", children: [{ text: "" }] }]);
    expect(fromHTML(editor(), "  ")).toEqual([{ type: "p", children: [{ text: "" }] }]);
  });

  it("paste keeps only allowed formats", () => {
    const out = round(
      '<h1 style="color:red">T</h1><table><tr><td>c</td></tr></table><img src="https://t/x.png"><p><u>u</u><span style="font-size:30px">s</span></p>',
    );
    expect(out).toBe("<p>T</p><p>c</p><p>us</p>");
  });

  it("pasting HTML into the editor takes the same path", () => {
    const e = editor();
    fromHTML(e, "<p>x</p>");
    e.tf.select(e.api.end([]));
    const html = '<h1>T</h1><table><tr><td>c</td></tr></table><img src="https://t/x.png">';
    e.tf.insertData({
      getData: (type: string) => (type === "text/html" ? html : ""),
      files: [],
      types: ["text/html"],
    } as unknown as DataTransfer);
    expect(toHTML(e.children)).toBe("<p>xT</p><p>c</p>");
  });

  it("deserialize runs no script and strips loaders", async () => {
    const w = window as unknown as { __hit?: number };
    const html =
      '<img src=x onerror="window.__hit=1"><script>window.__hit=2</script><iframe src=x></iframe><p style="background:url(x)">t</p>';
    const body = sanitize(html);
    expect(body.querySelector("img, iframe, script")).toBeNull();
    expect(body.querySelector("[src], [style], [onerror]")).toBeNull();
    expect(toHTML(fromHTML(editor(), html))).toBe("<p>t</p>");
    await new Promise((r) => setTimeout(r, 10));
    expect(w.__hit).toBeUndefined();
  });

  it("unsafe links dropped", () => {
    expect(round('<p><a href="javascript:alert(1)">x</a></p>')).toBe("<p>x</p>");
    expect(round('<p><a href="/rel">x</a><a href="#f">y</a></p>')).toBe("<p>xy</p>");
    expect(safeHref("ftp://x")).toBeNull();
    expect(safeHref("https://")).toBeNull();
    expect(safeHref("javascript:alert(1)")).toBeNull();
    expect(safeHref("mailto:a@b.c")).toBe("mailto:a@b.c");
    expect(safeHref("https://x.com/a")).toBe("https://x.com/a");
  });

  it("safeHref returns the normalised URL, only in the forms Go's SetLink accepts", () => {
    for (const bad of [
      "https:example.com",
      "https:/example.com",
      "mailto:",
      "mailto:  ",
      "java\tscript:alert(1)",
      "//x.com",
      "x.com",
      "",
    ]) {
      expect(safeHref(bad), bad).toBeNull();
    }
    expect(safeHref("  https://x.com  ")).toBe("https://x.com/");
    expect(safeHref("HTTPS://X.com/A")).toBe("https://x.com/A");
    expect(safeHref("MAILTO:a@b.c")).toBe("mailto:a@b.c");
    expect(round('<p><a href="https:example.com">x</a></p>')).toBe("<p>x</p>");
  });

  it("the serializer degrades nodes outside the format set to paragraphs", () => {
    const li = { type: "li", children: [{ type: "lic", children: [{ text: "t" }] }] };
    expect(toHTML([{ type: "taskList", children: [li] }])).toBe("<p>t</p>");
    expect(toHTML([li])).toBe("<p>t</p>");
    expect(toHTML([{ type: "h1", children: [{ text: "H", bold: true }] }])).toBe("<p>H</p>");
    expect(toHTML([{ type: "ul", children: [{ type: "p", children: [{ text: "x" }] }] }])).toBe(
      "<ul><li><p>x</p></li></ul>",
    );
  });

  it("list-classic's task list is not registered", () => {
    expect(editor().plugins.taskList).toBeUndefined();
  });
});

function data(d: { html?: string; text?: string; fragment?: unknown }): DataTransfer {
  const m: Record<string, string> = {
    "text/html": d.html ?? "",
    "text/plain": d.text ?? "",
    "application/x-slate-fragment": d.fragment
      ? btoa(encodeURIComponent(JSON.stringify(d.fragment)))
      : "",
  };
  return {
    getData: (t: string) => m[t] ?? "",
    files: [],
    types: Object.keys(m).filter((k) => m[k]),
  } as unknown as DataTransfer;
}

describe("paste", () => {
  const paste = (d: Parameters<typeof data>[0]) => {
    const e = editor();
    fromHTML(e, "<p>x</p>");
    e.tf.select(e.api.end([]));
    // slate-react's onPaste/onDrop call the editor's legacy insertData, which Plate binds to tf.
    (e as unknown as { insertData(d: DataTransfer): void }).insertData(data(d));
    return toHTML(e.children);
  };

  it("HTML that sanitises to nothing inserts nothing, or the plain text", () => {
    expect(paste({ html: '<img src="https://t/x.png">' })).toBe("<p>x</p>");
    expect(paste({ html: "<script>1</script>", text: "a\nb" })).toBe("<p>xa</p><p>b</p>");
  });

  it("plain text becomes paragraphs", () => {
    expect(paste({ text: "a\r\nb" })).toBe("<p>xa</p><p>b</p>");
  });

  it("a Slate fragment is held to the format set", () => {
    const fragment = [
      { type: "h1", children: [{ text: "H" }] },
      {
        type: "taskList",
        children: [{ type: "li", children: [{ type: "lic", children: [{ text: "t" }] }] }],
      },
      { type: "img", url: "https://t/x.png", children: [{ text: "" }] },
    ];
    expect(paste({ fragment, html: "<p>ignored</p>" })).toBe("<p>xH</p><p>t</p>");
  });
});

// Every text node of value with its path.
function texts(nodes: Descendant[], path: Path = []): [TText, Path][] {
  return nodes.flatMap((n, i) =>
    "text" in n ? [[n, [...path, i]] as [TText, Path]] : texts(n.children, [...path, i]),
  );
}

describe("⌘K plain text", () => {
  const cases: { name: string; html: string; text: string; start: number; end: number }[] =
    JSON.parse(shared);

  it.each(cases.map((c) => [c.name, c]))("%s: toText and offsets agree with Go", (_, c) => {
    const value: Value = fromHTML(editor(), c.html);
    const text = toText(value);
    expect(text).toBe(c.text);
    const offs = new Set<number>();
    for (const [n, path] of texts(value)) {
      for (let i = 0; i <= n.text.length; i++) {
        const o = offsetOf(value, { path, offset: i });
        offs.add(o);
        if (i < n.text.length) expect(text[o]).toBe(n.text[i]);
      }
    }
    expect(offs.has(c.start)).toBe(true);
    expect(offs.has(c.end)).toBe(true);
  });
});
