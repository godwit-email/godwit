import type { Ctx } from "@godwit/sdk"

export default function (ctx: Ctx) {
  ctx.translators.register({
    translate: (req) => ({ texts: req.texts.map((t) => t.toUpperCase()), source: "en" }),
  })
}
