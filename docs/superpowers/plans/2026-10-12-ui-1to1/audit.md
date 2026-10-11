# Visual audit: design vs app (screenshots side by side)

How these were made:
- **Design:** artboards rendered with headless Chrome at 1440×900 (compose at 860×720).
- **App, native screens:** `app/shots_test.go` with `GODWIT_SHOTS=dir`.
- **App, web pages:** `web/preview.html` (`bun run preview`).

Items are numbered per screen. Each item says what the design shows, then what the app shows (`now:`), then what to change (`→`). The exact design values are in the mockup source (`design-v2/project/*.dc.html`) and in `tokens.json`.

Out of scope this period (the user's decision, 2026-10-12). These are not built and must not appear in the UI:
- list-row plugin tags and quick-reply kind labels;
- multi-turn undo in the Wit panel, including the per-turn 「已应用到正文」 chip;
- smart views (待回复, 订阅与账单, 只通知智能视图);
- scheduled send (the ▾ half of the split Send button);
- 繁體中文 and 日本語 UI languages.

In scope as features (separate plans):
- model capability split;
- Wit + / @ quote and 本次对话都允许;
- search filters;
- move to junk and trust sender;
- time-based sync;
- mailbox behaviour;
- folder counts;
- signature images;
- settings search;
- send from the drafts pane;
- the Windows frame.

## Compose window (web, ComposeStreaming / Inline / Rewrite / Compose / CmdK)

**C1. Title bar.** Design: `Re: Q4 partnership proposal` (600) with `草稿已保存 · 09:52` (12, Muted) right after the title, left-aligned. Now: the status sits at the far right. → Put it inline after the title, gap 8.

**C2. Recipient row.** Design: one line, 38 px high. The chip is `Lena Fischer` (display name only, no ×), Hover bg, radius full, padding 2/8. `抄送 · 密送` sits at the right on the same line. Now: the chip shows the full `Name <addr>` plus ×, and a second line `添加收件人` input makes the row 82 px. → The chip shows the name (address in its tooltip). × appears only on hover/focus. The add input sits inline after the chips, with no placeholder line of its own. Row height 38.

**C3. Field rows.**
- Design: rows of 38 px (y = 66 / 103 / 141). Labels at x = 17 (Muted, 14). Values at x = 79. 1 px Border lines inset 16.
- Design 发件人 value: `you@example.com` followed directly by a small ▾ (no gap, no select box).
- Now: rows are 44 to 82 px, and the From select's chevron is spaced away from the text.
- → Rows 38, From as a borderless select with the chevron right after the text.

**C4. Banners (streaming / review).** These match apart from copy. The rewrite banner reads `已整体重写，改动约 70%`; now it shows only `已整体重写`. → Add the change percentage, which Go already has or can compute.

**C5. Streaming body.**
- Design: Wit's text streams into the body (`… though the webi|` with a caret). The body is tinted (locked) and keeps its normal layout.
- Now: the body is replaced by a skeleton and `Wit 正在写…`.
- → Stream the text into the read-only body as it arrives (the update_draft partial). Keep the tint.

**C6. Quote toggle.** Design: `▸ 显示引用的原邮件（助手只读）` (12, Muted) with a disclosure triangle, sitting inside the body area right under the text. Now: no triangle, larger, and pinned to the bottom of the body area. → Triangle and 12 Muted, placed right after the body content.

**C7. Bottom bar.**
- Design: a single row at the window bottom, padding 12/16.
  - Left: B, I, link, bullet list, a separator, attach (28 px buttons, #404040 icons).
  - Right: `发送` (h32, px16, 600, Primary, radius 8).
- Now: two rows.
  - The toolbar holds B, I, link, bullet, numbered list and quote, plus attach.
  - Under it sit `发送`, `丢弃草稿` and the trust note. Send is at the left.
- → One row with Send at the right. Ask the user about the numbered list, quote, 丢弃草稿 and the trust note, which the design drops (see Q-list). The split ▾ is out of scope, so Send is a plain button.

**C8. Wit panel conversation.**
- Tool disclosure, design: `▸ 调用了 read_message · update_draft` (12, Muted). It is left-aligned with a leading triangle and no count. Now: `调用了 N 个工具 · … ⌄` is center-aligned and wraps, with the chevron at the end, which is a layout bug. → Leading ▸, left-aligned, design wording (names joined with ` · `, no count, in the panel).
- Working row, design: `● 正在写入正文…  [停止]`. Now: a bare spinner. → Add the row.
- Review card, design: header on one line, `待确认` (600) + `update_draft` (mono 12, #404040), on a Hover bg. Body: the summary (14). Footer: buttons. Now: the header is on two lines. → One line.
- The first user turn is missing in the preview (preview fixture or real bug, to check).

**C9. Wit input (panel).**
- Design: the tray (调整 / 翻译 with icons) sits above the box. The box holds a two-line placeholder, then a bottom row: `+` at the left, `[MODEL] ⌄` (12 Muted), and a Primary 32 px send button with an arrow (dark even when empty).
- Now: no `+`, no model chip in the panel, and the send button is grey or disabled.
- → Add the `+` (feature: quote mail) and the model chip. The send button keeps the Primary look, and disabling is done by its opacity only, as the design shows.

**C10. ⌘K.**
- Design: the selected paragraph shows as a rounded block (bg Border #E5E5E5, radius 8, padding ≈ 4 px outset). The popover sits right under that paragraph, inside the body flow, at the full body width. It holds a sparkles icon, the prompt text (14), then a row: `⌘K · 只修改选中的这一段` (12 Muted), Esc (outline, xs) and `生成 ↵` (primary, xs).
- Check the app once the preview's timer is accounted for.

## Wit page (web, Ask)

**W1. Tool disclosure.** Design: `▸ 调用了 2 个工具 · search_mail、read_message ×7` (13, Muted), with a leading triangle. Now: the chevron is at the end. → Leading ▸.

**W2. Table.**
- Design: a single bordered table, radius 8, header bg Sidebar (500).
  - Cells: padding 8/12, 14 px.
  - Rows: 1 px Hover dividers.
  - Source links `[1]` are underlined and open the cited mail.
- Now: a second outer box (border + padding) wraps the table, and the links are not underlined or clickable. → One border. Make `[n]` links work (open citation n).

**W3. Approval card.**
- Header, design: one line, `Wit 想要：创建草稿` + mono `create_draft`. Now: two lines. → One line.
- Body, design: 收件人 / 主题 lines plus the body preview (Muted). Now: an extra `创建草稿` summary line. → Drop the summary line when it equals the tool label.
- Buttons, design: 允许 / 本次对话都允许 / 拒绝 (feature: session allow). Now: 允许 / 拒绝, plus 全部允许 when several are pending.

**W4. Citations.**
- Design dates: absolute and zero-padded (`10/02`, `10/06`). Now: `10/2`, `周一`. → Use the design format.
- Design spacing: card header 12 Muted; card padding 10/12; gap 10. Close; recheck after W2.

**W5. Input.** Same as C9: `+` at the left, the model chip at the right, the Primary send button. Placement: max-width 760, centred, at the bottom.

## User decisions (2026-10-12)

**Remove** (the design drops these):
- the reader ⋯ menu (star, mark unread, view in browser, export .eml);
- compose's 丢弃草稿 button and its trust note;
- the AI and 邮件 sections of General settings;
- the thread count on list rows.

**Keep** (although the design drops them):
- compose's numbered list and quote buttons;
- the list-row paperclip;
- the attachment download button;
- the manual-server security and username fields;
- the date group headings in the list.

## Native screens

The native items are written as tasks in the plan (`2026-10-12-ui-1to1.md`, Tasks 1–5). The systemic ones:

1. **Line heights.** No line height was ever set, so all text renders at about 1.2× where the design uses 1.4–1.75.
2. **Max widths.** The max width includes the padding, so reader content is 664 instead of 720 and settings content is 696 instead of 760.
3. **Dates.** Dates are not zero-padded.
4. **Settings title bar.** It shows only the page, where the design has a breadcrumb.
