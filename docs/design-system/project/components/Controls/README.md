# Controls

Buttons, icon buttons and their groups, the send button with its menu, menus and tooltips: the controls every window shares (`internal/kit` button.go, group.go, overlay.go). Every size is a token.

## Buttons

Four heights, and its label at 500 in every look, since it acts:

| Size | Height | Side padding | Text | Icon | Corner | For |
| --- | --- | --- | --- | --- | --- | --- |
| extra small | 24 | 8 | `xs` | 14 | `radius-md` | an action among chips or in a dense row (a quick reply, 刷新) |
| small | 28 | 10 | `sm` | 16 | `radius-md` | settings rows, cards, bars |
| default | 32 | 14 | `sm` | 16 | `radius-lg` | dialogs, page actions |
| large | 36 | 18 | `sm` | 16 | `radius-lg` | onboarding |

A page gives a button no weight, size or corner of its own.

- **Primary**: `primary` fill, `primary-text`. One per view: the send of a message, the page action, the save of a dialog.
- **Secondary**: `hover` fill, `foreground` text, no outline: a quiet action that still reads as a button (a quick reply, a pressed toggle).
- **Outline**: `background` fill, `input` outline, `foreground` text, `shadow-xs` (a `Flat` outline drops the shadow: settings selects, a button inside a card that already has an edge). Everything else.
- **Ghost**: no fill or outline; `hover-tint` behind it on hover.
- **Destructive**: ghost with `danger` text and `danger-bg` on hover. It is the confirm of a delete (删除 in a confirmation) and the 删除 on the left of an edit dialog's footer. A destructive row in settings is an outline with `danger-border` and `danger` text.

## Icon buttons

A square of a button's height: 32 by default, 28 small, 24 extra small with a 14 px glyph, rounded as a button of that height; and a 16 px round one with a 12 px glyph (a chip's ×). A 16 px Remix `-line` icon in `muted`, `foreground` with `hover-tint` behind it on hover. Every icon button has a tooltip and an `aria-label` with the same words. Mirror an icon rather than draw a second one when the action is the opposite direction (forward is reply mirrored), and never use one icon for two actions.

**Group**: related icon buttons sit side by side, unframed, ghost, `foreground` icons, 2 px apart. A 1 × 20 `border` line, 6 px from each side, splits groups. The reader's toolbar is two groups (reply, reply all, forward | star, inbox, archive, junk, delete) and a 更多 menu; a draft is one (continue, send, delete).

## Send button

The primary of a compose window, on the right of its bottom bar: one `primary` box, `radius-lg`, 32 high, holding 发送 (`space-4` side padding, 500) and a 32 px chevron square, split by a 1 px line of `primary-text` at 20%. The chevron opens a menu of later sends: an `xs` `muted` heading 定时发送, then presets (明天上午 with the day and time as its hint) and 选择时间… last. A send never needs the menu; scheduling never needs a second button.

## Menu

Liquid glass: `glass-bg` over a 20 px blur, a hairline `glass-border`, `radius-lg`, `shadow-md`, `space-1` padding. Items are 32 high, `radius-md`, 8 px side padding, `hover-tint` behind the hovered one; the label on the left and a hint in `xs` `muted` on the right. A heading over a set of items is `xs` `muted`. The menu opens 4 px from its trigger toward the free side (up from a bottom bar), aligned to the trigger's nearer edge, fading in over 120 ms with a 4 px rise.

## Tooltip

`primary` fill, `primary-text`, `xs`, `space-1` / `space-2` padding, `radius-md`, no shadow; centered `space-1-5` below its control, or above near the window's bottom. It shows at once on keyboard focus and after a pause on hover, and only names the action (回复全部, not 点击回复全部).

## Bars

A bar of a window (the compose window's bottom bar) is 56 high with `space-3` vertical padding, inset `space-4` from the window's sides like the fields above it, and has no line above it. Its tools go on the left (the format buttons, ghost icon buttons) and its one primary on the right.

## Badge

The one small label (`kit.Badge`, `kit.BadgeWith`): 20 high as a label chip, `xs` text, 6 px at its sides, `radius-sm`, an optional 12 px icon 4 before the text. It names and does not act. One that acts among badges, as 标签 and 退订 beside a mail's label chips, is a badge button (`kit.BadgeButton`): the same box, neutral or outline, a step darker hovered and another pressed. Anywhere else what is clicked is a button.

| Tone | When | Look |
| --- | --- | --- |
| neutral | a kind, a count, a tag (订阅, 收件箱) | `hover` fill, `foreground`; `muted` text for what does not apply (不支持) |
| outline | a state of the row it is in (编辑中) | hairline `border`, no fill |
| surface | a badge on a card or a tinted row (a service's capabilities) | `background` fill |
| success, warning, danger | a status in words (已同步, 待确认, 已拦截) | the status `-bg` under its `-text` |

## Motion

Hover and selection change over 120 ms (`MotionFast`), moves over 200 ms (`MotionBase`), ease-out; an overlay comes in rising 4 px and goes fading over 120 ms. A pressed button goes a step past its hover. With reduced motion everything appears at once.
