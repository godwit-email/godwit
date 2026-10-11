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
    render(<SignatureEditor initial="<p>Bob</p>" onChange={onChange} t={t} />);
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
});
