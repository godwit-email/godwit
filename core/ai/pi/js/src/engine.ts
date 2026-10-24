import "./web";
import { stream as openaiCompletions } from "@earendil-works/pi-ai/api/openai-completions";
import { postClassifierRequest } from "@earendil-works/pi-ai/api/classifier-shared";
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
      // pi-ai insists on a key; a keyless model (a plugin's: its
      // prepareRequest authenticates) gets a placeholder whose header the null
      // below drops, as the Genkit path sends none.
      apiKey: req.apiKey || "godwit-no-key",
      headers: req.apiKey ? undefined : { authorization: null },
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
  // pi-ai's input leaves out the cached tokens; Godwit's is the whole prompt.
  const u = m.usage;
  return { stopReason: m.stopReason, error: m.errorMessage ?? "", input: u.input + u.cacheRead + u.cacheWrite, output: u.output };
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
