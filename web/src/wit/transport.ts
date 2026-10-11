import type { ChatTransport, UIMessage, UIMessageChunk } from "ai";
import {
  type Approval,
  type Citation,
  events,
  type WitCall,
  type WitEvent,
  type WitHistory,
  type WitResult,
} from "@/mygo";

/** A question's request: free text, or a skill (with its language) when set. */
export type WitMeta = { skill?: string; lang?: string };
/** Citations of the mail a run read; the approvals an interrupt waits on, or decided. */
export type WitData = { citations: Citation[]; approvals: Approval[] };
/** A question or an answer in the panel. Wit's tools are untyped here: dynamic-tool parts. */
export type WitUIMessage = UIMessage<WitMeta, WitData>;
type Chunk = UIMessageChunk<WitMeta, WitData>;
type Part = WitUIMessage["parts"][number];

// The open text part's key in `seen` (no tool call ID has a NUL).
const TEXT = "\0text";

const json = (s: string): unknown => {
  if (!s) return {};
  try {
    return JSON.parse(s);
  } catch {
    return s;
  }
};

const input = (c: WitCall): Chunk => ({
  type: "tool-input-available",
  toolCallId: c.id,
  toolName: c.name,
  input: json(c.args),
  dynamic: true,
});

const result = (r: WitResult): Chunk =>
  r.error
    ? { type: "tool-output-error", toolCallId: r.callId, errorText: r.error }
    : { type: "tool-output-available", toolCallId: r.callId, output: json(r.output) };

/**
 * The tool part ID of approval a's call: the call's own when it was shown, else one of its own
 * (a skill's call ID is always "skill": the parts of different skills must not collide).
 */
const partId = (a: Approval, seen?: Set<string>) =>
  seen?.has(a.callId) ? a.callId : `${a.callId}-${a.id}`;

/** What a decided or waiting approval makes of its call's part id (executing: nothing yet). */
function decided(a: Approval, id: string): Chunk[] {
  switch (a.state) {
    case "pending":
      return [{ type: "tool-approval-request", approvalId: String(a.id), toolCallId: id }];
    case "done":
      return [{ type: "tool-output-available", toolCallId: id, output: { summary: a.summary } }];
    case "failed":
      return [{ type: "tool-output-error", toolCallId: id, errorText: a.error || a.state }];
    case "rejected":
    case "stale":
      return [{ type: "tool-output-denied", toolCallId: id }];
  }
  return [];
}

/** Whether ev ends the run: an interrupt whose approvals were all decided at once (a first generation) resumes it (webui ends). */
export function ends(ev: WitEvent): boolean {
  if (ev.kind === "done" || ev.kind === "error") return true;
  return (
    ev.kind === "interrupted" &&
    (ev.approvals.length === 0 || ev.approvals.some((a) => a.state === "pending"))
  );
}

/**
 * One event of a run as chunks of its answer. `seen` is the stream's: the tool calls already shown
 * and whether a text part is open. An error's text is its i18n key ("" for a stop: no failure).
 */
