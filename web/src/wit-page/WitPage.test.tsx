import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { WitEvent, WitHistory, WitOpenMsg, WitPageState } from "@/mygo";

const h = vi.hoisted(() => {
  const on =
    <T,>(set: Set<(x: T) => void>) =>
    (f: (x: T) => void) => {
      set.add(f);
      return () => set.delete(f);
    };
  return {
    on,
    wit: new Set<(e: WitEvent) => void>(),
    open: new Set<(m: WitOpenMsg) => void>(),
    state: new Set<(s: WitPageState) => void>(),
    ask: new Set<(q: string) => void>(),
  };
});
vi.mock("@/mygo", () => ({
  WitPage: {
    load: vi.fn(),
    follow: vi.fn(),
    ask: vi.fn(),
    stop: vi.fn(),
    decide: vi.fn(),
    decideAll: vi.fn(),
    openCited: vi.fn(),
    openDraft: vi.fn(),
    openLink: vi.fn(),
    openModels: vi.fn(),
    models: vi.fn(),
    navigate: vi.fn(),
  },
  events: {
    wit: { on: h.on(h.wit) },
    witOpen: { on: h.on(h.open) },
    witState: { on: h.on(h.state) },
    witAsk: { on: h.on(h.ask) },
    theme: { on: () => () => {} },
  },
}));
vi.mock("sonner", async (orig) => ({
  ...(await orig<typeof import("sonner")>()),
  toast: Object.assign(vi.fn(), { error: vi.fn() }),
}));

import { WitPage as api } from "@/mygo";
import { WitPage } from "./WitPage";

const state = (conversation: number, version: number, model = "m1"): WitPageState => ({
  conversation,
  version,
  model,
  locale: "zh-Hans",
  theme: { mode: "light", light: {}, dark: {}, fontSize: 14, bodyFontSize: 14 },
});
const msg = (id: string, role: string, t: string, runs: number[]) => ({
  id,
  role,
  text: t,
  reasoning: "",
  quoted: [],
  calls: [],
  results: [],
  citations: [],
  runs,
});
const hist = (conversation: number, answer?: string): WitHistory => ({
  conversation,
  model: "",
  effort: "",
  usage: { input: 0, output: 0, context: 0, history: 0 },
  running: 0,
  approvals: [],
  messages: answer
    ? [msg(`q${conversation}`, "user", "Q", [1]), msg(`a${conversation}`, "assistant", answer, [1])]
    : [],
});
const ev = (e: Partial<WitEvent>): WitEvent => ({
  conversation: 1,
  run: 7,
  kind: "text",
  text: "",
  call: null,
  result: null,
  interrupts: [],
  citations: [],
  approvals: [],
  error: "",
  ...e,
});
const send = <T,>(set: Set<(x: T) => void>, x: T) =>
  act(() => {
    for (const f of [...set]) f(x);
  });
const emit = (e: Partial<WitEvent>) => send(h.wit, ev(e));
const open = (conversation: number, version: number, created = false) =>
  send(h.open, { conversation, version, created });
const input = () => screen.getByRole("textbox", { name: "向 Wit 提问" });

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.follow).mockImplementation(async (c) => hist(c));
});

async function start(conv = 0, answer?: string) {
  vi.mocked(api.load).mockResolvedValue(state(conv, 1));
  if (answer) vi.mocked(api.follow).mockResolvedValueOnce(hist(conv, answer));
  const r = render(<WitPage />);
  await screen.findByRole("textbox", { name: "向 Wit 提问" });
  return r;
}

test("listeners first, then load: witOpen before load resolves wins (versions decide)", async () => {
  let resolve!: (s: WitPageState) => void;
  vi.mocked(api.load).mockReturnValue(new Promise((r) => (resolve = r)));
  vi.mocked(api.follow).mockImplementation(async (c) => hist(c, `answer ${c}`));
  render(<WitPage />);
  expect(h.open.size).toBe(1);
  expect(h.wit.size).toBe(1);
  open(5, 2);
  resolve(state(3, 1)); // older than what came
  expect(await screen.findByText("answer 5")).toBeInTheDocument();
  expect(api.follow).toHaveBeenCalledTimes(1);
  expect(api.follow).toHaveBeenCalledWith(5);
});

