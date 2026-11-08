# Godwit UI canvas

Screens and flows of the app, one board per state, in the Design (canvas) format: `project/canvas.json` indexes the boards, each board is a `project/<Name>.dc.html`.

What is true of every screen (tokens, components, rules) lives in the design system, [`docs/design-system`](../design-system/project/README.md), not here. When a board and the system disagree, the board is out of date.

## Pages

主界面 · 读信 · 搜索 · 同步状态 · 撰写 · Wit · 联系人 · 标签 · 视图 · 规则 · 添加账户 · 设置 · 签名 · 扩展 · 验证码, in the app's order, then 画板组件: the building blocks other boards `<dc-import>` (settings sidebar and row, empty state, Wit input, template field). A page is a product area, never a project: a redesign replaces the boards it changes, and the old ones are deleted.

## Names

A board's file name starts with its area: `Main…`, `Reader…`, `SearchPalette…`, `Sync…`, `Compose…`, `Wit…`, `Contacts…`, `Labels…`, `Views…`, `Rules…`, `Onboarding…`, `Settings…`, `Signature…`, `Extensions…`, `Code…`. `Main.dc.html` is the entry and the main window that other boards import with props (`reader`, `sync`, `search`, `labels`, …).

## Working on it

- Open it: `node <claude-design skill>/scripts/artifact.mjs open docs/design`.
- After changing the design system, copy its tokens in: `artifact.mjs ds-sync docs/design godwit`.
- Before committing: `artifact.mjs check docs/design`. Known: some provider logos (`/_blob/…`) were never exported into `project/assets/` and show as missing.
