# Godwit

Godwit is an open-source AI email client. Its interface is drawn natively (mygo) in shadcn/ui's Nova look on the **neutral** base color, with every color taken from the **Tailwind v4 palette**. These tokens are the app's `internal/theme`, value for value: the design canvas and the code read the same names.

## Visual foundations

- **Neutral first.** Surfaces step from `window` (top bar and rail, transparent over the system material) to `sidebar` to `background` (content). Hue is reserved for meaning: `unread` blue, and the status tints.
- **Borders, not lines everywhere.** `border` outlines panels, cards and overlays; `input` outlines controls (a step stronger in dark); `ring` is the keyboard focus at half opacity and a hovered divider. Shadows are Tailwind v4's, as shadcn uses them: `shadow-xs` under the page panels, outline buttons, inputs, checkboxes and logo tiles (settings selects are flat); `shadow-sm` the chosen segmented tab and cards; `shadow-md` menus, popovers and select lists; `shadow-lg` toasts and floating cards; `shadow-2xl` dialogs and the search palette.
- **One panel.** The sidebar and content form one rounded panel (`radius-xl`), inset `space-2` from the window's right and bottom edges. The sidebar is 240–480 wide, one width for every page.
- **Status is tinted, not shouted.** Success, warning and error toasts take their color's 50 behind, 200 around (warning: amber-300) and 700 for text and icon (warning: amber-800); a plain toast is white. Toasts sit centered under the top bar.
- **Few lines.** Draw a divider only between peers: fields of a form, rows of a card, both inset to the content edge. Nothing separates a bottom bar from the content above it, and a toolbar sits on the bar rather than in a row of its own; whitespace does that job.
- **Spacing in 4s.** Every size is `space-n` = 4n px (half steps allowed); controls are 32 high (28 small, 36 large).

## Content fundamentals

Plain, short, sentence case. Chinese and English both ship; say what happened, not what the system did ("已保存", "2 封未能处理").

## Iconography

Remix Icon at 16 (12, 14, 18, 20, 28 by role), drawn as filled 24 × 24 paths taking the text color. Use the `-line` variant at rest and the `-fill` variant for the selected item (the selected rail or sidebar entry).

## Dark theme

Dark follows shadcn neutral: `border` is white at 10%, `input` white at 15%, `ring` neutral-500, and a dialog's panel (`raised`) and a settings card (`card`) step up to neutral-900. Status tints take Tailwind's dark pairing: the -950 step behind, -800 around, -300 for text and icon (success, warning, danger, the Wit revision marks, `highlight`). The status dots (`success`, `attention`, `danger`, `unread`) are the same in both.

## Glass and backdrops

Menus, popovers, select lists and the search palette are liquid glass: `glass-bg` over a 20 px blur with a hairline `glass-border`, `glass-hover` behind a hovered item. A dialog dims the window with `scrim` (black 20%), a confirmation with `alert-scrim` (gray 60%); backdrops are never blurred.

## Labels

Mail labels come in twelve colors (gray, red, orange, amber, yellow, lime, green, teal, sky, blue, violet, pink), each four tokens: `label-<color>-dot` for the picker, `-fill` and `-text` for a label set by hand, `-edge` for the outline of one the model set. See Label chip.

## Layout

The `size` tokens are the app's fixed measures: sidebars 248 (240–480 by drag), the mail list 340 (280–560), the top bar 44, dialogs 440 / 560 / 640, toasts at most 420, a settings column 760, the reading column 720. Text sizes follow the user's font size (body 14 by default) and comfortable density scales every `space-*` by 1.25; radii and the `size` tokens do not change.

## Motion

Hover and selection change over 120 ms, moves over 200 ms, ease-out. An overlay fades in rising 4 px (a toast 8). With reduced motion everything appears at once.

## Components

Controls (buttons, icon groups, send button, menu, tooltip, bars), Inputs, Tabs, Dialog (with popovers), Toast, Alert (with status dots), Empty, Label chip, Template field, Search palette, and the Settings pattern. Each component's README names its source in `internal/kit`; when it and this book disagree, the code wins and this book is fixed.

## Where design work lives

- This system (`docs/design-system`): what is true of every screen — tokens, components, rules.
- The canvas (`docs/design`): screens and flows, built from this system (it installs a copy under `project/ds/godwit/`; after changing tokens here run `ds-sync docs/design godwit`). A canvas board does not define a component: when a board and this system disagree, the board is out of date.