test("load during navigation: a newer witOpen switches, an older one is ignored", async () => {
  vi.mocked(api.follow).mockImplementation(async (c) => hist(c, `answer ${c}`));
  await start(3);
  expect(await screen.findByText("answer 3")).toBeInTheDocument();
  open(4, 2);
  expect(await screen.findByText("answer 4")).toBeInTheDocument();
  open(3, 1);
  open(4, 2);
  expect(api.follow).toHaveBeenCalledTimes(2);
  expect(screen.queryByText("answer 3")).toBeNull();
});

test("switching conversations drops the old stream", async () => {
  vi.mocked(api.ask).mockResolvedValue({ run: 7, conversation: 3, revision: 0, conflict: false });
  await start(3);
  await userEvent.setup().type(input(), "hi{Enter}");
  await waitFor(() => expect(api.ask).toHaveBeenCalledWith(3, "hi", [], "", "", -1));
  emit({ conversation: 3, run: 7, text: "old " });
  expect(await screen.findByText("old")).toBeInTheDocument();
  vi.mocked(api.follow).mockResolvedValueOnce(hist(4, "four"));
  open(4, 2);
  expect(await screen.findByText("four")).toBeInTheDocument();
  emit({ conversation: 3, run: 7, text: "late" }); // the core run goes on: not shown here
  expect(screen.queryByText(/old|late/)).toBeNull();
  expect(api.stop).not.toHaveBeenCalled();
  expect(screen.getByRole("button", { name: "发送给 Wit" })).toBeInTheDocument();
});

test("out-of-order follow results: A's late result is ignored after B", async () => {
  let resolveA!: (x: WitHistory) => void;
  vi.mocked(api.load).mockResolvedValue(state(1, 1));
  vi.mocked(api.follow)
    .mockReturnValueOnce(new Promise((r) => (resolveA = r)))
    .mockResolvedValueOnce(hist(2, "B's"));
  render(<WitPage />);
  await waitFor(() => expect(api.follow).toHaveBeenCalledWith(1));
  open(2, 2);
  expect(await screen.findByText("B's")).toBeInTheDocument();
  await act(async () => resolveA(hist(1, "A's")));
  expect(screen.queryByText("A's")).toBeNull();
  expect(screen.getByText("B's")).toBeInTheDocument();
});

test("first ask creates and keeps the conversation", async () => {
  const user = userEvent.setup();
  type RS = { run: number; conversation: number; revision: number; conflict: boolean };
  let resolve!: (x: RS) => void;
  vi.mocked(api.ask).mockReturnValueOnce(new Promise<RS>((r) => (resolve = r)));
  await start(0);
  expect(api.follow).toHaveBeenCalledWith(0);
  await user.type(input(), "first{Enter}");
  open(7, 2, true); // native took the new conversation (ConversationCreated) before ask returned
  await act(async () => resolve({ run: 9, conversation: 7, revision: 0, conflict: false }));
  emit({ conversation: 7, run: 9, text: "made" });
  emit({ conversation: 7, run: 9, kind: "done" });
  expect(await screen.findByText("made")).toBeInTheDocument();
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "发送给 Wit" })).toBeInTheDocument(),
  );
  // no switch (it would have dropped the answer): 7's only read is the end's reconcile
  await waitFor(() => expect(api.follow).toHaveBeenLastCalledWith(7));
  expect(vi.mocked(api.follow).mock.calls.filter(([c]) => c === 7)).toHaveLength(1);
  await user.type(input(), "second{Enter}");
  await waitFor(() => expect(api.ask).toHaveBeenLastCalledWith(7, "second", [], "", "", -1));
  expect(screen.getByText("made")).toBeInTheDocument();
});

