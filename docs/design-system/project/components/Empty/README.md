# Empty

What a place shows when it has nothing yet, and how to start (`kit.Empty`, `internal/kit/empty.go`): the rules page, signatures, a saved view with no matches.

- A centered column, 16 gap, 32 / 24 padding. A collection's page (rules, model services) frames it dashed: `radius-xl`, a hairline dashed `border`; a pane with nothing to show (a view, the reader) leaves it bare.
- A 40 px tile, `radius-lg`, `hover` fill, holding the place's 20 px icon in `foreground`.
- Title in `title` (16 / 600); one or two lines in `body` `muted` at line height 1.6, at most 400 wide, 4 below.
- Actions, 8 apart: the primary first, then at most one outline alternative (新建规则 · 告诉 Wit 你想怎么整理).

Copy: the title says what is missing (还没有规则); the description what goes here and how to begin, with an example (比如让账单自动打标签并归档). A search or view that matches nothing says so (没有符合条件的会话) and offers no action it cannot keep.
