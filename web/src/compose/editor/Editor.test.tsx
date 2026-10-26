import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Editor, type EditorHandle } from "./Editor";

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

  it.each([
    ["without a model", false],
    ["with a model", true],
  ])("⌘K on a collapsed cursor opens the link popover %s", async (_, wit) => {
    const onCmdK = vi.fn();
    render(
      <Editor
        initial="<p>hello world</p>"
        readOnly={false}
        placeholder="p"
        onChange={() => {}}
        onComposing={() => {}}
        onCmdK={wit ? onCmdK : undefined}
        t={t}
      />,
    );
    const box = screen.getByLabelText("body");
    await select(box, 3, 0);
    fireEvent.keyDown(box, { key: "k", code: "KeyK", ...mod });
    expect(await screen.findByPlaceholderText("https://")).toBeInTheDocument();
    expect(onCmdK).not.toHaveBeenCalled();
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

  it("the font menus apply to the selection and show the choice", async () => {
    const user = userEvent.setup();
    const { box, onChange } = setup("<p>hello world</p>");
    await select(box, 0, 5);
    await user.click(screen.getByRole("button", { name: "compose.font_size" }));
    await user.click(await screen.findByRole("menuitem", { name: "24 pt" }));
    await waitFor(() =>
      expect(last(onChange)).toBe('<p><span style="font-size: 24pt">hello</span> world</p>'),
    );
    // jsdom drops the DOM selection when the menu refocuses the editor (a browser keeps it), and Slate's
    // throttled selectionchange reads that back on a slow run: select again, as a user's highlight would stay.
    await select(box, 0, 5);
    await user.click(screen.getByRole("button", { name: "compose.font_family" }));
    await user.click(await screen.findByRole("menuitem", { name: "Georgia" }));
    await waitFor(() =>
      expect(last(onChange)).toBe(
        '<p><span style="font-size: 24pt; font-family: Georgia, serif">hello</span> world</p>',
      ),
    );
    await user.click(screen.getByRole("button", { name: "compose.font_family" }));
    const georgia = await screen.findByRole("menuitem", { name: "Georgia" });
    expect(georgia.querySelector("svg")).not.toBeNull(); // the check
  });

  it("the quote toggle unwraps a list inside a quote, keeping the list", async () => {
    const user = userEvent.setup();
    const { box, onChange } = setup("<blockquote><ul><li><p>1</p></li></ul></blockquote>");
    await select(box, 0, 0);
    await user.click(screen.getByRole("button", { name: "compose.quote" }));
    await waitFor(() => expect(last(onChange)).toBe("<ul><li><p>1</p></li></ul>"));
  });

  it("the toolbar renders in the host's slot, still driving the editor", async () => {
    const user = userEvent.setup();
    const slot = document.createElement("div");
    document.body.append(slot);
    const onChange = vi.fn();
    render(
      <Editor
        initial="<p>a</p>"
        readOnly={false}
        placeholder="p"
        onChange={onChange}
        onComposing={() => {}}
        toolbarSlot={slot}
        t={t}
      />,
    );
    const quote = screen.getByRole("button", { name: "compose.quote" });
    expect(slot).toContainElement(quote);
    const box = screen.getByLabelText("body");
    await select(box, 0, 1);
    await user.click(quote);
    await waitFor(() => expect(last(onChange)).toBe("<blockquote><p>a</p></blockquote>"));
    slot.remove();
  });

  // ⌘K on [from, from + len) of the text node reading `text`; the popover shows while `pop`.
  async function cmdk(initial: string, text: string, from: number, len: number, onCmdK = () => {}) {
    const ui = (pop: boolean) => (
      <Editor
        initial={initial}
        readOnly={false}
        placeholder="p"
        onChange={() => {}}
        onComposing={() => {}}
        onCmdK={onCmdK}
        popover={pop ? <input aria-label="pop" /> : undefined}
        t={t}
      />
    );
    const { rerender } = render(ui(false));
    const box = screen.getByLabelText("body");
    const walker = document.createTreeWalker(box, NodeFilter.SHOW_TEXT);
    let node = walker.nextNode();
    while (node && node.textContent !== text) node = walker.nextNode();
    await act(async () => {
      box.focus();
      const r = document.createRange();
      r.setStart(node as Node, from);
      r.setEnd(node as Node, from + len);
      window.getSelection()?.removeAllRanges();
      window.getSelection()?.addRange(r);
      document.dispatchEvent(new Event("selectionchange"));
      await new Promise((res) => setTimeout(res, 150));
    });
    fireEvent.keyDown(box, { key: "k", code: "KeyK", ...mod });
    rerender(ui(true));
    return { box, close: () => rerender(ui(false)) };
  }

  it("the ⌘K selection stays marked while its popover shows, the popover under its block", async () => {
    const { box, close } = await cmdk("<p>hello world</p><p>next</p>", "hello world", 6, 5);
    await waitFor(() => expect(box.querySelector(".bg-border")).toHaveTextContent(/^world$/));
    const pop = screen.getByRole("textbox", { name: "pop" });
    // right after the selected paragraph, before the next one, outside the editable text
    const slot = pop.closest("[contenteditable=false]") as HTMLElement;
    expect(slot.previousElementSibling).toHaveTextContent("hello world");
    expect(slot.nextElementSibling).toHaveTextContent("next");
    expect(box.querySelector("[data-held-block]")).toBeNull(); // part of a block: no block mark
    close();
    await waitFor(() => expect(box.querySelector(".bg-border")).toBeNull());
    expect(screen.queryByRole("textbox", { name: "pop" })).toBeNull();
  });

  it("a ⌘K on a whole paragraph marks the paragraph as one block", async () => {
    const { box } = await cmdk("<p>one</p><p>two words</p><p>three</p>", "two words", 0, 9);
    await waitFor(() =>
      expect(box.querySelector("[data-held-block]")).toHaveTextContent(/^two words$/),
    );
  });

  it("keys in the popover are not the editor's: ⌘K there is not another ⌘K", async () => {
    const onCmdK = vi.fn();
    await cmdk("<p>hello world</p>", "hello world", 0, 5, onCmdK);
    expect(onCmdK).toHaveBeenCalledTimes(1);
    const pop = await screen.findByRole("textbox", { name: "pop" });
    fireEvent.keyDown(pop, { key: "k", code: "KeyK", ...mod });
    expect(onCmdK).toHaveBeenCalledTimes(1);
  });

  it("insertReplacementText preserves fontSize marks and Enter does not split block", async () => {
    const { box, onChange } = setup('<p><span style="font-size: 36px;">Schullle</span></p>');
    await select(box, 8, 0);

    const ev = new InputEvent("beforeinput", {
      inputType: "insertReplacementText",
      data: "Schule",
      bubbles: true,
      cancelable: true,
    });
    act(() => {
      box.dispatchEvent(ev);
    });

    await waitFor(() => {
      const html = last(onChange);
      expect(html).toContain("Schule");
      expect(html).toContain("font-size: 36px");
      expect(html).not.toContain("Schullle");
    });

    // An Enter key right after accepting autocorrect should be suppressed
    fireEvent.keyDown(box, { key: "Enter", code: "Enter" });
    const htmlAfterEnter = last(onChange);
    expect(htmlAfterEnter).toBe('<p><span style="font-size: 36px">Schule</span></p>');
  });

  // Without target ranges (jsdom has none), the replacement takes the selection, or the word before the caret.
  const replace = (box: HTMLElement, data: string) =>
    act(() => {
      box.dispatchEvent(
        new InputEvent("beforeinput", {
          inputType: "insertReplacementText",
          data,
          bubbles: true,
          cancelable: true,
        }),
      );
    });

  it("insertReplacementText replaces a whole selection", async () => {
    const { box, onChange } = setup("<p>foo bar</p>");
    await select(box, 0, 7);
    replace(box, "baz");
    await waitFor(() => expect(last(onChange)).toBe("<p>baz</p>"));
  });

  it("insertReplacementText after a space keeps the space", async () => {
    const { box, onChange } = setup("<p>wrd </p>");
    await select(box, 4, 0);
    replace(box, "word");
    await waitFor(() => expect(last(onChange)).toBe("<p>word </p>"));
  });

  it("setValue creates multiple undoable steps", async () => {
    const ref = { current: null as unknown as EditorHandle };
    const onChange = vi.fn();
    render(
      <Editor
        ref={ref}
        initial="<p>first</p>"
        readOnly={false}
        placeholder=""
        onChange={onChange}
        onComposing={() => {}}
        t={t}
      />,
    );
    act(() => {
      ref.current.setValue("<p>second</p>");
    });
    expect(ref.current.canUndo()).toBe(true);
    act(() => {
      ref.current.setValue("<p>third</p>");
    });
    expect(ref.current.canUndo()).toBe(true);
    act(() => {
      ref.current.undo();
    });
    expect(onChange).toHaveBeenLastCalledWith("<p>second</p>");
    expect(ref.current.canUndo()).toBe(true);
    act(() => {
      ref.current.undo();
    });
    expect(onChange).toHaveBeenLastCalledWith("<p>first</p>");
    expect(ref.current.canUndo()).toBe(false);
    expect(ref.current.canRedo()).toBe(true);
    act(() => {
      ref.current.redo();
    });
    expect(onChange).toHaveBeenLastCalledWith("<p>second</p>");
  });
});
