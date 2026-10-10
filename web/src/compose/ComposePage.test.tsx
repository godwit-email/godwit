import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ComposePage } from "./ComposePage";

const h = vi.hoisted(() => ({
  closing: undefined as undefined | ((gen: number) => void),
  theme: undefined as undefined | ((t: unknown) => void),
}));
vi.mock("@/mygo", () => ({
  ComposePage: { load: vi.fn(), save: vi.fn(), closed: vi.fn() },
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
vi.mock("mygo-runtime", () => ({ currentWindow: { setTitle: vi.fn() } }));

import { currentWindow } from "mygo-runtime";
import { ComposePage as api } from "@/mygo";

const theme = { mode: "light", light: {}, dark: {}, fontSize: 14 };
const state = {
  draftId: 1,
  accounts: [{ id: 1, email: "me@x.com" }],
  from: 1,
  to: ["a@x.com"],
  cc: [],
  bcc: [],
  replyTo: "",
  subject: "你好",
  body: "正文",
  quote: "> 原文",
  signature: "",
  revision: 7,
  theme,
};

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(api.load).mockResolvedValue(state);
  vi.mocked(api.save).mockResolvedValue({ revision: 8, conflict: false });
  vi.mocked(api.closed).mockResolvedValue();
});

const body = () => screen.findByRole("textbox", { name: "正文" });

test("loads the draft", async () => {
  render(<ComposePage />);
  expect(await body()).toHaveValue("正文");
  expect(screen.getByRole("textbox", { name: "主题" })).toHaveValue("你好");
  expect(screen.getByRole("textbox", { name: "收件人" })).toHaveValue("a@x.com");
  expect(screen.queryByText("> 原文")).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "显示引用的原邮件" }));
  expect(screen.getByText("> 原文")).toBeInTheDocument();
  expect(currentWindow.setTitle).toHaveBeenLastCalledWith("你好");
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
  act(() => {
    h.closing?.(1);
    h.closing?.(2);
  });
  await act(async () => release({ revision: 8, conflict: false }));
  expect(api.closed).toHaveBeenCalledWith(1, true);
  expect(api.closed).toHaveBeenCalledWith(2, true);
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
    expect(screen.getByRole("alert")).toHaveTextContent("草稿已在别处修改，本窗口不再保存");
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
