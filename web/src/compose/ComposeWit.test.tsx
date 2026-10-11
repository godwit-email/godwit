import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Approval, BodyChange, ComposeState, Review, WitEvent, WitHistory } from "@/mygo";
import { ComposePage } from "./ComposePage";

const h = vi.hoisted(() => ({ wit: new Set<(e: WitEvent) => void>() }));
vi.mock("@/mygo", () => ({
  ComposePage: {
    load: vi.fn(),
    save: vi.fn(),
    closed: vi.fn(),
    latest: vi.fn(),
    follow: vi.fn(),
    ask: vi.fn(),
    skill: vi.fn(),
    stop: vi.fn(),
    review: vi.fn(),
    decide: vi.fn(),
    finish: vi.fn(),
    undo: vi.fn(),
    rewrite: vi.fn(),
    openLink: vi.fn(),
    send: vi.fn(),
  },
  events: {
    closing: { on: () => () => {} },
    theme: { on: () => () => {} },
    wit: {
      on: (f: (e: WitEvent) => void) => {
        h.wit.add(f);
        return () => h.wit.delete(f);
      },
    },
  },
}));
vi.mock("mygo-runtime", () => ({ currentWindow: { setTitle: vi.fn() } }));
vi.mock("sonner", async (orig) => ({
  ...(await orig<typeof import("sonner")>()),
  toast: Object.assign(vi.fn(), { error: vi.fn() }),
}));
// The body as a textarea that reads `initial` at mount; ⌘K on a selection gives its plain text
// (the textarea's value) and the selection, as the Plate editor does (its own tests cover toText).
vi.mock("./editor/Editor", async () => {
  const { useState } = await import("react");
  return {
    Editor(p: {
      initial: string;
      readOnly: boolean;
      onChange(html: string): void;
      onCmdK?(s: unknown): void;
      t(k: string): string;
    }) {
      const [v, setV] = useState(p.initial);
      return (
        <textarea
          aria-label={p.t("compose.body")}
          data-cmdk={!!p.onCmdK}
          value={v}
          readOnly={p.readOnly}
          onChange={(e) => {
            if (p.readOnly) return;
            setV(e.target.value);
            p.onChange(e.target.value);
          }}
          onKeyDown={(e) => {
            const el = e.currentTarget;
            if (e.key !== "k" || !e.metaKey || !p.onCmdK || el.selectionStart === el.selectionEnd)
              return;
            p.onCmdK({
              text: el.value,
              start: el.selectionStart,
              end: el.selectionEnd,
              at: { left: 0, top: 0 },
            });
          }}
        />
      );
    },
  };
});

import { toast } from "sonner";
import { ComposePage as api } from "@/mygo";

