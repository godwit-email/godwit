import type { ChatTransport, UIMessage, UIMessageChunk } from "ai";
import {
  type Approval,
  type Citation,
  events,
  type MailHit,
  type WitCall,
  type WitCommand,
  type WitEvent,
  type WitHistory,
  type WitResult,
} from "@/mygo";

/**
 * A question's request: free text (with the mail it quotes, `hits` as its chips), or a skill (with
 * its language) when set. `rewind` asks it in place of the question of that turn (and what
 * followed it), on a host that rewinds. A question read from the history has its `turn`, and
 * `canAskAgain` when the host takes it now.
 */
export type WitMeta = {
  skill?: string;
  lang?: string;
  quoted?: number[];
  hits?: MailHit[];
  rewind?: string;
  turn?: string;
  canAskAgain?: boolean;
  /** An answer's runs' time at work, in milliseconds (core's; none while it streams). */
  elapsed?: number;
  /** When a question was asked, or an answer last answered, in Unix milliseconds. */
  time?: number;
};
/** Citations of the mail a run read; the approvals an interrupt waits on, or decided. */
export type WitData = { citations: Citation[]; approvals: Approval[] };
/** A question or an answer in the panel. Wit's tools are untyped here: dynamic-tool parts. */
export type WitUIMessage = UIMessage<WitMeta, WitData>;
type Chunk = UIMessageChunk<WitMeta, WitData>;
type Part = WitUIMessage["parts"][number];

