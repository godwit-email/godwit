import "./web";
import { runAgentLoop, type AgentTool, type AgentMessage } from "@earendil-works/pi-agent-core";
import type { AssistantMessage, Message } from "@earendil-works/pi-ai";
import { stream as openaiCompletions } from "@earendil-works/pi-ai/api/openai-completions";
import { normalizeContext, toToolDeclaration } from "@earendil-works/pi-ai/utils/transcript";

declare const hostEmit: (kind: "text" | "reasoning" | "refused", text: string) => void;
// hostArgs rewrites a call's arguments (Request.Args); it throws to refuse the call.
declare const hostArgs: (args: string) => string;
// hostTool runs a call in Go: its output as JSON, or its error.
declare const hostTool: (id: string, name: string, args: string) => Promise<{ output: string; error: string }>;

// Service is the part of pi.Request and pi.AgentRequest naming the model.
interface Service {
  baseUrl: string;
  model: string;
  apiKey: string;
  effort: "" | "low" | "medium" | "high";
}

// Request is pi.Request, as JSON.
interface Request extends Service {
  system: string;
  prompt: string;
}

function modelOf(req: Service) {
  return {
    id: req.model,
    name: req.model,
    api: "openai-completions" as const,
    provider: "godwit",
    baseUrl: req.baseUrl,
    reasoning: req.effort !== "",
    input: ["text" as const],
    contextWindow: 0, // unknown here; nothing below reads it
    maxTokens: 0, // no max_tokens is sent: the service's own limit applies, as before
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    // pi-ai would send a reasoning model's system prompt as "developer" and
    // store: false to any service it does not know; many compatible services
    // reject both, and the Genkit path sends neither.
    compat: { supportsDeveloperRole: false, supportsStore: false },
  };
}

function optionsOf(req: Service) {
  return {
    apiKey: req.apiKey,
    reasoningEffort: req.effort || undefined,
    maxRetries: 2, // openai-go's default, which the Genkit path uses
    identity: { userAgent: { custom: "Godwit" } },
  };
}

// pi-ai's input leaves out the cached tokens; Godwit's is the whole prompt.
const inputOf = (m: AssistantMessage) => m.usage.input + m.usage.cacheRead + m.usage.cacheWrite;

// generate streams one reply to a one-off prompt: its text and readable
// reasoning go out through hostEmit as they arrive; the result is how it ended.
export async function generate(req: Request) {
  const s = openaiCompletions(
    modelOf(req),
    normalizeContext({ systemPrompt: req.system || undefined, messages: [{ role: "user", content: req.prompt, timestamp: Date.now() }] }),
    optionsOf(req),
  );
  for await (const ev of s) {
    if (ev.type === "text_delta") hostEmit("text", ev.delta);
    else if (ev.type === "thinking_delta") hostEmit("reasoning", ev.delta);
  }
  const m = await s.result();
  return { stopReason: m.stopReason, error: m.errorMessage ?? "", input: inputOf(m), output: m.usage.output };
}

// AgentRequest is pi.AgentRequest, as JSON.
interface AgentRequest extends Service {
  system: string;
  messages: Message[];
  tools: { name: string; description: string; parameters: Record<string, unknown> }[];
  maxTurns: number;
  limitNote: string;
}

// agent runs one turn of a conversation on pi-agent-core's loop: messages
// end with the question, each tool call runs in Go (hostTool). At maxTurns
// rounds of calls the next round's calls are refused and dropped, and the
// model answers once more, told so by limitNote, with no tool choice.
export async function agent(req: AgentRequest) {
  const model = modelOf(req);
  const options = optionsOf(req);
  const ran = new Set<string>(); // calls hostTool ran: it reported them
  const asked = new Map<string, unknown>(); // each call's arguments as the model wrote them
  const rounds = new Set<AssistantMessage>();
  let limited: AssistantMessage | undefined;
  const tools: AgentTool[] = req.tools.map((t) => ({
    ...t,
    label: t.name,
    parameters: t.parameters as AgentTool["parameters"],
    prepareArguments: (args: unknown) => JSON.parse(hostArgs(JSON.stringify(args ?? {}))),
    async execute(id: string, params: unknown) {
      ran.add(id);
      const r = await hostTool(id, t.name, JSON.stringify(params ?? {}));
      if (r.error) return { content: [{ type: "text" as const, text: JSON.stringify({ error: r.error }) }], details: {}, isError: true };
      return { content: [{ type: "text" as const, text: r.output }], details: {} };
    },
  }));
  const decls = tools.map(toToolDeclaration);
  const history = req.messages.slice(0, -1);
  const question = req.messages[req.messages.length - 1];
  const context = { messages: normalizeContext({ systemPrompt: req.system || undefined, tools: decls, messages: history }).messages, tools };
  const added = await runAgentLoop(
    [question],
    context,
    {
      model,
      ...options,
      convertToLlm: (msgs: AgentMessage[]) => msgs as Message[],
      async beforeToolCall({ assistantMessage }) {
        if (limited === assistantMessage) return { block: true, terminate: true };
        if (!rounds.has(assistantMessage) && rounds.size >= req.maxTurns) {
          limited = assistantMessage;
          return { block: true, terminate: true };
        }
        rounds.add(assistantMessage);
        return undefined;
      },
    },
    async (ev) => {
      if (ev.type === "message_update") {
        const e = ev.assistantMessageEvent;
        if (e.type === "text_delta") hostEmit("text", e.delta);
        else if (e.type === "thinking_delta") hostEmit("reasoning", e.delta);
      } else if (ev.type === "tool_execution_start") {
        asked.set(ev.toolCallId, ev.args);
      } else if (ev.type === "tool_execution_end" && !ran.has(ev.toolCallId) && !limited) {
        // refused before it ran (unknown tool, invalid or rewritten arguments): Go reports it
        const text = ev.result?.content?.[0]?.text ?? "";
        hostEmit("refused", JSON.stringify({ id: ev.toolCallId, name: ev.toolName, args: asked.get(ev.toolCallId) ?? {}, error: text }));
      }
    },
    undefined,
    (m, ctx, o) => openaiCompletions(m, ctx, { ...options, signal: o?.signal }),
  );
  // A system message would declare changed tools; the next turn declares its own.
  let messages = (added as Message[]).filter((m) => m.role !== "system");
  if (limited) {
    // The turn ends after its last whole round: the refused calls go.
    messages = messages.slice(0, messages.indexOf(limited));
    const system = req.system ? `${req.system}\n\n${req.limitNote}` : req.limitNote;
    const s = openaiCompletions(
      model,
      normalizeContext({ systemPrompt: system, tools: decls, messages: [...history, ...messages] }),
      { ...options, toolChoice: "none" },
    );
    for await (const ev of s) {
      if (ev.type === "text_delta") hostEmit("text", ev.delta);
      else if (ev.type === "thinking_delta") hostEmit("reasoning", ev.delta);
    }
    messages.push(await s.result());
  }
  const replies = messages.filter((m): m is AssistantMessage => m.role === "assistant");
  const last = replies[replies.length - 1];
  return {
    messages: JSON.stringify(messages),
    stopReason: last?.stopReason ?? "error",
    error: last?.errorMessage ?? "",
    limited: limited !== undefined,
    calls: last?.content.some((c) => c.type === "toolCall") ?? false,
    input: replies.reduce((n, m) => n + inputOf(m), 0),
    output: replies.reduce((n, m) => n + m.usage.output, 0),
    last: last ? inputOf(last) : 0,
  };
}
