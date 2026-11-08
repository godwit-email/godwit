# Inputs

Text inputs, selects, checkboxes, radios and switches (`internal/kit` input.go, choice.go). They share one frame: 32 high, 10 px side padding, `radius-md`, a 1 px `input` outline, `background` fill and `shadow-xs`.

## Text input

- Focus: the outline turns `ring` and a 3 px ring of `ring` at 50% grows outside it, over 120 ms. Invalid: the outline is `danger`.
- Mono (keys, addresses, ids) is the `mono` style at 12. A password is masked; a secret field adds a show button inside its right edge.
- A textarea has 8 px vertical padding. An input group (search with an icon) draws the frame once around a bare input and its icons.
- Field: the label at 500, the control 6 below, then either a hint in `sm` `muted` or the error in `sm` `danger`, never both.
- In a settings row it takes `radius-lg` and is 260 wide. On glass (the search palette, the Wit box) it is `glass-bg` with a hairline `glass-border` and `radius-lg`.

## Select

The input frame with the value and a 16 `chevron-down` in `muted`, 8 apart, at least 120 wide (200–280 in a settings row, `radius-lg`, flat). The list opens 4 below: liquid glass (`glass-bg`, `glass-border`), `radius-lg`, `shadow-md`, 4 padding; items 32 high, `radius-md`, 8 side padding, `hover` behind the highlighted one, a 16 check after the chosen one, disabled ones `muted`. Arrows open and move, Home / End jump, Enter or Space choose.

## Checkbox and radio

16 px, `input` outline, `background`, `shadow-xs`; the label 8 after. A checkbox is `radius-sm`; checked it fills `primary` with a 12 check in `primary-text`. A radio is round; chosen, its outline turns `primary` around an 8 px `primary` dot. Space toggles.

A **radio card** is a choice that needs a line of help (when a rule runs): the whole card clicks, 8 padding, `radius-lg`, `card` fill, `hover` mixed half into it on hover or when chosen, and a hairline `foreground` edge when chosen.

## Switch

A 32 × 18 round track, `input` off and `primary` on, with a 14 `background` thumb inset 2 (`foreground` in the dark while off); both move over 120 ms. A **switch row** toggles from anywhere on the row: 8 / 16 padding, at least 48 high, the label in `body` at 500 with help in `sm` `muted`, 24 from the switch.

A setting that is on or off is a switch row. A checkbox is for an option inside a form (存到子文件夹, 不需要 Key) and for the items of a multi-pick list.
