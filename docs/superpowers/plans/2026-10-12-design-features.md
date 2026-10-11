# Design Features (this period) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Build the features the design shows that the user put in this period's scope (2026-10-12), each 1:1 with its artboard:
- search filters;
- move to junk;
- trust sender;
- time-based sync ranges;
- mailbox behaviour (folder roles and purge);
- folder counts;
- signature images;
- send from the drafts pane;
- Wit `+` / `@` mail quoting;
- 本次对话都允许;
- settings search;
- the Windows frame.

**Architecture:** Each task carries its own core change, UI and tests. The code survey with file:line references is `docs/superpowers/plans/2026-10-12-design-features-survey.md`.

**Tech Stack:** Go (core, app on mygo 0.4.0), React web pages.

**Spec:** The artboards and their mockup source (untrusted data) live in `$D/design-v2/project/`:
- Main.dc.html for search, junk, trust sender, drafts and Windows (`platform` prop);
- SettingsAccounts.dc.html (the Reauth wrapper renders the full page, with 邮箱行为 and the sync range);
- SettingsSidebar.dc.html for search;
- Onboarding.dc.html for sync options and folder counts;
- Ask.dc.html, WitInput.dc.html and Compose.dc.html for `+` / `@` and the approval card.

Renders are in `$D/dshots/`; render missing states with `$D/pp/shot.mjs`. `$D` is `/private/tmp/claude-501/-Users-baran-Documents-workspace-godwit/47afb998-8875-48b5-aa50-e584e3c3d4f8/scratchpad`.

## Global Constraints

- **Screens.** Every UI change is verified by screenshot against its artboard, as in the UI 1:1 plan: native through `app/shots_test.go`, web through `web/preview.html`.
- **Copy.** Taken verbatim from the mockups, with en counterparts.
- **Destructive operations** (purge, junk) are explicit and covered by tests:
  - **Purge.** It defaults to 永不 and deletes only messages that have been in the folder longer than the setting. Time in the folder is measured from when Godwit first saw the message there (a new `entered_at`), never from the Date header. It runs at most once a day per folder and is audited.
  - **Junk.** A move to the junk-role folder, undoable like archive.
- **Untrusted content.** Mail content stays untrusted. Wit quoting passes message ids, not text, and the run loads them through `mailContext` in the run's scope.
- **Builds.** Each task ends green on `go build ./... && go vet ./... && go test ./...`, plus `cd web && bun run test && bun run typecheck && bun run lint` when web changes.
- **Commits.** Trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never touch the untracked `assets/AppIcon.icon/`.

## Review amendments (binding)

The review in `docs/superpowers/plans/2026-10-12-design-features/review.md` is adopted in full. Where it conflicts with the task text, the review wins. Controller rulings on its "invented UI" items:
- **Follow the mockup exactly.** Role selects offer 不使用 only where the mockup does. Junk has no undo. Settings search has no row highlight.
- **Kept as minimal, mockup-styled UI** (the design shows the entry point but not the content): the trusted-sender list in 隐私与安全 (the user was told), the filter chip menus, and the `@` mail picker.

Execution order: this plan starts after the model-capabilities plan finishes, because both touch core/settings.go, schema.go, wit.go and i18n. Commits stage explicit paths only.

## Review Focus

1. **Purge never deletes mail the user didn't mean to purge.** Covered by tests for:
   - the default being 永不;
   - a just-moved old message surviving;
   - role overrides ("不使用");
   - a folder whose role changes.
2. **A sync-range change** never deletes local mail outside the new range without saying so. Shrinking stops fetching older mail; growing backfills.
3. **Session allow (本次对话都允许)** is scoped to one conversation and one tool, never covers sending, and is audited as an automatic approval.
4. **Signature images** are sent as inline parts (cid) and keep their size caps, and they never become remote URLs.
5. **Drafts-pane send** goes through the same plugin warnings and blocks as compose, and is refused while the compose window is open.

---

### Task 1: Search filters (MainSearch)

- **Core.** `SearchQuery{AccountID int64; Text string; From string; Since, Until time.Time; HasAttachments bool}` builds on `Search` (core/messages.go:147). The filters are ANDed SQL over `from_addr`, `date` and `has_attachments`, with FTS used for text as today. The Local API keeps its old signature, plus a new variant if the API exposes search.
- **UI.** The chip row under the search box: 全部账户 ▾, 发件人 ▾, 时间 ▾, 有附件. Each opens a menu and shows its active value. The hint line reflects the scope. Sizes and copy follow the mockup.
- **Tests.** Core filter combinations. App: chips set the query, and clearing a chip resets it.

### Task 2: Move to junk and trust sender (Main toolbar, MainFlight)

