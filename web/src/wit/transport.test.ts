import { Chat } from "@ai-sdk/react";
import type { UIMessageChunk } from "ai";
import type { Approval, WitEvent, WitHistory } from "@/mygo";
import { historyToMessages, toChunks, WitTransport, type WitUIMessage } from "./transport";

vi.mock("@/mygo", () => ({ ComposePage: {}, events: { wit: { on: () => () => {} } } }));

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
  args: "",
  ...a,
});

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
  it("a question sent with rewind drops the old one first; a retry does not again", async () => {
    const { api, tr } = fake();
    const order: string[] = [];
    const rewind = vi.fn(async () => {
      order.push("rewind");
    });
    const t2 = new WitTransport({ ...api, rewind }, () => () => {});
    api.ask.mockImplementation(async () => {
      order.push("ask");
      return 7;
    });
    expect(tr.rewinds).toBe(false);
    expect(t2.rewinds).toBe(true);
    const m = userMsg("again", { quoted: [3], rewind: 2 });
    void t2.sendMessages({
      trigger: "submit-message",
      chatId: "c",
      messageId: undefined,
      messages: [m],
      abortSignal: undefined,
    });
    await vi.waitFor(() => expect(api.ask).toHaveBeenCalledWith("again", [3]));
    expect(rewind).toHaveBeenCalledWith(2);
    expect(order).toEqual(["rewind", "ask"]);
    void t2.sendMessages({
      trigger: "regenerate-message",
      chatId: "c",
      messageId: undefined,
      messages: [m],
      abortSignal: undefined,
    });
    await vi.waitFor(() => expect(api.ask).toHaveBeenCalledTimes(2));
    expect(rewind).toHaveBeenCalledOnce();
  });

  it("a retry after the rewind itself failed rewinds again", async () => {
    const { api } = fake();
    const rewind = vi.fn().mockRejectedValueOnce(new Error("boom")).mockResolvedValue(undefined);
    api.ask.mockResolvedValue(7);
    const tr = new WitTransport({ ...api, rewind }, () => () => {});
    const m = userMsg("again", { rewind: 0 });
    const req = (trigger: "submit-message" | "regenerate-message") =>
      tr.sendMessages({
        trigger,
        chatId: "c",
        messageId: undefined,
        messages: [m],
        abortSignal: undefined,
      });
    const failed = await readAll(await req("submit-message"));
    expect(failed.at(-1)).toMatchObject({ type: "error", errorText: "boom" });
    expect(api.ask).not.toHaveBeenCalled();
    void req("regenerate-message");
    await vi.waitFor(() => expect(api.ask).toHaveBeenCalledWith("again"));
    expect(rewind).toHaveBeenCalledTimes(2);
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
    const [m] = historyToMessages({
      conversation: 1,
      model: "",
      usage: { input: 0, output: 0, context: 0, history: 0 },
      running: 0,
      approvals: [],
      messages: [
        {
          id: "a0",
          role: "assistant",
          text: "三笔",
          reasoning: "先查账单",
          quoted: [],
          calls: [],
          results: [],
          citations: [],
          runs: [1],
        },
      ],
    });
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
  it("groups questions and answers with results, approvals and citations", () => {
    const cite = {
      n: 1,
      messageId: 5,
      subject: "s",
      from: "f",
      date: 0,
      suspect: true,
      quote: "q",
    };
    const h: WitHistory = {
      conversation: 1,
      model: "",
      usage: { input: 0, output: 0, context: 0, history: 0 },
      running: 0,
      messages: [
        {
          id: "q0",
          role: "user",
          text: "Q",
          reasoning: "",
          quoted: [],
          calls: [],
          results: [],
          citations: [],
          runs: [1],
        },
        {
          id: "a1",
          role: "assistant",
          text: "A",
          reasoning: "",
          quoted: [],
          calls: [
            { id: "c1", name: "search", args: '{"q":1}' },
            { id: "c2", name: "update_draft", args: "{}" },
            { id: "c3", name: "update_draft", args: "{}" },
          ],
          results: [{ callId: "c1", name: "search", output: '"ok"', error: "" }],
          citations: [cite],
          runs: [1],
        },
        {
          id: "q2",
          role: "user",
          text: "更正式",
          reasoning: "",
          quoted: [],
          calls: [],
          results: [],
          citations: [],
          runs: [2],
        },
      ],
      approvals: [
        approval({ id: 1, run: 1, callId: "c2", state: "rejected" }),
        approval({ id: 2, run: 1, callId: "c3", state: "failed", error: "nope" }),
        approval({ id: 4, run: 2, callId: "skill", state: "pending" }), // its run has no answer yet
        approval({ id: 5, run: 1, callId: "skill", state: "done" }), // an older skill's, decided
      ],
    };
    const m = historyToMessages(h);
    expect(m.map((x) => [x.id, x.role])).toEqual([
      ["q0", "user"],
      ["a1", "assistant"],
      ["q2", "user"],
      [expect.any(String), "assistant"],
    ]);
    expect(m[0].parts).toEqual([{ type: "text", text: "Q" }]);
    const tools = m[1].parts.filter((p) => p.type === "dynamic-tool");
    expect(tools.map((p) => [p.toolCallId, p.state])).toEqual([
      ["c1", "output-available"],
      ["c2", "output-denied"],
      ["c3", "output-error"],
      ["skill-5", "output-available"],
    ]);
    expect(m[1].parts).toContainEqual({ type: "text", text: "A", state: "done" });
    expect(m[1].parts).toContainEqual({ type: "data-citations", data: [cite] });
    const pending = m[3].parts.find((p) => p.type === "dynamic-tool");
    expect(pending).toMatchObject({
      toolCallId: "skill-4",
      state: "approval-requested",
      approval: { id: "4" },
    });
    expect(m[3].parts).toContainEqual({ type: "data-approvals", data: [h.approvals[2]] });
    expect(m[1].parts).toContainEqual({
      type: "data-approvals",
      data: [h.approvals[0], h.approvals[1], h.approvals[3]],
    });
  });
});

