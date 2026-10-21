import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { hasLayout, SignatureEditor } from "./Signature";

// jsdom lacks isContentEditable, which Slate checks on every event target.
Object.defineProperty(HTMLElement.prototype, "isContentEditable", {
  configurable: true,
  get(this: HTMLElement) {
    return this.closest("[contenteditable]")?.getAttribute("contenteditable") === "true";
  },
});

const t = (k: string) => k;
const none = () => Promise.resolve("");
const png = "data:image/png;base64,iVBORw0KGgo=";
const img = (src = png) => `<img src="${src}">`;

async function selectAll(box: HTMLElement) {
  const walker = document.createTreeWalker(box, NodeFilter.SHOW_TEXT);
  let node = walker.nextNode();
  while (node && !node.textContent?.trim()) node = walker.nextNode();
  if (!node) throw new Error("no text");
  await act(async () => {
    box.focus();
    const r = document.createRange();
    r.setStart(node, 0);
    r.setEnd(node, node.textContent?.length ?? 0);
    window.getSelection()?.removeAllRanges();
    window.getSelection()?.addRange(r);
    document.dispatchEvent(new Event("selectionchange"));
    await new Promise((res) => setTimeout(res, 150)); // Slate throttles selectionchange
  });
}

describe("SignatureEditor", () => {
  it("the toolbar toggles bold and underline", async () => {
    const onChange = vi.fn();
    render(<SignatureEditor initial="<p>Bob</p>" onChange={onChange} pickImage={none} t={t} />);
    const box = screen.getByLabelText("settings.account.signature.label");
    await selectAll(box);
    fireEvent.click(screen.getByRole("button", { name: "compose.bold" }));
    await waitFor(() => expect(onChange).toHaveBeenLastCalledWith("<p><strong>Bob</strong></p>"));
    fireEvent.click(screen.getByRole("button", { name: "compose.underline" }));
    await waitFor(() =>
      expect(onChange).toHaveBeenLastCalledWith("<p><strong><u>Bob</u></strong></p>"),
    );
    expect(screen.queryByRole("button", { name: "compose.list" })).not.toBeInTheDocument();
  });

  it("loads its images, and an image alone is a signature", async () => {
    const onChange = vi.fn();
    render(
      <SignatureEditor
        initial={`<p>Bob</p>${img()}${img("https://t.example/p.png")}`}
        onChange={onChange}
        pickImage={none}
        t={t}
      />,
    );
    const box = screen.getByLabelText("settings.account.signature.label");
    expect(box.querySelectorAll("img")).toHaveLength(1);
    expect(box.querySelector("img")?.getAttribute("src")).toBe(png);
    await selectAll(box);
    fireEvent.click(screen.getByRole("button", { name: "compose.bold" }));
    await waitFor(() =>
      expect(onChange).toHaveBeenLastCalledWith(`<p><strong>Bob</strong></p>${img()}`),
    );
  });

  it("插入图片 inserts the picked image; a refused one is said", async () => {
    const onChange = vi.fn();
    const pick = vi.fn().mockResolvedValueOnce(png).mockRejectedValueOnce(new Error("no"));
    render(<SignatureEditor initial="<p>Bob</p>" onChange={onChange} pickImage={pick} t={t} />);
    const button = screen.getByRole("button", { name: "settings.account.signature.image" });
    fireEvent.click(button);
    await waitFor(() => expect(onChange).toHaveBeenLastCalledWith(expect.stringContaining(img())));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    fireEvent.click(button);
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "settings.account.signature.image_invalid",
    );
  });

  it("链接 asks native for the address, with the link already there, and applies its answer", async () => {
    const onChange = vi.fn();
    const askLink = vi
      .fn()
      .mockResolvedValueOnce({ url: "https://example.com/", ok: true })
      .mockResolvedValueOnce({ url: "", ok: true });
    render(
      <SignatureEditor
        initial="<p>Bob</p>"
        onChange={onChange}
        pickImage={none}
        askLink={askLink}
        t={t}
      />,
    );
    const box = screen.getByLabelText("settings.account.signature.label");
    await selectAll(box);
    fireEvent.click(screen.getByRole("button", { name: "compose.link" }));
    expect(askLink).toHaveBeenLastCalledWith("");
    await waitFor(() =>
      expect(onChange).toHaveBeenLastCalledWith('<p><a href="https://example.com/">Bob</a></p>'),
    );
    await selectAll(box);
    fireEvent.click(screen.getByRole("button", { name: "compose.link" }));
    expect(askLink).toHaveBeenLastCalledWith("https://example.com/");
    await waitFor(() => expect(onChange).toHaveBeenLastCalledWith("<p>Bob</p>"));
  });

  it("allows at most four images", () => {
    render(<SignatureEditor initial={img().repeat(4)} onChange={vi.fn()} pickImage={none} t={t} />);
    expect(screen.getByRole("button", { name: "settings.account.signature.image" })).toBeDisabled();
  });
});

