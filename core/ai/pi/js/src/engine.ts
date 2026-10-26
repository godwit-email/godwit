import "./web";
import { runAgentLoop, runAgentLoopContinue, runToolCall, type AgentMessage, type AgentTool } from "@earendil-works/pi-agent-core";
import type { AssistantMessage, AssistantMessageEvent, Message } from "@earendil-works/pi-ai";
import { stream as anthropicMessages } from "@earendil-works/pi-ai/api/anthropic-messages";
import { stream as openaiCodexResponses } from "@earendil-works/pi-ai/api/openai-codex-responses";
import { stream as openaiCompletions } from "@earendil-works/pi-ai/api/openai-completions";
import { stream as openaiResponses } from "@earendil-works/pi-ai/api/openai-responses";
import { postClassifierRequest } from "@earendil-works/pi-ai/api/classifier-shared";
import { normalizeContext, toToolDeclaration } from "@earendil-works/pi-ai/utils/transcript";

declare const hostEmit: (kind: "text" | "reasoning" | "refused", text: string) => void;
// hostArgs rewrites a call's arguments (Request.Args); it throws to refuse the call.
declare const hostArgs: (args: string) => string;
// hostGate decides a call before any call of its round runs: true holds it
// for the user (an approval or a question).
declare const hostGate: (id: string, name: string, args: string) => boolean;
// hostTool runs a call in Go: its output as JSON, or its error; interrupt:
// it waits for the user instead.
declare const hostTool: (id: string, name: string, args: string) => Promise<{ output: string; error: string; interrupt: boolean }>;

// Request is pi.Request, as JSON.
interface Request {
  api: "openai-completions" | "anthropic-messages" | "openai-responses" | "openai-codex-responses";
  baseUrl: string;
  model: string;
  apiKey: string;
  system: string;
  prompt: string;
  effort: "" | "low" | "medium" | "high";
  maxTokens: number;
  thinkingBudget: number;
  session: string;
}

type Payload = Record<string, unknown> & { thinking?: Record<string, unknown> };

