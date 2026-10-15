# Page preview (dev only)

Opens a page in a normal browser over fake data, to screenshot it against the design.

    cd web && bun run preview      # http://localhost:5173/preview.html

`?page=<page>&state=<state>&theme=light|dark` (theme defaults to light; the pages draw the tokens
from `theme.json`, a copy of `webui.ThemeOf` at font size 14; zh-CN text comes from the generated i18n catalog).

| page      | states                                                         |
| --------- | -------------------------------------------------------------- |
| compose   | `plain`, `streaming`, `inline`, `rewrite`, `applied`, `cmdk`, `signature`, `translate` (no model, a plugin translates) |
| wit       | `conversation`, `empty`, `plus`, `picker`, `quoted`            |
| signature | `filled`                                                       |
| reader    | `basic` (not in the design set)                                |

Example: `/preview.html?page=compose&state=inline&theme=dark`.

`preview.html` is not a build input; nothing here reaches `vite build`. `fake.ts` is the
`window.mygo`, `fixtures.ts` the data, `boot.ts` picks the state and scripts what a state needs
(`streaming`'s tool calls, `applied`'s finished run, `cmdk`'s selection and ⌘K).
`streaming`'s body is its update_draft call's text, cut short where the mockup's caret is.
