# Review: docs/superpowers/plans/2026-10-12-design-features.md

Read-only review against the code at 4e306e0 plus the working tree, and against the mockups in `$D/design-v2/project/`. The tags are H (high), M (medium) and L (low). Paths are repo-relative unless they start with `$D`.

## Purge, roles and entered_at (Task 4)

**1. H — The purge can expunge the wrong server copy.**
- **Evidence.**
  - `move()` sets only `folder_id` and queues an op (core/messages.go:441-443).
  - `pushOp` acts on `server_folder_id`/`server_uid` (core/mailsync/ops.go:112-118).
  - When an earlier op failed, it clears `depends_on` and "runs from the message's current server location" (ops.go:100-110).
  - So a message moved into Trash whose move has not landed (offline, failed, `awaiting_claim`) has `folder_id = trash` but sits in INBOX on the server. A purge selected by `folder_id` would expunge the INBOX UID.
- **Fix.**
  - Select only rows with `folder_id = server_folder_id = <effective trash/junk folder>` that have no op in `pending`/`awaiting_claim`.
  - Store the purge folder in the op's `dst_folder_id`. At push time, refuse (conflict) unless `server_folder_id = dst_folder_id` and the folder's UIDVALIDITY matches.
  - Add a test for each case.

**2. H — A rescue loses to a purge already queued.**
- **Evidence.**
  - A move queued after a purge op gets `depends_on = latestOp`, which is the purge (messages.go:443). So the purge pushes first, and the user's "move back out of Trash" fails afterwards.
  - `pushOp`'s switch knows only `flags`/`move` (ops.go:132-150). An unknown kind fails.
- **Fix.**
  - Queueing any `move` for a message cancels its pending `purge` op in the same transaction.
  - Re-check eligibility (entered_at, role, still in folder) at push time, not only at queue time.
  - Add a test: purge queued, then the user moves the message out, then push. The message must survive.

**3. H — The `entered_at` migration and its write sites are unspecified.**
- **Evidence.**
  - The table has one row per (server folder, UID) (core/store/schema.go:31-47). SQLite `ADD COLUMN` cannot default to `unixepoch()`.
  - If the default is 0 or NULL and that reads as "old", the first enabled purge empties Trash/Junk at once.
- **Fix.**
  - The migration does `ADD COLUMN entered_at INTEGER NOT NULL DEFAULT 0` followed by `UPDATE messages SET entered_at = unixepoch()`.
  - The purge treats 0 as not eligible.
  - Every `folder_id` write sets `entered_at = unixepoch()`:
    - the import INSERT (mailsync/messages.go:37);
    - local `move` (messages.go:441);
    - the failed-move revert (ops.go:269);
    - the vanished-folder revert (mailsync/folders.go:82).
  - A claim (mailsync/messages.go:104) keeps the old value.
  - Document the result: the first sync, a UIDVALIDITY reset (fetch.go:126-151) and mail another device moved in all start the clock at "first seen", which errs safe.

**4. H — Role changes restart nothing, and INBOX can become Trash.**
- **Evidence.**
  - Detection rewrites `role` on every sync (mailsync/folders.go:67-68). A server rename or a new SPECIAL-USE flag can turn a long-lived folder into `trash`.
  - An override pointing Trash at a big folder would purge everything older than N days at once, because its `entered_at` values are old.
  - The mockup's candidate list holds only non-INBOX folders (SettingsAccounts.dc.html:133-157).
- **Fix.**
  - Store `role_since` per folder. The purge cutoff becomes `max(entered_at, role_since) + N`.
  - INBOX and the sent/drafts/archive folders cannot be picked as junk or trash (core validation, not just UI).
  - Turning a purge on also starts its clock (`purge_since`).
  - Test both the detected change and the override change.

**5. M — Several folders can hold the same role.**
- **Evidence.** `Delete` picks `role='trash' ORDER BY id LIMIT 1` (messages.go:420). The roleNames map can match both "Junk" and "Spam" (folders.go:26-31).
- **Fix.**
  - The effective role is unique per account: the override wins, otherwise the lowest id. Other folders with that detected role get `''` for every role consumer, purge included.
  - Put this in one helper, `roleFolder(ctx, acc, role)`. Delete, junk, archive and purge use it.

