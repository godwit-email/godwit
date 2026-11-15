# Label chip

A mail label, in list rows, the reader and the label picker (`kit.LabelChip`, `internal/kit/label.go`).

- 20 high, `radius-sm`, `xs` text, a 12 px icon 4 before the name, 6 px side padding (2 on the right when it has a ×).
- Sixteen colors, each four tokens `label-<color>-dot | fill | edge | text`: gray, red, orange, amber, yellow, lime, green, emerald, teal, cyan, sky, blue, indigo, violet, fuchsia, pink. The dot is the picker's swatch.
- **Set by hand**: filled, `label-<color>-fill` under `label-<color>-text`. **Set by the model**: no fill, a 1 px `label-<color>-edge` outline, the same text color. The tooltip says which (手动标记 / 由模型标记).
- Shown as icon and name, icon only (the name in its tooltip and `aria-label`) or name only. As a menu it opens 改标签; removable, it ends with a 16 px × named 去掉标签「<name>」.
