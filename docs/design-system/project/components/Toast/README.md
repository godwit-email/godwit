# Toast

A short notice about something that just happened, at the top centre of the window (`kit.ToastHost`, `internal/kit/toast.go`). It never asks a question: anything that needs an answer is a Dialog, anything that stays true is an Alert.

## Kinds

| Kind | When | Fill / outline / text | Icon |
| --- | --- | --- | --- |
| plain | told, nothing more (链接已复制) | `background` / `border` / `foreground` | none |
| success | sent, saved, signed in again (邮件已发送, 设置已保存) | `success-bg` / `success-border` / `success-text` | circle-check |
| warning | stopped or partly skipped (缺少必填项) | `warning-bg` / `warning-border` / `warning-text` | triangle-alert |
| error | failed (2 封未能处理) | `danger-bg` / `danger-border` / `danger-text` | circle-x |

## Layout

Centered under the top bar (52 from the window's top), 8 apart, at most 3 at once (the oldest goes to make room), at most `toast-max-width` (420) wide. Each: 12 / 16 padding, 10 gap, `radius-2xl`, hairline outline, `shadow-lg`; a 16 icon, the title at 500, an optional description in `sm`. An action (报告问题) is a 28-high outline in the kind's color; the × is 20 square, `radius-sm`, named 关闭.

## Behaviour

It slides in 8 px and stays 4 s; hovering, focusing it or leaving the window pauses the timer. Escape closes the focused one. Screen readers read it as a status.

## Copy

Say what happened, in the past, with the count: 已删除「工作」, 2 封未能处理. No 成功！, no exclamation marks.
