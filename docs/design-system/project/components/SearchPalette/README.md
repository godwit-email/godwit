# Search palette

The ⌘K panel: search mail, attachments and Wit conversations, run an action, or hand the query to Wit (`kit` palette.go, used by `app/palette.go`).

- The frame is a dialog's: 640 wide, `dialog-top` (64) from the top, `radius-xl`, `shadow-2xl`, over `scrim`; the panel is liquid glass.
- The search row: 12 / 12 / 8 / 16 padding, a 16 search icon, the input bare. Under it the kinds (全部, 邮件, 附件, 对话) as tabs 28 high in `xs`, the chosen one on `hover-tint`; ← → switch kinds.
- Results: grouped by kind, rows 36 high, `radius-lg`, the chosen one on `hover-tint`; ↑ ↓ move (the input keeps focus and names the chosen row). Beside the chosen mail a 288 wide glass preview, `radius-xl`, `shadow-lg`.
- The hint bar at the bottom: keycaps (`kbd`: 20 high, `sidebar` fill, hairline `border`, `mono` `xs` `muted`, a cap per key) each with an `xs` `muted` label, 16 apart: ↵ 打开 · ⌥↵ 操作 · / 筛选 · Tab 问 Wit.
- Tab turns the box into Wit's: a framed textarea 56–240 high with a send button; Tab turns it back. Nothing found offers the same hand-over (没有结果 · 按 Tab 问 Wit).
