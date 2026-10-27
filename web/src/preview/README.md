# Page preview (dev only)

Opens a page in a normal browser over fake data, to screenshot it against the design.

    cd web && bun run preview      # http://localhost:5173/preview.html

`?page=<page>&state=<state>&theme=light|dark` (theme defaults to light; the pages draw the tokens
from `theme.json`, a copy of `webui.ThemeOf` at font size 14; zh-Hans text comes from the generated i18n catalog).

| page      | states                                                                                                                                                     |
| --------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| compose   | `plain`, `streaming`, `inline`, `rewrite`, `applied`, `cmdk`, `signature`, `translate` (no model, a plugin translates)                                     |
| wit       | `conversation`, `empty`, `plus`, `picker`, `quoted`                                                                                                        |
| signature | `filled`                                                                                                                                                   |
| reader    | `basic`, `neutral`, `colored`, `natural-dark`, `author-dark`, `fallback`, `media`, `gradients`, `mixed`, `large-paragraphs`, `large-cells`, `large-nested` |

Example: `/preview.html?page=compose&state=inline&theme=dark`.

`preview.html` is not a build input; nothing here reaches `vite build`. `fake.ts` is the
`window.mygo`, `fixtures.ts` the data, `boot.ts` picks the state and scripts what a state needs
(`streaming`'s tool calls, `applied`'s finished run, `cmdk`'s selection and ⌘K).
`streaming`'s body is its update_draft call's text, cut short where the mockup's caret is.

The `media` reader fixture embeds PNG/JPEG references for transparent logos, white screenshots, and colour preservation. Background `data:` images stand in for already authorized proxy responses at the `ReaderBody` boundary; the production Go image policy is unchanged.

The `gradients` fixture covers single linear/radial gradients with percentage stops, translucent colours, descendant text, decoration, and width-dependent colours. Repeating gradients, length stops, colour hints, multiple layers, and URL combinations retain the sender's complete pairing.

The `mixed` fixture is also passed unchanged to `ReaderApp` integration tests. It combines neutral cards, a coloured CTA inside an opaque card over URL/gradient artwork, a transparent logo, a text gradient, low-alpha fallback, and a wide table. At widths above 480px the faint caption keeps its original gradient; at narrower widths its opaque text permits adaptation. Resize in both directions and switch light/dark to check that fallback and author declarations return without drift. The integration tests cover the same transition with active CSS rules because jsdom does not evaluate viewport media queries; browser acceptance must exercise the actual widths.

The three `large-*` fixtures match `webui.TestReaderBodyWhole`: 6000 paragraphs, 3000 table cells, and 30 nested divs around 25000 inline elements. Each ends with `#large-tail`. Use these in a real browser to inspect tail visibility, iframe height, horizontal scrolling, first-visible timing and frame gaps; they are deliberately not large jsdom tests or fixed cross-machine performance thresholds.
