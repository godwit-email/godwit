// The Godwit agent for magpie (https://usemagpie.ai/docs/plugins#agents).
// magpie writes these files and restores them when the user disconnects;
// Godwit mirrors them as read-only settings (ADR 0041 in the Godwit repo).
// One path on every system: this file runs in moejs and cannot tell them apart.
// Godwit creates the folder when it starts, which is how magpie finds it installed.
const dir = "~/.godwit/external"

export const agent = {
  id: "godwit",
  name: "Godwit",
  icon: "https://godwit.email/icon.png", // on the agent itself: a plugin loaded from a folder has no package icon
  bin: "godwit",
  dir,
  config: `${dir}/models.json`,
  model: "magpie.model", // the default model Godwit switches to
  ua: ["godwit"],
  notice: "Godwit picks this up within a few seconds.",
  library: {
    instructions: `${dir}/AGENTS.md`,
    mcp: `${dir}/mcp.json`,
    mcpFormat: "claude",
    skills: `${dir}/skills`,
  },
}

// connect is the service Godwit adds: the gateway's address, this agent's own
// key and every model magpie gives it. magpie calls it again when they change.
export function connect({ gateway, models }) {
  return {
    "magpie.by": "magpie",
    "magpie.baseURL": gateway.v1,
    "magpie.apiKey": gateway.key,
    "magpie.kind": "openai",
    "magpie.models": models.map((m) => m.id),
    // the names magpie shows, a routing group's own among them
    "magpie.names": Object.fromEntries(models.filter((m) => m.name && m.name !== m.id).map((m) => [m.id, m.name])),
    "magpie.contexts": Object.fromEntries(models.filter((m) => m.context > 0).map((m) => [m.id, m.context])),
  }
}