**6. M — Some role consumers are missing.**
- **Evidence.** The outbox archives to Sent through `mailsync.SentFolder`, which re-detects from LIST and never reads the DB (folders.go:135-146; core/outbox.go:273). Other consumers:
  - `viewCond` inbox (messages.go:106);
  - the app's archive map (app/mail_list.go:948);
  - the drafts folder mapping (mail_list.go:387);
  - sidebar order and icons (mail_sidebar.go:19,36,109).
- **Fix.**
  - List these consumers in Task 4.
  - The outbox gets the Sent folder from the effective role in the DB.
  - State what the 草稿 role does in Godwit: drafts are local, so it only picks which server folder the local drafts view stands in for. Nothing is APPENDed.

**7. M — The plan invents 不使用 for every role.**
- **Evidence.** The mockup adds `不使用` only when a role has no folder, or after a set archive (SettingsAccounts.dc.html:156-157). There is no 不使用 for a detected drafts/sent/junk/trash.
- **Fix.** Follow the mockup, or record the deviation for user sign-off.

**8. M — How overrides are stored and survive is unclear.**
- **Evidence.** Folders are keyed by name, and vanished folders are deleted (folders.go:72-86). A rename makes a new id, so an override by folder id dangles or disappears.
- **Fix.**
  - Use a `folders.role_override` column with a 'none' sentinel.
  - When the folder vanishes, the role falls back to detection. Its `role_since` restarts (item 4) and its purge stays off until confirmed, or the purge stays tied to that folder id.
  - Test: rename on the server, then purge does not hit the new folder.

**9. M — Expunge needs capability checks and an audit format.**
- **Evidence.** Without UIDPLUS, a plain EXPUNGE removes every `\Deleted` message in the folder (ops.go:211-235 comment).
- **Fix.**
  - The purge requires UIDPLUS (or MOVE plus UIDPLUS). Otherwise it skips and shows that in the settings row.
  - Delete the local row only after a confirmed `UID EXPUNGE`, or leave it to `syncFlags`.
  - Add one `audit_log` row per purge batch: account, folder, count and cutoff, with no subjects.
  - Store "once a day per folder" as `folders.purged_at`.

## Sync range (Task 3)

**10. H — Growing to 全部 never backfills.**
- **Evidence.**
  - `backfill` returns at once when `a.limit <= 0` (fetch.go:270).
  - Validation requires a limit of at least 1 (core/accounts.go:404).
  - So moving 2000 → 全部 (limit 0) leaves older mail unfetched, which breaks Review Focus 2.
- **Fix.**
  - Model the range as `(kind: count|months|all)`.
  - For `all`, backfill pages down below the oldest stored UID with no cap, in pages of a few thousand UIDs (fetch.go:296 collects everything into memory).
  - Test growing to 全部.

**11. H — SINCE breaks the "below the oldest stored UID" cursor.**
- **Evidence.** Backfill looks only at UIDs `1:min(server_uid)-1` (fetch.go:273-286). Under a date window, UIDs and INTERNALDATE are not monotonic (imports, APPEND with old dates, server migrations). A UID above the minimum but outside the old window is never fetched when the window grows.
- **Fix.**
  - For months or all, backfill runs `UID SEARCH SINCE <new cutoff>` (or ALL), limited to `UID 1:last_uid`, then fetches the difference against the stored UIDs of that folder, newest first, in pages.
  - The first sync in months mode uses `UID SEARCH SINCE` instead of the sequence range (fetch.go:181-188).
  - Remember that SINCE is date-only, uses INTERNALDATE and the server's timezone. Compute the cutoff as `now.AddDate(0,-3,0)` truncated to the date.

**12. M — Existing limits and dependent copy are not covered.**
- **Evidence.**
  - Onboarding offers 10000 and 100000 today (app/onboarding.go:838; zh.go:177-178). `limitOptions` already shows a custom value (app/settings_accounts.go:471-480).
  - The empty-search copy hardcodes `（每个文件夹最近 2000 封）` (zh.go:218).
  - The onboarding hint `更早的邮件在你翻到或搜索时再加载` (zh.go:175) promises lazy loading that does not exist.
- **Fix.**
  - Keep the custom "最近 N 封" option for existing accounts, and map 100000 to 全部 only if the user agrees.
  - Make `mail.search.empty_desc` follow the account's range (in unified view, one string per range, or drop the parenthesis).
  - Flag the onboarding hint to the user. It is design copy, but untrue today.

