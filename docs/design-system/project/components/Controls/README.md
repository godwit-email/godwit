# Controls

Buttons, icon buttons and their groups, the send button with its menu, menus and tooltips: the controls every window shares (`internal/kit` button.go, group.go, overlay.go). Every size is a token.

## Buttons

32 high (28 small, 36 large), `radius-lg`, 14 px side padding, `body` text.

- **Primary**: `primary` fill, `primary-text` at 500. One per view: the send of a message, the page action, the save of a dialog.
- **Outline**: `background` fill, `input` outline, `foreground` text, `shadow-xs` (a `Flat` outline drops the shadow: settings selects, a button inside a card that already has an edge). Everything else.
- **Ghost**: no fill or outline; `hover-tint` behind it on hover.
- **Destructive**: ghost with `danger` text and `danger-bg` on hover. It is the confirm of a delete (删除 in a confirmation) and the 删除 on the left of an edit dialog's footer. A destructive row in settings is an outline with `danger-border` and `danger` text.

## Icon buttons

A 32 px square by default, 28 small (`radius-md`), 16 extra small and round with a 12 px glyph (a chip's ×). A 16 px Remix `-line` icon in `muted`, `foreground` with `hover-tint` behind it on hover. Every icon button has a tooltip and an `aria-label` with the same words. Mirror an icon rather than draw a second one when the action is the opposite direction (forward is reply mirrored), and never use one icon for two actions.

**Group**: related icon buttons sit side by side, unframed, ghost, `foreground` icons, 2 px apart. A 1 × 20 `border` line, 6 px from each side, splits groups. The reader's toolbar is two groups (reply, reply all, forward | star, inbox, archive, junk, delete) and a 更多 menu; a draft is one (continue, send, delete).

## Send button

The primary of a compose window, on the right of its bottom bar: one `primary` box, `radius-lg`, 32 high, holding 发送 (`space-4` side padding, 500) and a 32 px chevron square, split by a 1 px line of `primary-text` at 20%. The chevron opens a menu of later sends: an `xs` `muted` heading 定时发送, then presets (明天上午 with the day and time as its hint) and 选择时间… last. A send never needs the menu; scheduling never needs a second button.

## Menu

Liquid glass: `glass-bg` over a 20 px blur, a hairline `glass-border`, `radius-lg`, `shadow-md`, `space-1` padding. Items are 32 high, `radius-md`, 8 px side padding, `hover-tint` behind the hovered one; the label on the left and a hint in `sm` `muted` on the right. A heading over a set of items is `xs` `muted`. The menu opens 4 px from its trigger toward the free side (up from a bottom bar), aligned to the trigger's nearer edge, fading in over 120 ms with a 4 px rise.

## Tooltip

`primary` fill, `primary-text`, `xs`, `space-1` / `space-2` padding, `radius-md`, no shadow; centered `space-1-5` below its control, or above near the window's bottom. It shows at once on keyboard focus and after a pause on hover, and only names the action (回复全部, not 点击回复全部).

## Bars

A bar of a window (the compose window's bottom bar) is 56 high with `space-3` vertical padding, inset `space-4` from the window's sides like the fields above it, and has no line above it. Its tools go on the left (the format buttons, ghost icon buttons) and its one primary on the right.

## Motion

Hover and selection change over 120 ms (`MotionFast`), moves over 200 ms (`MotionBase`), ease-out; an overlay comes in rising 4 px. With reduced motion everything appears at once.
