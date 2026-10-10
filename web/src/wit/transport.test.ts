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

describe("toChunks", () => {
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

  it("maps results to output or error", () => {
    const seen = new Set<string>();
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
      toolCallId: "skill",
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
    const cite = { n: 1, messageId: 5, subject: "s", from: "f", date: 0, suspect: true };
    const h: WitHistory = {
      conversation: 1,
      running: 0,
      messages: [
        { id: "q0", role: "user", text: "Q", calls: [], results: [], citations: [], runs: [1] },
        {
          id: "a1",
          role: "assistant",
          text: "A",
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
      ["skill", "output-available"],
    ]);
    expect(m[1].parts).toContainEqual({ type: "text", text: "A", state: "done" });
    expect(m[1].parts).toContainEqual({ type: "data-citations", data: [cite] });
    const pending = m[3].parts.find((p) => p.type === "dynamic-tool");
    expect(pending).toMatchObject({
      toolCallId: "skill",
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
      running: 7,
      approvals: [],
      messages: [
        { id: "q0", role: "user", text: "Q", calls: [], results: [], citations: [], runs: [1] },
        {
          id: "a1",
          role: "assistant",
          text: "Hel",
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
    const got = await tr.follow();
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
});