test("ask returning after a switch is ignored", async () => {
  const user = userEvent.setup();
  let resolve!: (x: {
    run: number;
    conversation: number;
    revision: number;
    conflict: boolean;
  }) => void;
  vi.mocked(api.ask).mockReturnValueOnce(new Promise((r) => (resolve = r)));
  await start(0);
  await user.type(input(), "first{Enter}");
  open(4, 2);
  await act(async () => resolve({ run: 9, conversation: 7, revision: 0, conflict: false }));
  await waitFor(() => expect(api.follow).toHaveBeenLastCalledWith(4));
  vi.mocked(api.ask).mockResolvedValue({ run: 10, conversation: 4, revision: 0, conflict: false });
  await user.type(await screen.findByRole("textbox", { name: "向 Wit 提问" }), "next{Enter}");
  await waitFor(() => expect(api.ask).toHaveBeenLastCalledWith(4, "next", [], "", "", -1));
});

test("witState updates the model label and copy without a new follow", async () => {
  await start(3);
  expect(screen.getByRole("button", { name: /m1/ })).toBeInTheDocument();
  send(h.state, state(3, 1, "m2"));
  expect(await screen.findByRole("button", { name: /m2/ })).toBeInTheDocument();
  expect(api.follow).toHaveBeenCalledTimes(1);
});

test("the model chip picks the next question's model; its last item opens settings", async () => {
  const user = userEvent.setup();
  vi.mocked(api.openModels).mockResolvedValue(undefined);
  vi.mocked(api.models).mockResolvedValue([{ ref: "s/m2", service: "S", name: "m2" }]);
  vi.mocked(api.ask).mockResolvedValue({ run: 7, conversation: 3, revision: 0, conflict: false });
  await start(3);
  await user.click(screen.getByRole("button", { name: /m1/ }));
  await user.click(await screen.findByRole("menuitemradio", { name: "m2" }));
  expect(screen.getByRole("button", { name: /m2/ })).toBeInTheDocument();
  await user.type(input(), "hi{Enter}");
  await waitFor(() => expect(api.ask).toHaveBeenCalledWith(3, "hi", [], "s/m2", "", -1));
  await user.click(screen.getByRole("button", { name: /m2/ }));
  await user.click(await screen.findByRole("menuitem", { name: "模型服务设置" }));
  expect(api.openModels).toHaveBeenCalled();
});

test("the model menu's effort submenu picks the next question's reasoning effort", async () => {
  const user = userEvent.setup();
  vi.mocked(api.models).mockResolvedValue([]);
  vi.mocked(api.ask).mockResolvedValue({ run: 7, conversation: 3, revision: 0, conflict: false });
  await start(3);
  await user.click(screen.getByRole("button", { name: /m1/ }));
  await user.click(await screen.findByRole("menuitem", { name: /思考强度/ }));
  // jsdom leaves the submenu inert to the pointer while it opens: pick from the keyboard
  (await screen.findByRole("menuitemradio", { name: "高" })).focus();
  await user.keyboard("{Enter}");
  await waitFor(() => expect(screen.getByRole("button", { name: /m1/ })).toHaveTextContent("高"));
  await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
  await user.type(input(), "hi{Enter}");
  await waitFor(() => expect(api.ask).toHaveBeenCalledWith(3, "hi", [], "", "high", -1));
});

test("cited click opens", async () => {
  vi.mocked(api.openCited).mockResolvedValue(undefined);
  vi.mocked(api.load).mockResolvedValue(state(3, 1));
  vi.mocked(api.follow).mockResolvedValue({
    ...hist(3, "A"),
    messages: [
      msg("q", "user", "Q", [1]),
      {
        ...msg("a", "assistant", "A", [1]),
        citations: [
          {
            n: 1,
            messageId: 51,
            subject: "S",
            from: "f",
            date: 0,
            suspect: false,
            quote: "",
            snippet: "",
            callId: "",
          },
        ],
      },
    ],
  });
  render(<WitPage />);
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "引用了 1 封邮件" }));
  await user.click(screen.getByRole("button", { name: /^1f/ }));
  expect(api.openCited).toHaveBeenCalledWith(51);
});