**13. L — Folder count placement.**
- **Evidence.** The mockup shows `6 个文件夹` in three places: the IMAP check row detail (Onboarding.dc.html:230), the done header (:182) and the account header meta (SettingsAccounts.dc.html:130,134). The plan names only two.
- **Fix.**
  - Add the check-row detail.
  - A failing LIST must not fail the imap step.
  - Count only selectable folders (folders.go:62).
  - Hide the count while it is 0 (before the first sync).

## Junk and trust sender (Task 2)

**14. M — Task 2's junk is built before Task 4's roles.**
- **Fix.**
  - Implement a `MoveToRole(ctx, ids, role)` that generalizes Delete (messages.go:413-427) on top of the `roleFolder` helper from item 5. Task 4 then changes only the helper.
  - Add a typed `ErrNoRoleFolder{Role}` and register it in the rpcbind codec list (rpcbind/bind.go:35 area). It is exposed automatically, because `Expose` covers every exported method (bind.go:101).

**15. L — The undo claim is false.**
- **Evidence.** Global Constraints say junk is "undoable like archive". No mail op has undo, and toasts have no action (app/shell.go:134-146).
- **Fix.** Drop "undoable", or scope a toast action plus a reverse Move as its own task. The mockup shows no toast.

**16. M — Trust-sender matching and storage.**
- **Evidence.**
  - `SetSettings` replaces the whole struct (core/settings.go:113-133). A reader click doing read-modify-write races the settings pages and the concurrent plan's `JudgeService` writes.
  - The Settings JSON is readable over the Local API (contract/api.go:38).
- **Fix.**
  - Add `TrustImageSender(ctx, addr)` and `UntrustImageSender` that modify the list under a lock.
  - Normalize with `mail.AddrKey`, and only when `net/mail.ParseAddress` succeeds (AddrKey falls back to the raw string, core/mail/reply.go:11-16).
  - Match the From address only, never the display name, Reply-To or Sender.
  - Never auto-load images for messages in the junk-role folder. From is unauthenticated, and spoofed "trusted" senders are mostly spam.
  - Decide in core (webui/reader.go `build` and the image gate at :422-437), not only in the app.

**17. L — The trusted-senders list in 隐私与安全 has no artboard.**
- **Evidence.** The mockup only has the `始终信任此发件人` link (Main.dc.html:230). The list in 隐私与安全 is invented, though reasonable.
- **Fix.** Mark it as a deviation that needs user sign-off, and say whether it is built with the existing settings row helpers.

## Signature images (Task 5)

**18. H — cid resolution scope, lifetime and garbage collection.**
- **Evidence.**
  - The signature HTML is copied into every draft body (core/compose.go:114,138-150) and swapped on account change (core/drafts_extra.go:39-41).
  - Build runs at send time in `enqueue` (compose.go:556).
  - Drafts created before the user edits or removes an image still reference the old cid.
  - Quoted mail can carry another Godwit user's `cid:sig-*`.
- **Fix.**
  - Assets are content-addressed and immutable, keyed by (account, hash).
  - Build resolves only cids belonging to the draft's account. Unknown or foreign `cid:sig-*` sources are stripped before send.
  - Garbage-collect an asset only when neither the account signature nor any draft references it.
  - Use `Content-ID: <sig-<hash>@godwit>`, the RFC 2392 msg-id form.

**19. H — The sanitizer allowance needs a resolver.**
- **Evidence.**
  - `SanitizeCompose` runs on every save, read and build (sanitize.go:198; build.go:83; GetDraft compose.go:252).
  - A bare prefix allowance would keep `cid:sig-*` in Wit/MCP-written HTML and in quotes (QuoteHTML).
- **Fix.**
  - Pass the known set of sig cids into the compose finish step (an `inline`-like map), and drop everything else.
  - Keep `data:` and remote sources stripped. Test both.

**20. M — Editor and preview round trip.**
- **Evidence.**
  - A webview cannot load `cid:`.
  - The compose page receives the signature as plain text (webui/compose.go:241) and keeps the HTML server-side (:41,273). This is good, because the editor never re-saves it.
  - The native drafts pane renders with `richtext`, which only accepts `data:` images (internal/richtext/model.go:47).
  - The signature editor (web/src/signature/Signature.tsx, webui/signature.go) saves through `SanitizeCompose`, which would strip `data:`.
