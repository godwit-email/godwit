# Godwit

Godwit is an open-source AI email client. Its interface is drawn natively (mygo) in shadcn/ui's Nova look on the **neutral** base color, with every color taken from the **Tailwind v4 palette**. These tokens are the app's `internal/theme`, value for value: the design canvas and the code read the same names.

## Visual foundations

- **Neutral first.** Surfaces step from `window` (top bar and rail, transparent over the system material) to `sidebar` to `background` (content). Hue is reserved for meaning: `unread` blue, and the status tints.
- **Borders, not lines everywhere.** `border` outlines panels, cards and overlays; `input` outlines controls (a step stronger in dark); `ring` is the keyboard focus at half opacity and a hovered divider. Shadows are Tailwind v4's, as shadcn uses them (`shadow-xs` the page panel and small logo tiles — never controls: buttons, inputs and selects are flat; `shadow-sm` cards, `shadow-md` popovers, `shadow-lg` dialogs and toasts).
- **One panel.** The sidebar and content form one rounded panel (`radius-xl`), inset `space-2` from the window's right and bottom edges. The sidebar is 240–480 wide, one width for every page.
- **Status is tinted, not shouted.** Success, warning and error toasts take their color's 50 behind, 200 around (warning: amber-300) and 700 for text and icon (warning: amber-800); a plain toast is white. Toasts sit centered under the top bar.
- **Few lines.** Draw a divider only between peers: fields of a form, rows of a card, both inset to the content edge. Nothing separates a bottom bar from the content above it, and a toolbar sits on the bar rather than in a row of its own; whitespace does that job.
- **Spacing in 4s.** Every size is `space-n` = 4n px (half steps allowed); controls are 32 high (28 small, 36 large).

## Content fundamentals

Plain, short, sentence case. Chinese and English both ship; say what happened, not what the system did ("已保存", "2 封未能处理").

## Iconography

Remix Icon at 16 (12, 14, 18, 20, 28 by role), drawn as filled 24 × 24 paths taking the text color. Use the `-line` variant at rest and the `-fill` variant for the selected item (the selected rail or sidebar entry).

## Dark theme

Dark follows shadcn neutral: `border` is white at 10%, `input` white at 15%, `ring` neutral-500. The status tints are the same values in both themes.

