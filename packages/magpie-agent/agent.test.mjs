import { expect, test } from "bun:test"
import { agent, connect } from "./agent.js"

const gateway = { url: "http://127.0.0.1:3425", v1: "http://127.0.0.1:3425/v1", key: "magpie-godwit" }
const haiku = { id: "group/claude-haiku-5-5", name: "Claude Haiku 5.5", context: 1000000, efforts: ["low", "high"] }
const cheap = { id: "group/cheap-first", name: "Cheap", context: 1048576 }

test("says where Godwit reads magpie's settings", () => {
  expect(agent).toMatchObject({ id: "godwit", dir: "~/.godwit/external", config: "~/.godwit/external/models.json", model: "magpie.defaultModel" })
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

const jev = { id: "typesafe/jev-latest", name: "jev-latest", provider: "typesafe", endpoints: ["/v1/systemone", "/v1/decisions"] }

test("writes the service key by key, with magpie's generate and judge models as it describes them", () => {
  const out = connect({ gateway, model: cheap, models: [haiku, cheap], decisionModels: [jev], agent: { id: "godwit" } })
  expect(out).toEqual({
    "magpie.by": "magpie",
    "magpie.baseURL": "http://127.0.0.1:3425/v1",
    "magpie.apiKey": "magpie-godwit", // a key of its own: magpie reads it to tell Godwit is connected
    "magpie.kind": "openai",
    "magpie.generateModels": [haiku, cheap],
    "magpie.judgeModels": [jev],
  })
})

test("writes no judge models before magpie gives any", () => {
  expect(connect({ gateway, models: [haiku] })["magpie.judgeModels"]).toEqual([])
})

test("offers the chosen model even when the list leaves it out", () => {
  const out = connect({ gateway, model: cheap, models: [haiku] })
  expect(out["magpie.generateModels"].map((m) => m.id)).toEqual([haiku.id, cheap.id])
})

test("writes nothing without a gateway", () => {
  expect(() => connect({ gateway: { v1: "" }, models: [] })).toThrow()
})
