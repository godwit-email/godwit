import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Approval, WitEvent, WitHistory, WitInfo } from "@/mygo";
import { makeT } from "@/shared/i18n";
import { Panel, type PanelReview } from "./Panel";
import { WitTransport } from "./transport";

vi.mock("@/mygo", () => ({ events: { wit: { on: () => () => {} } } }));

const t = makeT({
  "wit.compose.placeholder": "告诉 Wit 你想怎么回",
  "wit.compose.input": "Wit 输入",
  "wit.compose.adjust": "调整",
  "wit.compose.translate": "翻译",
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
  "wit.tool.create_draft": "创建草稿",
  "wit.approval.wants": "Wit 想要：%s",
  "wit.approval.to": "收件人",
  "wit.approval.cc": "抄送",
  "wit.approval.subject": "主题",
  "wit.approval.destinations": "会把数据发送到：%s",
  "wit.approval.allow": "允许",
  "wit.approval.allow_all": "全部允许",
  "wit.approval.reject": "拒绝",
  "wit.approval.send_note": "发送仍需你在撰写页亲自点击",
  "wit.input": "向 Wit 提问",
  "ask.citations": "引用的邮件",
  "ask.citation.suspect": "含疑似指令，已作为数据处理",
  "mail.no_subject": "（无主题）",
  "link.title": "打开这个链接？",
  "link.open": "打开",
});