const text: Record<string, string> = {
  "compose.body": "正文",
  "compose.subject": "主题",
  "compose.sending.locked": "正在发送，暂时不能修改草稿",
  "error.unknown": "出错了：%s",
  "common.retry": "重试",
  "wit.send": "发送",
  "wit.stop": "停止",
  "wit.compose.input": "告诉 Wit 你想怎么回",
  "wit.compose.writing_banner": "助手正在写入正文，暂时不能编辑",
  "wit.compose.writing": "Wit 正在写…",
  "wit.compose.locked": "先接受或拒绝助手的修改",
  "wit.compose.pending.one": "%d 处修改待确认",
  "wit.compose.pending.other": "%d 处修改待确认",
  "wit.compose.accept_all": "全部接受",
  "wit.compose.reject_all": "全部拒绝",
  "wit.compose.accept": "接受",
  "wit.compose.reject": "拒绝",
  "wit.compose.link": "链接改为 %s",
  "wit.compose.rewrite": "已整体重写，改动约 %d%%",
  "wit.compose.rewrite_plain": "已整体重写",
  "wit.compose.compare": "对比原文",
  "wit.compose.back": "返回新版本",
  "wit.compose.original": "原文",
  "wit.compose.new": "新版本",
  "wit.compose.review": "待确认",
  "wit.compose.review.inline.other": "修改 %d 处。鼠标停在某处修改上，可以单独接受或拒绝。",
  "wit.compose.review.rewrite": "整体重写，改动约 %d%%。",
  "wit.compose.review_body": "正文修订",
  "wit.compose.applied": "已应用到正文",
  "wit.compose.undo": "撤销",
  "wit.compose.cmdk": "⌘K · 只修改选中的这一段",
  "wit.compose.cmdk.input": "修改选中的内容",
  "wit.compose.cmdk.esc": "Esc",
  "wit.compose.cmdk.go": "生成 ↵",
  "wit.tool.update_draft": "修改正文",
};
const state: ComposeState = {
  draftId: 1,
  accounts: [{ id: 1, email: "me@x.com" }],
  from: 1,
  to: ["a@x.com"],
  cc: [],
  bcc: [],
  replyTo: "",
  subject: "你好",
  body: "正文",
  quote: "",
  signature: "",
  attachments: [],
  revision: 7,
  updatedAt: 0,
  theme: { mode: "light", light: {}, dark: {}, fontSize: 14 },
  text,
  wit: { enabled: true, skills: [], languages: [], chips: [] },
} as unknown as ComposeState;
const empty: WitHistory = { conversation: 1, running: 0, messages: [], approvals: [] };
const pending: Approval = {
  id: 4,
  run: 7,
  callId: "c1",
  tool: "update_draft",
  summary: "partial",
  state: "pending",
  error: "",
  destinations: [],
};
const withPending: WitHistory = {
  conversation: 1,
  running: 0,
  messages: [
    { id: "q0", role: "user", text: "改", calls: [], results: [], citations: [], runs: [7] },
    {
      id: "a1",
      role: "assistant",
      text: "",
      calls: [{ id: "c1", name: "update_draft", args: "{}" }],
      results: [],
      citations: [],
      runs: [7],
    },
  ],
  approvals: [pending],
};
const op = (kind: string, change: number, s: string) => ({
  kind,
  change,
  tokens: [{ text: s, bold: false, italic: false, href: "" }],
  link: "",
});
const inline: Review = {
  approval: 4,
  tool: "update_draft",
  inline: true,
  ratio: 0.2,
  exact: true,
  changes: 2,
  blocks: [
    {
      kind: "paragraph",
      level: 0,
      ordered: false,
      added: false,
      removed: false,
      whole: -1,
      ops: [
        op("same", -1, "Hi "),
        op("del", 0, "Bob"),
        op("ins", 0, "Ann"),
        op("same", -1, " and "),
        op("ins", 1, "more"),
      ],
    },
  ],
  old: "<p>Hi Bob and</p>",
  new: "<p>Hi Ann and more</p>",
};
const change = (body: string, revision: number): BodyChange => ({
  revision,
  body,
  conflict: false,
  attachments: [],
  signature: "",
  updatedAt: 0,
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
const emit = (e: Partial<WitEvent>) =>
  act(() => {
    for (const f of [...h.wit]) f(ev(e));
  });
const latest = (body: string, revision: number) => ({ ...state, body, revision });

beforeEach(() => {
  vi.resetAllMocks();
  h.wit.clear();
  vi.mocked(api.load).mockResolvedValue(state);
  vi.mocked(api.save).mockResolvedValue({ revision: 8, conflict: false });
  vi.mocked(api.follow).mockResolvedValue(empty);
  vi.mocked(api.review).mockResolvedValue(inline);
  vi.mocked(api.stop).mockResolvedValue();
});

const body = () => screen.findByRole("textbox", { name: "正文" });
const setup = async (history = empty) => {
  vi.mocked(api.follow).mockResolvedValue(history);
  render(<ComposePage />);
  await screen.findByRole("textbox", { name: "告诉 Wit 你想怎么回" });
};

test("inline review: accept one, reject one → finish([1,-1]); the body takes the result", async () => {
  vi.mocked(api.finish).mockResolvedValue(change("<p>Hi Ann and</p>", 9));
  vi.mocked(api.latest).mockResolvedValue(latest("<p>Hi Ann and</p>", 9));
  await setup(withPending);
  const region = await screen.findByRole("region", { name: "正文修订" });
  expect(screen.getByText("2 处修改待确认")).toBeInTheDocument();
  expect(region.querySelector("del")).toHaveTextContent("Bob");
  expect(
    screen.getByText("修改 2 处。鼠标停在某处修改上，可以单独接受或拒绝。"),
  ).toBeInTheDocument();
  const changes = region.querySelectorAll("[data-change]");
  fireEvent.mouseEnter(changes[0]);
  fireEvent.click(screen.getByRole("button", { name: "接受" }));
  expect(screen.getByText("1 处修改待确认")).toBeInTheDocument();
  act(() => (changes[1] as HTMLElement).focus()); // the caret's stand-in
  fireEvent.click(screen.getByRole("button", { name: "拒绝" }));
  await waitFor(() => expect(api.finish).toHaveBeenCalledWith(4, [1, -1]));
  expect(await body()).toHaveValue("<p>Hi Ann and</p>");
  expect(screen.queryByRole("region", { name: "正文修订" })).toBeNull();
  // a mixed decision changed the body: undo offers it back
  expect(await screen.findByRole("button", { name: "撤销" })).toBeInTheDocument();
});

test("accept all → decide(id, true)", async () => {
  vi.mocked(api.decide).mockResolvedValue(change("<p>Hi Ann and more</p>", 9));
  vi.mocked(api.latest).mockResolvedValue(latest("<p>Hi Ann and more</p>", 9));
  await setup(withPending);
  await screen.findByRole("region", { name: "正文修订" });
  // the banner's and the panel card's
  expect(screen.getAllByRole("button", { name: "全部接受" })).toHaveLength(2);
  fireEvent.click(screen.getAllByRole("button", { name: "全部接受" })[1]);
  await waitFor(() => expect(api.decide).toHaveBeenCalledWith(4, true));
  expect(await body()).toHaveValue("<p>Hi Ann and more</p>");
  expect(api.finish).not.toHaveBeenCalled();
  // the run the decision resumed streams in the panel, on its answer
  emit({ kind: "text", text: "已改好。" });
  emit({ kind: "done" });
  expect(await screen.findByText("已改好。")).toBeInTheDocument();
  expect(await body()).toHaveValue("<p>Hi Ann and more</p>");
  expect(await screen.findByText("已应用到正文")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "撤销" })).toBeInTheDocument();
});

