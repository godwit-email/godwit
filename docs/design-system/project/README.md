# Godwit

Godwit is an open-source AI email client. Its interface is drawn natively (mygo) in shadcn/ui's Nova look on the **neutral** base color, with every color taken from the **Tailwind v4 palette**. These tokens are the app's `internal/theme`, value for value: the design canvas and the code read the same names.

## Visual foundations

- **Neutral first.** Surfaces step from `window` (top bar and rail, transparent over the system material) to `sidebar` to `background` (content). Hue is reserved for meaning: `unread` blue, and the status tints.
- **Borders, not lines everywhere.** `border` outlines panels, cards and overlays; `input` outlines controls (a step stronger in dark); `ring` is the keyboard focus at half opacity and a hovered divider. Shadows are Tailwind v4's, as shadcn uses them: `shadow-xs` under the page panels, outline buttons, inputs, checkboxes and logo tiles (settings selects are flat); `shadow-sm` the chosen segmented tab and cards; `shadow-md` menus, popovers and select lists; `shadow-lg` toasts and floating cards; `shadow-2xl` dialogs and the search palette.
- **One panel.** The sidebar and content form one rounded panel (`radius-xl`), inset `space-2` from the window's right and bottom edges. The sidebar is 240–480 wide, one width for every page.
- **Status is tinted, not shouted.** Success, warning and error toasts take their color's 50 behind, 200 around (warning: amber-300) and 700 for text and icon (warning: amber-800); a plain toast is white. Toasts sit centered under the top bar.
- **Few lines.** Draw a divider only between peers: fields of a form, rows of a card, both inset to the content edge. Nothing separates a bottom bar from the content above it, and a toolbar sits on the bar rather than in a row of its own; whitespace does that job.
- **Spacing in 4s.** Every size is `space-n` = 4n px (half steps allowed).
- **One scale for controls.** A control is 32 high, 28 small, 24 extra small or 36 large, and nothing between: a button, an input and a select of one height sit side by side alike. Its corner follows its height: `radius-lg` from 32, `radius-md` at 28 and 24, `radius-sm` for a badge (20). Inside a rounded box a part rounds no more than the box: `radius-md` items in a `radius-lg` menu or toast. Rows (a sidebar's 32 and 28, a settings row) are not controls and keep `radius-lg`.
- **Two regions, four fills.** A section of a page is a card (`kit.Card`, `radius-xl`); a region inside a page or a card is a block (`kit.Block`, `radius-lg`). Either takes one of four tones: `card` (the card gray, apart from the page; a card of it is in a hairline), `tint` (`hover-tint`, for what stands out more: a note, a group being edited; never outlined), `outline` (the background in a hairline, on the page's own white) `raised` (the background, bare: a white block on a card), and the status tints `warning`, `danger` and `success` (the kind's `-bg` in a hairline of its `-border`, under its `-text`) for a note that warns or reports; an alert is a card of one. A tile (`kit.Tile`) is a small box in one of those tones for a glyph or a few letters (a service's mark, a file's kind), rounded as a control of its shorter side. A job under way shows a progress bar (`kit.Progress`: 4 high, `primary` on a `border` track); a state shows a status dot (`kit.StatusDot`: 6, green, amber, red, or gray while there is nothing to report).
- **A step off the surface is a tint.** What stands a step off whatever it is on (a badge, a tab list's track, a secondary button, a tint block or tile, a chosen choice) is filled with `hover-tint`, the foreground at 4% (white at 8% in the dark), not the solid `hover` gray. Over white the two are one color; on a dialog's frost, on the card gray or on the window's material a solid `hover` is the surface's own color or lighter, and what it fills goes unseen. Hovered and pressed add a step of the tint each. `hover` stays for what covers what is under it: an outline button's own fill, a row of a list.
- **One way to be chosen.** Whatever the user picks among (a provider's card, a model's chip, a filter that is on, a tile of figures, a radio card) is drawn by `kit.Choice`: chosen, the `hover-tint` fill in a 1 px `primary` outline; else the `background` in a `border` outline, half way to `hover` under the pointer. An unchosen one on a card has no outline; one on offer but not taken is outlined in dashes. A chip that is on or off is `kit.ToggleChip`: 28 high, `xs`, `muted` while off. A choice between views is a segmented tab list, never a hand-made track.
- **Pages draw no surfaces of their own.** A box a page rounds and fills or outlines by hand is a component the kit lacks: add it there. `app/kit_guard_test.go` holds the line: the hand-drawn ones left are pinned per file in `app/testdata/hand_styled.txt` and only go down, and a control gets no height off the scale.
- **Five text sizes, Tailwind's.** `xs` 12 (secondary text, badges, hints), `sm` 14 (the interface's text), `base` 16 (dialog, card and section titles), `xl` 20 (page headings, figures) and `3xl` 30: the names and sizes the app's web pages use, so `sm` is one size everywhere. There is no step between 12 and 14, and nothing is under 12 at any font-size setting. Every step is on one line height, 1.45 (`theme.LineUI`, set once on the window; the web pages' steps carry the same): a line is as tall with CJK in it as without, so the rows of a list are one height whatever they say. A text names a line height only to differ: 1 in a box of fixed height, 1.6 for a paragraph of prose, 1.75 for a mail's body. Weight 400 reads, 500 marks what acts or is chosen (buttons, a chosen tab, a row's label), 600 is for titles and unread mail.
- **Three inks.** `foreground` for what is read first, `muted` for what is secondary, and `subdued` between them for what has been seen. A list row leads with one line at 500 (who, or the subject where the user put it first), 600 while unread; the line under it is 400 and the snippet `xs` `muted`. Read mail takes `subdued` on both lines, so unread differs by ink as well as weight. A count beside a name (a conversation's messages, a folder's unread) is a plain `xs` `muted` number, never a pill.

