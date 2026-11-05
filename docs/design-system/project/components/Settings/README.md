# Settings

Every settings page is a centered column of grouped cards: a page header, then groups, each a small heading over a card of rows. The sidebar, groups, rows and, for collections, a list with a dialog are the only parts.

## Page

The content panel is `background`. Its column is centered, at most 760 wide, with `space-8` padding all round and `space-8` between groups. Every padding, gap and radius on these pages is a token: spacing from `space-*`, corners from `radius-*`, sizes from the type scale; no in-between values. Secondary text anywhere on the page is `sm` (12 px) in `muted`.

## Sidebar

248 wide, `sidebar` ground, `space-3` padding, `outline` edge with a `border` line on the right; the panel's left corners take `radius-xl`.

- Title 设置 in `xl`, then a search field (32 high, `radius-lg`, `window` fill, a 16 px search icon in `muted`), then two groups labelled in `sm` `muted`.
- **账户**: one row per account — provider logo (a 20 px tile, `radius-sm`, `shadow-xs`), address, status dot (6, `success` healthy, `attention` needs a look). Last row: **+ 添加账户** in `muted`.
- **应用**: 通用, 模型服务, 扩展, 隐私与安全, 关于, each with a 16 px icon: line icon in `muted` at rest, the filled icon in `foreground` when selected.
- Rows are 32 high, `radius-lg`, `space-0-5` apart. The selected row is `sidebar-selected` at regular weight.

## Page header

Title in `display` (29 / 600), one line of description in `body` `muted` 6 px below, and at most one primary action on the right, aligned to the bottom. An account page uses the identity header instead: logo (48, `radius-xl`), address in `display`, then provider · folders, a 默认发件 badge and a status badge on one line.

An alert about the whole page (login expired, the default model unreachable) sits between the header and the first group: `radius-xl`, its tint's background and border, its one action as a filled button in the tint's text color.

## Group

A heading in `body` at 600, 10 px above a card: `background`, `border` outline, `radius-xl`. Rows inside are separated by `border` lines (each row draws the line above it; the card hides the first). A note about the whole group goes under the card in `sm` `muted`.

## Row

One setting, `space-2` / `space-4` padding (8 / 16), at least `space-8` high. The title is at 500 with its description below in `sm` `muted`; the description says what the setting does, not what it is.

| Shape | When | Layout |
| --- | --- | --- |
| inline (default) | switch, select, a short text input | title and description on the left, the control on the right, 24 apart; selects hug their content (200–280), inputs 260 |
| action | a state plus a button (同步状态 › 立即同步, 本地占用 › 清理附件缓存), or a read-only value (IMAP address) | title and description on the left; on the right, the value in `muted` (mono for addresses) and/or an outline button; not a `<label>` |
| stacked | textarea, long input | title and description on top, the control full width under them |

Free content (a theme picker, the expanded review of a plugin) takes a row of its own with the same padding. Mono text (addresses, model ids) is the `mono` style, 12 px.

Controls:

- **Every boolean is a Switch**, never a checkbox: 32 × 18 track, `primary` when on, `input` when off, 14 px white thumb. Checkboxes appear only as items of a multi-pick list.
- Select and input: 32 high, `radius-lg`, `input` outline, `background` fill, no shadow.
- Buttons: 32 high, `radius-lg`. **Primary** (`primary` / `primary-text`) only for the page action and the save of a dialog — one per view. **Outline** (`input` outline, no shadow) for everything else. Icon-only actions and groups of them follow Controls. **Danger** outline (`danger-border`, `danger` text) for a destructive row; **ghost danger** (`danger` text, no outline) on the left of a dialog's footer.

## Collections

- **List + dialog** for a record filled in once and rarely changed (model services, MCP servers). The group's card holds one row per item — name with a status dot, mono address, capability tags, a chevron — and a row opens the edit dialog; the page action opens it empty. A setting that applies to the whole collection (which model does generation, which does decisions) is a group above the list.
- **Expandable row** for an item that needs reading before it is turned on (a plugin awaiting approval): the row shows a 待授权 badge and, under it, the permissions on a `sidebar` panel, the config fields and 允许并启用 / 暂不启用. Approved items are plain switch rows.
- **Empty**: when a collection has nothing yet, the page shows only its header (without the page action) and one **Empty** in place of every group: a dashed `divider-hover` box, `radius-xl`, `space-8` / `space-6` padding, centered — a 40 px `hover` tile with the page's icon, a title in `title` (16 / 600), one or two lines in `muted` saying what needs it, then the primary action and at most one outline alternative (添加服务 · 连接本地网关). Settings that depend on the collection (用途) are hidden until it has an item.

Capability tags in a list are `radius-sm` chips on `hover`: `muted` text, or `foreground` with a small check when it is in use as a default.

## Dialog

640 wide, `radius-2xl`, `shadow-lg`, over a scrim of the foreground at 28%, 56 from the top; header and footer fixed, the body scrolls.

- Header: title in `xl` — 添加模型服务 or 编辑 <name> — and one line in `muted`.
- Choices that shape the rest of the form (服务商) come first as a row of tiles: name at 500, what it offers in `sm` `muted`; the picked one has a `primary` outline on `hover`.
- Form: labels 96 wide on the left at the control's 32 px line, fields on the right, 12 apart; a hint under a field says what the value does (请求发往 <url>). Preset values are editable inputs with 恢复默认 beside them once changed. A secret has a show button inside its right edge; when editing it stays empty with 已配置 · 留空表示保持不变.
- Optional capabilities are cards (`border`, `radius-xl`, `space-3` / `space-4`): title at 600, what it does, a Switch; one the provider lacks is a `sidebar` card marked 不支持 that says what happens instead. Inside: a segmented control when there is more than one API format, the request URL, the model picker, and the test result.
- Status lines: a 6 px dot and `sm` text — `muted` while testing, `success-text` when it passed, `danger-text` with the reason when it failed. Turning off something in use shows a `warning` note saying what takes over.
- Footer (`border` above): ghost danger 删除 on the left when editing; a `sm` `muted` reason when the primary is disabled; then 测试连接 and 取消 (outline) and the primary. Deleting replaces the footer with a `danger-bg` confirmation saying what it affects.

## Badges

`radius-sm`, `sm`, 2 / 8 padding. Neutral: `hover` with `foreground`; healthy: `success-bg` with `success-text`; needs a look: `warning-bg` with `warning-text`.

## In the design canvas

`Empty.dc.html` (props `title`, `desc`, `icon` as a path, `actions` as `[{label, primary, onClick}]`), `SettingsSidebar.dc.html` (prop `current`: `account-0`, `account-1`, `general`, `models`, `extensions`) and `SettingsRow.dc.html` (props `label`, `desc`, `control` = switch | select | input | textarea | button | value, `layout` = inline | stacked, `on`, `options` as `a|b|c`, `value`, `action`, `tone`, `mono`) are imported with `<dc-import>`. Page header, group card, list rows and dialog are markup copied as written above, since an import cannot take child content.