// service is what every request of a run to req's service shares: the model,
// the options that send each request as the Genkit path does, and what the
// service's last reply told of how it ended. system is what onPayload sends
// as a Responses request's instructions.
function service(req: Request) {
  const model = {
    id: req.model,
    name: req.model,
    api: req.api,
    provider: "godwit",
    baseUrl: req.baseUrl,
    reasoning: req.effort !== "",
    input: ["text" as const],
    contextWindow: 0, // unknown here; nothing below reads it
    maxTokens: req.maxTokens, // 0 sends none where the API allows it: the service's own limit applies, as before
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    // pi-ai would send a reasoning model's system prompt as "developer" and
    // store: false to any service it does not know; many compatible services
    // reject both, and the Genkit path sends neither.
    compat: { supportsDeveloperRole: false, supportsStore: false },
  };
  const st = {
    code: "", // the error code the service last answered with
    blocked: false, // a Responses refusal or content filter: a withheld reply
    incomplete: "", // why a Responses reply ended incomplete
    system: req.system,
    strict: {} as Record<string, boolean>, // each tool's, by name
  };
  const keyless = req.apiKey === "";
  const options = {
    // pi-ai insists on a key; a keyless service (a local gateway) gets a
    // placeholder that never leaves: the fetch below drops it.
    apiKey: req.apiKey || "godwit-no-key",
    async fetch(url: string, init: { headers?: unknown } = {}) {
      const headers = new Headers(init.headers);
      if (keyless) {
        headers.delete("authorization");
        headers.delete("x-api-key");
      }
      st.code = "";
      const r = await fetch(url, { ...init, headers });
      if (r.ok) return r;
      const text = await r.text();
      try {
        const e = JSON.parse(text);
        st.code = String(e?.error?.code ?? e?.code ?? "");
      } catch {}
      return new Response(text, { status: r.status, headers: r.headers });
    },
    onProviderStreamEvent(ev: any) {
      if (req.api === "openai-completions") {
        for (const c of ev?.choices ?? []) if (c?.finish_reason) c.finish_reason = finishReason(c.finish_reason);
        return;
      }
      switch (ev?.type) {
        case "response.refusal.delta":
        case "response.refusal.done":
          st.blocked = true;
          break;
        case "response.output_item.done":
          st.blocked ||= ev.item?.type === "message" && (ev.item.content ?? []).some((c: any) => c?.type === "refusal");
          break;
        case "response.incomplete":
          st.incomplete = ev.response?.incomplete_details?.reason ?? "unknown";
          break;
        case "response.failed":
          st.code = String(ev.response?.error?.code ?? "");
          break;
      }
    },
    // Each request goes as the Genkit path sends it.
    onPayload(p: Payload) {
      st.blocked = false;
      st.incomplete = "";
      if (req.api === "openai-responses") {
        p.include = ["reasoning.encrypted_content"]; // stateless: reasoning comes back encrypted, always
        if (st.system) p.instructions = st.system;
      }
      if (req.api === "openai-codex-responses") delete p.text; // pi-ai's low verbosity
      // Responses takes a function without strict as strict; Genkit sends each tool's.
      if (req.api.endsWith("responses") && Array.isArray(p.tools)) {
        for (const t of p.tools) if (t?.type === "function") t.strict = st.strict[t.name] ?? false;
      }
      if (p.thinking) delete p.thinking.display;
      return p;
    },
    // A conversation's turns repeat its history: cache it, keyed by the
    // conversation; a one-off prompt is not asked again, so it would pay
    // Anthropic's cache write for nothing.
    cacheRetention: req.session ? ("short" as const) : ("none" as const),
    sessionId: req.session || undefined,
    transport: "sse" as const, // the Codex backend's WebSocket is not supported here
    headers: req.api === "openai-codex-responses" ? { originator: "godwit" } : undefined,
    maxRetries: 2, // the SDKs' default, which the Genkit path uses
    identity: { userAgent: { custom: "Godwit" } },
    reasoningEffort: req.effort || undefined,
    // Anthropic: budget-based thinking, as Genkit sends it
    thinkingEnabled: req.effort !== "",
    thinkingBudgetTokens: req.thinkingBudget,
    interleavedThinking: false,
    maxTokens: req.maxTokens || undefined,
  };
  const streams = {
    "openai-completions": openaiCompletions,
    "anthropic-messages": anthropicMessages,
    "openai-responses": openaiResponses,
    "openai-codex-responses": openaiCodexResponses,
  };
  // Responses: pi-ai ends each reasoning summary with "\n\n"; Genkit puts it
  // between summaries, so it goes out only before the next one.
  let gap = false;
  return {
    st,
    model,
    options,
    stream: (context: unknown, extra?: object) => (streams[req.api] as any)(model, context, { ...options, ...extra }),
    // Responses sends the system prompt as instructions (onPayload).
    systemPrompt: (system: string) => (req.api === "openai-responses" ? undefined : system || undefined),
    // relay sends a reply's text and readable reasoning out as they arrive.
    relay(ev: AssistantMessageEvent) {
      if (st.blocked) return; // a refusal is no answer: none of it is shown
      if (ev.type === "text_delta") hostEmit("text", ev.delta);
      else if (ev.type === "thinking_delta") {
        if (ev.delta === "\n\n" && req.api.endsWith("responses")) gap = true;
        else {
          if (gap) hostEmit("reasoning", "\n\n");
          gap = false;
          hostEmit("reasoning", ev.delta);
        }
      }
    },
    // stopReason is how reply m, the last, ended.
    stopReason(m: AssistantMessage): string {
      if (st.blocked || st.incomplete === "content_filter") return "blocked";
      if (m.stopReason === "error" && m.errorMessage?.startsWith(finishPrefix)) // a Chat Completions reply that ended abnormally
        return m.errorMessage === finishPrefix + "content_filter" ? "blocked" : "incomplete";
      if (st.incomplete && st.incomplete !== "max_output_tokens") return "incomplete";
      return m.stopReason;
    },
  };
}

// finishPrefix starts the error pi-ai gives a Chat Completions reply that
// ended with any finish_reason but stop, length and tool_calls.
const finishPrefix = "Provider finish_reason: ";

// finishReason is a Chat Completions finish_reason as the Genkit path read it,
// in the words pi-ai knows: one neither knew is a normal end, not a failure.
function finishReason(r: string): string {
  switch (r) {
    case "stop":
    case "length":
    case "tool_calls":
    case "function_call":
    case "content_filter":
    case "error":
    case "network_error":
      return r;
    case "model_context_window_exceeded":
      return "length";
    case "sensitive":
      return "content_filter";
    case "insufficient_system_resource":
      return "error";
  }
  return "stop"; // end_turn (xAI) and any other
}

// pi-ai's input leaves out the cached tokens; Godwit's is the whole prompt.
const inputOf = (m: AssistantMessage) => m.usage.input + m.usage.cacheRead + m.usage.cacheWrite;

// generate streams one reply to a one-off prompt: its text and readable
// reasoning go out through hostEmit as they arrive; the result is how it ended.
export async function generate(req: Request) {
  const svc = service(req);
  const s = svc.stream(normalizeContext({ systemPrompt: svc.systemPrompt(req.system), messages: [{ role: "user", content: req.prompt, timestamp: Date.now() }] }));
  for await (const ev of s) svc.relay(ev);
  const m = await s.result();
  return { stopReason: svc.stopReason(m), error: m.errorMessage ?? "", code: svc.st.code, input: inputOf(m), output: m.usage.output, cached: m.usage.cacheRead };
}

