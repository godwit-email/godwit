import { createPlateEditor } from "platejs/react";
import { fromHTML, safeHref, sanitize, toHTML } from "./html";
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
});