test("reject all: the body is kept, and there is nothing to undo", async () => {
  vi.mocked(api.decide).mockResolvedValue(change("正文", 9));
  vi.mocked(api.latest).mockResolvedValue(latest("正文", 9));
  await setup(withPending);
  await screen.findByRole("region", { name: "正文修订" });
  fireEvent.click(screen.getAllByRole("button", { name: "全部拒绝" })[0]);
  await waitFor(() => expect(api.decide).toHaveBeenCalledWith(4, false));
  expect(await body()).toHaveValue("正文");
  await act(async () => {});
  expect(screen.queryByText("已应用到正文")).toBeNull();
});

test("a subject edited and saved during a run does not keep Wit's body from reloading", async () => {
  vi.mocked(api.ask).mockResolvedValue(7);
  await setup();
  await userEvent.type(screen.getByRole("textbox", { name: "告诉 Wit 你想怎么回" }), "写{Enter}");
  await waitFor(async () => expect(await body()).toHaveAttribute("readonly"));
  await userEvent.type(screen.getByRole("textbox", { name: "主题" }), "！");
  await waitFor(() => expect(api.save).toHaveBeenCalled(), { timeout: 2000 });
  vi.mocked(api.latest).mockResolvedValue(latest("Wit 写的", 10));
  emit({ kind: "done" });
  expect(await screen.findByText("已应用到正文")).toBeInTheDocument();
  expect(await body()).toHaveValue("Wit 写的");
});

test("typing while sending toasts once", async () => {
  vi.mocked(api.send).mockReturnValue(new Promise(() => {}));
  await setup();
  const b = await body();
  fireEvent.keyDown(b, { key: "Enter", metaKey: true });
  await waitFor(() => expect(api.send).toHaveBeenCalled());
  for (const key of ["a", "b", "c"]) fireEvent.keyDown(b, { key });
  expect(
    vi.mocked(toast).mock.calls.filter(([m]) => m === "正在发送，暂时不能修改草稿"),
  ).toHaveLength(1);
});

test("opening ⌘K clears the applied note", async () => {
  vi.mocked(api.latest).mockResolvedValue(latest("Wit 写的", 9));
  await setup();
  emit({ kind: "done" });
  expect(await screen.findByText("已应用到正文")).toBeInTheDocument();
  const b = (await body()) as HTMLTextAreaElement;
  b.setSelectionRange(0, 3);
  fireEvent.keyDown(b, { key: "k", metaKey: true });
  expect(screen.getByText("⌘K · 只修改选中的这一段")).toBeInTheDocument();
  expect(screen.queryByText("已应用到正文")).toBeNull();
});