// AgentRequest is pi.AgentRequest, as JSON.
interface AgentRequest extends Request {
  messages: Message[];
  answers?: Record<string, { text: string; isError: boolean }>;
  tools: { name: string; description: string; parameters: Record<string, unknown>; strict: boolean }[];
  maxTurns: number;
  limitNote: string;
}

// agent runs one turn of a conversation on pi-agent-core's loop: messages
// end with the question, each tool call runs in Go (hostTool). Past maxTurns
// rounds of calls (run or refused) the next round's calls are refused and
// dropped, and the model answers once more, told so by limitNote, with no
// tool choice. A round with a call held for the user (hostGate, or a tool
// that interrupts as it runs) runs no call that was not running yet and ends
// the turn: held lists those calls, deferred the ones held back with them.
// Given answers, the turn resumes such a round instead: messages end with
// it, answers give the results of its held calls (and of those that ran),
// and the calls held back run first.
export async function agent(req: AgentRequest) {
  const svc = service(req);
  const ran = new Set<string>(); // calls hostTool ran: it reported them
  const held = new Set<string>();
  const deferred = new Set<string>();
  const asked = new Map<string, unknown>(); // each call's arguments as the model wrote them
  const replies: AssistantMessage[] = []; // every reply the turn was billed for, the dropped one too
  let rounds = 0; // replies that called tools
  let limited: AssistantMessage | undefined;
  for (const t of req.tools) svc.st.strict[t.name] = t.strict;
  const tools: AgentTool[] = req.tools.map(({ strict: _, ...t }) => ({
    ...t,
    label: t.name,
    parameters: t.parameters as AgentTool["parameters"],
    prepareArguments: (args: unknown) => JSON.parse(hostArgs(JSON.stringify(args ?? {}))),
    // pi-agent-core runs a round's calls only once beforeToolCall has seen every one of them
    async execute(id: string, params: unknown) {
      const stop = { content: [{ type: "text" as const, text: "held" }], details: {}, terminate: true };
      if (held.size > 0) {
        deferred.add(id);
        return stop;
      }
      ran.add(id);
      const r = await hostTool(id, t.name, JSON.stringify(params ?? {}));
      if (r.interrupt) {
        held.add(id);
        return stop;
      }
      if (r.error) return { content: [{ type: "text" as const, text: JSON.stringify({ error: r.error }) }], details: {}, isError: true };
      return { content: [{ type: "text" as const, text: r.output }], details: {} };
    },
  }));
  const decls = tools.map(toToolDeclaration);
  const transcript = (system: string, messages: Message[]) =>
    normalizeContext({ systemPrompt: svc.systemPrompt(system), tools: decls, messages }).messages;
  // refused tells Go of a call refused before it ran (unknown tool, invalid or rewritten arguments).
  const refused = (id: string, name: string, args: unknown, text: string) =>
    hostEmit("refused", JSON.stringify({ id, name, args: args ?? {}, error: text }));
  const config = {
    model: svc.model,
    ...svc.options,
    convertToLlm: (msgs: AgentMessage[]) => msgs as Message[],
    async beforeToolCall({ assistantMessage, toolCall, args }: { assistantMessage: AssistantMessage; toolCall: { id: string; name: string }; args: unknown }) {
      if (limited === assistantMessage || hostGate(toolCall.id, toolCall.name, JSON.stringify(args ?? {}))) {
        if (limited !== assistantMessage) held.add(toolCall.id);
        return { block: true, terminate: true };
      }
      return undefined;
    },
    // A round whose calls were all refused never reaches beforeToolCall; a
    // held round ends the turn whatever its other calls returned.
    finishTurn: () => (limited || held.size > 0 ? { action: "end" as const } : undefined),
  };
  const emit = async (ev: any) => {
    if (ev.type === "message_end" && ev.message.role === "assistant") {
      // before its calls are prepared: past maxTurns rounds none of them runs
      replies.push(ev.message);
      if (ev.message.content.some((c: any) => c.type === "toolCall") && ++rounds > req.maxTurns) limited = ev.message;
    } else if (ev.type === "message_update") {
      svc.relay(ev.assistantMessageEvent);
    } else if (ev.type === "tool_execution_start") {
      asked.set(ev.toolCallId, ev.args);
    } else if (ev.type === "tool_execution_end" && !ran.has(ev.toolCallId) && !limited && !held.has(ev.toolCallId) && !deferred.has(ev.toolCallId)) {
      refused(ev.toolCallId, ev.toolName, asked.get(ev.toolCallId), ev.result?.content?.[0]?.text ?? "");
    }
  };
  const streamFn = (_m: unknown, ctx: unknown, o?: { signal?: AbortSignal }) => svc.stream(ctx, { signal: o?.signal });
  let history = req.messages; // what the turn goes on from
  let added: AgentMessage[];
  if (req.answers) {
    // The held round's results, in call order: the host's, or the held-back call's now.
    const round = history[history.length - 1] as AssistantMessage;
    const results: Message[] = [];
    for (const c of round.content) {
      if (c.type !== "toolCall") continue;
      const a = req.answers[c.id];
      if (a) {
        results.push({ role: "toolResult", toolCallId: c.id, toolName: c.name, content: [{ type: "text", text: a.text }], isError: a.isError, timestamp: Date.now() });
        continue;
      }
      const o = await runToolCall(c, { tools, assistantMessage: round, context: { systemPrompt: "", messages: history, tools } });
      if (!ran.has(c.id)) refused(c.id, c.name, c.arguments, (o.result.content?.[0] as { text?: string })?.text ?? "");
      results.push({ role: "toolResult", toolCallId: c.id, toolName: c.name, content: o.result.content ?? [], details: o.result.details, isError: o.isError, timestamp: Date.now() } as Message);
    }
    added = [...results, ...(await runAgentLoopContinue({ messages: transcript(req.system, [...history, ...results]), tools }, config, emit, undefined, streamFn as any))];
  } else {
    history = history.slice(0, -1);
    added = await runAgentLoop([req.messages[req.messages.length - 1]], { messages: transcript(req.system, history), tools }, config, emit, undefined, streamFn as any);
  }
  // A system message would declare changed tools; the next turn declares its own.
  let messages = (added as Message[]).filter((m) => m.role !== "system");
  if (limited) {
    // The turn ends after its last whole round: the refused calls go.
    messages = messages.slice(0, messages.indexOf(limited));
    svc.st.system = req.system ? `${req.system}\n\n${req.limitNote}` : req.limitNote;
    const s = svc.stream({ messages: transcript(svc.st.system, [...history, ...messages]) }, { toolChoice: "none" });
    for await (const ev of s) svc.relay(ev);
    const answer = await s.result();
    messages.push(answer);
    replies.push(answer);
  }
  const last = replies[replies.length - 1];
  return {
    messages: JSON.stringify(messages),
    stopReason: last ? svc.stopReason(last) : "error",
    error: last?.errorMessage ?? "",
    code: svc.st.code,
    limited: limited !== undefined,
    calls: last?.content.some((c) => c.type === "toolCall") ?? false,
    input: replies.reduce((n, m) => n + inputOf(m), 0),
    output: replies.reduce((n, m) => n + m.usage.output, 0),
    cached: replies.reduce((n, m) => n + m.usage.cacheRead, 0),
    last: last ? inputOf(last) : 0,
    held: [...held],
    deferred: [...deferred],
  };
}

