import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode } from "react";
import { ComposePage } from "./ComposePage";

const h = vi.hoisted(() => ({
  closing: undefined as undefined | ((gen: number) => void),
  theme: undefined as undefined | ((t: unknown) => void),
}));
vi.mock("@/mygo", () => ({
  ComposePage: {
    load: vi.fn(),
    save: vi.fn(),
    closed: vi.fn(),
    latest: vi.fn(),
    setAccount: vi.fn(),
    addAttachments: vi.fn(),
    removeAttachment: vi.fn(),
    send: vi.fn(),
  },
  events: {
    closing: {
      on: (f: (gen: number) => void) => {
        h.closing = f;
        return () => {};
      },
    },
    theme: {
      on: (f: (t: unknown) => void) => {
        h.theme = f;
        return () => {};
      },
    },
  },
}));
vi.mock("mygo-runtime", async (original) => ({
  ...(await original<typeof import("mygo-runtime")>()),
  currentWindow: { setTitle: vi.fn() },
}));
// Plate has its own tests; here the body is a textarea that, like Plate, reads `initial` at mount
// and takes no input while read-only.
vi.mock("./editor/Editor", async () => {
  const { useState } = await import("react");
  return {
    Editor(p: {
      initial: string;
      readOnly: boolean;
      onChange(html: string): void;
      t(k: string): string;
      attach?: React.ReactNode;
    }) {
      const [v, setV] = useState(p.initial);
      return (
        <div>
          {p.attach}
          <textarea
            aria-label={p.t("compose.body")}
            value={v}
            readOnly={p.readOnly}
            onChange={(e) => {
              if (p.readOnly) return;
              setV(e.target.value);
              p.onChange(e.target.value);
            }}
          />
        </div>
      );
    },
  };
});

import { currentWindow } from "mygo-runtime";
import { ComposePage as api, type ComposeState as ComposeStateT } from "@/mygo";

const theme = { mode: "light", light: {}, dark: {}, fontSize: 14, bodyFontSize: 14 };
const state = {
  draftId: 1,
  accounts: [
    { id: 1, email: "me@x.com" },
    { id: 2, email: "work@x.com" },
  ],
  from: 1,
  to: ["a@x.com"],
  cc: [],
  bcc: [],
  replyTo: "",
  subject: "你好",
  body: "正文",
  quote: "> 原文",
  signature: "-- me",
  signatureImages: [],
  attachments: [],
  revision: 7,
  updatedAt: new Date(2026, 9, 10, 9, 5).getTime(),
  theme,
  locale: "zh-Hans",
  fonts: [],
  wit: { enabled: false, translateOnly: false, skills: [], languages: [], chips: [], model: "" },
};
const noWarnings = { sent: false, conflict: false, warnings: [], digest: "", blocked: null };

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(api.load).mockResolvedValue(state);
  vi.mocked(api.save).mockResolvedValue({ revision: 8, conflict: false });
  vi.mocked(api.closed).mockResolvedValue();
  vi.mocked(api.send).mockResolvedValue({ ...noWarnings, sent: true });
});

const body = () => screen.findByRole("textbox", { name: "正文" });

test("loads the draft", async () => {
  render(<ComposePage />);
  expect(await body()).toHaveValue("正文");
  expect(screen.getByRole("textbox", { name: "主题" })).toHaveValue("你好");
  expect(screen.getByRole("button", { name: "移除 a@x.com" })).toBeInTheDocument();
  expect(screen.getByText("-- me")).toBeInTheDocument();
  expect(screen.queryByText("> 原文")).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "显示引用的原邮件（助手只读）" }));
  expect(screen.getByText("> 原文")).toBeInTheDocument();
  expect(currentWindow.setTitle).toHaveBeenLastCalledWith("你好");
});

