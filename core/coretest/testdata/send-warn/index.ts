export default function (ctx: any) {
  ctx.on("message.beforeSend", (d: any) => ({ warn: "check before sending: " + d.subject }))
}