describe("WitTransport", () => {
  it("listener before start: events before ask resolves are not lost", async () => {
    const { api, tr, emit } = fake();
    let resolve!: (n: number) => void;
    api.ask.mockReturnValue(new Promise((r) => (resolve = r)));
    const s = await send(tr, userMsg("hi"));
    expect(api.ask).toHaveBeenCalledWith("hi");
    emit({ kind: "text", text: "early" });
    resolve(7);
    await Promise.resolve();
    emit({ kind: "done" });
    const out = await readAll(s);
    expect(out).toContainEqual({ type: "text-delta", id: expect.any(String), delta: "early" });
    expect(tr.run).toBe(7);
  });

  it("runs a skill from the message's metadata", async () => {
    const { api, tr, emit } = fake();
    api.skill.mockResolvedValue(8);
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
    const h: WitHistory = {
      conversation: 1,
      model: "",
      usage: { input: 0, output: 0, context: 0, history: 0 },
      running: 7,
      approvals: [],
      messages: [
        {
          id: "q0",
          role: "user",
          text: "Q",
          reasoning: "",
          quoted: [],
          calls: [],
          results: [],
          citations: [],
          runs: [1],
        },
        {
          id: "a1",
          role: "assistant",
          text: "Hel",
          reasoning: "",
          quoted: [],
          calls: [],
          results: [],
          citations: [],
          runs: [1],
        },
      ],
    };
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
    expect(chat.messages.map((m) => m.id)).toEqual(["q0", "a1"]);
    const texts = chat.messages[1].parts.flatMap((p) => (p.type === "text" ? [p.text] : []));
    expect(texts).toEqual(["Hello world"]); // one part: no paragraph break across the reconnect
    expect(tr.run).toBe(7);
  });

  it("keeps only the conversation followed", async () => {
    const { api, tr, emit } = fake();
    api.follow.mockImplementation(async () => {
      emit({ conversation: 2, kind: "text", text: "old" }); // in flight from a switch
      emit({ conversation: 1, kind: "text", text: "a" });
      return { conversation: 1, running: 7, approvals: [], messages: [] };
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
    let resolve!: (n: number) => void;
    api.ask.mockReturnValue(new Promise((r) => (resolve = r)));
    const s = await send(tr, userMsg("hi"));
    emit({ conversation: 3, run: 2, kind: "text", text: "stale" }); // a ⌘K run's, in flight
    emit({ conversation: 5, run: 9, kind: "text", text: "mine" });
    resolve(9);
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
    let resolve!: (n: number) => void;
    api.ask.mockReturnValue(new Promise((r) => (resolve = r)));
    api.stop.mockResolvedValue(undefined);
    await send(tr, userMsg("hi"));
    await tr.stop();
    expect(api.stop).not.toHaveBeenCalled();
    resolve(9);
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

  it("decided skill runs with no message each get their own answer, in run order, kept across a reconnect", async () => {
    const msg = (id: string, role: string, runs: number[], text = id) => ({
      id,
      role,
      text,
      reasoning: "",
      quoted: [],
      calls: [],
      results: [],
      citations: [],
      runs,
    });
    const h: WitHistory = {
      conversation: 1,
      model: "",
      usage: { input: 0, output: 0, context: 0, history: 0 },
      running: 5,
      messages: [
        msg("q0", "user", [1]),
        msg("a1", "assistant", [1]),
        msg("q5", "user", [5]),
        msg("a6", "assistant", [5], "Go"),
      ],
      approvals: [
        approval({ id: 11, run: 3, callId: "skill", state: "rejected" }),
        approval({ id: 10, run: 2, callId: "skill", state: "done" }),
      ],
    };
    const all = historyToMessages(h);
    expect(all.map((m) => m.id)).toEqual(["q0", "a1", "w2", "w3", "q5", "a6"]);
    const tool = (m: WitUIMessage) => m.parts.filter((p) => p.type === "dynamic-tool");
    expect(tool(all[2]).map((p) => [p.toolCallId, p.state])).toEqual([
      ["skill-10", "output-available"],
    ]);
    expect(tool(all[3]).map((p) => [p.toolCallId, p.state])).toEqual([
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
    emit({ run: 5, kind: "text", text: "!" });
    emit({ run: 5, kind: "done" });
    await done;
    expect(chat.messages.map((m) => m.id)).toEqual(["q0", "a1", "w2", "w3", "q5", "a6"]);
    expect(tool(chat.messages[2]).map((p) => p.state)).toEqual(["output-available"]);
    expect(tool(chat.messages[3]).map((p) => p.state)).toEqual(["output-denied"]);
  });

  it("a live skill's part has the history's ID: a resumed run updates it", async () => {
    const pending = approval({ id: 12, run: 2, callId: "skill", state: "pending" });
    const h: WitHistory = {
      conversation: 1,
      model: "",
      usage: { input: 0, output: 0, context: 0, history: 0 },
      running: 0,
      messages: [],
      approvals: [pending],
    };
    const all = historyToMessages(h);
    expect(all.map((m) => m.id)).toEqual(["w2"]);
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
      kind: "interrupted",
      interrupts: [{ id: "skill", name: "update_draft", args: "" }],
      approvals: [{ ...pending, state: "done" }],
    });
    emit({ run: 2, kind: "done" });
    await done;
    const tools = chat.messages[0].parts.filter((p) => p.type === "dynamic-tool");
    expect(tools.map((p) => [p.toolCallId, p.state])).toEqual([["skill-12", "output-available"]]);
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
  const hist = (conversation: number, runs: number[] = []): WitHistory => ({
    conversation,
    model: "",
    usage: { input: 0, output: 0, context: 0, history: 0 },
    running: 0,
    approvals: [],
    messages: runs.length
      ? [
          {
            id: "q",
            role: "user",
            text: "Q",
            reasoning: "",
            quoted: [],
            calls: [],
            results: [],
            citations: [],
            runs,
          },
        ]
      : [],
  });

  it("switching conversations drops the old stream", async () => {
    const { api, tr, emit } = fake();
    api.ask.mockResolvedValue(7);
    const s = await send(tr, userMsg("hi"));
    await vi.waitFor(() => expect(tr.run).toBe(7));
    emit({ run: 7, kind: "text", text: "a" });
    api.follow.mockResolvedValue(hist(2));
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
    api.follow.mockResolvedValueOnce(hist(2));
    const a = tr.follow();
    expect(await tr.follow()).toEqual(hist(2));
    resolveA(hist(1));
    expect(await a).toBeUndefined();
    expect(tr.conversation).toBe(2);
  });

  it("ask returning after a switch is ignored", async () => {
    const { api, tr, emit } = fake();
    let resolve!: (n: number) => void;
    api.ask.mockReturnValue(new Promise((r) => (resolve = r)));
    await send(tr, userMsg("hi"));
    api.follow.mockResolvedValue(hist(2));
    await tr.follow();
    resolve(9);
    await Promise.resolve();
    emit({ conversation: 5, run: 9, kind: "text", text: "x" });
    expect(tr.run).toBe(0);
    expect(tr.conversation).toBe(2);
  });

  it("history reads the conversation again without a reset; another conversation's is none", async () => {
    const { api, tr } = fake();
    api.follow.mockResolvedValue(hist(1));
    await tr.follow();
    api.follow.mockResolvedValue(hist(1, [7]));
    expect(await tr.history()).toEqual(hist(1, [7]));
    expect(tr.conversation).toBe(1);
    api.follow.mockResolvedValue(hist(3, [7]));
    expect(await tr.history()).toBeUndefined();
  });

  it("watch hears the followed conversation's events only", async () => {
    const { api, tr, emit } = fake();
    api.follow.mockResolvedValue(hist(1));
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
    let resolve!: (n: number) => void;
    api.ask.mockReturnValue(new Promise((r) => (resolve = r)));
    api.stop.mockResolvedValue(undefined);
    await send(tr, userMsg("hi"));
    await tr.stop();
    api.follow.mockResolvedValue(hist(2));
    await tr.follow(); // a switch: the start's result is no longer shown
    resolve(9);
    await vi.waitFor(() => expect(api.stop).toHaveBeenCalledWith(9));
    expect(tr.run).toBe(0);
  });
});
