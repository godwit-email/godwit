import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Editor } from "./Editor";

// jsdom lacks isContentEditable, which Slate checks on every event target.
Object.defineProperty(HTMLElement.prototype, "isContentEditable", {
  configurable: true,
  get(this: HTMLElement) {
    return this.closest("[contenteditable]")?.getAttribute("contenteditable") === "true";
  },
});

const t = (k: string) => (k === "compose.body" ? "body" : k);
// Plate binds Mod to ⌘ on Apple platforms and Ctrl elsewhere (jsdom).
const mod = /Mac|iPhone|iPad/.test(navigator.userAgent) ? { metaKey: true } : { ctrlKey: true };

function setup(initial = "<p>hello world</p>", readOnly = false) {
  const onChange = vi.fn();
  const onComposing = vi.fn();
  const ui = (ro: boolean) => (
    <Editor
      initial={initial}
      readOnly={ro}
      placeholder="p"
      onChange={onChange}
      onComposing={onComposing}
      t={t}
    />
  );
  const { rerender } = render(ui(readOnly));
  const box = screen.getByLabelText("body");
  return { box, onChange, onComposing, setReadOnly: (ro: boolean) => rerender(ui(ro)) };
}

// Selects the first `len` characters after `from` in the first text node, as a user drag would.
async function select(box: HTMLElement, from: number, len: number) {
  const walker = document.createTreeWalker(box, NodeFilter.SHOW_TEXT);
  let node = walker.nextNode();
  while (node && !node.textContent?.trim()) node = walker.nextNode();
  if (!node) throw new Error("no text");
  await act(async () => {
    box.focus();
    const r = document.createRange();
    r.setStart(node, from);
    r.setEnd(node, from + len);
    const s = window.getSelection();
    s?.removeAllRanges();
    s?.addRange(r);
    document.dispatchEvent(new Event("selectionchange"));
    await new Promise((res) => setTimeout(res, 150)); // Slate throttles selectionchange
  });
}

const last = (f: ReturnType<typeof vi.fn>) => f.mock.calls.at(-1)?.[0] as string;

