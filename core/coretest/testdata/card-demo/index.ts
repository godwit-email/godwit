// A card on messages whose subject has "Invoice", with copy and open-url buttons.
export default function (ctx: any) {
  ctx.cards.register({
    match: (m: any) => m.subject.includes("Invoice"),
    render: (m: any) => ({ title: "Invoice", fields: [{ label: "From", value: m.from }],
      buttons: [{ label: "Copy", kind: "copy", value: "INV-1" },
        { label: "Open", kind: "open-url", value: "https://pay.example/inv" }] }),
  })
}