test("the editor is a card on the window's material, under a borderless title bar", async () => {
  render(<ComposePage />);
  const section = (await body()).closest("section");
  const main = screen.getByRole("main");
  expect(main).toHaveClass("bg-window");
  expect(main.querySelector("header")).not.toHaveClass("border-b");
  expect(section).toHaveClass("overflow-hidden", "rounded-xl", "border-[0.5px]", "bg-background");
  // in a resizable panel of the row inset on the material; the grip is the 8 px gap
  expect(section?.closest("[data-group]")).toHaveClass("px-2", "pb-2");
});

test("flushes on closing", async () => {
  render(<ComposePage />);
  await userEvent.type(await body(), "!");
  await act(async () => h.closing?.(3));
  expect(api.save).toHaveBeenCalledWith(expect.objectContaining({ body: "正文!" }));
  expect(api.closed).toHaveBeenCalledWith(3, true);
});

test("closed(gen, false) when the save fails or conflicts", async () => {
  vi.mocked(api.save).mockRejectedValueOnce(new Error("x"));
  render(<ComposePage />);
  await userEvent.type(await body(), "!");
  await act(async () => h.closing?.(4));
  expect(api.closed).toHaveBeenLastCalledWith(4, false);

  vi.mocked(api.save).mockResolvedValue({ revision: 7, conflict: true });
  await userEvent.type(await body(), "!");
  await act(async () => h.closing?.(5));
  expect(api.closed).toHaveBeenLastCalledWith(5, false);
});

test("a second closing answers with its own gen", async () => {
  let release!: (r: { revision: number; conflict: boolean }) => void;
  vi.mocked(api.save).mockImplementationOnce(() => new Promise((r) => (release = r)));
  render(<ComposePage />);
  await userEvent.type(await body(), "!");
  await act(async () => {
    h.closing?.(1);
    h.closing?.(2);
  });
  await act(async () => release({ revision: 8, conflict: false }));
  expect(api.closed).toHaveBeenCalledWith(1, true);
  expect(api.closed).toHaveBeenCalledWith(2, true);
});

test("after a saved=false answer the fields take input again", async () => {
  vi.mocked(api.save).mockRejectedValueOnce(new Error("x"));
  render(<ComposePage />);
  const el = await body();
  await userEvent.type(el, "!");
  await act(async () => h.closing?.(4));
  expect(api.closed).toHaveBeenLastCalledWith(4, false);
  await userEvent.type(el, "?");
  expect(el).toHaveValue("正文!?");
});

test("a composition in progress at closing is committed, then flushed", async () => {
  render(<ComposePage />);
  const el = await body();
  el.focus();
  act(() => {
    el.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true }));
  });
  fireEvent.change(el, { target: { value: "正文ni" } });
  let done!: Promise<void>;
  act(() => {
    done = h.closing?.(5) as unknown as Promise<void>;
  });
  await act(async () => {}); // closing waits for the composition
  expect(api.closed).not.toHaveBeenCalled();
  act(() => {
    // WebKit's order: compositionend, then the committed text's input
    el.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true }));
    fireEvent.change(el, { target: { value: "正文你" } });
  });
  await act(() => done);
  expect(api.save).toHaveBeenLastCalledWith(expect.objectContaining({ body: "正文你" }));
  expect(api.closed).toHaveBeenCalledWith(5, true);
});

test("an edit as soon as the body shows reaches the saver", async () => {
  // A slow first render: React yields between the commit that shows the body and its passive
  // effects, and a timer task (here the poll) runs in between.
  let now = performance.now();
  const clock = vi.spyOn(performance, "now").mockImplementation(() => (now += 10));
  render(<ComposePage />);
  const el = await new Promise<HTMLElement>(function poll(done) {
    const found = screen.queryByRole("textbox", { name: "正文" });
    if (found) done(found);
    else setTimeout(() => poll(done), 0);
  });
  clock.mockRestore();
  fireEvent.change(el, { target: { value: "正文!" } });
  await act(async () => h.closing?.(3));
  expect(api.save).toHaveBeenCalledWith(expect.objectContaining({ body: "正文!" }));
  expect(api.closed).toHaveBeenCalledWith(3, true);
});