describe("Editor", () => {
  it("⌘B toggles bold on a selection", async () => {
    const { box, onChange } = setup();
    await select(box, 0, 5);
    fireEvent.keyDown(box, { key: "b", code: "KeyB", ...mod });
    await waitFor(() => expect(last(onChange)).toBe("<p><strong>hello</strong> world</p>"));
    fireEvent.keyDown(box, { key: "b", code: "KeyB", ...mod });
    await waitFor(() => expect(last(onChange)).toBe("<p>hello world</p>"));
  });

  it("typing calls onChange with HTML", async () => {
    const { box, onChange } = setup("<p>hi</p>");
    await select(box, 2, 0);
    // jsdom has no native beforeinput, so Slate takes React's onBeforeInput, which React builds
    // from keypress's `which` (always 0 in jsdom, hence set by hand).
    const ev = new KeyboardEvent("keypress", {
      key: "!",
      charCode: 33,
      bubbles: true,
      cancelable: true,
    });
    Object.defineProperty(ev, "which", { value: 33 });
    act(() => {
      box.dispatchEvent(ev);
    });
    await waitFor(() => expect(last(onChange)).toBe("<p>hi!</p>"));
  });

  it("composition start and end call onComposing", () => {
    const { box, onComposing } = setup();
    fireEvent.compositionStart(box);
    expect(onComposing).toHaveBeenLastCalledWith(true);
    fireEvent.compositionEnd(box, { data: "" });
    expect(onComposing).toHaveBeenLastCalledWith(false);
  });

  it("readOnly refuses input", async () => {
    const { box, onChange } = setup("<p>hi</p>", true);
    expect(box).toHaveAttribute("contenteditable", "false");
    fireEvent.keyDown(box, { key: "b", code: "KeyB", ...mod });
    await userEvent.type(box, "x");
    expect(onChange).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "compose.bold" })).toBeDisabled();
  });

  it("⌘K during an IME composition opens nothing", async () => {
    const { box } = setup();
    await select(box, 0, 5);
    fireEvent.keyDown(box, { key: "k", code: "KeyK", isComposing: true, ...mod });
    await act(async () => {});
    expect(screen.queryByPlaceholderText("https://")).not.toBeInTheDocument();
  });

  it("the link popover applies a link to the selection and rejects ftp://", async () => {
    const user = userEvent.setup();
    const { box, onChange } = setup();
    await select(box, 0, 5);
    fireEvent.keyDown(box, { key: "k", code: "KeyK", ...mod });
    const url = await screen.findByPlaceholderText("https://");
    expect(screen.getByText("compose.link.url")).toBeInTheDocument();

    await user.type(url, "ftp://x");
    await user.click(screen.getByRole("button", { name: "compose.link.apply" }));
    expect(url).toHaveAttribute("aria-invalid", "true");
    expect(onChange.mock.calls.some(([h]) => h.includes("<a"))).toBe(false);

    await user.clear(url);
    await user.type(url, "https://x.com/");
    await user.click(screen.getByRole("button", { name: "compose.link.apply" }));
    await waitFor(() =>
      expect(last(onChange)).toBe('<p><a href="https://x.com/">hello</a> world</p>'),
    );
  });

  it("⌘K with Wit gives the selection in the plain text, not the link popover", async () => {
    const onCmdK = vi.fn();
    render(
      <Editor
        initial="<p>one</p><p>hello world</p>"
        readOnly={false}
        placeholder="p"
        onChange={() => {}}
        onComposing={() => {}}
        onCmdK={onCmdK}
        t={t}
      />,
    );
    const box = screen.getByLabelText("body");
    const walker = document.createTreeWalker(box, NodeFilter.SHOW_TEXT);
    let node = walker.nextNode();
    while (node && node.textContent !== "hello world") node = walker.nextNode();
    await act(async () => {
      box.focus();
      const r = document.createRange();
      r.setStart(node as Node, 6);
      r.setEnd(node as Node, 11);
      window.getSelection()?.removeAllRanges();
      window.getSelection()?.addRange(r);
      document.dispatchEvent(new Event("selectionchange"));
      await new Promise((res) => setTimeout(res, 150));
    });
    fireEvent.keyDown(box, { key: "k", code: "KeyK", ...mod });
    expect(onCmdK).toHaveBeenCalledWith(
      expect.objectContaining({ text: "one\nhello world", start: 10, end: 15 }),
    );
    expect(screen.queryByPlaceholderText("https://")).toBeNull();
  });

  it("the link popover closes and applies nothing when the editor turns read-only", async () => {
    const { box, onChange, setReadOnly } = setup();
    await select(box, 0, 5);
    fireEvent.keyDown(box, { key: "k", code: "KeyK", ...mod });
    await screen.findByPlaceholderText("https://");
    setReadOnly(true);
    await waitFor(() => expect(screen.queryByPlaceholderText("https://")).toBeNull());
    expect(onChange.mock.calls.some(([h]) => h.includes("<a"))).toBe(false);
  });

  it("the quote toggle wraps and unwraps blocks", async () => {
    const user = userEvent.setup();
    const quote = () => user.click(screen.getByRole("button", { name: "compose.quote" }));
    const { box, onChange } = setup("<p>a</p>");
    await select(box, 0, 0);
    await quote();
    await waitFor(() => expect(last(onChange)).toBe("<blockquote><p>a</p></blockquote>"));
    await quote();
    await waitFor(() => expect(last(onChange)).toBe("<p>a</p>"));
    await quote();
    await waitFor(() => expect(last(onChange)).toBe("<blockquote><p>a</p></blockquote>"));
  });

  it("the quote toggle unwraps a list inside a quote, keeping the list", async () => {
    const user = userEvent.setup();
    const { box, onChange } = setup("<blockquote><ul><li><p>1</p></li></ul></blockquote>");
    await select(box, 0, 0);
    await user.click(screen.getByRole("button", { name: "compose.quote" }));
    await waitFor(() => expect(last(onChange)).toBe("<ul><li><p>1</p></li></ul>"));
  });
});