## Content fundamentals

Plain, short, sentence case. Chinese and English both ship; say what happened, not what the system did ("已保存", "2 封未能处理").

## Iconography

Remix Icon at 16 (12, 14, 18, 20, 28 by role), drawn as filled 24 × 24 paths taking the text color. Use the `-line` variant at rest and the `-fill` variant for the selected item (the selected rail or sidebar entry).

## Dark theme

Dark follows shadcn neutral: `border` is white at 10%, `input` white at 15%, `ring` neutral-500, and a dialog's panel (`raised`) and a settings card (`card`) step up to neutral-900. Status tints take Tailwind's dark pairing: the -950 step behind, -800 around, -300 for text and icon (success, warning, danger, the Wit revision marks, `highlight`). The status dots (`success`, `attention`, `danger`, `unread`) are the same in both.

## Glass and backdrops

Menus, popovers, select lists and the search palette are liquid glass: `glass-bg` over a 20 px blur with a hairline `glass-border`, `glass-hover` behind a hovered item. A dialog dims the window with `scrim` (black 20%), a confirmation with `alert-scrim` (gray 60%); backdrops are never blurred.

## Labels

Mail labels come in sixteen colors (gray, red, orange, amber, yellow, lime, green, emerald, teal, cyan, sky, blue, indigo, violet, fuchsia, pink), each four tokens: `label-<color>-dot` for the picker, `-fill` and `-text` for a label set by hand, `-edge` for the outline of one the model set. See Label chip.

## Layout

The `size` tokens are the app's fixed measures: sidebars 248 (240–480 by drag), the mail list 340 (280–560), the top bar 44, dialogs 440 / 560 / 640, toasts at most 420, a settings column 760, the reading column 720. Text sizes follow the user's font size (`sm` 14 by default) and comfortable density scales every `space-*` by 1.25; radii and the `size` tokens do not change.

## Motion

Hover and selection change over 120 ms, moves over 200 ms, ease-out. An overlay fades in rising 4 px (a toast 8) and fades out over 120 ms. A pressed button goes a step past its hover. What marks the chosen one of a set slides to it over 200 ms rather than jumping: a segmented list's raised segment, an underlined list's line, the page switch's pill. A control that flips (a switch, a check box, a radio) changes color over 120 ms. A menu's highlight follows the pointer and the keys at once. With reduced motion everything appears at once.

## The web pages' look

Compose, Wit and the signature editor are web pages drawing shadcn's Nova components unchanged (`web/src/components/ui`; `web:check` refuses an edit). Their text scale is this one, names and sizes (`web/src/shared/styles.css`), and a page there gives a button no size, weight or padding of its own either. The native kit takes Nova's measures wherever both draw the same thing, so that a button, a menu or a dialog is one size in either: a control's heights, corners and side padding, a menu's and a select's 28-high items, a tooltip's and a popover's padding, an alert's box, a dialog's `radius-xl`. Where they still differ it is on purpose: a native badge keeps `radius-sm` beside the mail label chips, and titles are at 600 where Nova's are 500. Nova's small button asks for 0.8 rem, which is under 12 at the root's 14: both sides draw it in `xs` (`styles.css` lifts the web one).

## Components

Controls (buttons, icon groups, send button, menu, tooltip, bars), Inputs, Tabs, Dialog (with popovers), Toast, Alert (with status dots), Empty, Label chip, Template field, Search palette, and the Settings pattern. Each component's README names its source in `internal/kit`; when it and this book disagree, the code wins and this book is fixed.

## Where design work lives

- This system (`docs/design-system`): what is true of every screen — tokens, components, rules.
- The canvas (`docs/design`): screens and flows, built from this system (it installs a copy under `project/ds/godwit/`; after changing tokens here run `ds-sync docs/design godwit`). A canvas board does not define a component: when a board and this system disagree, the board is out of date.

