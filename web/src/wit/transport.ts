import type { ChatTransport, UIMessage, UIMessageChunk } from "ai";
import {
  type Approval,
  type Citation,
  ComposePage,
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

/** What a decided or waiting approval makes of its call (executing: nothing yet). */
function decided(a: Approval): Chunk[] {
  const id = a.callId;
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
      if (ev.result) out.push(result(ev.result));
      break;
    case "interrupted":
      for (const c of ev.interrupts) {
        if (seen.has(c.id)) continue;
        seen.add(c.id);
        out.push(input(c)); // a skill's ("skill") has no tool_call before it
      }
      if (ev.approvals.length > 0) out.push({ type: "data-approvals", data: ev.approvals });
      for (const a of ev.approvals) out.push(...decided(a));
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
 * CallID. A pending approval with no call in the history (a skill's) goes on the last answer.
 */
export function historyToMessages(h: WitHistory): WitUIMessage[] {
  const byCall = new Map(h.approvals.map((a) => [a.callId, a]));
  const shown = new Set<number>();
  const out: WitUIMessage[] = h.messages.map((m) => {
    if (m.role === "user")
      return { id: m.id, role: "user", parts: [{ type: "text", text: m.text }] };
    const parts: Part[] = [];
    const as: Approval[] = [];
    for (const c of m.calls) {
      const a = byCall.get(c.id);
      if (a) {
        shown.add(a.id);
        as.push(a);
      }
      parts.push(
        toolPart(
          c,
          m.results.find((r) => r.callId === c.id),
          a,
        ),
      );
    }
    if (as.length > 0) parts.push({ type: "data-approvals", data: as });
    if (m.text) parts.push({ type: "text", text: m.text, state: "done" });
    if (m.citations.length > 0) parts.push({ type: "data-citations", data: m.citations });
    return { id: m.id, role: "assistant", parts };
  });
  const waiting = h.approvals.filter((a) => a.state === "pending" && !shown.has(a.id));
  if (waiting.length > 0) {
    let last = out.at(-1);
    if (last?.role !== "assistant") {
      last = { id: `w${h.conversation}`, role: "assistant", parts: [] };
      out.push(last);
    }
    for (const a of waiting)
      last.parts.push(toolPart({ id: a.callId, name: a.tool, args: "" }, undefined, a));
    last.parts.push({ type: "data-approvals", data: waiting });
  }
  return out;
}

/** The chunks that rebuild message m (the answer a resumed run goes on with); its calls go in seen. */
function replay(m: WitUIMessage, seen: Set<string>): Chunk[] {
  return m.parts.flatMap((p, i): Chunk[] => {
    if (p.type === "text") {
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

type Api = Pick<typeof ComposePage, "ask" | "skill" | "follow">;
type On = (f: (ev: WitEvent) => void) => () => void;

/**
 * The window's Wit runs as chat streams. It listens to WitStream from construction, before any run
 * starts or follow is called, and never filters by run: the window has one subscription. Events
 * with no stream open wait in a log until the next resume (cleared by a send or a follow).
 */
export class WitTransport implements ChatTransport<WitUIMessage> {
  /** The run followed: the last started, resumed, or seen in an event; Stop cancels it. */
  run = 0;
  private log: WitEvent[] = [];
  private sink?: (ev: WitEvent) => void;
  private resuming?: { open?: WitUIMessage; id: string };
  private off: () => void;

  constructor(
    private api: Api = ComposePage,
    on: On = (f) => events.wit.on(f),
  ) {
    this.off = on((ev) => {
      if (ev.run) this.run = ev.run;
      this.log.push(ev);
      this.sink?.(ev);
    });
  }

  /** Follows the draft's conversation (api.follow); events from now on are kept for resume. */
  follow(): Promise<WitHistory> {
    this.log = [];
    return this.api.follow();
  }

  /** The next reconnect streams the live run, going on with open (the history's last answer, taken out of the chat's messages). */
  resume(open: WitUIMessage | undefined, run: number) {
    this.run = run;
    this.resuming = { open, id: open?.id ?? `live${run}` };
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
    const s = new ReadableStream<Chunk>({
      start: (c) => {
        ctl = c;
        for (const ch of first) c.enqueue(ch);
      },
      cancel: () => end(),
    });
    abortSignal?.addEventListener("abort", end);
    this.sink = push;
    return { s, push, fail };
  }

  async sendMessages({
    messages,
    abortSignal,
  }: Parameters<ChatTransport<WitUIMessage>["sendMessages"]>[0]) {
    const m = messages.findLast((x) => x.role === "user");
    const text = m?.parts.flatMap((p) => (p.type === "text" ? [p.text] : [])).join("") ?? "";
    this.log = [];
    this.resuming = undefined;
    const { s, fail } = this.stream(abortSignal, [], new Set());
    const skill = m?.metadata?.skill;
    (skill ? this.api.skill(skill, m?.metadata?.lang ?? "") : this.api.ask(text)).then(
      (run) => {
        this.run = run;
      },
      (e: unknown) => fail(e instanceof Error ? e.message : String(e)),
    );
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
