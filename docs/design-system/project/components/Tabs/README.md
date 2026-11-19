# Tabs

Looks of one control, `kit.Tabs(c, sel, kit.TabsOpts{Look, Fill}, items)`, for choosing what a place shows (`internal/kit/tabs.go`). The list is one Tab stop; arrows, Home and End choose a tab and focus follows. An item may carry an icon: 16 before the label, 6 apart, `muted` line style, `foreground` when chosen (the fill style in a segmented list).

| Look | For | Spec |
| --- | --- | --- |
| Segmented | a small mode inside a pane: 全部 / 未读, a list's filter | a `hover-tint` track, `radius-lg`, 2 padding and gap, 32 high as a control; tabs 28 high, 10 side padding, `radius-md` (the track's corner less its inset), `sm` at 500 `muted`; the chosen one is `background`, `foreground` and `shadow-sm`, a raised segment that slides to the tab chosen over 200 ms. Every tab is at 500, so choosing one moves nothing. The page switch in a sidebar's head is this control with an icon on every item: only the chosen page shows its name, the others are 28 wide, icon-only and named by a tooltip, `foreground` on hover |
| Fill | a choice between two whole forms (Gmail's 应用专用密码 \| 浏览器登录) | segmented, each tab an equal share of the width |
| Underline | the sections of a page (隐私与安全, 智能, 扩展) | 4 apart over a `border` hairline; tabs 32 high, 12 side padding, `sm` `muted`; the chosen one 500 `foreground` with a 2 px `primary` underline that slides to it over 200 ms (every tab keeps a clear 2 px line, so nothing shifts) |
| Pill | the kinds in a palette's head | no track, 4 apart; tabs 28 high, 10 side padding, `radius-md`, `xs` `muted`; the chosen one a `hover-tint` fill in `foreground` |

Colors fade over 120 ms; there is no sliding indicator. A label may carry a count (技能 3).