export function toChunks(ev: WitEvent, seen: Set<string>): Chunk[] {
  const out: Chunk[] = [];
  const closeText = () => {
    if (seen.delete(TEXT)) out.push({ type: "text-end", id: TEXT });
  };
  if (ev.kind !== "text") closeText();
  if (ev.citations.length > 0) out.push({ type: "data-citations", data: ev.citations });
  switch (ev.kind) {
    case "text":
      if (!seen.has(TEXT)) {
        seen.add(TEXT);
        out.push({ type: "text-start", id: TEXT });
      }
      out.push({ type: "text-delta", id: TEXT, delta: ev.text });
      break;
    case "tool_call":
      if (ev.call) {
        seen.add(ev.call.id);
        out.push(input(ev.call));
      }
      break;
    case "tool_result":
      // only a shown call's: the SDK fails on a part it does not have
      if (ev.result && seen.has(ev.result.callId)) out.push(result(ev.result));
      break;
    case "interrupted":
      for (const c of ev.interrupts) {
        // a skill's ("skill") has no tool_call before it: its part gets an ID of its own
        const a = ev.approvals.find((x) => x.callId === c.id);
        const id = seen.has(c.id) || !a ? c.id : partId(a);
        if (seen.has(id)) continue;
        seen.add(id);
        out.push(input({ ...c, id }));
      }
      if (ev.approvals.length > 0) out.push({ type: "data-approvals", data: ev.approvals });
      for (const a of ev.approvals) {
        const id = partId(a, seen);
        if (seen.has(id)) out.push(...decided(a, id));
      }
      if (ends(ev)) out.push({ type: "finish" });
      break;
    case "done":
      out.push({ type: "finish" });
      break;
    case "error":
      out.push(ev.error ? { type: "error", errorText: ev.error } : { type: "finish" });
      break;
  }
  return out;
}

function toolPart(c: WitCall, r: WitResult | undefined, a: Approval | undefined): Part {
  const base = {
    type: "dynamic-tool",
    toolCallId: c.id,
    toolName: c.name,
    input: json(c.args),
  } as const;
  if (r)
    return r.error
      ? { ...base, state: "output-error", errorText: r.error }
      : { ...base, state: "output-available", output: json(r.output) };
  switch (a?.state) {
    case "pending":
      return { ...base, state: "approval-requested", approval: { id: String(a.id) } };
    case "done":
      return { ...base, state: "output-available", output: { summary: a.summary } };
    case "failed":
      return { ...base, state: "output-error", errorText: a.error || a.state };
    case "rejected":
    case "stale":
      return { ...base, state: "output-denied", approval: { id: String(a.id), approved: false } };
  }
  return { ...base, state: "input-available" };
}

/**
 * The history as messages: each answer's calls with their results, or their approval's state by
 * CallID. An approval with no call in the history (a skill's: core writes no message for it) gets
 * a tool part of its own (partId), in any state: on its run's answer, else a new answer after its
 * run's question, else a message of its own (one per run) placed in run order among the others.
 */
export function historyToMessages(h: WitHistory): WitUIMessage[] {
  const byCall = new Map(h.approvals.map((a) => [a.callId, a]));
  const calls = new Set(h.messages.flatMap((m) => m.calls.map((c) => c.id)));
  const ms = h.messages;
  const on = new Map<number, Approval[]>(); // on answer i
  const after = new Map<number, Approval[]>(); // after question i
  const alone = new Map<number, Approval[]>(); // by run: a run with no message
  const put = (m: Map<number, Approval[]>, i: number, a: Approval) =>
    m.set(i, [...(m.get(i) ?? []), a]);
  for (const a of h.approvals) {
    if (calls.has(a.callId)) continue;
    const i = ms.findIndex((m) => m.role === "assistant" && m.runs.includes(a.run));
    const q = ms.findLastIndex((m) => m.runs.includes(a.run));
    if (i >= 0) put(on, i, a);
    else if (q >= 0) put(after, q, a);
    else put(alone, a.run, a);
  }
  const synth = (as: Approval[]) =>
    as.map((a) => toolPart({ id: partId(a), name: a.tool, args: "" }, undefined, a));
  const answer = (as: Approval[]): WitUIMessage => ({
    id: `w${as[0].run}`,
    role: "assistant",
    parts: [...synth(as), { type: "data-approvals", data: as }],
  });
  const out: { m: WitUIMessage; runs: number[] }[] = [];
  ms.forEach((m, i) => {
    if (m.role === "user") {
      out.push({
        m: { id: m.id, role: "user", parts: [{ type: "text", text: m.text }] },
        runs: m.runs,
      });
    } else {
      const parts: Part[] = [];
      const as: Approval[] = [];
      for (const c of m.calls) {
        const a = byCall.get(c.id);
        if (a) as.push(a);
        parts.push(
          toolPart(
            c,
            m.results.find((r) => r.callId === c.id),
            a,
          ),
        );
      }
      const own = on.get(i) ?? [];
      parts.push(...synth(own));
      as.push(...own);
      if (as.length > 0) parts.push({ type: "data-approvals", data: as });
      if (m.text) parts.push({ type: "text", text: m.text, state: "done" });
      if (m.citations.length > 0) parts.push({ type: "data-citations", data: m.citations });
      out.push({ m: { id: m.id, role: "assistant", parts }, runs: m.runs });
    }
    const as = after.get(i);
    if (as) out.push({ m: answer(as), runs: [as[0].run] });
  });
  for (const [run, as] of [...alone].sort((x, y) => x[0] - y[0])) {
    const k = out.findIndex((o) => o.runs.length > 0 && Math.min(...o.runs) > run);
    out.splice(k < 0 ? out.length : k, 0, { m: answer(as), runs: [run] });
  }
  return out.map((o) => o.m);
}