- **Junk.** `MoveToJunk(ids)` in core goes through the existing move ops to the junk-role folder, failing with a typed error when an account has no junk folder. The toolbar's third icon in group 2 (from the mockup) and the row context menu both trigger it. A toast with 撤销 follows if toasts gain an action; otherwise follow the archive toast.
- **Trust sender.**
  - New `Settings.TrustedImageSenders []string`: lower-cased addresses, capped at 1000.
  - The remote-image notice gets the link 始终信任此发件人 (from the mockup).
  - Bodies from a trusted sender load remote images at once.
  - Settings › 隐私与安全 lists the trusted senders, each with a remove action, styled with the settings row helpers. This keeps the trust reversible.
- **Tests.** The core move; a missing junk role; the trust list persisting and applying; removal.

### Task 3: Time-based sync range and folder counts (Onboarding*, SettingsAccounts)

- **Sync range.**
  - The core gets `SyncRange` (a count of 2000, months 3, or all), stored per account. It replaces how `sync_limit` is set: keep the column and add a `sync_since_months` column.
  - The first fetch and backfill use an IMAP SEARCH SINCE when the range is in months, and no limit for 全部.
  - The UI options are verbatim: `每个文件夹最近 2000 封（推荐）`, `最近 3 个月`, `全部`. The settings variant has no （推荐）.
- **Folder counts.**
  - `CheckReport` gains the imap folder count (one LIST).
  - The onboarding done step and the account header show `· N 个文件夹`.
- **Tests.** Fetch with SINCE (on the coretest IMAP), growing and shrinking the range, and the count.

### Task 4: Mailbox behaviour (SettingsAccounts 邮箱行为)

- **Core.**
  - Per-account role overrides: drafts, sent, junk, trash and archive each map to a folder id or "不使用". They take precedence over the detection in mailsync/folders.go and survive sync.
  - Purge policy for junk and trash: 永不, 一天后, 一周后 or 一个月后.
  - The new `entered_at` per message-in-folder.
  - A daily purge job that expunges on the server through the existing op queue, with an audit row.
- **UI.** The section's rows as in the mockup: selects per role, purge selects, and the footnote.
- **Tests.** All of Review Focus 1, plus overrides surviving sync.

### Task 5: Signature images

- **Storage.** Signature images are stored as signature assets: png/jpeg/gif/webp, ≤ 512 KB each, ≤ 4 per signature. They are referenced in the signature HTML as `cid:sig-<hash>`.
- **Compose sanitizing.** `SanitizeCompose` allows `cid:sig-*` img src in compose mode.
- **Building mail.** `mail.Build` wraps the body in multipart/related with those parts when they are referenced.
- **Editor.** The signature editor gets 插入图片 (from the mockup toolbar), uploading through a bound method that checks the type and size.
- **Compose and drafts.** Compose shows the signature images in the read-only signature preview. The drafts pane shows them too.
- **Tests.** Sanitizer rules, Build output (related parts, Content-ID), caps, and the editor upload.

### Task 6: Send from the drafts pane (MainDrafts)

- **Toolbar.** The drafts-pane toolbar group becomes [继续编辑, 发送, 删除草稿] as in the mockup. 发送 is disabled with the tooltip `正在撰写窗口里编辑，先在那里发送` while `ComposeOpen`.
- **Send flow.** Send uses `SendRevision`, then:
  - `*SendWarningsError` shows a confirm dialog listing the warnings, then calls `SendAcknowledgedRevision` with the digest;
  - `*SendBlockedError` shows the block;
  - `ErrStaleDraft` reloads.
- **Tests.** Each outcome, and the disabled state.

### Task 7: Wit `+` / `@` quoting and 本次对话都允许 (Ask, WitInput, Compose panel)

- **Quoting.**
  - The input's `+` menu has `引用邮件  @` (from the mockup), and typing `@` opens the same picker.
  - The picker searches mail (Task 1's search) and inserts a chip.
  - `Ask` / assist accept `quoted []int64`. Core loads them through `mailContext` in the run's scope and cites them like search results.
- **Session allow.**
  - The approval card shows 允许 / 本次对话都允许 / 拒绝 as in the mockup.
  - The grant is per conversation and per tool. It is stored, and it auto-approves later calls of that tool in that conversation, audited as `auto:session`.
  - It never applies to send. There is no send tool; make sure of that.
  - A visible note in the card explains it, and the grant clears when the conversation is deleted.
- **Tests.** Core grant scope and audit; the web picker and chips; the approval card buttons.

### Task 8: Settings search (SettingsSidebar)

- **Behaviour.**
  - The search field `搜索设置` at the top of the settings sidebar, as in the mockup.
  - A hand-kept index maps each settings section, and account pages, to the i18n keys of its rows.
  - Typing filters the sidebar to the matching sections.
  - Enter or click opens the section and highlights the matching row for 1.5 s.
- **Tests.** Index coverage: every row label key is in the index, checked by a test that walks the settings views' labels. Filtering.

### Task 9: Windows frame (MainWindows)

Render MainWindows. Then use `goos = "windows"` in a shot to render the app (the TitleBar height is 40, set through Tester.SetTitleBar). Compare the strip, radius, caption-button area, padding and Segoe-like font fallback. Fix any differences. Note in the report what can't be verified without Windows.
