# Tabs

Three looks of one control, for choosing what a place shows (`internal/kit/tabs.go`). Arrows, Home and End choose a tab and focus follows.

| Look | For | Spec |
| --- | --- | --- |
| Segmented | a small mode inside a pane: 全部 / 未读, a list's filter | a `hover` track, `radius-lg`, 2 padding and gap, 32 high as a control; tabs 28 high, 10 side padding, `radius-md`, `xs` `muted`; the chosen one is `background`, `foreground` and `shadow-sm` |
| Fill | a choice between two whole forms (Gmail's 应用专用密码 \| 浏览器登录) | segmented, each tab an equal share of the width |
| Underline | the sections of a page (隐私与安全, 智能, 扩展) | 4 apart over a `border` hairline; tabs 32 high, 12 side padding, `sm` `muted`; the chosen one 500 `foreground` with a 2 px `primary` underline (every tab keeps a clear 2 px line, so nothing shifts) |

Colors fade over 120 ms; there is no sliding indicator. A label may carry a count (技能 3).
