import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Approval, WitEvent, WitHistory, WitInfo } from "@/mygo";
import { makeT } from "@/shared/i18n";
import { Panel, type PanelReview } from "./Panel";
import { WitTransport } from "./transport";

vi.mock("@/mygo", () => ({
  ComposePage: { openLink: vi.fn(async () => {}) },
  events: { wit: { on: () => () => {} } },
}));

const t = makeT({
  "wit.compose.placeholder": "告诉 Wit 你想怎么回",
  "wit.compose.input": "Wit 输入",
  "wit.compose.adjust": "调整 ▾",
  "wit.compose.translate": "翻译 ▾",
  "wit.compose.translate_to": "翻译为%s",
  "wit.compose.review": "待确认",
  "wit.send": "发送",
  "wit.stop": "停止",
  "common.retry": "重试",
  "error.ai.unavailable": "服务不可用",
  "error.unknown": "出错了：%s",
  "wit.tools.one": "调用了 %d 个工具",
  "wit.tools.other": "调用了 %d 个工具",
  "wit.suspect": "来源 %s 中包含疑似指令。",
  "mail.list_sep": "、",
  "wit.tool.update_draft": "修改正文",
});

const info: WitInfo = {
  enabled: true,
  skills: [
    { id: "tone-formal", label: "更正式" },
    { id: "shorter", label: "更短" },
  ],
  languages: [
    { tag: "zh-CN", name: "简体中文" },
    { tag: "en", name: "English" },
  ],
  chips: ["原邮件", "a.pdf"],
};

const empty: WitHistory = { conversation: 1, running: 0, messages: [], approvals: [] };

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

async function setup(
  o: {
    history?: WitHistory;
    locked?: boolean;
    review?: PanelReview;
    onRunning?(on: boolean): void;
  } = {},
) {
  let listener: ((e: WitEvent) => void) | undefined;
  const api = {
    ask: vi.fn().mockResolvedValue(7),
    skill: vi.fn().mockResolvedValue(7),
    follow: vi.fn(),
    stop: vi.fn().mockResolvedValue(undefined),
  };
  const tr = new WitTransport(api, (f) => {
    listener = f;
    return () => {};
  });
  api.follow.mockResolvedValue(o.history ?? empty);
  await tr.follow(); // as the page does: the history read through this transport
  const ui = (resume?: { run: number }) => (
    <Panel
      info={info}
      history={o.history ?? empty}
      locked={o.locked ?? false}
      review={o.review}
      resume={resume}
      onRunning={o.onRunning}
      transport={tr}
      t={t}
    />
  );
  const r = render(ui());
  const emit = (e: Partial<WitEvent>) => act(() => listener?.(ev(e)));
  return { ...r, api, tr, emit, resume: (run: number) => r.rerender(ui({ run })) };
}

const input = () => screen.getByRole("textbox", { name: "Wit 输入" });

beforeEach(() => vi.clearAllMocks());

