import type { Card, Ctx, MailMessage } from "@godwit/sdk"

export default function (ctx: Ctx) {
  ctx.models.register({
    listModels: () => ({ models: [{ id: "demo", name: "Demo", transport: "http", protocol: "openai-responses", baseURL: "https://example.com/v1" }] }),
    prepareRequest: (call) => call.request,
    judge: (call) => ({ answers: call.questions.map(q => ({ name: q.name, type: "predicate", probability: 0.5 })) }),
  })
  ctx.on("message.received", async (msg, info) => {
    await ctx.storage.set(info.idempotencyKey, String(msg.id))
  })
  ctx.on("message.beforeSend", async (draft) => (draft.subject ? undefined : { warn: "No subject" }))
  ctx.actions.register({
    id: "translate",
    label: { default: "Translate", "zh-CN": "翻译" },
    on: ["message", "selection"],
    run: async (target) => {
      const m: MailMessage = await ctx.mail.get(target.id)
      const res = await ctx.net.fetch("https://api.deepl.com/v2/translate", { method: "POST", body: m.text })
      return { text: await res.text() }
    },
  })
  ctx.cards.register({
    match: (m) => m.subject.includes("invoice"),
    render: (m): Card => ({ title: m.subject, fields: [{ label: "From", value: m.from }], buttons: [{ label: "Copy", kind: "copy", value: m.subject }] }),
  })
  ctx.tools.register({
    name: "lookup",
    description: "Look something up",
    schema: { type: "object", properties: { q: { type: "string" } } },
    write: false,
    run: async (args) => ctx.llm.complete(String(args.q)),
  })
  const key = ctx.config.apiKey
  void key
}