describe("hasLayout", () => {
  it("is a table or any attribute but <a href> and <img src>", () => {
    expect(hasLayout(`<p><strong>Bob</strong> <a href="https://x.example">x</a></p>${img()}`)).toBe(
      false,
    );
    expect(hasLayout("<table><tbody><tr><td>Bob</td></tr></tbody></table>")).toBe(true);
    expect(hasLayout('<p style="color:red">Bob</p>')).toBe(true);
    expect(hasLayout('<img src="x" width="48">')).toBe(true);
    expect(hasLayout('<a href="x" title="t">x</a>')).toBe(true);
  });
});

describe("SignatureEditor modes", () => {
  const layout = '<table><tbody><tr><td style="color:red">Bob</td></tr></tbody></table>';

  it("a layout signature shows as given, its text editable, formats off", () => {
    const onHTML = vi.fn();
    render(
      <SignatureEditor
        initial={layout}
        onChange={vi.fn()}
        pickImage={none}
        t={t}
        mode={{ onHTML }}
      />,
    );
    const box = screen.getByLabelText("settings.account.signature.label");
    expect(box.querySelector("td")?.getAttribute("style")).toBe("color:red");
    expect(box).toHaveAttribute("contenteditable", "true");
    expect(screen.getByRole("button", { name: "compose.bold" })).toBeDisabled();
    expect(screen.getByText("settings.account.signature.layout")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "settings.account.signature.edit_html" }));
    expect(onHTML).toHaveBeenCalledOnce();
  });

  it("a layout signature takes pasted text as text, and Enter as a <br>", () => {
    const onChange = vi.fn();
    render(<SignatureEditor initial={layout} onChange={onChange} pickImage={none} t={t} />);
    const box = screen.getByLabelText("settings.account.signature.label");
    const cell = box.querySelector("td")?.firstChild as Text;
    const r = document.createRange();
    r.setStart(cell, 3);
    r.collapse(true);
    window.getSelection()?.removeAllRanges();
    window.getSelection()?.addRange(r);
    fireEvent.paste(box, {
      clipboardData: { getData: (k: string) => (k === "text/plain" ? "<b>!</b>" : "<b>!</b>") },
    });
    expect(onChange).toHaveBeenLastCalledWith(
      '<table><tbody><tr><td style="color:red">Bob&lt;b&gt;!&lt;/b&gt;</td></tr></tbody></table>',
    );
    fireEvent.keyDown(box, { key: "Enter" });
    expect(onChange.mock.lastCall?.[0]).toContain("&lt;/b&gt;<br></td>");
  });

  it("says what Go refused beside the HTML toggle", () => {
    const onHTML = vi.fn();
    render(
      <SignatureEditor
        initial="<p>Bob</p>"
        onChange={vi.fn()}
        pickImage={none}
        t={t}
        mode={{ alert: "settings.account.signature.too_many", onHTML }}
      />,
    );
    expect(screen.getByRole("alert")).toHaveTextContent("settings.account.signature.too_many");
    fireEvent.click(screen.getByRole("button", { name: "settings.account.signature.html.label" }));
    expect(onHTML).toHaveBeenCalledOnce();
  });
});