test("a compositionend fired synchronously by the blur still flushes and answers", async () => {
  render(<ComposePage />);
  const el = await body();
  el.focus();
  act(() => {
    el.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true }));
  });
  fireEvent.change(el, { target: { value: "正文你" } });
  el.blur = () => el.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true }));
  await act(async () => h.closing?.(7));
  expect(api.save).toHaveBeenLastCalledWith(expect.objectContaining({ body: "正文你" }));
  expect(api.closed).toHaveBeenCalledWith(7, true);
});

// An address being composed when the settling starts: its final input lands, then it is committed.
const composeAddress = async () => {
  render(<ComposePage />);
  await body();
  const to = screen.getByRole("textbox", { name: "收件人" });
  to.focus();
  act(() => {
    to.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true }));
  });
  fireEvent.change(to, { target: { value: "ni@example.com" } });
  return () =>
    act(() => {
      // WebKit's order: compositionend, then the committed text's input
      to.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true }));
      fireEvent.change(to, { target: { value: "你@example.com" } });
    });
};

test("an address composition in progress at closing saves the final address", async () => {
  const end = await composeAddress();
  let done!: Promise<void>;
  act(() => {
    done = h.closing?.(5) as unknown as Promise<void>;
  });
  await act(async () => {});
  expect(api.closed).not.toHaveBeenCalled();
  end();
  await act(() => done);
  expect(api.save).toHaveBeenLastCalledWith(
    expect.objectContaining({ to: ["a@x.com", "你@example.com"] }),
  );
  expect(api.closed).toHaveBeenCalledWith(5, true);
});

test("an address composition in progress at send sends the final address", async () => {
  const end = await composeAddress();
  fireEvent.click(screen.getByRole("button", { name: "发送" }));
  await act(async () => {});
  expect(api.send).not.toHaveBeenCalled();
  end();
  await waitFor(() => expect(api.send).toHaveBeenCalled());
  expect(api.save).toHaveBeenLastCalledWith(
    expect.objectContaining({ to: ["a@x.com", "你@example.com"] }),
  );
});