- **Fix.**
  - On load, map cid to `data:` for display.
  - On save in the signature editor, map back to cid (or keep cid in the model and resolve it in a node view).
  - The compose preview is render-only and must never be saved.
  - Add a round-trip test.
  - Update the help text to the mockup's `可以加粗、加链接和图片（比如公司标志）。` (SettingsAccounts.dc.html; zh.go:298 still has the old text).

**21. M — Caps and validation.**
- **Fix.**
  - Sniff the type with `http.DetectContentType` and ignore the extension.
  - Strip EXIF (GPS) by re-encoding the image, or document that it is not stripped.
  - The images count toward `attachLimit`. The raw check exists (compose.go:560), but the early `total` check (:522-528) should include them.
  - Build produces related(alternative(text, html), images…), plus mixed when there are attachments. Test it by parsing with go-message.
  - MCP `get_draft` keeps returning text (no cid noise).
  - Signature assets need a migration. See item 27.

## Drafts-pane send (Task 6)

**22. M — The compose-open guard is stale, and the platform is not told.**
- **Evidence.**
  - `editing` is sampled at reload (app/drafts.go:67).
  - `deleteDraft` calls `a.plat.DraftDeleted` (drafts.go:197-206). A send must too, because the draft is deleted at enqueue (compose.go:571).
- **Fix.**
  - Re-check `ComposeOpen(d.ID)` at click time.
  - Chain on `draft:<id>` like delete.
  - Disable the button while a send is in flight.
  - On success, call `DraftDeleted`, clear `draftSel` and reload.
  - Take the warning dialog's copy from Compose.dc.html (`仍然发送`, `来自插件 … （message.beforeSend）`). The MainDrafts artboard has no dialog.
  - Handle digest expiry with a second warnings round.
  - Tests:
    - the warnings path, then acknowledged, then sent;
    - blocked;
    - stale, then reload;
    - compose opened between render and click, then refused.

## Wit quoting and session allow (Task 7)

**23. H — A session grant on external tools becomes an exfiltration channel.**
- **Evidence.**
  - Besides `create_draft` (core/wit.go:151-155), the tools that need approval are plugin and MCP tools with egress (core/mcp_tools.go:164; destinations at schema.go:174).
  - Quoting marks the run as having read mail (mailcontext.go:66). That taint is exactly when external calls need approval.
  - A per-tool grant lets injected mail drive repeated egress unseen.
  - The mockup shows the button only on `create_draft` (Ask.dc.html:89-102).
- **Fix.** Allow the grant only for built-in, non-external tools without destinations (today only `create_draft`). Never allow it for `modeExternal`, and never for a call with destinations. Test that an MCP/plugin approval offers no session button and is never auto-approved.

**24. M — Where and how the grant applies.**
- **Evidence.**
  - Approvals are inserted in `settleRun` (core/conversations.go:893-900).
  - `applyInitial` (core/assist.go:343-358) already auto-decides approvals after settle, then resumes under `runMu` (conversations.go:820-826).
  - `decide` takes `runMu` (approvals.go:237).
- **Fix.**
  - Apply the grant inside `applyInitial`, generalized, using `decide(..., resume=false)`. Do not apply it in `settleRun`, which would deadlock or double-resume.
  - The grant API is `ResolveApprovalForConversation(ctx, approvalID)`. It reads the tool and conversation from the approval row, never from client parameters. It also accepts that tool's other pending approvals in the run.
  - Thread a decision value through `execute` so `auditRow` records `auto:session` (approvals.go:93,279) instead of `executed`.
  - Key the grant on (conversation, tool, binding).
  - Keep it in memory, matching "本次", or in `conversations` with cascade on delete.
  - The visible note is the mockup's verbatim `发送仍需你在撰写页亲自点击` (Ask.dc.html:102), not invented copy.
  - The button appears in Ask only. Compose has no such button.

**25. M — Quoting API and limits.**
- **Evidence.**
  - `Ask(ctx, conv, input)` is part of the contract and Local API (contract/api.go:43,258).
  - `mailContext` budgets total text but not the number of messages (mailcontext.go:65-).
  - The scope exists only after `claimRun` (conversations.go:650-675), so quoted blocks must be built after the claim (the `claimed` hook or `request`), with citations from `s.number(..., call="")`.