test("rewrite: the new version, compare toggles to 原文 | 新版本", async () => {
  vi.mocked(api.review).mockResolvedValue({ ...inline, inline: false, ratio: 0.62, blocks: [] });
  await setup(withPending);
  expect(await screen.findByText("已整体重写，改动约 62%")).toBeInTheDocument();
  expect(screen.getByText("整体重写，改动约 62%。")).toBeInTheDocument(); // the panel card
  expect(screen.getByText("Hi Ann and more")).toBeInTheDocument();
  expect(screen.queryByRole("region", { name: "原文" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "对比原文" }));
  expect(screen.getByRole("region", { name: "原文" })).toHaveTextContent("Hi Bob and");
  expect(screen.getByRole("region", { name: "新版本" })).toHaveTextContent("Hi Ann and more");
  fireEvent.click(screen.getByRole("button", { name: "返回新版本" }));
  expect(screen.queryByRole("region", { name: "原文" })).toBeNull();
});

test("locked while running: typing in the body toasts once", async () => {
  vi.mocked(api.ask).mockResolvedValue(7);
  await setup();
  await userEvent.type(screen.getByRole("textbox", { name: "告诉 Wit 你想怎么回" }), "改短{Enter}");
  await waitFor(() => expect(api.ask).toHaveBeenCalledWith("改短"));
  expect(await screen.findByText("助手正在写入正文，暂时不能编辑")).toBeInTheDocument();
  const b = await body();
  expect(b).toHaveAttribute("readonly");
  fireEvent.keyDown(b, { key: "a" });
  fireEvent.keyDown(b, { key: "b" });
  expect(vi.mocked(toast).mock.calls.filter(([m]) => m === "先接受或拒绝助手的修改")).toHaveLength(
    1,
  );
  vi.mocked(api.latest).mockResolvedValue(latest("正文", 7));
  emit({ kind: "done" });
  await waitFor(() => expect(b).not.toHaveAttribute("readonly"));
});

test("a run on a blank body shows Wit writing", async () => {
  vi.mocked(api.load).mockResolvedValue({ ...state, body: "<p><br></p>" });
  vi.mocked(api.ask).mockResolvedValue(7);
  await setup();
  await userEvent.type(screen.getByRole("textbox", { name: "告诉 Wit 你想怎么回" }), "写{Enter}");
  expect(await screen.findByText("Wit 正在写…")).toBeInTheDocument();
  expect(screen.queryByRole("textbox", { name: "正文" })).toBeNull();
});

test("reload skips a local edit", async () => {
  vi.mocked(api.latest).mockResolvedValue(latest("Wit 写的", 9));
  await setup();
  const b = await body();
  await userEvent.type(b, "！"); // not saved yet
  emit({ kind: "done" });
  await waitFor(() => expect(api.latest).toHaveBeenCalled());
  await act(async () => {});
  expect(b).toHaveValue("正文！");
  expect(screen.queryByText("已应用到正文")).toBeNull();
});

test("reload: the newest read wins", async () => {
  const reads: ((s: ComposeState) => void)[] = [];
  vi.mocked(api.latest).mockImplementation(() => new Promise((r) => reads.push(r)));
  await setup();
  emit({ kind: "done" });
  emit({ run: 8, kind: "done" });
  await waitFor(() => expect(reads).toHaveLength(2));
  await act(async () => reads[1](latest("新的", 9)));
  await act(async () => reads[0](latest("旧的", 11))); // an older read, answered later
  expect(await body()).toHaveValue("新的");
});

test("a failed reload offers 重试", async () => {
  vi.mocked(api.latest).mockRejectedValueOnce(new Error("x"));
  await setup();
  emit({ kind: "done" });
  vi.mocked(api.latest).mockResolvedValue(latest("Wit 写的", 9));
  fireEvent.click(await screen.findByRole("button", { name: "重试" }));
  expect(await screen.findByText("已应用到正文")).toBeInTheDocument();
});