// The open text and reasoning parts' keys in `seen` (no tool call ID has a NUL).
const TEXT = "\0text";
const THINK = "\0reasoning";

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
  if (ev.kind !== "reasoning" && seen.delete(THINK)) out.push({ type: "reasoning-end", id: THINK });
  if (ev.citations.length > 0) out.push({ type: "data-citations", data: ev.citations });
  switch (ev.kind) {
    case "text":
      if (!seen.has(TEXT)) {
        seen.add(TEXT);
        out.push({ type: "text-start", id: TEXT });
      }
      out.push({ type: "text-delta", id: TEXT, delta: ev.text });
      break;
    case "reasoning":
      if (!seen.has(THINK)) {
        seen.add(THINK);
        out.push({ type: "reasoning-start", id: THINK });
      }
      out.push({ type: "reasoning-delta", id: THINK, delta: ev.text });
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
      if (ev.ends) out.push({ type: "finish" });
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

/** A question's message ID, and its answer's: by their turn (live answers take it too). */
export const questionId = (turn: string) => `q${turn}`;
export const answerId = (turn: string) => `a${turn}`;

/**
 * The history as messages: each turn's question, then its answer's steps in order (its calls with
 * their results, or the approval each waits on, as core placed it) and the approvals that answer no call of it (a
 * skill's: core writes no call for it), each a tool part of its own (partId). label names a
 * command's question (a skill run on the draft).
 */
export function historyToMessages(
  h: WitHistory,
  label: (c: WitCommand) => string = (c) => c.skill,
): WitUIMessage[] {
  const out: WitUIMessage[] = [];
  for (const t of h.turns) {
    const metadata: WitMeta = { turn: t.id, canAskAgain: t.canAskAgain };
    if (t.asked > 0) metadata.time = t.asked;
    if (t.quoted.length)
      Object.assign(metadata, { quoted: t.quoted.map((h) => h.id), hits: t.quoted });
    const text = t.command ? label(t.command) : t.question;
    out.push({ id: questionId(t.id), role: "user", metadata, parts: [{ type: "text", text }] });
    const parts: Part[] = [];
    const as: Approval[] = [];
    for (const s of t.steps) {
      if (s.kind === "reasoning") parts.push({ type: "reasoning", text: s.text, state: "done" });
      else if (s.kind === "text") {
        if (!t.command) parts.push({ type: "text", text: s.text, state: "done" });
      } else {
        const c = t.calls[s.call];
        if (c) {
          if (c.approval) as.push(c.approval);
          parts.push(toolPart(c.call, c.result ?? undefined, c.approval ?? undefined));
        }
      }
    }
    for (const a of t.approvals) {
      as.push(a);
      parts.push(toolPart({ id: partId(a), name: a.tool, args: "" }, undefined, a));
    }
    if (as.length > 0) parts.push({ type: "data-approvals", data: as });
    if (t.citations.length > 0) parts.push({ type: "data-citations", data: t.citations });
    if (parts.length === 0) continue;
    const meta: WitMeta = {};
    if (t.elapsed > 0) meta.elapsed = t.elapsed;
    if (t.answered > 0) meta.time = t.answered;
    out.push({
      id: answerId(t.id),
      role: "assistant",
      metadata: Object.keys(meta).length > 0 ? meta : undefined,
      parts,
    });
  }
  return out;
}

/**
 * The chunks that rebuild message m (the answer a resumed run goes on with); its calls go in seen.
 * Its last text part stays open when nothing came after it: the run's deltas go on in it.
 */
function replay(m: WitUIMessage, seen: Set<string>): Chunk[] {
  const lastText = m.parts.findLastIndex((p) => p.type === "text");
  const lastStep = m.parts.findLastIndex(
    (p) => p.type === "text" || p.type === "reasoning" || p.type === "dynamic-tool",
  );
  const open = lastText === lastStep ? lastText : -1;
  return m.parts.flatMap((p, i): Chunk[] => {
    if (p.type === "text") {
      if (i === open) {
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
    if (p.type === "reasoning") {
      const id = `t${i}`;
      return [
        { type: "reasoning-start", id },
        { type: "reasoning-delta", id, delta: p.text },
        { type: "reasoning-end", id },
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

/** A run started: its ID and the turn it answers. */
export type Started = { run: number; turn: string };

/**
 * The host's runs: ask (or a skill, where the host has them) starts one and resolves to it, in
 * place of the question of turn rewind and what followed when given (a host that `rewinds`;
 * refused, nothing changes); follow reads the conversation the host shows (subscribing to it);
 * stop cancels a run; compact (where the host has it) summarizes the conversation's older turns.
 */
export type WitApi = {
  ask(text: string, quoted?: number[], rewind?: string): Promise<Started>;
  skill?(id: string, lang: string): Promise<Started>;
  follow(): Promise<WitHistory>;
  stop(run: number): Promise<void>;
  compact?(): Promise<void>;
  rewinds?: boolean;
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
  private turnOf = new Map<number, string>(); // the turn each run answers, as its events name it
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
    if (ev.run && ev.turn) this.turnOf.set(ev.run, ev.turn);
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
    this.runsOf = runsOf(h);
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
    this.runsOf = runsOf(h);
    return h;
  }

  /** Whether m is run's answer: by the history's runs, or by the turn the run's events name. */
  owns(m: WitUIMessage | undefined, run: number): boolean {
    if (m?.role !== "assistant") return false;
    const turn = this.turnOf.get(run);
    return (
      (turn !== undefined && m.id === answerId(turn)) ||
      (this.runsOf.get(m.id)?.includes(run) ?? false)
    );
  }

  /**
   * The next reconnect streams the live run, going on with open (its own answer, the chat's last) or
   * else in an answer of its own, its turn's (turn: the run's, when the history names it).
   */
  resume(open: WitUIMessage | undefined, run: number, turn?: string) {
    this.run = run;
    const t = turn ?? this.turnOf.get(run);
    this.resuming = { open, id: open?.id ?? (t ? answerId(t) : `live${run}`) };
  }

  /** Stops the run: one still starting is stopped as soon as its ID is known; none, nothing. */
  stop(): Promise<void> {
    if (this.starting) {
      this.stopStart?.();
      return Promise.resolve();
    }
    return this.run ? this.api.stop(this.run) : Promise.resolve();
  }

  /** Whether the host compacts a conversation when asked. */
  get compacts(): boolean {
    return !!this.api.compact;
  }

  /** Summarizes the conversation's older turns now (the host's compact). */
  compact(): Promise<void> {
    return this.api.compact ? this.api.compact() : Promise.resolve();
  }

  /** Whether a question can be asked again in place (the host rewinds). */
  get rewinds(): boolean {
    return !!this.api.rewinds;
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
    const quoted = m?.metadata?.quoted;
    // A retry (regenerate) asks in place again: a run that saved nothing replaced nothing.
    const at = this.api.rewinds ? m?.metadata?.rewind : undefined;
    (skill && this.api.skill
      ? this.api.skill(skill, m?.metadata?.lang ?? "")
      : at !== undefined
        ? this.api.ask(text, quoted ?? [], at)
        : quoted?.length
          ? this.api.ask(text, quoted)
          : this.api.ask(text)
    )
      .then(
        ({ run, turn }) => {
          if (g !== this.gen) return req.stop ? this.api.stop(run) : undefined; // a switch came: not shown
          this.turnOf.set(run, turn);
          named(answerId(turn)); // before its events: none goes through until the conversation is adopted
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

/** The history's answers' runs, by message ID. */
function runsOf(h: WitHistory): Map<string, number[]> {
  return new Map(h.turns.map((t) => [answerId(t.id), t.runs]));
}

/** h's approvals: on their calls, then each turn's own, in turn order. */
export function approvalsOf(h: WitHistory): Approval[] {
  return h.turns.flatMap((t) => [
    ...t.calls.flatMap((c) => (c.approval ? [c.approval] : [])),
    ...t.approvals,
  ]);
}
