# Toast

A short notice about something that just happened, at the top centre of the window (`kit.ToastHost`, `internal/kit/toast.go`). It never asks a question: anything that needs an answer is a Dialog, anything that stays true is an Alert.

## Kinds

| Kind | When | Fill / outline / text | Icon |
| --- | --- | --- | --- |
| plain | told, nothing more (链接已复制) | `raised` / `border` / `foreground` | none |
| success | sent, saved, signed in again (邮件已发送, 设置已保存) | `success-bg` / `success-border` / `success-text` | circle-check |
| warning | stopped or partly skipped (缺少必填项) | `warning-bg` / `warning-border` / `warning-text` | triangle-alert |
| error | failed (2 封未能处理) | `danger-bg` / `danger-border` / `danger-text` | circle-x |

## Layout

Centered under the top bar (52 from the window's top), 8 apart, at most 3 at once (the oldest goes to make room), at most `toast-max-width` (420) wide. Each is a control high (36; more with a description): 4 padding, 12 before the text, `radius-lg`, hairline outline, `shadow-lg`, text in `sm` (14); a 16 icon, then the title, at 500 only over a description in `xs`. An action (撤销, 报告问题) is a 28-high filled button at the end, `radius-md`, at 500: `hover` under a plain toast, the kind's outline color under a tinted one, a step darker hovered and another pressed. A toast that goes by itself has no close button; one that stays until closed has a 28-square ×, named 关闭.

## Behaviour

It slides in 8 px and stays 4 s; hovering, focusing it or leaving the window pauses the timer. Escape closes the focused one. Screen readers read it as a status.

## Copy

Say what happened, in the past, with the count: 已删除「工作」, 2 封未能处理. No 成功！, no exclamation marks.
