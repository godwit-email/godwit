import { Chat } from "@ai-sdk/react";
import type { UIMessageChunk } from "ai";
import type {
  Approval,
  WitCall,
  WitEvent,
  WitHistory,
  WitResult,
  WitTurn,
  WitTurnCall,
} from "@/mygo";
import { stepsOf } from "./fixtures";
import { historyToMessages, toChunks, WitTransport, type WitUIMessage } from "./transport";

vi.mock("@/mygo", () => ({ ComposePage: {}, events: { wit: { on: () => () => {} } } }));

const ev = (e: Partial<WitEvent>): WitEvent => ({
  conversation: 1,
  run: 7,
  turn: "1",
  ends: false,
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

const approval = (a: Partial<Approval>): Approval => ({
  id: 3,
  run: 7,
  callId: "c1",
  tool: "update_draft",
  summary: "partial",
  state: "pending",
  error: "",
  destinations: [],
  sessionAllow: false,
  draft: 0,
  args: "",
  draftEdit: false,
  ...a,
});

const turn = (t: Partial<WitTurn> & Pick<WitTurn, "id">): WitTurn => {
  const all = {
    question: "Q",
    command: null,
    quoted: [],
    text: "",
    reasoning: "",
    calls: [],
    approvals: [],
    citations: [],
    runs: [1],
    elapsed: 0,
    canAskAgain: false,
    asked: 0,
    answered: 0,
    ...t,
  };
  return { ...all, steps: t.steps ?? stepsOf(all) };
};

const on = (
  call: WitCall,
  result: WitResult | null = null,
  a: Approval | null = null,
): WitTurnCall => ({
  call,
  result,
  approval: a,
});

const hist = (turns: WitTurn[], p: Partial<WitHistory> = {}): WitHistory => ({
  conversation: 1,
  model: "",
  effort: "",
  usage: { input: 0, output: 0, context: 0, cached: 0, history: 0, summary: 0, compactable: false },
  running: 0,
  turns,
  ...p,
});

/** A run started, as the host's ask and skill resolve to it. */
const started = (run: number, turn = String(run)) => ({ run, turn });

/** A fake bound API and WitStream: emit sends an event to the transport's listener. */
function fake() {
  let listener: ((e: WitEvent) => void) | undefined;
  const api = { ask: vi.fn(), skill: vi.fn(), follow: vi.fn(), stop: vi.fn() };
  const tr = new WitTransport(api, (f) => {
    listener = f;
    return () => {
      listener = undefined;
    };
  });
  return { api, tr, emit: (e: Partial<WitEvent>) => listener?.(ev(e)) };
}

async function readAll(s: ReadableStream<UIMessageChunk>) {
  const out: UIMessageChunk[] = [];
  for await (const c of s) out.push(c);
  return out;
}

const userMsg = (text: string, metadata?: WitUIMessage["metadata"]): WitUIMessage => ({
  id: "u",
  role: "user",
  metadata,
  parts: [{ type: "text", text }],
});

const send = (tr: WitTransport, m: WitUIMessage) =>
  tr.sendMessages({
    trigger: "submit-message",
    chatId: "c",
    messageId: undefined,
    messages: [m],
    abortSignal: undefined,
  });

describe("rewind", () => {
  const send = (
    tr: WitTransport,
    m: WitUIMessage,
    trigger: "submit-message" | "regenerate-message",
  ) =>
    tr.sendMessages({
      trigger,
      chatId: "c",
      messageId: undefined,
      messages: [m],
      abortSignal: undefined,
    });

  it("a question sent with rewind is asked in place, and so is its retry", async () => {
    const { api, tr } = fake();
    const t2 = new WitTransport({ ...api, rewinds: true }, () => () => {});
    api.ask.mockResolvedValue(started(7));
    expect(tr.rewinds).toBe(false);
    expect(t2.rewinds).toBe(true);
    const m = userMsg("again", { quoted: [3], rewind: "2" });
    void send(t2, m, "submit-message");
    await vi.waitFor(() => expect(api.ask).toHaveBeenCalledWith("again", [3], "2"));
    // a retry follows a run that saved nothing: core replaced nothing yet
    void send(t2, m, "regenerate-message");
    await vi.waitFor(() => expect(api.ask).toHaveBeenCalledTimes(2));
    expect(api.ask).toHaveBeenLastCalledWith("again", [3], "2");
  });

  it("a retry after the ask in place was refused asks in place again", async () => {
    const { api } = fake();
    api.ask.mockRejectedValueOnce(new Error("boom")).mockResolvedValue(started(7));
    const tr = new WitTransport({ ...api, rewinds: true }, () => () => {});
    const m = userMsg("again", { rewind: "1" });
    const failed = await readAll(await send(tr, m, "submit-message"));
    expect(failed.at(-1)).toMatchObject({ type: "error", errorText: "boom" });
    void send(tr, m, "regenerate-message");
    await vi.waitFor(() => expect(api.ask).toHaveBeenCalledTimes(2));
    expect(api.ask).toHaveBeenLastCalledWith("again", [], "1");
  });

  it("a host that does not rewind just asks", async () => {
    const { api, tr } = fake();
    api.ask.mockResolvedValue(started(7));
    void send(tr, userMsg("again", { rewind: "1" }), "submit-message");
    await vi.waitFor(() => expect(api.ask).toHaveBeenCalledWith("again"));
  });
});

describe("toChunks", () => {
  it("streams reasoning in a part of its own, closed when the text starts", () => {
    const seen = new Set<string>();
    const a = toChunks(ev({ kind: "reasoning", text: "先查" }), seen);
    const b = toChunks(ev({ kind: "reasoning", text: "账单" }), seen);
    const c = toChunks(ev({ kind: "text", text: "三笔" }), seen);
    expect(a.map((x) => x.type)).toEqual(["reasoning-start", "reasoning-delta"]);
    expect(b.map((x) => x.type)).toEqual(["reasoning-delta"]);
    expect(c.map((x) => x.type)).toEqual(["reasoning-end", "text-start", "text-delta"]);
  });

  it("an answer's saved reasoning leads its parts", () => {
    const [, m] = historyToMessages(hist([turn({ id: "1", text: "三笔", reasoning: "先查账单" })]));
    expect(m.parts.map((p) => p.type)).toEqual(["reasoning", "text"]);
    expect(m.parts[0]).toMatchObject({ text: "先查账单", state: "done" });
  });

  it("opens text once, closes it before anything else", () => {
    const seen = new Set<string>();
    const a = toChunks(ev({ kind: "text", text: "Hi" }), seen);
    const b = toChunks(ev({ kind: "text", text: " there" }), seen);
    const c = toChunks(
      ev({ kind: "tool_call", call: { id: "c1", name: "search", args: '{"q":"x"}' } }),
      seen,
    );
    expect(a.map((x) => x.type)).toEqual(["text-start", "text-delta"]);
    expect(b).toEqual([{ type: "text-delta", id: expect.any(String), delta: " there" }]);
    expect(c).toEqual([
      { type: "text-end", id: expect.any(String) },
      {
        type: "tool-input-available",
        toolCallId: "c1",
        toolName: "search",
        input: { q: "x" },
        dynamic: true,
      },
    ]);
  });

  it("maps results to output or error; drops a result for a call not shown", () => {
    const seen = new Set<string>(["c1", "c2"]);
    expect(
      toChunks(
        ev({ kind: "tool_result", result: { callId: "cx", name: "s", output: "", error: "" } }),
        seen,
      ),
    ).toEqual([]);
    expect(
      toChunks(
        ev({ kind: "tool_result", result: { callId: "c1", name: "s", output: "[1]", error: "" } }),
        seen,
      ),
    ).toEqual([{ type: "tool-output-available", toolCallId: "c1", output: [1] }]);
    expect(
      toChunks(
        ev({ kind: "tool_result", result: { callId: "c2", name: "s", output: "", error: "boom" } }),
        seen,
      ),
    ).toEqual([{ type: "tool-output-error", toolCallId: "c2", errorText: "boom" }]);
  });

  it("skill interrupt synthesizes a tool part before the approval request", () => {
    const a = approval({ id: 9, callId: "skill" });
    const out = toChunks(
      ev({
        kind: "interrupted",
        interrupts: [{ id: "skill", name: "update_draft", args: "" }],
        approvals: [a],
        ends: true,
      }),
      new Set(),
    );
    const types = out.map((c) => c.type);
    expect(types.indexOf("tool-input-available")).toBeLessThan(
      types.indexOf("tool-approval-request"),
    );
    expect(out).toContainEqual({
      type: "tool-approval-request",
      approvalId: "9",
      toolCallId: "skill-9", // every skill's call is "skill": its part has an ID of its own
    });
    expect(types.at(-1)).toBe("finish"); // pending: the run waits
  });

  it("shows an auto-applied first generation as output and keeps the run going", () => {
    const seen = new Set(["c1"]);
    const out = toChunks(
      ev({
        kind: "interrupted",
        interrupts: [{ id: "c1", name: "update_draft", args: "{}" }],
        approvals: [approval({ state: "done", summary: "initial" })],
      }),
      seen,
    );
    expect(out.map((c) => c.type)).toEqual(["data-approvals", "tool-output-available"]);
  });

  it("maps citations, done and errors", () => {
    const cite = {
      n: 0,
      messageId: 5,
      subject: "s",
      from: "f",
      date: 0,
      suspect: false,
      quote: "",
      snippet: "",
      callIds: [],
      gone: false,
    };
    expect(toChunks(ev({ kind: "context", citations: [cite] }), new Set())).toEqual([
      { type: "data-citations", data: [cite] },
    ]);
    const seen = new Set<string>();
    toChunks(ev({ kind: "text", text: "x" }), seen);
    expect(toChunks(ev({ kind: "done" }), seen).map((c) => c.type)).toEqual(["text-end", "finish"]);
    expect(toChunks(ev({ kind: "error", error: "error.ai.unavailable" }), new Set())).toEqual([
      { type: "error", errorText: "error.ai.unavailable" },
    ]);
    // a stop is no failure
    expect(toChunks(ev({ kind: "error", error: "" }), new Set())).toEqual([{ type: "finish" }]);
  });
});

describe("historyToMessages", () => {
  it("each turn is its question, then its answer: calls with their results or approvals, the turn's own approvals, citations", () => {
    const cite = {
      n: 1,
      messageId: 5,
      subject: "s",
      from: "f",
      date: 0,
      suspect: true,
      quote: "q",
      snippet: "",
      callIds: [],
      gone: false,
    };
    const rejected = approval({ id: 1, run: 1, callId: "c2", state: "rejected" });
    const failed = approval({ id: 2, run: 1, callId: "c3", state: "failed", error: "nope" });
    const skill = approval({ id: 5, run: 1, callId: "skill", state: "done" });
    const waiting = approval({ id: 4, run: 2, callId: "skill", state: "pending" });
    const h = hist([
      turn({
        id: "1",
        text: "A",
        calls: [
          on(
            { id: "c1", name: "search", args: '{"q":1}' },
            { callId: "c1", name: "search", output: '"ok"', error: "" },
          ),
          on({ id: "c2", name: "update_draft", args: "{}" }, null, rejected),
          on({ id: "c3", name: "update_draft", args: "{}" }, null, failed),
        ],
        approvals: [skill],
        citations: [cite],
        canAskAgain: true,
      }),
      turn({
        id: "9",
        command: { skill: "tone-formal", lang: "" },
        approvals: [waiting],
        runs: [2],
      }),
    ]);
    const m = historyToMessages(h, (c) => `/${c.skill}`);
    expect(m.map((x) => [x.id, x.role])).toEqual([
      ["q1", "user"],
      ["a1", "assistant"],
      ["q9", "user"],
      ["a9", "assistant"],
    ]);
    expect(m[0]).toMatchObject({
      parts: [{ type: "text", text: "Q" }],
      metadata: { turn: "1", canAskAgain: true },
    });
    expect(m[2].parts).toEqual([{ type: "text", text: "/tone-formal" }]);
    const tools = m[1].parts.filter((p) => p.type === "dynamic-tool");
    expect(tools.map((p) => [p.toolCallId, p.state])).toEqual([
      ["c1", "output-available"],
      ["c2", "output-denied"],
      ["c3", "output-error"],
      ["skill-5", "output-available"],
    ]);
    expect(m[1].parts).toContainEqual({ type: "text", text: "A", state: "done" });
    expect(m[1].parts).toContainEqual({ type: "data-citations", data: [cite] });
    expect(m[1].parts).toContainEqual({ type: "data-approvals", data: [rejected, failed, skill] });
    expect(m[3].parts.find((p) => p.type === "dynamic-tool")).toMatchObject({
      toolCallId: "skill-4",
      state: "approval-requested",
      approval: { id: "4" },
    });
  });

  it("an answer carries how long its runs worked; none when not known; a turn with no answer has none", () => {
    const ms = historyToMessages(
      hist([
        turn({ id: "1", text: "a", elapsed: 6400 }),
        turn({ id: "3", text: "b" }),
        turn({ id: "5" }),
      ]),
    );
    expect(ms.map((m) => m.id)).toEqual(["q1", "a1", "q3", "a3", "q5"]);
    expect(ms[1].metadata).toEqual({ elapsed: 6400 });
    expect(ms[3].metadata).toBeUndefined();
  });

  it("an answer's parts follow its steps: text, a call, then the text after it", () => {
    const c = { id: "c1", name: "search_mail", args: "{}" };
    const [, a] = historyToMessages(
      hist([
        turn({
          id: "1",
          calls: [on(c, { callId: "c1", name: "search_mail", output: "[]", error: "" })],
          steps: [
            { kind: "reasoning", text: "r", call: 0 },
            { kind: "text", text: "a", call: 0 },
            { kind: "call", text: "", call: 0 },
            { kind: "text", text: "b", call: 0 },
          ],
        }),
      ]),
    );
    expect(a.parts.map((p) => p.type)).toEqual(["reasoning", "text", "dynamic-tool", "text"]);
  });

  it("a question carries when it was asked, its answer when it was answered", () => {
    const [q, a] = historyToMessages(
      hist([turn({ id: "1", text: "a", asked: 1000, answered: 2000 })]),
    );
    expect(q.metadata?.time).toBe(1000);
    expect(a.metadata).toEqual({ time: 2000 });
  });

  it("a question keeps the mail it quoted", () => {
    const hit = { id: 7, subject: "s", from: "f", date: 0 };
    const [q] = historyToMessages(hist([turn({ id: "1", quoted: [hit] })]));
    expect(q.metadata).toMatchObject({ quoted: [7], hits: [hit] });
  });

  it("a command turn ignores raw text steps in answer, keeping reasoning and approvals", () => {
    const a = approval({ id: 9, callId: "skill" });
    const [, ans] = historyToMessages(
      hist([
        turn({
          id: "1",
          command: { skill: "longer", lang: "" },
          approvals: [a],
          steps: [
            { kind: "reasoning", text: "thought", call: 0 },
            { kind: "text", text: "<subject>Title</subject><p>Draft</p>", call: 0 },
          ],
        }),
      ]),
    );
    expect(ans.parts.map((p) => p.type)).toEqual(["reasoning", "dynamic-tool", "data-approvals"]);
    expect(ans.parts.find((p) => p.type === "text")).toBeUndefined();
  });
});

describe("WitTransport", () => {
  it("listener before start: events before ask resolves are not lost", async () => {
    const { api, tr, emit } = fake();
    let resolve!: (r: { run: number; turn: string }) => void;
    api.ask.mockReturnValue(new Promise((r) => (resolve = r)));
    const s = await send(tr, userMsg("hi"));
    expect(api.ask).toHaveBeenCalledWith("hi");
    emit({ kind: "text", text: "early" });
    resolve(started(7));
    await Promise.resolve();
    emit({ kind: "done" });
    const out = await readAll(s);
    expect(out).toContainEqual({ type: "text-delta", id: expect.any(String), delta: "early" });
    expect(tr.run).toBe(7);
  });

  it("runs a skill from the message's metadata", async () => {
    const { api, tr, emit } = fake();
    api.skill.mockResolvedValue(started(8));
    const s = await send(tr, userMsg("翻译为English", { skill: "translate-draft", lang: "en" }));
    await vi.waitFor(() => expect(tr.run).toBe(8));
    emit({ kind: "done", run: 8 });
    await readAll(s);
    expect(api.skill).toHaveBeenCalledWith("translate-draft", "en");
    expect(api.ask).not.toHaveBeenCalled();
  });

  it("a refused start ends the stream with its error key", async () => {
    const { api, tr } = fake();
    api.ask.mockRejectedValue(new Error("error.run_active"));
    const out = await readAll(await send(tr, userMsg("hi")));
    expect(out).toEqual([{ type: "error", errorText: "error.run_active" }]);
  });

  it("has nothing to reconnect to without a live run", async () => {
    const { tr } = fake();
    expect(await tr.reconnectToStream({ chatId: "c" })).toBeNull();
  });

  it("reconnects to a live run: deltas, even ones before follow resolved, append to the open answer", async () => {
    const { api, tr, emit } = fake();
    const h = hist([turn({ id: "1", text: "Hel", runs: [7] })], { running: 7 });
    api.follow.mockImplementation(async () => {
      emit({ kind: "text", text: "lo" }); // emitted before follow resolves
      return h;
    });
    const got = (await tr.follow()) as WitHistory;
    const msgs = historyToMessages(got);
    const open = msgs.pop();
    const chat = new Chat<WitUIMessage>({ transport: tr, messages: msgs });
    tr.resume(open, got.running);
    const done = chat.resumeStream();
    await vi.waitFor(() => expect(chat.status).toBe("streaming"));
    emit({ kind: "text", text: " world" });
    emit({ kind: "done" });
    await done;
    expect(chat.messages.map((m) => m.id)).toEqual(["q1", "a1"]);
    const texts = chat.messages[1].parts.flatMap((p) => (p.type === "text" ? [p.text] : []));
    expect(texts).toEqual(["Hello world"]); // one part: no paragraph break across the reconnect
    expect(tr.run).toBe(7);
  });

  it("keeps only the conversation followed", async () => {
    const { api, tr, emit } = fake();
    api.follow.mockImplementation(async () => {
      emit({ conversation: 2, kind: "text", text: "old" }); // in flight from a switch
      emit({ conversation: 1, kind: "text", text: "a" });
      return hist([], { running: 7 });
    });
    await tr.follow();
    emit({ conversation: 2, kind: "text", text: "old2" });
    emit({ conversation: 1, kind: "text", text: "b" });
    emit({ conversation: 1, kind: "done" });
    tr.resume(undefined, 7);
    const out = await readAll(
      (await tr.reconnectToStream({ chatId: "c" })) as ReadableStream<UIMessageChunk>,
    );
    const deltas = out.flatMap((c) => (c.type === "text-delta" ? [c.delta] : []));
    expect(deltas).toEqual(["a", "b"]);
  });

  it("a started run names the conversation: its first event; earlier ones of others are dropped", async () => {
    const { api, tr, emit } = fake();
    let resolve!: (r: { run: number; turn: string }) => void;
    api.ask.mockReturnValue(new Promise((r) => (resolve = r)));
    const s = await send(tr, userMsg("hi"));
    emit({ conversation: 3, run: 2, kind: "text", text: "stale" }); // a ⌘K run's, in flight
    emit({ conversation: 5, run: 9, kind: "text", text: "mine" });
    resolve(started(9));
    await vi.waitFor(() => expect(tr.run).toBe(9));
    emit({ conversation: 3, run: 2, kind: "done" });
    emit({ conversation: 5, run: 9, kind: "text", text: "!" });
    emit({ conversation: 5, run: 9, kind: "done" });
    const deltas = (await readAll(s)).flatMap((c) => (c.type === "text-delta" ? [c.delta] : []));
    expect(deltas).toEqual(["mine", "!"]);
  });

  it("Stop while the run is starting stops it once its ID is known", async () => {
    const { api, tr } = fake();
    tr.run = 4; // an older run's
    let resolve!: (r: { run: number; turn: string }) => void;
    api.ask.mockReturnValue(new Promise((r) => (resolve = r)));
    api.stop.mockResolvedValue(undefined);
    await send(tr, userMsg("hi"));
    await tr.stop();
    expect(api.stop).not.toHaveBeenCalled();
    resolve(started(9));
    await vi.waitFor(() => expect(api.stop).toHaveBeenCalledWith(9));
    expect(api.stop).toHaveBeenCalledTimes(1);
    await tr.stop(); // started now: at once
    expect(api.stop).toHaveBeenLastCalledWith(9);
  });

  it("Stop with no run known stops nothing (never run 0)", async () => {
    const { api, tr } = fake();
    await tr.stop();
    expect(api.stop).not.toHaveBeenCalled();
  });

  it("a skill is a turn of its own, its edit on its answer, kept across a reconnect", async () => {
    const h = hist(
      [
        turn({ id: "1", text: "a1" }),
        turn({
          id: "3",
          command: { skill: "a", lang: "" },
          runs: [2],
          approvals: [approval({ id: 10, run: 2, callId: "skill", state: "done" })],
        }),
        turn({
          id: "4",
          command: { skill: "b", lang: "" },
          runs: [3],
          approvals: [approval({ id: 11, run: 3, callId: "skill", state: "rejected" })],
        }),
        turn({ id: "5", question: "Go?", text: "Go", runs: [5] }),
      ],
      { running: 5 },
    );
    const all = historyToMessages(h);
    expect(all.map((m) => m.id)).toEqual(["q1", "a1", "q3", "a3", "q4", "a4", "q5", "a5"]);
    const tool = (m: WitUIMessage) => m.parts.filter((p) => p.type === "dynamic-tool");
    expect(tool(all[3]).map((p) => [p.toolCallId, p.state])).toEqual([
      ["skill-10", "output-available"],
    ]);
    expect(tool(all[5]).map((p) => [p.toolCallId, p.state])).toEqual([
      ["skill-11", "output-denied"],
    ]);

    const { api, tr, emit } = fake();
    api.follow.mockResolvedValue(h);
    await tr.follow();
    const open = all.pop();
    const chat = new Chat<WitUIMessage>({ transport: tr, messages: all });
    tr.resume(open, 5);
    const done = chat.resumeStream();
    await vi.waitFor(() => expect(chat.status).toBe("streaming"));
    emit({ run: 5, turn: "5", kind: "text", text: "!" });
    emit({ run: 5, turn: "5", kind: "done", ends: true });
    await done;
    expect(chat.messages.map((m) => m.id)).toEqual([
      "q1",
      "a1",
      "q3",
      "a3",
      "q4",
      "a4",
      "q5",
      "a5",
    ]);
    expect(tool(chat.messages[3]).map((p) => p.state)).toEqual(["output-available"]);
  });

  it("a live skill's part has the history's ID: a resumed run updates it", async () => {
    const pending = approval({ id: 12, run: 2, callId: "skill", state: "pending" });
    const h = hist([
      turn({ id: "2", command: { skill: "a", lang: "" }, runs: [2], approvals: [pending] }),
    ]);
    const all = historyToMessages(h);
    expect(all.map((m) => m.id)).toEqual(["q2", "a2"]);
    const { api, tr, emit } = fake();
    api.follow.mockResolvedValue(h);
    await tr.follow();
    const open = all.pop();
    const chat = new Chat<WitUIMessage>({ transport: tr, messages: all });
    tr.resume(open, 2); // as after a decision
    const done = chat.resumeStream();
    await vi.waitFor(() => expect(chat.status).toBe("streaming"));
    emit({
      run: 2,
      turn: "2",
      kind: "interrupted",
      interrupts: [{ id: "skill", name: "update_draft", args: "" }],
      approvals: [{ ...pending, state: "done" }],
    });
    emit({ run: 2, turn: "2", kind: "done", ends: true });
    await done;
    const tools = chat.messages[1].parts.filter((p) => p.type === "dynamic-tool");
    expect(tools.map((p) => [p.toolCallId, p.state])).toEqual([["skill-12", "output-available"]]);
  });

  it("a started run's answer takes its turn's ID: the history read after it is the same message", async () => {
    const { api, tr, emit } = fake();
    api.ask.mockResolvedValue(started(7, "5"));
    const chat = new Chat<WitUIMessage>({ transport: tr });
    const sent = chat.sendMessage({ text: "hi" });
    await vi.waitFor(() => expect(tr.run).toBe(7));
    emit({ run: 7, turn: "5", kind: "text", text: "yo" });
    emit({ run: 7, turn: "5", kind: "done", ends: true });
    await sent;
    expect(chat.messages.at(-1)?.id).toBe("a5");
    expect(tr.owns(chat.messages.at(-1), 7)).toBe(true);
  });

  it("a refused start drops what waited", async () => {
    const { api, tr, emit } = fake();
    let reject!: (e: Error) => void;
    api.ask.mockReturnValue(new Promise((_, r) => (reject = r)));
    const s = await send(tr, userMsg("hi"));
    emit({ conversation: 3, run: 2, kind: "text", text: "other" });
    reject(new Error("error.run_active"));
    await readAll(s);
    expect((tr as unknown as { early: unknown[] }).early).toEqual([]);
  });
});

describe("WitTransport selections", () => {
  const histOf = (conversation: number, runs: number[] = []): WitHistory =>
    hist(runs.length ? [turn({ id: "1", runs })] : [], { conversation });

  it("switching conversations drops the old stream", async () => {
    const { api, tr, emit } = fake();
    api.ask.mockResolvedValue(started(7));
    const s = await send(tr, userMsg("hi"));
    await vi.waitFor(() => expect(tr.run).toBe(7));
    emit({ run: 7, kind: "text", text: "a" });
    api.follow.mockResolvedValue(histOf(2));
    await tr.follow(); // a switch: the old stream ends
    emit({ run: 7, kind: "text", text: "late" }); // conversation 1's, still in flight
    const deltas = (await readAll(s)).flatMap((c) => (c.type === "text-delta" ? [c.delta] : []));
    expect(deltas).toEqual(["a"]);
    expect(tr.conversation).toBe(2);
  });

  it("out-of-order follow results: an older follow's late result is ignored", async () => {
    const { api, tr } = fake();
    let resolveA!: (h: WitHistory) => void;
    api.follow.mockReturnValueOnce(new Promise((r) => (resolveA = r)));
    api.follow.mockResolvedValueOnce(histOf(2));
    const a = tr.follow();
    expect(await tr.follow()).toEqual(histOf(2));
    resolveA(histOf(1));
    expect(await a).toBeUndefined();
    expect(tr.conversation).toBe(2);
  });

  it("ask returning after a switch is ignored", async () => {
    const { api, tr, emit } = fake();
    let resolve!: (r: { run: number; turn: string }) => void;
    api.ask.mockReturnValue(new Promise((r) => (resolve = r)));
    await send(tr, userMsg("hi"));
    api.follow.mockResolvedValue(histOf(2));
    await tr.follow();
    resolve(started(9));
    await Promise.resolve();
    emit({ conversation: 5, run: 9, kind: "text", text: "x" });
    expect(tr.run).toBe(0);
    expect(tr.conversation).toBe(2);
  });

  it("history reads the conversation again without a reset; another conversation's is none", async () => {
    const { api, tr } = fake();
    api.follow.mockResolvedValue(histOf(1));
    await tr.follow();
    api.follow.mockResolvedValue(histOf(1, [7]));
    expect(await tr.history()).toEqual(histOf(1, [7]));
    expect(tr.conversation).toBe(1);
    api.follow.mockResolvedValue(histOf(3, [7]));
    expect(await tr.history()).toBeUndefined();
  });

  it("watch hears the followed conversation's events only", async () => {
    const { api, tr, emit } = fake();
    api.follow.mockResolvedValue(histOf(1));
    await tr.follow();
    const heard: number[] = [];
    const off = tr.watch((e) => heard.push(e.run));
    emit({ conversation: 2, run: 3 });
    emit({ conversation: 1, run: 4 });
    off();
    emit({ conversation: 1, run: 5 });
    expect(heard).toEqual([4]);
  });

  it("Stop while starting, then a switch: the run is still stopped once its ID is known", async () => {
    const { api, tr } = fake();
    let resolve!: (r: { run: number; turn: string }) => void;
    api.ask.mockReturnValue(new Promise((r) => (resolve = r)));
    api.stop.mockResolvedValue(undefined);
    await send(tr, userMsg("hi"));
    await tr.stop();
    api.follow.mockResolvedValue(histOf(2));
    await tr.follow(); // a switch: the start's result is no longer shown
    resolve(started(9));
    await vi.waitFor(() => expect(api.stop).toHaveBeenCalledWith(9));
    expect(tr.run).toBe(0);
  });
});