describe("autosave", () => {
  beforeEach(() => vi.useFakeTimers({ shouldAdvanceTime: true }));
  afterEach(() => vi.useRealTimers());
  const setup = () => userEvent.setup({ advanceTimers: vi.advanceTimersByTime });

  test("shows conflict", async () => {
    vi.mocked(api.save).mockResolvedValue({ revision: 7, conflict: true });
    const u = setup();
    render(<ComposePage />);
    await u.type(await body(), "!");
    await act(() => vi.advanceTimersByTimeAsync(1000));
    expect(screen.getByRole("alert")).toHaveTextContent("草稿在别处被修改");
  });

  test("first save carries the loaded revision, later ones the previous result's", async () => {
    const u = setup();
    render(<ComposePage />);
    await u.type(await body(), "!");
    await act(() => vi.advanceTimersByTimeAsync(1000));
    await u.type(screen.getByRole("textbox", { name: "正文" }), "?");
    await act(() => vi.advanceTimersByTimeAsync(1000));
    expect(vi.mocked(api.save).mock.calls.map((c) => c[0].revision)).toEqual([7, 8]);
    expect(screen.getByText(/草稿已保存 · \d\d:\d\d/)).toBeInTheDocument();
  });

  test("input after a saved closing answer is rejected until the window outlives it 2 s", async () => {
    const u = setup();
    render(<ComposePage />);
    const el = await body();
    await u.type(el, "!");
    await act(async () => h.closing?.(3));
    expect(api.closed).toHaveBeenCalledWith(3, true);
    await u.type(el, "?");
    fireEvent.change(el, { target: { value: "x" } });
    expect(el).toHaveValue("正文!");
    await act(() => vi.advanceTimersByTimeAsync(5000));
    expect(api.save).toHaveBeenCalledTimes(1);
    await u.type(el, "?");
    expect(el).toHaveValue("正文!?");
  });

  test("a composition still open after 1 s: not saved, answered unsaved, editable and focused again", async () => {
    const u = setup();
    render(<ComposePage />);
    const el = await body();
    await u.click(el);
    act(() => {
      el.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true }));
    });
    fireEvent.change(el, { target: { value: "正文ni" } });
    let done!: Promise<void>;
    act(() => {
      done = h.closing?.(6) as unknown as Promise<void>;
    });
    await act(() => vi.advanceTimersByTimeAsync(1000));
    await act(() => done);
    expect(api.save).not.toHaveBeenCalled();
    expect(api.closed).toHaveBeenCalledWith(6, false);
    expect(el).toHaveFocus();
    fireEvent.change(el, { target: { value: "正文你" } });
    expect(el).toHaveValue("正文你");
  });

  test("an address composition still open after 1 s: answered unsaved, its text kept pending", async () => {
    await composeAddress(); // never ended
    let done!: Promise<void>;
    act(() => {
      done = h.closing?.(6) as unknown as Promise<void>;
    });
    await act(() => vi.advanceTimersByTimeAsync(1000));
    await act(() => done);
    expect(api.save).not.toHaveBeenCalled();
    expect(api.closed).toHaveBeenCalledWith(6, false);
    expect(screen.getByRole("textbox", { name: "收件人" })).toHaveValue("ni@example.com");
    expect(screen.queryByRole("button", { name: "移除 ni@example.com" })).not.toBeInTheDocument();
  });

  test("IME: no save until compositionend", async () => {
    const u = setup();
    render(<ComposePage />);
    const el = await body();
    await u.click(el);
    el.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true }));
    await u.keyboard("x");
    await act(() => vi.advanceTimersByTimeAsync(5000));
    expect(api.save).not.toHaveBeenCalled();
    el.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true }));
    await act(() => vi.advanceTimersByTimeAsync(1000));
    expect(api.save).toHaveBeenCalledTimes(1);
  });
});

test("only the latest load applies", async () => {
  const rs: Array<(s: typeof state) => void> = [];
  vi.mocked(api.load).mockImplementation(() => new Promise((r) => rs.push(r)));
  render(
    <StrictMode>
      <ComposePage />
    </StrictMode>,
  );
  expect(rs).toHaveLength(2);
  await act(async () => rs[1]({ ...state, body: "新", revision: 20 }));
  await act(async () => rs[0]({ ...state, body: "旧", revision: 10 }));
  const el = await body();
  expect(el).toHaveValue("新");
  await userEvent.type(el, "!");
  await act(async () => h.closing?.(1));
  expect(api.save).toHaveBeenCalledWith(expect.objectContaining({ body: "新!", revision: 20 }));
});

test("an untouched draft reads as saved; unmount disposes the saver", async () => {
  const { unmount } = render(<ComposePage />);
  await body();
  // the stored draft's time, in the top bar
  expect(within(screen.getByRole("banner")).getByText("草稿已保存 · 09:05")).toBeInTheDocument();
  vi.useFakeTimers();
  fireEvent.change(screen.getByRole("textbox", { name: "正文" }), { target: { value: "x" } });
  unmount();
  await vi.advanceTimersByTimeAsync(5000);
  vi.useRealTimers();
  expect(api.save).not.toHaveBeenCalled();
});

test("unmount during a failing flush still answers closed(gen, false)", async () => {
  let fail!: (e: Error) => void;
  vi.mocked(api.save).mockImplementationOnce(() => new Promise((_, rej) => (fail = rej)));
  const { unmount } = render(<ComposePage />);
  await userEvent.type(await body(), "!");
  await act(async () => {
    h.closing?.(9);
  });
  unmount();
  await act(async () => fail(new Error("x")));
  expect(api.closed).toHaveBeenCalledWith(9, false);
});