describe("Panel", () => {
  it("shows the chips", async () => {
    await setup();
    expect(screen.getByText("原邮件")).toBeInTheDocument();
    expect(screen.getByText("a.pdf")).toBeInTheDocument();
    expect(input()).toHaveAttribute("placeholder", "告诉 Wit 你想怎么回");
  });

  it("sends with Enter, not with Shift+Enter or during composition", async () => {
    const user = userEvent.setup();
    const { api } = await setup();
    await user.type(input(), "hello");
    await user.keyboard("{Shift>}{Enter}{/Shift}");
    expect(api.ask).not.toHaveBeenCalled();
    fireEvent.keyDown(input(), { key: "Enter", isComposing: true });
    expect(api.ask).not.toHaveBeenCalled();
    await user.keyboard("{Enter}");
    await waitFor(() => expect(api.ask).toHaveBeenCalledWith("hello"));
    expect(screen.getByText("hello")).toBeInTheDocument(); // the question's bubble
    expect(input()).toHaveValue("");
  });

  it("streams an answer; Stop calls api.stop(run) while running", async () => {
    const user = userEvent.setup();
    const { api, emit } = await setup();
    await user.type(input(), "hi{Enter}");
    await waitFor(() => expect(api.ask).toHaveBeenCalled());
    emit({ kind: "text", text: "Hel" });
    emit({ kind: "text", text: "lo" });
    expect(await screen.findByText("Hello")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "停止" }));
    expect(api.stop).toHaveBeenCalledWith(7);
    emit({ kind: "error", error: "" }); // the stop: no failure
    expect(await screen.findByRole("button", { name: "发送" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "重试" })).toBeNull();
  });

  it("an error shows 重试, which repeats the last skill", async () => {
    const user = userEvent.setup();
    const { api, emit } = await setup();
    await user.click(screen.getByRole("button", { name: "调整 ▾" }));
    await user.click(await screen.findByRole("menuitem", { name: "更正式" }));
    await waitFor(() => expect(api.skill).toHaveBeenCalledWith("tone-formal", ""));
    emit({ kind: "error", error: "error.ai.unavailable" });
    expect(await screen.findByText("服务不可用")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "重试" }));
    await waitFor(() => expect(api.skill).toHaveBeenCalledTimes(2));
    expect(api.skill).toHaveBeenLastCalledWith("tone-formal", "");
    expect(screen.getAllByText("更正式")).toHaveLength(1); // the question once
  });

  it("retry repeats the last text; a refused start shows its error", async () => {
    const user = userEvent.setup();
    const { api } = await setup();
    api.ask.mockRejectedValueOnce(new Error("error.ai.unavailable"));
    await user.type(input(), "hi{Enter}");
    await user.click(await screen.findByRole("button", { name: "重试" }));
    await waitFor(() => expect(api.ask).toHaveBeenCalledTimes(2));
    expect(api.ask).toHaveBeenLastCalledWith("hi");
  });

  it("translates to a language from the 翻译 menu", async () => {
    const user = userEvent.setup();
    const { api } = await setup();
    await user.click(screen.getByRole("button", { name: "翻译 ▾" }));
    await user.click(await screen.findByRole("menuitem", { name: "English" }));
    await waitFor(() => expect(api.skill).toHaveBeenCalledWith("translate-draft", "en"));
    expect(screen.getByText("翻译为English")).toBeInTheDocument();
  });

  it("locked: the menus and Send are disabled", async () => {
    await setup({ locked: true });
    expect(screen.getByRole("button", { name: "调整 ▾" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "翻译 ▾" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "发送" })).toBeDisabled();
  });

  it("folds tool calls, numbers citations after the history's, notes suspect ones", async () => {
    const user = userEvent.setup();
    const cite = (n: number, id: number, suspect = false) => ({
      n,
      messageId: id,
      subject: `S${id}`,
      from: "f",
      date: 0,
      suspect,
    });
    const { emit } = await setup({
      history: {
        ...empty,
        messages: [
          { id: "q0", role: "user", text: "Q", calls: [], results: [], citations: [], runs: [7] },
          {
            id: "a1",
            role: "assistant",
            text: "A",
            calls: [
              { id: "c1", name: "search", args: "{}" },
              { id: "c2", name: "search", args: "{}" },
              { id: "c3", name: "read", args: "{}" },
            ],
            results: [],
            citations: [cite(1, 50), cite(2, 51, true)],
            runs: [7],
          },
        ],
      },
    });
    expect(screen.getByText("调用了 2 个工具 · search ×2、read")).toBeInTheDocument();
    expect(screen.getByText("来源 [2] 中包含疑似指令。")).toBeInTheDocument();
    await user.type(input(), "more{Enter}");
    emit({ kind: "context", citations: [cite(0, 51), cite(0, 52, true)] });
    emit({ kind: "text", text: "B" });
    expect(await screen.findByText("来源 [3] 中包含疑似指令。")).toBeInTheDocument();
    expect(screen.getAllByText("来源 [2] 中包含疑似指令。")).toHaveLength(2);
  });

  it("the reviewed edit shows a card with the page's summary and decisions", async () => {
    const a = {
      id: 4,
      run: 7,
      callId: "skill",
      tool: "update_draft",
      summary: "partial",
      state: "pending",
      error: "",
      destinations: [],
    };
    const history = (approvals: Approval[]): WitHistory => ({
      ...empty,
      messages: [
        {
          id: "q0",
          role: "user",
          text: "更短",
          calls: [],
          results: [],
          citations: [],
          runs: [7],
        },
      ],
      approvals,
    });
    const { unmount } = await setup({
      history: history([a]),
      review: { approval: 4, body: <button type="button">全部接受</button> },
    });
    expect(screen.getByText("待确认")).toBeInTheDocument();
    expect(screen.getByText("修改正文")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "全部接受" })).toBeInTheDocument();
    unmount();
    await setup({ history: history([a]) }); // not the one reviewed: no card
    expect(screen.queryByText("待确认")).toBeNull();
  });

  it("a resume streams the decided run onto the last answer; onRunning follows", async () => {
    const onRunning = vi.fn();
    const { emit, resume } = await setup({ onRunning });
    await userEvent.setup().type(input(), "改短{Enter}");
    emit({ run: 7, kind: "text", text: "好的" });
    emit({ run: 7, kind: "interrupted", approvals: [] });
    await waitFor(() => expect(onRunning).toHaveBeenLastCalledWith(false));
    expect(onRunning).toHaveBeenCalledWith(true);
    emit({ run: 7, kind: "text", text: "，已改。" }); // logged: no stream yet
    resume(7);
    await waitFor(() => expect(onRunning).toHaveBeenLastCalledWith(true));
    emit({ run: 7, kind: "done" });
    await waitFor(() => expect(onRunning).toHaveBeenLastCalledWith(false));
    expect(screen.getAllByText(/好的/)).toHaveLength(1);
    expect(screen.getByText(/已改/)).toBeInTheDocument();
    expect(screen.getAllByText("改短")).toHaveLength(1);
  });

  it("reconnects to a live run: deltas append to the open answer", async () => {
    const { api, emit } = await setup({
      history: {
        ...empty,
        running: 7,
        messages: [
          { id: "q0", role: "user", text: "Q", calls: [], results: [], citations: [], runs: [7] },
          {
            id: "a1",
            role: "assistant",
            text: "Hello",
            calls: [],
            results: [],
            citations: [],
            runs: [7],
          },
        ],
      },
    });
    expect(await screen.findByRole("button", { name: "停止" })).toBeInTheDocument();
    emit({ kind: "text", text: " world" });
    expect(await screen.findByText("Hello world")).toBeInTheDocument();
    expect(document.querySelectorAll("[data-role=assistant]")).toHaveLength(1);
    await userEvent.setup().click(screen.getByRole("button", { name: "停止" }));
    expect(api.stop).toHaveBeenCalledWith(7);
  });
});
