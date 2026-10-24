import "./web";
import { stream as anthropicMessages } from "@earendil-works/pi-ai/api/anthropic-messages";
import { stream as openaiCodexResponses } from "@earendil-works/pi-ai/api/openai-codex-responses";
import { stream as openaiCompletions } from "@earendil-works/pi-ai/api/openai-completions";
import { stream as openaiResponses } from "@earendil-works/pi-ai/api/openai-responses";
import { normalizeContext } from "@earendil-works/pi-ai/utils/transcript";

declare const hostEmit: (kind: "text" | "reasoning", text: string) => void;

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
}

type Payload = Record<string, unknown> & { thinking?: Record<string, unknown> };

// generate streams one reply to a one-off prompt: its text and readable
// reasoning go out through hostEmit as they arrive; the result is how it ended.
export async function generate(req: Request) {
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
  let code = ""; // the error code the service last answered with
  let blocked = false; // a Responses refusal or content filter: a withheld reply
  let incomplete = ""; // why a Responses reply ended incomplete
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
      code = "";
      const r = await fetch(url, { ...init, headers });
      if (r.ok) return r;
      const text = await r.text();
      try {
        const e = JSON.parse(text);
        code = String(e?.error?.code ?? e?.code ?? "");
      } catch {}
      return new Response(text, { status: r.status, headers: r.headers });
    },
    onProviderStreamEvent(ev: any) {
      switch (ev?.type) {
        case "response.refusal.delta":
        case "response.refusal.done":
          blocked = true;
          break;
        case "response.output_item.done":
          blocked ||= ev.item?.type === "message" && (ev.item.content ?? []).some((c: any) => c?.type === "refusal");
          break;
        case "response.incomplete":
          incomplete = ev.response?.incomplete_details?.reason ?? "unknown";
          break;
        case "response.failed":
          code = String(ev.response?.error?.code ?? "");
          break;
      }
    },
    // Each request goes as the Genkit path sends it.
    onPayload(p: Payload) {
      if (req.api === "openai-responses") {
        p.include = ["reasoning.encrypted_content"]; // stateless: reasoning comes back encrypted, always
        if (req.system) p.instructions = req.system;
      }
      if (req.api === "openai-codex-responses") delete p.text; // pi-ai's low verbosity
      if (p.thinking) delete p.thinking.display;
      return p;
    },
    cacheRetention: "none" as const,
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
  // Responses sends the system prompt as instructions (onPayload).
  const system = req.api === "openai-responses" ? undefined : req.system || undefined;
  const context = normalizeContext({ systemPrompt: system, messages: [{ role: "user", content: req.prompt, timestamp: Date.now() }] });
  const streams = {
    "openai-completions": openaiCompletions,
    "anthropic-messages": anthropicMessages,
    "openai-responses": openaiResponses,
    "openai-codex-responses": openaiCodexResponses,
  };
  const s = (streams[req.api] as any)(model, context, options);
  // Responses: pi-ai ends each reasoning summary with "\n\n"; Genkit puts it
  // between summaries, so it goes out only before the next one.
  let gap = false;
  for await (const ev of s) {
    if (blocked) continue; // a refusal is no answer: none of it is shown
    if (ev.type === "text_delta") hostEmit("text", ev.delta);
    else if (ev.type === "thinking_delta") {
      if (ev.delta === "\n\n" && req.api.endsWith("responses")) gap = true;
      else {
        if (gap) hostEmit("reasoning", "\n\n");
        gap = false;
        hostEmit("reasoning", ev.delta);
      }
    }
  }
  const m = await s.result();
  let stopReason: string = m.stopReason;
  if (blocked || incomplete === "content_filter") stopReason = "blocked";
  else if (incomplete && incomplete !== "max_output_tokens") stopReason = "incomplete";
  // pi-ai's input leaves out the cached tokens; Godwit's is the whole prompt.
  const u = m.usage;
  return { stopReason, error: m.errorMessage ?? "", code, input: u.input + u.cacheRead + u.cacheWrite, output: u.output };
}