test("cmd-[ forwards", async () => {
  vi.mocked(api.navigate).mockResolvedValue(undefined);
  await start(0);
  fireEvent.keyDown(input(), { key: "[", metaKey: true });
  expect(api.navigate).toHaveBeenLastCalledWith(-1);
  fireEvent.keyDown(document.body, { key: "]", metaKey: true });
  expect(api.navigate).toHaveBeenLastCalledWith(1);
  fireEvent.keyDown(input(), { key: "[", metaKey: true, isComposing: true });
  fireEvent.keyDown(input(), { key: "[" });
  expect(api.navigate).toHaveBeenCalledTimes(2);
});

test("answer hardening holds through this page; links go straight to WitPage.openLink (native confirms)", async () => {
  const user = userEvent.setup();
  vi.mocked(api.openLink).mockResolvedValue(undefined);
  const md =
    '![x](https://t/x.png)\n\n<script>alert(1)</script>\n\n<img src="x" onerror="alert(1)">\n\n[a](javascript:alert(1)) [b](https://ok.org/p)';
  const { container } = await start(3, md);
  await screen.findByText("b");
  expect(container.querySelector("img, script, a, [onerror]")).toBeNull();
  expect(screen.getByText("a").closest("[role=link]")).toBeNull();
  await user.click(screen.getByText("b"));
  expect(api.openLink).toHaveBeenCalledWith("https://ok.org/p");
  expect(screen.queryByText("打开这个链接？")).toBeNull();
});

test("approvals decide through WitPage.decide", async () => {
  vi.mocked(api.decide).mockResolvedValue(undefined);
  const a = {
    id: 5,
    run: 1,
    callId: "c1",
    tool: "create_draft",
    summary: "",
    state: "pending",
    error: "",
    destinations: [],
    sessionAllow: false,
    args: "{}",
  };
  vi.mocked(api.load).mockResolvedValue(state(3, 1));
  vi.mocked(api.follow).mockResolvedValue({
    conversation: 3,
    model: "",
    effort: "",
    usage: { input: 0, output: 0, context: 0, history: 0 },
    running: 0,
    approvals: [a],
    messages: [
      msg("q", "user", "Q", [1]),
      {
        ...msg("a", "assistant", "", [1]),
        reasoning: "",
        quoted: [],
        calls: [{ id: "c1", name: "create_draft", args: "{}" }],
      },
    ],
  });
  render(<WitPage />);
  await userEvent.setup().click(await screen.findByRole("button", { name: "允许" }));
  expect(api.decide).toHaveBeenCalledWith(5, true);
  await waitFor(() => expect(screen.queryByRole("button", { name: "允许" })).toBeNull());
});

test("navigation while the first question is creating switches at once; the ask's result doesn't pull it back", async () => {
  const user = userEvent.setup();
  type RS = { run: number; conversation: number; revision: number; conflict: boolean };
  let resolve!: (x: RS) => void;
  vi.mocked(api.ask).mockReturnValueOnce(new Promise<RS>((r) => (resolve = r)));
  await start(0);
  await user.type(input(), "first{Enter}");
  vi.mocked(api.follow).mockResolvedValueOnce(hist(4, "four"));
  open(4, 2); // the user picked conversation 4
  expect(await screen.findByText("four")).toBeInTheDocument();
  await act(async () => resolve({ run: 9, conversation: 7, revision: 0, conflict: false }));
  emit({ conversation: 7, run: 9, text: "elsewhere" });
  expect(screen.getByText("four")).toBeInTheDocument();
  expect(screen.queryByText("elsewhere")).toBeNull();
  expect(api.follow).not.toHaveBeenCalledWith(7);
  vi.mocked(api.ask).mockResolvedValue({ run: 10, conversation: 4, revision: 0, conflict: false });
  await user.type(input(), "next{Enter}");
  await waitFor(() => expect(api.ask).toHaveBeenLastCalledWith(4, "next", [], "", "", -1));
});

