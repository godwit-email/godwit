// Drafts a reply from the first account and returns it: the reader opens it.
export default function (ctx: any) {
  ctx.actions.register({ id: "draft", label: "Draft it", on: ["message"], run: async (t: any) => {
    const m = await ctx.mail.get(t.id)
    const draftId = await ctx.mail.createDraft({ accountId: 1, to: [m.from], subject: "Re: " + m.subject, html: "<p>drafted</p>" })
    return { draftId }
  } })
}