- **Fix.**
  - Add `AskWith(ctx, conv, input, quoted []int64)` and keep `Ask` (update contract and scenarios only if it is exposed).
  - Dedupe and cap the quoted ids (for example at 10).
  - Reject ids that don't exist.
  - Emit a `context` event with citations, as Assist does.
  - The `+` menu's only item is `引用邮件 @` (WitInput.dc.html:34). The picker and chip designs are not in the mockup, so mark them as invented and build them from kit tokens.

## Settings search (Task 8)

**26. M — The highlight is unbuildable as written, and the index test needs a mechanism.**
- **Evidence.**
  - The mockup shows only the `搜索设置` field (SettingsSidebar.dc.html:19). There is no results or highlight state.
  - The survey notes there are no scroll anchors (survey:51).
  - Keys for general and privacy are not section-prefixed (zh.go `settings.theme.*`, `settings.local_api.*`), so walking keys cannot find the section.
- **Fix.**
  - Filter the sidebar only, and drop "highlight 1.5 s" unless the user wants it.
  - The test renders each section in the harness, collects every row label, and asserts that each appears in that section's index terms.
  - Exclude plugin-provided config labels.
  - Run this task after the capability plan's Tasks 4 and 5 rebuild the models page. Otherwise the index churns.

## Search filters (Task 1)

**27. L — The menus' contents are invented.**
- **Evidence.**
  - The mockup chips have `aria-haspopup="menu"` but define no menu items (Main.dc.html:121). `有附件` is a toggle.
  - `has_attachments` counts inline parts too (mailsync/messages.go:39; `len(p.Attachments) > 0`).
- **Fix.**
  - Write the 发件人 and 时间 options into the plan (for example free-text sender, and 今天/最近 7 天/最近 30 天/今年) and mark them as invented.
  - Allow empty text when a filter is set (messages.go:149-151).
  - Add an `(account_id, date)` index, or accept a full scan with the limit.
  - Keep the contract's `Search` unchanged unless a new method is needed.

## Windows frame (Task 9)

**28. L — The mockup differs from the code.**
- **Evidence.**
  - The mockup's Windows window has a solid `#EEEEEE` background with a `#D4D4D4` border, a 40px strip, a right pad of 150 (3×46 caption buttons) and a left pad of 8 (Main.dc.html:470-475).
  - The code uses Mica vibrancy (cmd/godwit/gui.go:26-41).
- **Fix.** List the background and material difference for the user. Take the font fallback from theme tokens (memory: tokens only).

## Conflicts with the concurrent model-capabilities plan, and task order

**29. H — Shared working tree and shared files.**
- **Evidence.** The capability plan's Task 2 is uncommitted right now in these files:
  - core/store/schema.go (a new migration group);
  - core/settings.go;
  - core/models*.go.

  This plan's Tasks 3, 4, 5 and 7 add migrations. Task 2 edits core/settings.go. Task 7 edits core/wit.go (capability Task 3 adds `classify_mail` there). Both plans edit internal/i18n zh.go/en.go and app/shots_test.go.
- **Fix.**
  - Run in a separate worktree, or serialize.
  - Never `git add -A` or `commit -a`; stage explicit paths.
  - Each schema task appends one new group at the end, after the capability group lands, and never edits an existing group.
  - Order: Task 1 → Task 4 core (the roleFolder helper, entered_at) → Task 2 → Task 3 → Task 4 UI and purge → Task 6 → Task 5 → Task 7 after capability Task 3 → Task 8 after capability Task 5 → Task 9.

**30. M — Review Focus tests are missing.**
- **Purge:**
  - the entered_at migration;
  - pending/awaiting move into Trash, which must not purge (item 1);
  - rescue after a queued purge (item 2);
  - no UIDPLUS, which must skip;
  - duplicate trash folders;
  - a server rename;
  - an INBOX override, which must be refused.
- **Sync:**
  - growing to 全部;
  - non-monotonic UID/date;
  - shrinking never deletes;
  - the hint copy following the range.
- **Session:**
  - another conversation;
  - another tool;
  - external/MCP tools never granted;
  - binding change, which goes stale;
  - the audit value;
  - delete conversation, then no grant.
- **Signature:**
  - remote and `data:` sources dropped;
  - a foreign `cid:sig` dropped at build;
  - the MIME tree parsed;
  - the attachLimit including images.
- **Drafts:** the click-time ComposeOpen check, and `DraftDeleted` called.