/**
 * The chunks that rebuild message m (the answer a resumed run goes on with); its calls go in seen.
 * Its last text part stays open: the run's deltas go on in it.
 */
function replay(m: WitUIMessage, seen: Set<string>): Chunk[] {
  const lastText = m.parts.findLastIndex((p) => p.type === "text");
  return m.parts.flatMap((p, i): Chunk[] => {
    if (p.type === "text") {
      if (i === lastText) {
        seen.add(TEXT);
        return [
          { type: "text-start", id: TEXT },
          { type: "text-delta", id: TEXT, delta: p.text },
        ];
      }
      const id = `r${i}`;
      return [
        { type: "text-start", id },
        { type: "text-delta", id, delta: p.text },
        { type: "text-end", id },
      ];
    }
    if (p.type === "data-citations" || p.type === "data-approvals") return [p as Chunk];
    if (p.type !== "dynamic-tool") return [];
    seen.add(p.toolCallId);
    const c = p.toolCallId;
    const first: Chunk = {
      type: "tool-input-available",
      toolCallId: c,
      toolName: p.toolName,
      input: p.input,
      dynamic: true,
    };
    switch (p.state) {
      case "output-available":
        return [first, { type: "tool-output-available", toolCallId: c, output: p.output }];
      case "output-error":
        return [first, { type: "tool-output-error", toolCallId: c, errorText: p.errorText }];
      case "output-denied":
        return [first, { type: "tool-output-denied", toolCallId: c }];
      case "approval-requested":
        return [first, { type: "tool-approval-request", approvalId: p.approval.id, toolCallId: c }];
    }
    return [first];
  });
}

/**
 * The host's runs: ask (or a skill, where the host has them) starts one and resolves to its ID;
 * follow reads the conversation the host shows (subscribing to it); stop cancels a run.
 */
export type WitApi = {
  ask(text: string): Promise<number>;
  skill?(id: string, lang: string): Promise<number>;
  follow(): Promise<WitHistory>;
  stop(run: number): Promise<void>;
};
type On = (f: (ev: WitEvent) => void) => () => void;

/**
 * The window's Wit runs as chat streams. It listens to WitStream from construction, before any run
 * starts or follow is called, and never filters by run: the window has one subscription. It keeps
 * only the conversation followed: from follow's history, or a started run's (its first event);
 * events wait while that is unknown, and other conversations' (in flight from a switch) are
 * dropped. Events with no stream open wait in a log until the next resume (cleared by a send or a
 * follow). Each follow or send is a selection: the newest wins, an older one's result is ignored,
 * and a follow ends the stream open (a switch drops the old conversation's stream).
 */