test("a failing closed answer thaws the page", async () => {
  vi.mocked(api.closed).mockRejectedValueOnce(new Error("x"));
  render(<ComposePage />);
  const el = await body();
  await userEvent.type(el, "!");
  await act(async () => h.closing?.(3));
  expect(api.closed).toHaveBeenCalledWith(3, true);
  await userEvent.type(el, "?");
  expect(el).toHaveValue("正文!?");
});

const order = (f: ReturnType<typeof vi.fn>) => f.mock.invocationCallOrder[0];

test("an empty subject titles the window 新邮件", async () => {
  vi.mocked(api.load).mockResolvedValue({ ...state, subject: "" });
  render(<ComposePage />);
  await body();
  expect(currentWindow.setTitle).toHaveBeenLastCalledWith("新邮件");
  expect(screen.getByRole("heading", { name: "新邮件" })).toBeInTheDocument();
});

test("send flushes first and freezes", async () => {
  let sent!: (r: typeof noWarnings) => void;
  vi.mocked(api.send).mockImplementation(() => new Promise((r) => (sent = r)));
  render(<ComposePage />);
  const el = await body();
  fireEvent.change(el, { target: { value: "正文!" } });
  await userEvent.click(screen.getByRole("button", { name: "发送" }));
  expect(api.save).toHaveBeenCalledWith(expect.objectContaining({ body: "正文!", revision: 7 }));
  expect(api.send).toHaveBeenCalledWith(8, "");
  expect(order(vi.mocked(api.save))).toBeLessThan(order(vi.mocked(api.send)));
  // typing while it sends is refused, and says so
  await userEvent.type(el, "y");
  expect(el).toHaveValue("正文!");
  expect(await screen.findByText("正在发送，暂时不能修改草稿")).toBeInTheDocument();
  fireEvent.change(screen.getByRole("textbox", { name: "主题" }), { target: { value: "x" } });
  expect(screen.getByRole("textbox", { name: "主题" })).toHaveValue("你好");
  expect(await screen.findByText("正在发送，暂时不能修改草稿")).toBeInTheDocument();
  expect(el).toHaveAttribute("readonly");
  expect(screen.getByRole("textbox", { name: "主题" })).toHaveAttribute("readonly");
  expect(screen.getByRole("combobox", { name: "发件账户" })).toBeDisabled();
  await act(async () => sent({ ...noWarnings, sent: true }));
  expect(screen.getByRole("textbox", { name: "主题" })).toHaveAttribute("readonly");
  // sent: Go closes the window, the form stays frozen
  expect(el).toHaveAttribute("readonly");
  expect(screen.getByRole("button", { name: "发送" })).toBeDisabled();
});

test("send aborts on failed save", async () => {
  vi.mocked(api.save).mockRejectedValueOnce(new Error("x"));
  render(<ComposePage />);
  const el = await body();
  fireEvent.change(el, { target: { value: "正文!" } });
  await userEvent.click(screen.getByRole("button", { name: "发送" }));
  expect(api.save).toHaveBeenCalled();
  expect(api.send).not.toHaveBeenCalled();
  await userEvent.type(el, "?");
  expect(el).toHaveValue("正文!?");
  expect(screen.getByText("未保存")).toBeInTheDocument();
});

test("clicking 发送 commits a typed address", async () => {
  render(<ComposePage />);
  await body();
  fireEvent.change(screen.getByRole("textbox", { name: "收件人" }), {
    target: { value: "x@y.z" },
  });
  await userEvent.click(screen.getByRole("button", { name: "发送" }));
  await waitFor(() => expect(api.send).toHaveBeenCalledWith(8, ""));
  expect(api.save).toHaveBeenCalledWith(expect.objectContaining({ to: ["a@x.com", "x@y.z"] }));
});