// JudgeRequest is pi.JudgeRequest, as JSON.
interface JudgeRequest {
  url: string;
  apiKey: string;
  body: unknown;
}

// judge posts one judge API request (OpenAI Decisions or TypeSafe System
// One) through pi-ai's classifier transport and returns the reply as it came:
// Godwit encodes the questions and reads the answers itself, keeping what
// pi-ai's classify drops (level descriptions, refusals, score probabilities).
export async function judge(req: JudgeRequest) {
  const model = { id: "", provider: "godwit", baseUrl: "", headers: {}, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 } };
  try {
    const body = await postClassifierRequest("judge", new URL(req.url), model as never, req.body, {
      // pi-ai insists on a key; a keyless judge (a plugin's) gets a
      // placeholder whose header the null below drops.
      apiKey: req.apiKey || "godwit-no-key",
      headers: req.apiKey ? undefined : { authorization: null },
      // The Genkit path waits at most 2s for a Retry-After and then retries;
      // pi-ai would give up on a longer one instead.
      async fetch(url: string, init: object) {
        const r = await fetch(url, init);
        const after = Number.parseFloat(r.headers.get("retry-after") ?? "");
        if (after > 2) r.headers.set("retry-after", "2");
        return r;
      },
      maxRetries: 2, // three attempts, as the Genkit path makes
      identity: { userAgent: { custom: "Godwit" } },
    });
    return { body: JSON.stringify(body), status: 200 };
  } catch (e) {
    const status = (e as { status?: unknown }).status;
    return { body: "", status: typeof status === "number" ? status : 0 };
  }
}