export class WitTransport implements ChatTransport<WitUIMessage> {
  /** The run followed: the last started, resumed, or seen in an event; Stop cancels it. */
  run = 0;
  private conv?: number; // the conversation followed; undefined while unknown
  private want = 0; // a started run, whose first event names the conversation
  private early: WitEvent[] = []; // while conv is unknown
  private starting = false; // a run start is in flight: its ID is not known yet
  private stopStart?: () => void; // marks the start in flight to be stopped once its ID is known
  private log: WitEvent[] = [];
  private sink?: (ev: WitEvent) => void;
  private resuming?: { open?: WitUIMessage; id: string };
  private runsOf = new Map<string, number[]>(); // the history's answers' runs, by message ID
  private gen = 0; // the selection: bumped by each follow and send
  private endOpen?: () => void; // ends the stream open
  private watchers = new Set<(ev: WitEvent) => void>();
  private off: () => void;

  constructor(
    private api: WitApi,
    on: On = (f) => events.wit.on(f),
  ) {
    this.off = on((ev) => this.receive(ev));
  }

  private receive(ev: WitEvent) {
    if (this.conv === undefined) {
      this.early.push(ev);
      if (this.want && ev.run === this.want) this.adopt(ev.conversation);
      return;
    }
    if (ev.conversation !== this.conv) return;
    if (ev.run) this.run = ev.run;
    this.log.push(ev);
    this.sink?.(ev);
    for (const f of this.watchers) f(ev);
  }

  /** f hears each event of the conversation followed, after the stream does; returns the remover. */
  watch(f: (ev: WitEvent) => void): () => void {
    this.watchers.add(f);
    return () => this.watchers.delete(f);
  }

  // Follows conversation conv: the events that waited go through, if of it.
  private adopt(conv: number) {
    this.conv = conv;
    const early = this.early;
    this.early = [];
    for (const ev of early) this.receive(ev);
  }

  // Forgets the conversation followed (its stream ends): a follow or a run start names the next.
  private reset() {
    this.endOpen?.();
    this.starting = false; // a start in flight keeps its own stop mark (sendMessages)
    this.stopStart = undefined;
    this.conv = undefined;
    this.want = 0;
    this.early = [];
    this.log = [];
  }

  /** The conversation followed; undefined while unknown. */
  get conversation(): number | undefined {
    return this.conv;
  }

  /**
   * Follows the host's conversation (api.follow); events from now on are kept for resume. Undefined
   * when a newer follow or send came meanwhile.
   */
  async follow(): Promise<WitHistory | undefined> {
    const g = ++this.gen;
    this.reset();
    const h = await this.api.follow();
    if (g !== this.gen) return undefined;
    this.runsOf = new Map(h.messages.map((m) => [m.id, m.runs]));
    this.adopt(h.conversation);
    return h;
  }

  /**
   * The conversation followed, read again (a run's end reconciles with it) without forgetting it.
   * Undefined when a selection came meanwhile or the host now shows another conversation.
   */
  async history(): Promise<WitHistory | undefined> {
    const g = this.gen;
    const conv = this.conv;
    if (conv === undefined) return undefined; // a switch under way: no read to race its follow
    const h = await this.api.follow();
    if (g !== this.gen || h.conversation !== conv) return undefined;
    this.runsOf = new Map(h.messages.map((m) => [m.id, m.runs]));
    return h;
  }

  /**
   * Whether m is run's answer: by the history's runs, or the ID a live run's answer takes
   * (`live<run>`; `w<run>` for a run's synthetic answer).
   */
  owns(m: WitUIMessage | undefined, run: number): boolean {
    if (m?.role !== "assistant") return false;
    return (
      m.id === `live${run}` || m.id === `w${run}` || (this.runsOf.get(m.id)?.includes(run) ?? false)
    );
  }

  /** The next reconnect streams the live run, going on with open (its own answer, the chat's last) or else in an answer of its own. */
  resume(open: WitUIMessage | undefined, run: number) {
    this.run = run;
    this.resuming = { open, id: open?.id ?? `live${run}` };
  }

  /** Stops the run: one still starting is stopped as soon as its ID is known; none, nothing. */
  stop(): Promise<void> {
    if (this.starting) {
      this.stopStart?.();
      return Promise.resolve();
    }
    return this.run ? this.api.stop(this.run) : Promise.resolve();
  }