test("cmd-enter commits pending address", async () => {
  render(<ComposePage />);
  const el = await body();
  fireEvent.change(screen.getByRole("textbox", { name: "收件人" }), {
    target: { value: "x@y.z" },
  });
  await act(async () => {
    fireEvent.keyDown(el, { key: "Enter", metaKey: true });
  });
  await waitFor(() => expect(api.send).toHaveBeenCalledWith(8, ""));
  expect(api.save).toHaveBeenCalledWith(expect.objectContaining({ to: ["a@x.com", "x@y.z"] }));
});

test("send warnings then send anyway", async () => {
  const w = { plugin: "p", message: "没有附件" };
  vi.mocked(api.send).mockResolvedValueOnce({ ...noWarnings, warnings: [w], digest: "d" });
  render(<ComposePage />);
  await body();
  await userEvent.click(screen.getByRole("button", { name: "发送" }));
  expect(api.send).toHaveBeenCalledWith(7, "");
  expect(screen.getByText("发送前检查：没有附件")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "仍然发送" }));
  expect(api.send).toHaveBeenLastCalledWith(7, "d");
});

test("an edit clears the warnings", async () => {
  const w = { plugin: "p", message: "没有附件" };
  vi.mocked(api.send).mockResolvedValueOnce({ ...noWarnings, warnings: [w], digest: "d" });
  render(<ComposePage />);
  const el = await body();
  await userEvent.click(screen.getByRole("button", { name: "发送" }));
  expect(screen.getByText("发送前检查：没有附件")).toBeInTheDocument();
  await userEvent.type(el, "!");
  expect(screen.queryByText("发送前检查：没有附件")).not.toBeInTheDocument();
});

test("account switch keeps body", async () => {
  vi.mocked(api.setAccount).mockResolvedValue({
    revision: 9,
    attachments: [],
    conflict: false,
    signature: "-- work",
    signatureImages: ["data:image/png;base64,AAAA"],
    updatedAt: new Date(2026, 9, 10, 11, 42).getTime(),
  });
  render(<ComposePage />);
  const el = await body();
  fireEvent.change(el, { target: { value: "正文!" } });
  await userEvent.click(screen.getByRole("combobox", { name: "发件账户" }));
  await userEvent.click(await screen.findByRole("option", { name: "work@x.com" }));
  await waitFor(() => expect(api.setAccount).toHaveBeenCalledWith(2, 8));
  expect(api.save).toHaveBeenCalledWith(expect.objectContaining({ body: "正文!" }));
  expect(order(vi.mocked(api.save))).toBeLessThan(order(vi.mocked(api.setAccount)));
  const sig = await screen.findByText("-- work");
  expect(sig.querySelector("img")?.getAttribute("src")).toBe("data:image/png;base64,AAAA");
  expect(el).toHaveValue("正文!");
  expect(within(screen.getByRole("banner")).getByText("草稿已保存 · 11:42")).toBeInTheDocument();
});

test("an account switch that conflicts reverts the select", async () => {
  vi.mocked(api.setAccount).mockResolvedValue({
    revision: 0,
    attachments: [],
    conflict: true,
    signature: "",
    signatureImages: [],
    updatedAt: 0,
  });
  render(<ComposePage />);
  await body();
  await userEvent.click(screen.getByRole("combobox", { name: "发件账户" }));
  await userEvent.click(await screen.findByRole("option", { name: "work@x.com" }));
  await waitFor(() => expect(api.setAccount).toHaveBeenCalled());
  await waitFor(() =>
    expect(screen.getByRole("combobox", { name: "发件账户" })).toHaveTextContent("me@x.com"),
  );
  expect(screen.getByText("-- me")).toBeInTheDocument();
});