test("a stale Load does not overwrite a newer witState", async () => {
  let resolve!: (s: WitPageState) => void;
  vi.mocked(api.load).mockReturnValue(new Promise((r) => (resolve = r)));
  render(<WitPage />);
  send(h.state, state(3, 1, "m2")); // settings changed after Load was issued
  await act(async () => resolve(state(3, 1, "m1")));
  expect(await screen.findByRole("button", { name: /m2/ })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /m1/ })).toBeNull();
});

test("Stop before the first ask returns, then navigation: the run is still stopped", async () => {
  const user = userEvent.setup();
  type RS = { run: number; conversation: number; revision: number; conflict: boolean };
  let resolve!: (x: RS) => void;
  vi.mocked(api.ask).mockReturnValueOnce(new Promise<RS>((r) => (resolve = r)));
  vi.mocked(api.stop).mockResolvedValue(undefined);
  await start(0);
  await user.type(input(), "first{Enter}");
  await user.click(await screen.findByRole("button", { name: "停止" }));
  expect(api.stop).not.toHaveBeenCalled();
  vi.mocked(api.follow).mockResolvedValueOnce(hist(4, "four"));
  open(4, 2);
  expect(await screen.findByText("four")).toBeInTheDocument();
  await act(async () => resolve({ run: 9, conversation: 7, revision: 0, conflict: false }));
  await waitFor(() => expect(api.stop).toHaveBeenCalledWith(9));
  expect(screen.getByText("four")).toBeInTheDocument();
});

test("witAsk asks the handed question once, in the new conversation", async () => {
  vi.mocked(api.ask).mockResolvedValue({ run: 9, conversation: 7, revision: 0, conflict: false });
  await start(3);
  vi.mocked(api.follow).mockResolvedValueOnce(hist(0));
  open(0, 2); // native navigates to a new conversation, then hands the question
  send(h.ask, "invoice");
  await waitFor(() => expect(api.ask).toHaveBeenCalledWith(0, "invoice", [], "", "", -1));
  expect(api.ask).toHaveBeenCalledTimes(1);
  expect(await screen.findByText("invoice")).toBeInTheDocument();
  // a later switch remounts the panel: the question is not asked again
  vi.mocked(api.follow).mockResolvedValueOnce(hist(4, "four"));
  open(4, 3);
  expect(await screen.findByText("four")).toBeInTheDocument();
  expect(api.ask).toHaveBeenCalledTimes(1);
});

test("witAsk on the page already showing a new conversation asks at once", async () => {
  vi.mocked(api.ask).mockResolvedValue({ run: 9, conversation: 7, revision: 0, conflict: false });
  await start(0);
  send(h.ask, "where is it");
  await waitFor(() => expect(api.ask).toHaveBeenCalledWith(0, "where is it", [], "", "", -1));
  expect(api.ask).toHaveBeenCalledTimes(1);
});

test("a handed question is dropped when the open fails: not asked in a later conversation", async () => {
  await start(3);
  vi.mocked(api.follow).mockRejectedValueOnce(new Error("boom"));
  open(0, 2);
  send(h.ask, "lost"); // native hands it right after the open, before the open fails
  await waitFor(() => expect(api.follow).toHaveBeenCalledWith(0));
  await act(async () => {});
  vi.mocked(api.follow).mockResolvedValueOnce(hist(4, "four"));
  open(4, 3);
  expect(await screen.findByText("four")).toBeInTheDocument();
  expect(api.ask).not.toHaveBeenCalled();
});
