import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { SignatureEditor } from "./Signature";

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

  it("allows at most four images", () => {
    render(<SignatureEditor initial={img().repeat(4)} onChange={vi.fn()} pickImage={none} t={t} />);
    expect(screen.getByRole("button", { name: "settings.account.signature.image" })).toBeDisabled();
  });
});