test("attachments: add reports oversize, × removes", async () => {
  const a = { id: 3, filename: "a.pdf", size: 1024 };
  vi.mocked(api.addAttachments).mockResolvedValue({
    revision: 9,
    attachments: [a],
    conflict: false,
    signature: "-- me",
    signatureImages: [],
    tooLarge: ["big.zip"],
    failed: ["bad.bin"],
    updatedAt: new Date(2026, 9, 10, 11, 43).getTime(),
  });
  vi.mocked(api.removeAttachment).mockResolvedValue({
    revision: 10,
    attachments: [],
    conflict: false,
    signature: "-- me",
    signatureImages: [],
    updatedAt: 0,
  });
  render(<ComposePage />);
  await body();
  await userEvent.click(screen.getByRole("button", { name: "添加附件" }));
  expect(api.addAttachments).toHaveBeenCalledWith(7);
  expect(screen.getByText("a.pdf")).toBeInTheDocument();
  expect(screen.getByText("big.zip")).toBeInTheDocument();
  expect(screen.getByText("未能添加：bad.bin")).toBeInTheDocument();
  expect(within(screen.getByRole("banner")).getByText("草稿已保存 · 11:43")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "移除 a.pdf" }));
  expect(api.removeAttachment).toHaveBeenCalledWith(3, 9);
  expect(screen.queryByText("a.pdf")).not.toBeInTheDocument();
});

const conflictBySend = async () => {
  vi.mocked(api.save).mockResolvedValueOnce({ revision: 0, conflict: true });
  render(<ComposePage />);
  const el = await body();
  fireEvent.change(el, { target: { value: "正文!" } });
  await userEvent.click(screen.getByRole("button", { name: "发送" }));
  expect(api.send).not.toHaveBeenCalled();
  expect(screen.getByText("草稿在别处被修改")).toBeInTheDocument();
  return el;
};

test("keep mine after conflict", async () => {
  vi.mocked(api.latest).mockResolvedValue({
    ...state,
    body: "别处",
    signature: "-- new",
    signatureImages: [],
    revision: 30,
  });
  vi.mocked(api.save).mockResolvedValue({ revision: 31, conflict: false });
  const el = await conflictBySend();
  await userEvent.click(screen.getByRole("button", { name: "保留我的版本" }));
  expect(api.latest).toHaveBeenCalled();
  expect(api.save).toHaveBeenLastCalledWith(
    expect.objectContaining({ body: "正文!", revision: 30 }),
  );
  expect(el).toHaveValue("正文!");
  expect(screen.getByText("-- new")).toBeInTheDocument();
  expect(screen.queryByText("草稿在别处被修改")).not.toBeInTheDocument();
});

test("use latest after conflict", async () => {
  vi.mocked(api.latest).mockResolvedValue({
    ...state,
    to: ["z@x.com"],
    subject: "新主题",
    body: "最新",
    revision: 30,
  });
  await conflictBySend();
  await userEvent.click(screen.getByRole("button", { name: "使用最新版本" }));
  expect(await body()).toHaveValue("最新");
  expect(screen.getByRole("textbox", { name: "主题" })).toHaveValue("新主题");
  expect(screen.getByRole("button", { name: "移除 z@x.com" })).toBeInTheDocument();
  expect(screen.queryByText("草稿在别处被修改")).not.toBeInTheDocument();
  await userEvent.type(await body(), "!");
  await act(async () => h.closing?.(1));
  expect(api.save).toHaveBeenLastCalledWith(
    expect.objectContaining({ body: "最新!", revision: 30 }),
  );
});

test("a failed read of the latest offers 重试", async () => {
  vi.mocked(api.latest).mockRejectedValueOnce(new Error("x"));
  vi.mocked(api.latest).mockResolvedValue({ ...state, body: "最新", revision: 30 });
  await conflictBySend();
  await userEvent.click(screen.getByRole("button", { name: "使用最新版本" }));
  await userEvent.click(await screen.findByRole("button", { name: "重试" }));
  expect(api.latest).toHaveBeenCalledTimes(2);
  expect(await body()).toHaveValue("最新");
});

