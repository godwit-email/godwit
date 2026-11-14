import { expect, test } from "bun:test"
import { agent, connect } from "./agent.js"

const gateway = { url: "http://127.0.0.1:3425", v1: "http://127.0.0.1:3425/v1", key: "magpie-godwit" }
const haiku = { id: "group/claude-haiku-5-5", name: "Claude Haiku 5.5", context: 1000000, efforts: ["low", "high"] }
const cheap = { id: "group/cheap-first", name: "Cheap", context: 1048576 }

test("says where Godwit reads magpie's settings", () => {
  expect(agent).toMatchObject({ id: "godwit", dir: "~/.godwit/external", config: "~/.godwit/external/models.json", model: "magpie.model" })
  expect(agent.library).toEqual({
    instructions: "~/.godwit/external/AGENTS.md",
    mcp: "~/.godwit/external/mcp.json",
    mcpFormat: "claude",
    skills: "~/.godwit/external/skills",
  })
})

test("shows Godwit's own icon, as the package gives it", async () => {
  const pkg = await Bun.file(new URL("./package.json", import.meta.url)).json()
  expect(agent.icon).toBe("https://godwit.email/icon.png")
  expect(pkg.magpie.icon).toBe(agent.icon)
})

test("writes the service key by key, with magpie's models as it describes them", () => {
  const out = connect({ gateway, model: cheap, models: [haiku, cheap], agent: { id: "godwit" } })
  expect(out).toEqual({
    "magpie.by": "magpie",
    "magpie.baseURL": "http://127.0.0.1:3425/v1",
    "magpie.apiKey": "magpie-godwit", // a key of its own: magpie reads it to tell Godwit is connected
    "magpie.kind": "openai",
    "magpie.models": [haiku, cheap],
  })
})

test("offers the chosen model even when the list leaves it out", () => {
  const out = connect({ gateway, model: cheap, models: [haiku] })
  expect(out["magpie.models"].map((m) => m.id)).toEqual([haiku.id, cheap.id])
})

test("writes nothing without a gateway", () => {
  expect(() => connect({ gateway: { v1: "" }, models: [] })).toThrow()
})
