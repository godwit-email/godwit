// Reads mail and fetches the coretest egress host: every run needs approval.
export default function (ctx: any) {
  ctx.actions.register({ id: "ping", label: "Ping", on: ["message"],
    run: async () => (await ctx.net.fetch("http://egress.godwit.test:80/ping")).text() })
}