test("the bottom bar holds the format toolbar and 发送 only: no discard, no note", async () => {
  render(<ComposePage />);
  await body();
  const bar = document.querySelector("[data-bar]") as HTMLElement;
  expect(within(bar).getByRole("button", { name: "发送" })).toBeInTheDocument();
  expect(within(bar).queryByRole("button", { name: "丢弃草稿" })).toBeNull();
  expect(bar).not.toHaveTextContent("只能写草稿");
  // a narrow window wraps it, the toolbar first and 发送 kept at the end (ml-auto)
  expect(bar).toHaveClass("flex-wrap");
  const send = within(bar).getByRole("button", { name: "发送" });
  expect(send).toHaveClass("ml-auto");
  expect(bar.lastElementChild).toBe(send);
});

test("send during composition: a composition that never ends sends nothing", async () => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
  try {
    const u = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<ComposePage />);
    const el = await body();
    await u.click(el);
    act(() => {
      el.dispatchEvent(new CompositionEvent("compositionstart", { bubbles: true }));
    });
    fireEvent.change(el, { target: { value: "正文ni" } });
    await u.click(screen.getByRole("button", { name: "发送" }));
    await act(() => vi.advanceTimersByTimeAsync(1500));
    expect(api.send).not.toHaveBeenCalled();
    expect(api.save).not.toHaveBeenCalled();
    fireEvent.change(el, { target: { value: "正文你" } });
    expect(el).toHaveValue("正文你");
  } finally {
    vi.useRealTimers();
  }
});

test("a conflict stopping the adding part way still lists the files added", async () => {
  const a = { id: 3, filename: "a.pdf", size: 1024 };
  vi.mocked(api.addAttachments).mockResolvedValue({
    revision: 12,
    attachments: [a],
    conflict: true,
    signature: "-- me",
    signatureImages: [],
    updatedAt: 0,
    tooLarge: [],
    failed: [],
  });
  render(<ComposePage />);
  await body();
  await userEvent.click(screen.getByRole("button", { name: "添加附件" }));
  expect(screen.getByText("a.pdf")).toBeInTheDocument();
  expect(screen.getByText("草稿在别处被修改")).toBeInTheDocument();
});

test("conflict resolution runs once at a time, frozen, and use-latest drops pending input", async () => {
  let give!: (s: ComposeStateT) => void;
  vi.mocked(api.latest).mockImplementation(() => new Promise((r) => (give = r)));
  const el = await conflictBySend();
  fireEvent.change(screen.getByRole("textbox", { name: "收件人" }), { target: { value: "p@q.r" } });
  fireEvent.click(screen.getByRole("button", { name: "使用最新版本" }));
  expect(el).toHaveAttribute("readonly");
  expect(screen.getByRole("textbox", { name: "主题" })).toHaveAttribute("readonly");
  fireEvent.click(screen.getByRole("button", { name: "保留我的版本" }));
  fireEvent.click(screen.getByRole("button", { name: "使用最新版本" }));
  expect(api.latest).toHaveBeenCalledTimes(1);
  await act(async () => give({ ...state, body: "最新", revision: 30 }));
  expect(await body()).toHaveValue("最新");
  expect(await body()).not.toHaveAttribute("readonly");
  expect(screen.getByRole("textbox", { name: "收件人" })).toHaveValue("");
});

test.each([
  ["send: draft changed since it was loaded", "草稿在别处被修改。"],
  ["boom", "出错了：boom"],
])("a rejected call reads in the page's copy: %s", async (msg, text) => {
  vi.mocked(api.send).mockRejectedValueOnce(new Error(msg));
  render(<ComposePage />);
  await body();
  await userEvent.click(screen.getByRole("button", { name: "发送" }));
  expect(await screen.findByText(text)).toBeInTheDocument();
});