  close() {
    this.off();
  }

  // A stream fed by the events from now on, until one ends the run.
  private stream(abortSignal: AbortSignal | undefined, first: Chunk[], seen: Set<string>) {
    let ctl!: ReadableStreamDefaultController<Chunk>;
    let open = true;
    const end = () => {
      if (!open) return;
      open = false;
      if (this.sink === push) this.sink = undefined;
      if (this.endOpen === end) this.endOpen = undefined;
      try {
        ctl.close();
      } catch {
        // already canceled
      }
    };
    const push = (ev: WitEvent) => {
      if (!open) return;
      const cs = toChunks(ev, seen);
      for (const c of cs) ctl.enqueue(c);
      if (cs.some((c) => c.type === "finish" || c.type === "error")) {
        this.resuming = undefined;
        this.log = [];
        end();
      }
    };
    const fail = (key: string) => {
      if (!open) return;
      ctl.enqueue(key ? { type: "error", errorText: key } : { type: "finish" });
      end();
    };
    // Names the answer before anything of it is written (sendMessages: once its run is known).
    const named = (id: string) => {
      if (open) ctl.enqueue({ type: "start", messageId: id });
    };
    const s = new ReadableStream<Chunk>({
      start: (c) => {
        ctl = c;
        for (const ch of first) c.enqueue(ch);
      },
      cancel: () => end(),
    });
    abortSignal?.addEventListener("abort", end);
    this.sink = push;
    this.endOpen = end;
    return { s, push, fail, named };
  }

  async sendMessages({
    messages,
    abortSignal,
  }: Parameters<ChatTransport<WitUIMessage>["sendMessages"]>[0]) {
    const m = messages.findLast((x) => x.role === "user");
    const text = m?.parts.flatMap((p) => (p.type === "text" ? [p.text] : [])).join("") ?? "";
    const g = ++this.gen;
    this.reset();
    this.resuming = undefined;
    this.run = 0;
    this.starting = true;
    // Stop's mark belongs to this start, not to what is shown: a switch drops its result, not the stop.
    const req = { stop: false };
    this.stopStart = () => {
      req.stop = true;
    };
    const { s, fail, named } = this.stream(abortSignal, [], new Set());
    const skill = m?.metadata?.skill;
    (skill && this.api.skill ? this.api.skill(skill, m?.metadata?.lang ?? "") : this.api.ask(text))
      .then(
        (run) => {
          if (g !== this.gen) return req.stop ? this.api.stop(run) : undefined; // a switch came: not shown
          named(`live${run}`); // before its events: none goes through until the conversation is adopted
          this.run = this.want = run;
          const ev = this.early.find((e) => e.run === run);
          if (ev) this.adopt(ev.conversation);
          if (req.stop) return this.api.stop(run);
        },
        (e: unknown) => {
          if (g !== this.gen) return;
          this.early = []; // nothing started: what waited is another run's
          fail(e instanceof Error ? e.message : String(e));
        },
      )
      .catch(() => {}) // ponytail: a failed deferred stop is dropped; Stop stays to try again
      .finally(() => {
        if (g === this.gen) {
          this.starting = false;
          this.stopStart = undefined;
        }
      });
    return s;
  }

  async reconnectToStream({
    abortSignal,
  }: Parameters<ChatTransport<WitUIMessage>["reconnectToStream"]>[0]) {
    const r = this.resuming;
    if (!r) return null;
    const seen = new Set<string>();
    // The answer's own ID: a second reconnect (StrictMode) replaces what the first wrote.
    const first: Chunk[] = [
      { type: "start", messageId: r.id },
      ...(r.open ? replay(r.open, seen) : []),
    ];
    const { s, push } = this.stream(abortSignal, first, seen);
    for (const ev of [...this.log]) push(ev); // what came since follow, then the live events
    return s;
  }
}
