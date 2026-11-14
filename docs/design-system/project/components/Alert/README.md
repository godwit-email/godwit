# Alert

A notice that stays while something is true about a page or a pane (登录已过期, 默认服务的 Key 失效), with at most two actions on its right (`kit.Alert`, `internal/kit/display.go`). Something that just happened is a Toast instead.

- A row: 12 / 16 padding, 12 gap, `radius-xl`, hairline outline, `sm` text that wraps while the actions stay on the right.
- Warning: `warning-bg` / `warning-border` / `warning-text`. Danger: `danger-bg` / `danger-border` / `danger-text`, as a toast and a badge of that kind. Info: `background` / `border` / `foreground`.
- Its action is solid: 28 high, 10 side padding, `radius-md`, 500, filled with the alert's text color under `on-solid` (in the dark, under the alert's own background); Info's is `primary` / `primary-text`.
- In settings it sits between the page header and the first group; in the reader, above the mail.

Status dots go with it: 6 px, `success` healthy, `attention` a trouble that clears by itself (a retry), `danger` one that needs the user, `idle` nothing to report.
