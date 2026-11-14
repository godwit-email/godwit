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
// key and every model magpie gives it, as magpie describes them (name, context
// window, efforts…) and its judge models; Godwit takes what it uses. Key by key, not one object:
// magpie tells it is connected by the key holding its key, and the model key
// sits beside them. magpie calls it again when the models change.
export function connect({ gateway, model, models, decisionModels }) {
  if (!gateway?.v1 || !gateway?.key) throw new Error("magpie gave no gateway address or key")
  const list = [...(models ?? [])]
  if (model?.id && !list.some((m) => m.id === model.id)) list.push(model) // the chosen one is always offered
  return {
    "magpie.by": "magpie",
    "magpie.baseURL": gateway.v1,
    "magpie.apiKey": gateway.key,
    "magpie.kind": "openai",
    "magpie.models": list,
    "magpie.decisionModels": decisionModels ?? [], // the judge models, magpie 0.1.4170 on
  }
}
