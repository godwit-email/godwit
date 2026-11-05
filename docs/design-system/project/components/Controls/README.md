# Controls

Buttons, icon buttons and their groups, the send button with its menu, menus and tooltips: the controls every window shares. All are flat (no shadow), and every size is a token.

## Buttons

32 high (28 small, 36 large), `radius-lg`, `space-3` side padding, `body` text.

- **Primary**: `primary` fill, `primary-text` at 600. One per view: the send of a message, the page action, the save of a dialog.
- **Outline**: `background` fill, `input` outline, `foreground` text. Everything else.
- **Ghost**: no fill or outline; `hover` behind it on hover.
- **Danger**: outline in `danger-border` with `danger` text for a destructive row; ghost danger (`danger` text only) on the left of a dialog footer.

## Icon buttons

A 28 px square, `radius-md`, a 16 px Remix `-line` icon in `muted`, `foreground` with `hover` behind it on hover. Every icon button has a tooltip and an `aria-label` with the same words. Mirror an icon rather than draw a second one when the action is the opposite direction (forward is reply mirrored), and never use one icon for two actions.

**Group**: related icon buttons sit in one `radius-lg` box, 32 high, `input` outline, `background` fill, `space-0-5` padding and gap; a 1 × 16 `border` line splits sub-groups inside it. Groups sit `space-2` apart. The mail toolbar is two groups (reply, reply all, forward | archive, delete, move); a draft is one (continue, send, delete).

## Send button

The primary of a compose window, on the right of its bottom bar: one `primary` box, `radius-lg`, 32 high, holding 发送 (`space-4` side padding, 600) and a 32 px chevron square, split by a 1 px line of `primary-text` at 20%. The chevron opens a menu of later sends: a `sm` `muted` heading 定时发送, then presets (明天上午 with the day and time as its hint) and 选择时间… last. A send never needs the menu; scheduling never needs a second button.

## Menu

`background`, `border` outline, `radius-lg`, `shadow-md`, `space-1` padding, at least 180 wide. Items are 32 high, `radius-md`, `space-2-5` side padding, `hover` behind the hovered one; the label on the left and a hint in `sm` `muted` on the right. A heading over a set of items is `sm` `muted`. The menu opens `space-2` from its trigger toward the free side (up from a bottom bar), aligned to the trigger's nearer edge.

## Tooltip

`primary` fill, `primary-text`, `sm`, `space-1` / `space-2` padding, `radius-md`, `shadow-md`; centered `space-1-5` below its control, or above near the window's bottom. It shows on hover and on keyboard focus, and only names the action (回复全部, not 点击回复全部).

## Bars

A bar of a window (the compose window's bottom bar) is 56 high with `space-3` vertical padding, inset `space-4` from the window's sides like the fields above it, and has no line above it. Its tools go on the left (the format buttons, ghost icon buttons) and its one primary on the right.