test("applied, then undo adopts the body; no reload overwrites it", async () => {
  vi.mocked(api.latest).mockResolvedValue(latest("Wit 写的", 9));
  vi.mocked(api.undo).mockResolvedValue(change("正文", 10));
  await setup();
  emit({ kind: "done" });
  expect(await screen.findByText("已应用到正文")).toBeInTheDocument();
  expect(await body()).toHaveValue("Wit 写的");
  fireEvent.click(screen.getByRole("button", { name: "撤销" }));
  await waitFor(() => expect(api.undo).toHaveBeenCalled());
  await waitFor(async () => expect(await body()).toHaveValue("正文"));
  expect(screen.queryByText("已应用到正文")).toBeNull();
  emit({ kind: "done" }); // a later read of the draft before the undo
  await waitFor(() => expect(api.latest).toHaveBeenCalledTimes(2));
  await act(async () => {});
  expect(await body()).toHaveValue("正文");
});

test("⌘K saves first, then rewrites the selection of the text saved", async () => {
  vi.mocked(api.rewrite).mockResolvedValue({ run: 9, revision: 8, conflict: false });
  await setup();
  const b = (await body()) as HTMLTextAreaElement;
  await userEvent.clear(b);
  await userEvent.type(b, "Hello world");
  b.setSelectionRange(6, 11);
  fireEvent.keyDown(b, { key: "k", metaKey: true });
  expect(screen.getByText("⌘K · 只修改选中的这一段")).toBeInTheDocument();
  await userEvent.type(screen.getByRole("textbox", { name: "修改选中的内容" }), "更正式{Enter}");
  await waitFor(() => expect(api.rewrite).toHaveBeenCalled());
  const [rev, txt, start, end, instr] = vi.mocked(api.rewrite).mock.calls[0];
  expect(vi.mocked(api.save).mock.invocationCallOrder[0]).toBeLessThan(
    vi.mocked(api.rewrite).mock.invocationCallOrder[0],
  );
  expect(vi.mocked(api.save).mock.calls.at(-1)?.[0].body).toBe("Hello world");
  expect([rev, instr]).toEqual([8, "更正式"]);
  expect(txt.slice(start, end)).toBe("world");
  await waitFor(() => expect(b).toHaveAttribute("readonly")); // the ⌘K run locks the body
  vi.mocked(api.latest).mockResolvedValue(latest("Hello world", 8));
  emit({ run: 9, conversation: 3, kind: "done" });
  await waitFor(() => expect(b).not.toHaveAttribute("readonly"));
  expect(api.follow).toHaveBeenCalledTimes(2); // the panel follows the draft's conversation again
});

test("after a ⌘K run, free input in the panel runs Ask", async () => {
  vi.mocked(api.rewrite).mockResolvedValue({ run: 9, revision: 8, conflict: false });
  vi.mocked(api.ask).mockResolvedValue(10);
  await setup();
  const b = (await body()) as HTMLTextAreaElement;
  await userEvent.clear(b);
  await userEvent.type(b, "Hello world");
  b.setSelectionRange(6, 11);
  fireEvent.keyDown(b, { key: "k", metaKey: true });
  await userEvent.type(screen.getByRole("textbox", { name: "修改选中的内容" }), "更正式{Enter}");
  await waitFor(() => expect(api.rewrite).toHaveBeenCalledTimes(1));
  vi.mocked(api.latest).mockResolvedValue(latest("Hello world", 8));
  emit({ run: 9, conversation: 3, kind: "done" });
  await waitFor(() => expect(b).not.toHaveAttribute("readonly"));
  await userEvent.type(screen.getByRole("textbox", { name: "告诉 Wit 你想怎么回" }), "更正式{Enter}");
  await waitFor(() => expect(api.ask).toHaveBeenCalledWith("更正式"));
  expect(api.rewrite).toHaveBeenCalledTimes(1);
});

test("⌘K goes to Wit only with a model and unlocked; else the link popover", async () => {
  await setup();
  expect(await body()).toHaveAttribute("data-cmdk", "true");
  vi.mocked(api.ask).mockResolvedValue(7);
  await userEvent.type(screen.getByRole("textbox", { name: "告诉 Wit 你想怎么回" }), "x{Enter}");
  await waitFor(async () => expect(await body()).toHaveAttribute("data-cmdk", "false"));
});

test("⌘K without a model keeps the link popover", async () => {
  vi.mocked(api.load).mockResolvedValue({ ...state, wit: { ...state.wit, enabled: false } });
  render(<ComposePage />);
  expect(await body()).toHaveAttribute("data-cmdk", "false");
});
