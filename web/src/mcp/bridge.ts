// The MCP Apps view side of the host bridge: JSON-RPC over postMessage with the host window. A
// view only takes tool results and calls server tools; it never sends ui/message or
// ui/update-model-context, so nothing it shows flows back to the model (ADR 0022).

export type Result = { content?: { type: string; text?: string }[]; isError?: boolean };

type Message = {
  id?: number | string;
  method?: string;
  params?: Record<string, unknown>;
  result?: unknown;
  error?: { message?: string };
};

const pending = new Map<Message["id"], (m: Message) => void>();
let next = 1;

const send = (m: Message) => window.parent.postMessage({ jsonrpc: "2.0", ...m }, "*");

function request(method: string, params: Record<string, unknown>): Promise<unknown> {
  const id = next++;
  send({ id, method, params });
  return new Promise((resolve, reject) =>
    pending.set(id, (m) => (m.error ? reject(new Error(m.error.message)) : resolve(m.result))),
  );
}

/** The result's text content. */
export const text = (r: Result) =>
  (r.content ?? [])
    .filter((c) => c.type === "text")
    .map((c) => c.text ?? "")
    .join("");

/** Calls a godwit mcp tool through the host. */
export const callTool = (name: string, args: Record<string, unknown>) =>
  request("tools/call", { name, arguments: args }) as Promise<Result>;

const theme = (ctx: unknown) => {
  const t = (ctx as { theme?: string } | undefined)?.theme;
  if (t === "light" || t === "dark") document.documentElement.style.colorScheme = t;
};

/** Connects to the host as view name and hands it each tool result. */
export async function connect(name: string, onResult: (r: Result) => void) {
  window.addEventListener("message", (e) => {
    const m = e.data as Message;
    if (e.source !== window.parent || typeof m !== "object" || m === null) return;
    if (m.method === undefined) {
      pending.get(m.id)?.(m);
      pending.delete(m.id);
    } else if (m.id !== undefined) {
      send({ id: m.id, result: {} }); // ping, ui/resource-teardown: nothing to do
    } else if (m.method === "ui/notifications/tool-result") {
      onResult(m.params as Result);
    } else if (m.method === "ui/notifications/host-context-changed") {
      theme(m.params);
    }
  });
  const init = (await request("ui/initialize", {
    protocolVersion: "2026-01-26",
    appInfo: { name: `godwit-${name}`, version: "1" },
    appCapabilities: {},
  })) as { hostContext?: unknown };
  theme(init?.hostContext);
  send({ method: "ui/notifications/initialized" });
  // The body, not the root: the root is at least the frame's height, so the card could not shrink.
  new ResizeObserver(() => {
    const { offsetWidth: width, offsetHeight: height } = document.body;
    send({ method: "ui/notifications/size-changed", params: { width, height } });
  }).observe(document.body);
}
