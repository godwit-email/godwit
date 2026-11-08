# Template field

A name built from text and variables, stored as `{date}`-style ids (`kit.TemplateField`, `internal/kit/templatefield.go`). Used for 文件夹命名 in a rule that saves files.

- The box: the input frame, flat, `radius-md`, at least 32 high, 3 / 8 padding, wrapping. Literal text is `mono` `sm`; each variable is a chip 24 high, `radius-sm`, `hover` fill, `sm` `foreground`, with an extra-small × named 删除「<variable>」. The typing area at the end is at least 64 wide. Invalid: the outline is `danger`.
- Under it: 插入 in `sm` `muted`, then a small outline button (28 high, `plus` icon) per variable. Then either the error in `sm` `danger`, or 示例 and the preview in `mono` `sm` `muted`, on one line.
- Keys: typing writes text at once; Backspace in the empty typing area removes the last variable.
- Placeholder: 输入文字，或从下面插入变量. Errors: 至少插入一个变量…, 「/」两边都要有内容。, 不能用「.」或「..」作为文件夹名。
