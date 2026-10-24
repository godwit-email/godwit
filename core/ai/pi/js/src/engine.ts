import "./web";
import { stream as openaiCompletions } from "@earendil-works/pi-ai/api/openai-completions";
import { normalizeContext } from "@earendil-works/pi-ai/utils/transcript";

declare const hostEmit: (kind: "text" | "reasoning", text: string) => void;

// Request is pi.Request, as JSON.
interface Request {
  baseUrl: string;
  model: string;
  apiKey: string;
  system: string;
  prompt: string;
  effort: "" | "low" | "medium" | "high";
}

// generate streams one reply to a one-off prompt: its text and readable
// reasoning go out through hostEmit as they arrive; the result is how it ended.
export async function generate(req: Request) {
  const model = {
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
  const s = openaiCompletions(
    model,
    normalizeContext({ systemPrompt: req.system || undefined, messages: [{ role: "user", content: req.prompt, timestamp: Date.now() }] }),
    {
      apiKey: req.apiKey,
      reasoningEffort: req.effort || undefined,
      maxRetries: 2, // openai-go's default, which the Genkit path uses
      identity: { userAgent: { custom: "Godwit" } },
    },
  );
  for await (const ev of s) {
    if (ev.type === "text_delta") hostEmit("text", ev.delta);
    else if (ev.type === "thinking_delta") hostEmit("reasoning", ev.delta);
  }
  const m = await s.result();
  return { stopReason: m.stopReason, error: m.errorMessage ?? "", input: m.usage.input, output: m.usage.output };
}