const info: WitInfo = {
  enabled: true,
  skills: [
    { id: "tone-formal", label: "更正式", hint: "语气" },
    { id: "shorter", label: "更短", hint: "长度" },
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
    onDecide?(a: Approval, accept: boolean, all: boolean): Promise<boolean>;
    onCite?(id: number): void;
    page?: boolean;
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
  const onLink = vi.fn().mockResolvedValue(undefined);
  await tr.follow(); // as the page does: the history read through this transport
  const ui = (resume?: { run: number }) => (
    <Panel
      info={info}
      history={o.history ?? empty}
      locked={o.locked ?? false}
      review={o.review}
      resume={resume}
      onRunning={o.onRunning}
      onDecide={o.onDecide}
      onLink={onLink}
      onCite={o.onCite}
      layout={o.page ? "page" : "panel"}
      transport={tr}
      t={t}
    />
  );
  const r = render(ui());
  const emit = (e: Partial<WitEvent>) => act(() => listener?.(ev(e)));
  return { ...r, api, tr, emit, onLink, resume: (run: number) => r.rerender(ui({ run })) };
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

  it("Send is an icon button named 发送; Stop is too while running", async () => {
    const user = userEvent.setup();
    const { api } = await setup();
    const send = screen.getByRole("button", { name: "发送" });
    expect(send).toHaveAttribute("aria-label", "发送");
    expect(send).toHaveTextContent("");
    await user.type(input(), "hi{Enter}");
    await waitFor(() => expect(api.ask).toHaveBeenCalled());
    const stop = await screen.findByRole("button", { name: "停止" });
    expect(stop).toHaveAttribute("aria-label", "停止");
    expect(stop).toHaveTextContent("");
  });

  it("the compose layout's 调整 / 翻译 tray sits right above the input; the page has none", async () => {
    const { unmount } = await setup();
    const tray = screen.getByRole("button", { name: "调整" }).closest("[data-tray]");
    expect(tray).not.toBeNull();
    expect(tray?.nextElementSibling).toContainElement(input());
    unmount();
    await setup({ page: true });
    expect(screen.queryByRole("button", { name: "调整" })).toBeNull();
  });

  it("the 调整 menu shows each skill's hint", async () => {
    const user = userEvent.setup();
    await setup();
    await user.click(screen.getByRole("button", { name: "调整" }));
    const item = await screen.findByRole("menuitem", { name: /更正式/ });
    expect(item).toHaveTextContent("语气");
    expect(screen.getByRole("menuitem", { name: /更短/ })).toHaveTextContent("长度");
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
    await user.click(screen.getByRole("button", { name: "调整" }));
    await user.click(await screen.findByRole("menuitem", { name: /更正式/ }));
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
    await user.click(screen.getByRole("button", { name: "翻译" }));
    await user.click(await screen.findByRole("menuitem", { name: "English" }));
    await waitFor(() => expect(api.skill).toHaveBeenCalledWith("translate-draft", "en"));
    expect(screen.getByText("翻译为English")).toBeInTheDocument();
  });

  it("locked: the menus and Send are disabled", async () => {
    await setup({ locked: true });
    expect(screen.getByRole("button", { name: "调整" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "翻译" })).toBeDisabled();
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
      args: "",
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
    expect(screen.getByText("update_draft")).toHaveClass("font-mono", "text-xs"); // the tool, as the design names it
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

  it("a tool's approval shows its card: what, arguments, destinations; Allow / Allow all / Reject", async () => {
    const user = userEvent.setup();
    const tool = (id: number, callId: string, tool: string, args: string): Approval => ({
      id,
      run: 7,
      callId,
      tool,
      summary: `Run ${tool}`,
      state: "pending",
      error: "",
      destinations: ["api.x.com", "cdn.x.com"],
      args,
    });
    const a = tool(
      5,
      "p1",
      "plugin_netty_ping",
      '{"to":["a@x.com"],"cc":"b@x.com","subject":"Hi","html":"<p>Body <b>text</b></p>"}',
    );
    const b = tool(6, "p2", "create_draft", "{}");
    const onDecide = vi.fn().mockResolvedValue(true);
    await setup({
      onDecide,
      history: {
        ...empty,
        messages: [
          { id: "q0", role: "user", text: "Q", calls: [], results: [], citations: [], runs: [7] },
          {
            id: "a1",
            role: "assistant",
            text: "",
            calls: [
              { id: "p1", name: "plugin_netty_ping", args: a.args },
              { id: "p2", name: "create_draft", args: "{}" },
            ],
            results: [],
            citations: [],
            runs: [7],
          },
        ],
        approvals: [a, b],
      },
    });
    expect(screen.getByText("Wit 想要：plugin_netty_ping")).toBeInTheDocument();
    expect(screen.getByText("Wit 想要：创建草稿")).toBeInTheDocument();
    expect(screen.getByText("Run plugin_netty_ping")).toBeInTheDocument();
    expect(screen.getByText("a@x.com")).toBeInTheDocument();
    expect(screen.getByText("b@x.com")).toBeInTheDocument();
    expect(screen.getByText("Hi")).toBeInTheDocument();
    expect(screen.getByText("Body text")).toBeInTheDocument();
    expect(screen.getAllByText("会把数据发送到：api.x.com、cdn.x.com")).toHaveLength(2);
    expect(screen.getByText("发送仍需你在撰写页亲自点击")).toBeInTheDocument(); // create_draft's only
    expect(screen.getAllByRole("button", { name: "全部允许" })).toHaveLength(2); // two of run 7
    await user.click(screen.getAllByRole("button", { name: "允许" })[0]);
    expect(onDecide).toHaveBeenCalledWith(a, true, false);
    await waitFor(() => expect(screen.queryByText("Wit 想要：plugin_netty_ping")).toBeNull());
    expect(screen.queryByRole("button", { name: "全部允许" })).toBeNull(); // one left
    await user.click(screen.getByRole("button", { name: "拒绝" }));
    expect(onDecide).toHaveBeenLastCalledWith(b, false, false);
  });

  it("全部允许 decides the run's approvals at once; a failed decision keeps the card", async () => {
    const user = userEvent.setup();
    const onDecide = vi.fn().mockResolvedValueOnce(false).mockResolvedValue(true);
    const { emit } = await setup({ onDecide });
    await user.type(input(), "ping{Enter}");
    const call = (id: string) => ({ id, name: "plugin_netty_ping", args: "{}" });
    const ap = (id: number, callId: string): Approval => ({
      id,
      run: 7,
      callId,
      tool: "plugin_netty_ping",
      summary: "",
      state: "pending",
      error: "",
      destinations: [],
      args: "{}",
    });
    emit({ kind: "tool_call", call: call("p1") });
    emit({ kind: "tool_call", call: call("p2") });
    emit({
      kind: "interrupted",
      interrupts: [call("p1"), call("p2")],
      approvals: [ap(5, "p1"), ap(6, "p2")],
    });
    const all = await screen.findAllByRole("button", { name: "全部允许" });
    await user.click(all[0]);
    expect(onDecide).toHaveBeenCalledWith(ap(5, "p1"), true, true);
    expect(screen.getAllByText("Wit 想要：plugin_netty_ping")).toHaveLength(2); // refused: kept
    await user.click(screen.getAllByRole("button", { name: "全部允许" })[1]);
    await waitFor(() => expect(screen.queryByText("Wit 想要：plugin_netty_ping")).toBeNull());
  });

  it("a resumed run that is not the last answer's streams into an answer of its own", async () => {
    const { emit } = await setup({
      history: {
        ...empty,
        running: 9, // run 9 saved nothing yet: its question is not in the history
        messages: [
          { id: "q0", role: "user", text: "Q", calls: [], results: [], citations: [], runs: [7] },
          {
            id: "a1",
            role: "assistant",
            text: "Answer A",
            calls: [],
            results: [],
            citations: [],
            runs: [7],
          },
        ],
      },
    });
    expect(await screen.findByRole("button", { name: "停止" })).toBeInTheDocument();
    emit({ run: 9, kind: "text", text: "Answer B" });
    expect(await screen.findByText("Answer B")).toBeInTheDocument();
    expect(screen.getByText("Answer A")).toBeInTheDocument();
    expect(document.querySelectorAll("[data-role=assistant]")).toHaveLength(2);
  });

  it("a decision's resume goes on in its run's answer only", async () => {
    const { emit, resume } = await setup({
      history: {
        ...empty,
        messages: [
          { id: "q0", role: "user", text: "Q", calls: [], results: [], citations: [], runs: [7] },
          {
            id: "a1",
            role: "assistant",
            text: "Answer A",
            calls: [],
            results: [],
            citations: [],
            runs: [7],
          },
        ],
      },
    });
    emit({ run: 9, kind: "text", text: "Answer B" }); // another run's (a ⌘K run's), logged
    resume(9);
    expect(await screen.findByText("Answer B")).toBeInTheDocument();
    expect(screen.getByText("Answer A")).toBeInTheDocument();
    expect(document.querySelectorAll("[data-role=assistant]")).toHaveLength(2);
  });

  it("a live answer goes on when a decision resumes its run", async () => {
    const { emit, resume } = await setup();
    await userEvent.setup().type(input(), "Q{Enter}");
    emit({ run: 7, kind: "text", text: "one" });
    emit({ run: 7, kind: "interrupted", approvals: [] });
    await screen.findByText("one");
    emit({ run: 7, kind: "text", text: " two" });
    resume(7);
    expect(await screen.findByText("one two")).toBeInTheDocument();
    expect(document.querySelectorAll("[data-role=assistant]")).toHaveLength(1);
  });

  it("reconcile after done replaces streamed text with the saved history", async () => {
    const { api, emit } = await setup();
    const saved: WitHistory = {
      ...empty,
      messages: [
        { id: "q9", role: "user", text: "Q", calls: [], results: [], citations: [], runs: [7] },
        {
          id: "a9",
          role: "assistant",
          text: "Hello whole world",
          calls: [],
          results: [],
          citations: [],
          runs: [7],
        },
      ],
    };
    api.follow.mockResolvedValue(saved);
    await userEvent.setup().type(input(), "Q{Enter}");
    emit({ kind: "text", text: "Hello wo" }); // a slow subscriber lost the rest
    emit({ kind: "done" });
    expect(await screen.findByText("Hello whole world")).toBeInTheDocument();
    expect(screen.getAllByText("Q")).toHaveLength(1);
    expect(api.follow).toHaveBeenCalledTimes(2);
  });

  it("a reconcile read that lacks the run keeps what streamed", async () => {
    const { api, emit } = await setup(); // follow still gives the empty history
    await userEvent.setup().type(input(), "Q{Enter}");
    emit({ kind: "text", text: "kept" });
    emit({ kind: "done" });
    await waitFor(() => expect(api.follow).toHaveBeenCalledTimes(2));
    expect(screen.getByText("kept")).toBeInTheDocument();
  });

  it("links go through the host's openLink", async () => {
    const user = userEvent.setup();
    const { emit, onLink } = await setup();
    await user.type(input(), "Q{Enter}");
    emit({ kind: "text", text: "[b](https://ok.org/p)" });
    emit({ kind: "done" });
    await user.click(await screen.findByText("b"));
    await user.click(await screen.findByRole("button", { name: "打开" }));
    expect(onLink).toHaveBeenCalledWith("https://ok.org/p");
  });

  it("citations: both layouts render; cited click opens", async () => {
    const user = userEvent.setup();
    const onCite = vi.fn();
    const cite = (n: number, id: number, suspect = false) => ({
      n,
      messageId: id,
      subject: id === 51 ? "" : `S${id}`,
      from: `f${id}`,
      date: 0,
      suspect,
    });
    await setup({
      page: true,
      onCite,
      history: {
        ...empty,
        messages: [
          { id: "q0", role: "user", text: "Q", calls: [], results: [], citations: [], runs: [7] },
          {
            id: "a1",
            role: "assistant",
            text: "A",
            calls: [],
            results: [],
            citations: [cite(1, 50), cite(2, 51, true)],
            runs: [7],
          },
        ],
      },
    });
    // jsdom has no container queries: both branches are in the DOM, each behind its query
    const lists = screen.getAllByRole("region", { name: "引用的邮件" });
    expect(lists).toHaveLength(2);
    const column = lists.find((l) => l.closest("aside"));
    const under = lists.find((l) => !l.closest("aside"));
    expect(column?.closest("aside")?.className).toContain("@4xl:flex");
    expect(column?.closest("aside")?.className).toContain("hidden");
    expect(under?.className).toContain("@4xl:hidden");
    expect(under?.closest("[data-role=assistant]")).not.toBeNull();
    expect(screen.getByRole("textbox", { name: "向 Wit 提问" })).toBeInTheDocument();
    for (const l of lists) {
      expect(l.textContent).toContain("[1] f50");
      expect(l.textContent).toContain("（无主题）");
      expect(l.textContent).toContain("含疑似指令，已作为数据处理");
    }
    await user.click(screen.getAllByRole("button", { name: /\[2\] f51/ })[0]);
    expect(onCite).toHaveBeenCalledWith(51);
  });
});
