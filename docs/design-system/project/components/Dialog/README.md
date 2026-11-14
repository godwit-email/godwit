# Dialog

A modal panel for a form or a confirmation: a header, a body that scrolls, a footer of buttons (`kit.Dialog`, `internal/kit/overlay.go`). Popovers and hover cards, which are not modal, are at the end.

## Sizes

| Size | Width | Tallest | For |
| --- | --- | --- | --- |
| `xs` | `dialog-sm` 440 | 760 | confirmations (alerts) |
| `md` (default) | `dialog-md` 560 | 760 | forms: add account |
| `lg` | `dialog-lg` 640 | 788 | larger forms: edit a model service |

A dialog's centre sits at 40% of the window's height, at least 32 from the top. A form whose height changes as it is filled (steps, a test result) is pinned `dialog-top` (64) from the top instead and grows down, so its header never jumps.

## Frame

- Panel: `raised` (white; neutral-900 in the dark), hairline `border`, `radius-xl`, `shadow-2xl`, clipped. Under window vibrancy it is frosted: `raised` at 80% over a 16 px blur.
- Backdrop: `scrim` (black 20%) for a dialog, `alert-scrim` (gray 60%) for a confirmation; never blurred. It fades in with the panel, which rises 4 px over 200 ms; closed, both fade out over 120 ms.
- Header: 16 padding (13 at the bottom when a body follows, so the first field's focus ring fits). Title in `base` (16 / 600), one line of description in `sm` `muted` 4 below. A dialog has a ghost 28 px × (关闭) in the corner, 8 from the edges; a confirmation has none.
- Body: 3 / 16 / 16 padding, 16 between fields; it alone scrolls.
- Footer: a row on the right, 8 apart, 12 / 16 padding, `sidebar` fill under a `border` hairline. 取消 (outline) comes before the action (primary). A destructive action is the Destructive ghost (`danger` text); a footer that asks to confirm a delete turns `danger-bg` with a `danger-border` line.

## Behaviour

Escape and a click on the backdrop close it. Focus moves into it, Tab cycles inside, and what is behind is inert. A confirmation closes by 取消, Escape or a click outside.

## Copy

A confirmation's title is a question naming the object in 「」 (删除「工作」？, 永久删除？); its description says the consequence (所选邮件将从服务器上删除，此操作无法撤销。). The action button repeats the verb (删除), never 确定.

## Popovers and hover cards

- Popover (a filter chip's menu, the sync status list): 10 padding, 10 gap, `radius-lg`, liquid glass (`glass-bg` over a 20 blur, `glass-border` hairline), `shadow-md`, 4 from its anchor, in over 120 ms.
- Hover card (a cited mail): `background`, hairline `border`, `radius-lg`, 10 padding, `shadow-md`, 320 wide.
