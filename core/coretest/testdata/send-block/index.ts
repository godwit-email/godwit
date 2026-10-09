export default function (ctx: any) {
  ctx.on("message.beforeSend", (d: any) => ({ block: "blocked: " + d.subject }))
}
