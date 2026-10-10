# Web UI Plan 2: Full Compose in the Web Window — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The compose window does everything the native compose page does except Wit: rich-text body (Plate), all header fields, account switch, attachments, quote and signature, send with plugin warnings and blocks, discard, conflict resolution, transparent title bar, empty drafts deleted on close, and a window closed when its draft is deleted elsewhere.

**Architecture:** `webui.ComposePage` grows from `Load`/`Save`/`Closed` to the spec §B2 table minus Wit. Every method still acts only on the calling window's draft. The page is the editor and a small state machine (plan 1's saver, extended with exclusive operations). Go owns the rules: sanitising, revisions, send checks and deleting. The page's copy comes from the native i18n tables through `Load`, so the window speaks the user's language without a second translation table. The window stays behind the dev-only menu item: plan 3 adds Wit, then switches every entry point over and deletes the native page (spec §F, as revised).

**Tech Stack:** Go 1.27, mygo v0.3.7, React 19, shadcn (Base UI; generated, never hand-edited), Plate v53 (`platejs`, `@platejs/basic-nodes`, `@platejs/link`, `@platejs/list-classic`), Vitest.

**Spec:** `docs/superpowers/specs/2026-10-10-web-ui-design.md` (§B1–§B3; §B4 is plan 3). Builds on plan 1 (`docs/superpowers/plans/2026-10-10-web-ui-plan-1-foundation.md`). Branch `feat/compose-full`, stacked on `feat/compose-window` (PR #14).

## Global Constraints

- Every bound method acts only on the draft attached to the calling window (`mygo.CallerWindow`); the page never sends a draft ID. Window management stays on unbound types (spec §B2).
- Only `ComposePage` is bound. New bound methods: `Latest`, `SetAccount`, `AddAttachments`, `RemoveAttachment`, `Send`, `Discard` (plus plan 1's `Load`, `Save`, `Closed`).
- Saving follows `draftsaver`: 1 s after the last input; never while an IME composition is active; serial; flush before close, send, account switch and attachment changes; while sending, edits are refused with 「正在发送，暂时不能修改草稿」 (`compose.sending.locked`) (spec §B2).
- The body's formats are exactly: paragraph, bold, italic, link, bulleted list, numbered list, block quote (`richtext.FromComposeHTML`'s set). No other Plate plugins (spec §B3).
- HTML in: Plate's HTML deserializer, run on an inert `DOMParser` document (no script runs). Paste uses the same path. HTML out: the page's own serializer for that node set. Go stores the result through `SaveDraft`, which runs `SanitizeCompose` (spec §B3).
- The quote and signature never enter the editor. They show below the body as read-only plain text; the quote is collapsed behind 「显示引用的原邮件（助手只读）」 (`compose.quote.show`) (spec §B3).
- Links in the editor accept only `http`/`https` with a host, or `mailto` (as `editor.SetLink`).
- Copy comes from the native i18n keys (`compose.*`, `common.*`, `close.*`, `error.*`, `mail.no_subject`), delivered in `ComposeState.Text`. The page hard-codes no user-facing string. Plan 1's hard-coded strings move to keys too.
- One operation at a time per window: autosave, account switch, attachment changes, send, discard and the close answer all go through the saver's queue; while an operation runs the whole form (fields, editor, toolbar, attachment buttons) is frozen, and every path — rejection included — unfreezes in `finally`.
- Before any operation, pending input is settled: uncommitted address text becomes chips, an open IME composition commits (blur, up to 1 s, as plan 1's close path), then the save completes. If the save fails, conflicts, or the composition does not finish, the operation does not run.
- Send carries the revision the page saved; Go sends only that revision (else `Conflict`). Empty-draft deletion is atomic on the revision Go checked.
- Attachment limit: `core.MaxMessageBytes` (25 MB). Oversize reports `compose.attach.too_large` with the limit formatted as `byteSize` does (`%d B` / `%d KB` rounded / `%.1f MB`).
- The window stays reachable only from the dev-only Mail › 写邮件（网页窗口）. No existing entry point changes; the native compose page is untouched (Ruling: Wit lives only there until plan 3).
- Generated shadcn components are added only through `web/scripts/components.ts`, never edited. Exact npm pins; `bun.lock` committed.
- Commits end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Go doc comments on every exported name.

## Review Focus

1. **Send while a save is pending or in flight**: the sent message holds the last keystrokes; nothing typed during sending is silently dropped (Task 6 `send flushes first and freezes`; Task 2 `TestComposeSendSendsLatest`).
2. **Account switch or attachment change racing an autosave**: no conflict is raised against the page's own write, and the body typed just before is kept (Task 4 `run waits for the save then chains the revision`; Task 2 `TestComposeSetAccountKeepsBody`).
3. **Pasting rich HTML from a web page or Word** (styles, images, tables, scripts): only the allowed formats land in the editor, no script runs, no remote request is made (Task 3 `paste keeps only allowed formats`, `deserialize runs no script`).
4. **Closing a window whose draft is empty versus one with only a quote and signature** (a fresh reply): both are deleted; a draft with an attachment or any header is kept (Task 1 `TestComposeReleaseDeletesEmpty`).
5. **Draft deleted from the drafts list while its window is open**: the window closes without asking, and no save resurrects the draft (Task 2 `TestWindowsDraftDeletedCloses`; Task 1 `TestComposeSaveAfterDeleteRefused`).

---

## File Structure

| Path | Responsibility |
|---|---|
| `webui/compose.go` | session, `Compose` (unbound), `ComposePage` facade: grows the new methods |
| `webui/compose_state.go` | the wire types (`ComposeState`, `ComposeEdit`, `Attachment`, `SendResult`, …) moved out of compose.go as they grow |
| `webui/text.go` | **deleted** (plain text body is gone); `webui/strings.go`: the i18n key list for `ComposeState.Text` |
| `webui/windows.go` | `Done(winID)` (close without asking), `CloseDraft(draftID)` |
| `cmd/godwit/platform.go` | file picker, transparent title bar, cascade, `DraftDeleted` |
| `app/drafts.go`, `app/app.go` | `Platform.DraftDeleted(id)` after a drafts-list delete |
| `web/src/shared/i18n.ts` | `t(key, ...args)` over `ComposeState.Text` |
| `web/src/compose/saver.ts` | `run(op)` exclusive operations; `freeze` |
| `web/src/compose/editor/` | `plugins.ts`, `html.ts` (deserialize / serialize), `Editor.tsx`, `Toolbar.tsx`, `LinkPopover.tsx` |
| `web/src/compose/fields/` | `AddressInput.tsx`, `address.ts` (commit / validate), `Fields.tsx` |
| `web/src/compose/Attachments.tsx`, `SendBar.tsx`, `Notices.tsx`, `size.ts` | attachments, send area, warnings / block / conflict, `byteSize` |
| `web/src/compose/ComposePage.tsx` | assembles the page |

---

### Task 1: The compose service, rich body and the new methods

**Files:**
- Create: `webui/compose_state.go`, `webui/strings.go`
- Modify: `webui/compose.go`
- Delete: `webui/text.go`, `webui/text_test.go`
- Test: `webui/compose_test.go`, `webui/strings_test.go`

**Interfaces:**
- Consumes: core `GetDraft`, `SaveDraft`, `SetDraftAccount`, `AddAttachment`, `RemoveAttachment`, `Send`, `SendAcknowledged`, `DeleteDraft`, `ErrStaleDraft`, `ErrTooLarge`, `SendWarningsError`, `SendBlockedError`, `MaxMessageBytes`; `mail.SplitDraft` / `JoinDraft` / `HTMLToText`; `i18n.T`.
- Produces (wire types, JSON camelCase as in plan 1):
```go
type Attachment struct{ ID int64; Filename string; Size int }
type ComposeState struct {
	DraftID int64; Accounts []Account; From int64
	To, Cc, Bcc []string; ReplyTo, Subject string
	Body string            // the editable part as HTML (sanitised)
	Quote, Signature string // plain text, read-only
	Attachments []Attachment
	Revision int64; Theme Theme
	Text map[string]string // i18n key → text in the UI language, for the keys in strings.go
}
type ComposeEdit struct{ To, Cc, Bcc []string; ReplyTo, Subject, Body string; Revision int64 } // Body is HTML
type SaveResult struct{ Revision int64; Conflict bool }
type Change struct{ Revision int64; Conflict bool; Signature string; Attachments []Attachment } // result of SetAccount / attachment ops: Revision and Attachments re-read from the draft after the change
type AttachResult struct{ Change; TooLarge []string; Failed []string } // per-file outcome: over the limit, or failed for another reason (logged); never an error for a partial success
type SendWarning struct{ Plugin, Message string }
type SendResult struct{ Sent, Conflict bool; Warnings []SendWarning; Digest string; Blocked *SendWarning } // Blocked: the plugin and its message
```
- Produces (bound, on `*ComposePage`): `Load`, `Save`, `Closed` (plan 1) plus
```go
func (p *ComposePage) Latest(ctx) (ComposeState, error)                     // re-read for conflict resolution; resets the session's quote/signature
func (p *ComposePage) SetAccount(ctx, id, revision int64) (Change, error)  // SetDraftAccount; stale → Conflict
func (p *ComposePage) AddAttachments(ctx, revision int64) (AttachResult, error) // stale revision → Conflict before the dialog; picks files (dialog parented to the calling window; no window → error), adds each
func (p *ComposePage) RemoveAttachment(ctx, id, revision int64) (Change, error)
func (p *ComposePage) Send(ctx, revision int64, digest string) (SendResult, error) // sends exactly that revision: core SendRevision / SendAcknowledgedRevision; else Conflict
func (p *ComposePage) Discard(ctx) error                                    // DeleteDraft, then the window closes
```
- Produces (unbound): `NewComposePage(c *Compose, d PageDeps) *ComposePage` with `type PageDeps struct{ WinOf func(context.Context) (int, bool); PickFiles func(ctx context.Context, winID int) ([]string, error) }`; `(*Compose).OnDone(fn func(winID int))` (fires after a successful send or discard, so `Windows` destroys without asking); `(*Compose).Release(winID int)` (replaces `Detach` for a closed window: marks the session closed so new calls get `ErrNoDraft`, waits for the call in progress, then deletes the draft if it is empty, atomically). Callbacks (`OnDone`, `OnClosed`) run after the session lock is released.
- Produces (core, `core/drafts_extra.go`, with tests in `core/drafts_extra_test.go`): `func (s *Service) SendRevision(ctx, draftID, revision int64) error` and `func (s *Service) SendAcknowledgedRevision(ctx, draftID, revision int64, digest string) error` (as `Send` / `SendAcknowledged`, `ErrStaleDraft` if the stored revision differs — pass it into `send`'s existing revision check); `func (s *Service) DeleteDraftIfEmpty(ctx, id, revision int64) (deleted bool, err error)` (under `editMu`: the row still has that revision and is empty by the rule below → delete as `DeleteDraft`).

- [ ] **Step 1: Failing tests** (`coretest`, as plan 1; names start with `TestCompose`):
  - `TestComposeLoadRichBody`: a draft saved with `<p>a <strong>b</strong></p>` + quote + signature → `Body == "<p>a <strong>b</strong></p>"`, `Quote`/`Signature` plain text, `Text["compose.send"] == "发送"` with UI language zh.
  - `TestComposeSaveRichBody`: `Save{Body: "<p>x</p><ul><li>y</li></ul><script>z</script>"}` → stored editable has the list and no script; quote/signature bytes unchanged.
  - `TestComposeSetAccountKeepsBody` (Review Focus 2): save body B at r, `SetAccount(acc2, r+1)` → `Change.Signature` is acc2's signature text, revision r+2, stored body still B.
  - `TestComposeSetAccountStale`: wrong revision → `Change{Conflict: true}`, account unchanged.
  - `TestComposeAttachments`: `PickFiles` fake returning a small file and a 26 MB sparse file → one attachment added, `TooLarge == [big name]`, `Revision` advanced by 1; `RemoveAttachment` → gone, revision +1. `PickFiles` returning nothing → no change, no error.
  - core `TestSendRevisionStale`, `TestDeleteDraftIfEmptyRevision` (a save after the check keeps the draft).
  - `TestComposeAttachPartial`: one readable file and one unreadable path → one added, `Failed == [name]`, no error, `Revision` and `Attachments` equal a fresh `GetDraft`.
  - `TestComposeReleaseWaitsForSave`: a `Save` in progress when `Release` runs → release waits; the saved non-empty draft is kept.
  - `TestComposeSendSendsLatest` (Review Focus 1): `Save` body B, `Send(r, "")` → `Sent`, outbox message body contains B; `OnDone` fired for the window; draft gone.
  - `TestComposeSendWarnings` / `TestComposeSendBlocked`: with core's test send-check plugin (see `core/beforesend_test.go`) → `Warnings` + `Digest`; `Send(r, digest)` → `Sent`; `Send` with an older revision → `Conflict`, nothing sent. Blocked → `Blocked` carries the plugin and its message, not sent, no `OnDone`.
  - `TestComposeDiscard`: `Discard` → draft deleted, `OnDone` fired.
  - `TestComposeLatest`: external `SaveDraft` → `Latest` returns its body and revision.
  - `TestComposeReleaseDeletesEmpty` (Review Focus 4): `Release` deletes a new empty draft, a reply draft with only quote + signature, and a draft whose body is `<p></p><p> </p>`; keeps one with a subject, one with only a Bcc, one with an attachment, one with body text.
  - `TestComposeSaveAfterDeleteRefused` (Review Focus 5): draft deleted externally → `Save` returns an error (not a resurrected draft; `GetDraft` still not found).
  - `TestComposeBoundSurface`: exported methods of `*ComposePage` are exactly `AddAttachments, Closed, Discard, Latest, Load, RemoveAttachment, Save, Send, SetAccount`.
  - `strings_test.go` `TestComposeStringsExist`: every key in the list has a zh and an en translation (`i18n` tables), so `Text` never carries a raw key.
- Migrate plan 1's tests that use `bodyText`/`bodyHTML` (`webui/compose_test.go`, `webui/windows_test.go`) to HTML bodies.
- [ ] **Step 2: Run** `CGO_ENABLED=0 go test ./webui ./core -run 'TestCompose|TestSendRevision|TestDeleteDraftIfEmpty'` → FAIL.
- [ ] **Step 3: Implement.** Session keeps the loaded quote/signature HTML (plan 1) and refreshes them on `SetAccount` (signature) and `Latest`. Empty = no To/Cc/Bcc/ReplyTo/Subject, no attachments, and `strings.TrimSpace(mail.HTMLToText(editable))` (non-breaking spaces trimmed too) is empty. A block maps to `Blocked: &SendWarning{Plugin, Message}`; the page shows it with `compose.warn.from`. `strings.go` lists the keys the page uses: all `compose.*` from i18n lines 490–535 except `compose.sent`, plus `common.cancel`, `common.close`, `common.retry`, `close.unsaved.title`, `error.stale_draft`, `error.too_large`, `mail.no_subject`. Delete `text.go` and its tests; `Load`/`Save` pass HTML through.
- [ ] **Step 4: Run** `go test -race ./webui ./core` (race needs cgo) → PASS; `go vet ./...`.
- [ ] **Step 5: Commit** `feat(webui): rich body, account, attachments, send, discard and latest for compose windows; empty drafts deleted on close`

---

### Task 2: Windows: done, deleted elsewhere, title bar, cascade, wiring

**Files:**
- Modify: `webui/windows.go`, `cmd/godwit/platform.go`, `cmd/godwit/gui.go`, `app/app.go` (`Platform.DraftDeleted`), `app/drafts.go`, `app/compose.go` (native `gone()` after send or discard also calls `DraftDeleted` — the native page can still send or discard a draft open in a window until plan 3), fake platforms in `app/*_test.go`
- Regenerate: `web/src/mygo.ts`
- Test: `webui/windows_test.go`, `cmd/godwit/compose_window_test.go`, `app/compose_window_test.go`

**Interfaces:**
- Consumes: Task 1's `OnDone`, `Release`, `PageDeps`.
- Produces: `(*Windows).Done(winID int)` (destroy now, no flush, no ask — the draft was sent or discarded); `(*Windows).CloseDraft(draftID int64)` (same, for a draft deleted elsewhere); `Windows.Closed` calls `Compose.Release` instead of `Detach`; `app.Platform.DraftDeleted(id int64)`; `composeWindowOptions()` with the transparent title bar.

- [ ] **Step 1: Failing tests:**
  - `TestWindowsDoneDestroysWithoutAsking`: an open window, `Done` → destroyed, `ask` not called, no `EmitClosing`; a pending close of that window settles.
  - `TestWindowsDraftDeletedCloses` (Review Focus 5): `CloseDraft(7)` → the draft-7 window destroyed without asking; others untouched; unknown draft → no-op.
  - `TestWindowsClosedReleases`: with a real `Compose` (coretest), after `Closed` the window's empty draft is deleted and its `Load` gets `ErrNoDraft`.
  - `cmd/godwit` `TestComposeWindowOptions` extended: title bar hidden-inset style on darwin (mygo's option for a transparent title bar with traffic lights — find its name in `window.go`), `Title` 「新邮件」 kept for the window menu.
  - `cmd/godwit` `TestComposeWindowCascade`: the adapter offsets a new window by (24, 24) from the last open compose window's position (fake position source).
  - `app` `TestDeleteDraftNotifiesPlatform`: drafts-list 删除草稿, native discard and native send → fake platform records `DraftDeleted(id)` after each succeeds.
- [ ] **Step 2: Run** `CGO_ENABLED=0 go test ./webui ./cmd/godwit ./app -run 'TestWindows|TestComposeWindow|TestDeleteDraftNotifies'` → FAIL.
- [ ] **Step 3: Implement.** `PickFiles` in `cmd/godwit`: mygo's open-file dialog, multi-select, parented to the window with that ID (find the window via `Windows`/mygo by ID). Bind with `NewComposePage(compose, webui.PageDeps{…})`. `Compose.OnDone(windows.Done)`. `platform.DraftDeleted` → `windows.CloseDraft`. Run `mise run generate`; commit the client.
PickFiles: `mygo.WindowByID`; no window → error (never an unparented dialog); `mygo.Dialog.Open{Parent, Multiple: true}`. Title bar: `TitleBarHiddenInset`.
- [ ] **Step 4: Run** `go test -race ./webui ./cmd/godwit ./app`, `go test ./...`, `go vet ./...` → PASS.
- [ ] **Step 5: Commit** `feat(webui): compose windows close on send, discard and deletion elsewhere; transparent title bar and cascade`

---

### Task 3: The editor (Plate) with HTML in and out

**Files:**
- Modify: `web/package.json` (+ `platejs` 53.3.15, `@platejs/basic-nodes` 53.0.0, `@platejs/link` 53.3.5, `@platejs/list-classic` 53.0.0 — or the current versions when the task runs, exact), `web/scripts/components.ts` (+ `toggle`, `toggle-group`, `popover`, `tooltip`, `separator`)
- Create: `web/src/compose/editor/plugins.ts`, `html.ts`, `Editor.tsx`, `Toolbar.tsx`, `LinkPopover.tsx`
- Test: `web/src/compose/editor/html.test.ts`, `Editor.test.tsx`

**Interfaces:**
- Produces:
```ts
export const plugins: AnyPluginConfig[]                    // paragraph, bold, italic, link, list-classic (ul/ol/li), blockquote — nothing else
export function fromHTML(editor: PlateEditor, html: string): Value  // DOMParser("text/html") (inert), then strip every element and attribute that can load a resource (img, picture, source, video, audio, iframe, object, embed, link, style, script, svg; src, srcset, background, poster, style) before Plate's deserializer; empty → [{type:"p",children:[{text:""}]}]
export function toHTML(value: Value): string                // <p>, <strong>, <em>, <a href>, <ul>/<ol>/<li> (nested allowed), <blockquote>, <br> for a soft line break (Shift+Enter, and a newline inside a text node); text escaped; an empty paragraph is <p></p>
export function safeHref(url: string): string | null       // http/https with host, or mailto; else null
export function Editor(props: { initial: string; readOnly: boolean; placeholder: string; onChange(html: string): void; onComposing(on: boolean): void; t: T }): JSX.Element
```
`Toolbar` (toggle group: 加粗 / 斜体 / 链接 / 列表 / 编号列表 / 引用, labels from `compose.bold` … `compose.quote`; plus the 添加附件 button slot) and `LinkPopover` (input placeholder `https://`, label `compose.link.url`, button `compose.link.apply`).

- [ ] **Step 0: Verify Plate** — install the pins; in a scratch test confirm and note in the report: the list-classic node shape (`ul`/`ol`/`li`/`lic`), the deserialize entry point and its input type, how Plate exposes IME composition (or use the editable's DOM `compositionstart/end`), and the paste hook to override (`editor.tf.insertData` or the HTML plugin's parser). Adjust names in this task to the real APIs; the behaviour stays.
- [ ] **Step 1: Failing tests** (`html.test.ts`, jsdom):
  - `round-trips the allowed formats`: for each of `<p>a</p>`, `<p><strong>b</strong> <em>i</em></p>`, `<p><a href="https://x.com/">l</a></p>`, `<ul><li><p>1</p></li></ul>`, `<ol><li><p>1</p></li><li><p>2</p></li></ol>`, `<blockquote><p>q</p></blockquote>`, `<p>a<br>b</p>`, `<p>a<br></p>`, `<p></p><p>x</p>`, `<ul><li><p>1</p><ul><li><p>1.1</p></li></ul></li></ul>` → `toHTML(fromHTML(e, s)) === s` (normalised as `toHTML` writes it); and the native page's `richtext.ToHTML` output for a sample with soft breaks (copy one from `internal/richtext` tests) keeps its line breaks.
  - `paste keeps only allowed formats` (Review Focus 3): `<h1 style="color:red">T</h1><table><tr><td>c</td></tr></table><img src="https://t/x.png"><p><u>u</u><span style="font-size:30px">s</span></p>` → no `h1`, `table`, `img`, `style`, `u`; text `T`, `c`, `u`, `s` kept as paragraphs.
  - `deserialize runs no script and strips loaders` (Review Focus 3): `<img src=x onerror="window.__hit=1"><script>window.__hit=2</script><iframe src=x></iframe><p style="background:url(x)">t</p>` → `window.__hit` undefined after a tick; the stripped document handed to Plate has no `img`/`iframe`/`script` and no `src`/`style` attribute (assert on the sanitising step's output). Request-free parsing in WebKit is checked by hand in Task 6 (Network tab).
  - `unsafe links dropped`: `<a href="javascript:alert(1)">x</a>` → text `x`, no link; `safeHref("ftp://x")` null, `safeHref("mailto:a@b.c")` kept.
  - `Editor.test.tsx`: ⌘B toggles bold on a selection; typing calls `onChange` with HTML; `compositionstart`/`end` call `onComposing(true/false)`; `readOnly` refuses input; the link popover applies a link to the selection and rejects `ftp://`.
- [ ] **Step 2: Run** `mise run web:test` → FAIL.
- [ ] **Step 3: Implement.** Toolbar state from Plate's marks / block type. ⌘K opens the link popover when there is a selection (plan 3 routes ⌘K to Wit when a model is configured). Paste: Plate's default HTML paste goes through the same deserializer; check that it parses with `DOMParser` (inert) — if Plate parses with a live element, override the paste handler to call `fromHTML`.
- [ ] **Step 4: Run** → PASS.
- [ ] **Step 5: Commit**, then `mise run web:check` (needs a clean tree). `feat(web): compose editor on Plate — the native page's formats, HTML in and out, safe paste`

---

### Task 4: Saver operations, i18n and the address fields

**Files:**
- Modify: `web/src/compose/saver.ts`
- Create: `web/src/shared/i18n.ts`, `web/src/compose/fields/address.ts`, `AddressInput.tsx`, `Fields.tsx`; components script + `select`, `badge`
- Test: `saver.test.ts`, `web/src/shared/i18n.test.ts`, `fields/address.test.ts`, `fields/Fields.test.tsx`

**Interfaces:**
- Produces:
```ts
// saver
flush(): Promise<boolean>          // settles input (settle hook: commit pending addresses, finish IME), saves; false if the save failed, conflicted, or the composition did not finish
run<T extends { revision: number; conflict: boolean }>(op: (revision: number) => Promise<T>): Promise<T | null> // queued: waits for any running op, flush() (false → null, op not run), freezes, runs op with the current revision, adopts result.revision; conflict → status "conflict"; unfreezes in finally (also on rejection)
setSettle(fn: () => Promise<boolean>): void // the page's input-settling hook used by flush
onRefused(fn: () => void): void    // an edit arrived while frozen (the page toasts compose.sending.locked)
keepMine(latestRevision: number): Promise<boolean> // conflict → save the current local snapshot at latestRevision; stale responses of older requests ignored
useLatest(revision: number): void  // conflict → drop pending edits and the timer, ignore in-flight responses, adopt revision, status "saved"
// i18n
export type T = (key: string, ...args: (string | number)[]) => string // %s / %d substituted in order; a missing key returns the key
export function makeT(text: Record<string, string>): T
// address
export function commit(pending: string, list: string[], single: boolean): string[] // split on ",", trim, skip empty and exact duplicates; single (Reply-To) keeps the last
export function invalid(list: string[]): string[]  // not parsable as an address (name <addr> or addr)
```
`Fields` props: `{ accounts, from, onFrom(id), to, cc, bcc, replyTo, subject, onChange(edit), t }`. To always shown; 「抄送 · 密送」 button reveals Cc, Bcc and Reply-To together; they start revealed when any has a value. Invalid chips red, and `compose.invalid` lists them below. Commit on Enter, `,`, blur, and on `commitPending()` (exposed for the settle hook, so ⌘Enter and closing never drop typed-but-uncommitted addresses).

- [ ] **Step 1: Failing tests:**
  - saver `run waits for the save then chains the revision` (Review Focus 2): edit, `run(op)` before the debounce → the save happens first with r, `op` receives r+1, a later edit saves with `op`'s revision.
  - saver `frozen refuses edits`: during a slow `run`, `edit` → refused, `onRefused` once; after it resolves (or rejects) edits save again.
  - saver `flush false stops run`: a failing save → `run` returns null, `op` not called; a settle hook returning false (composition never ended) → same.
  - saver `one op at a time`: two `run`s and a close flush started together execute in order, never overlapping.
  - saver `use latest drops old content`: pending edit, conflict, `useLatest(r)` → the old edit is never saved; a late response from before is ignored.
  - saver `run conflict stops saves`: `op` resolves `{conflict: true}` → status `conflict`; `keepMine(r)` saves the local snapshot at r and saves resume.
  - i18n `substitutes in order`: `makeT({"a": "x %s y %d"})("a", "1", 2) === "x 1 y 2"`; missing → key.
  - address: `commit("a@b.c, a@b.c,  ,c@d.e", [], false)` → `["a@b.c","c@d.e"]`; existing duplicate skipped; `commit("x@y.z,w@v.u", ["a@b.c"], true)` → `["w@v.u"]`; `invalid(["ok@x.io","Name <n@x.io>","bad"])` → `["bad"]`.
  - `Fields.test.tsx`: typing `a@b.c,` makes a chip; blur commits pending text; `commitPending()` commits text still in the input; × removes; Cc hidden until 抄送 · 密送 or a value; invalid shows `地址无效：bad`; choosing another account calls `onFrom`.
- [ ] **Step 2: Run** `mise run web:test` → FAIL. **Step 3: Implement**; plan 1's close handshake uses `flush()`'s result for `closed(gen, saved)` and shares the settle hook. **Step 4: Run** → PASS.
- [ ] **Step 5: Commit** `feat(web): compose fields, saver operations and the page's copy from the native tables`

---

### Task 5: Attachments, send area and notices

**Files:**
- Create: `web/src/compose/size.ts`, `Attachments.tsx`, `SendBar.tsx`, `Notices.tsx`; components script + `alert-dialog`, `sonner`
- Test: `size.test.ts`, `Attachments.test.tsx`, `SendBar.test.tsx`, `Notices.test.tsx`

**Interfaces:**
- Produces: `byteSize(n: number): string` (`%d B` under 1 KB, `%d KB` rounded under 1 MB, `%.1f MB`); `Attachments({ items, tooLarge, onRemove, t })` (name, size, × labelled `compose.remove`; `compose.attach.too_large` with `byteSize(25 << 20)` when `tooLarge` is non-empty); `SendBar({ sending, status, savedAt, onSend, onDiscard, t })` (发送, 丢弃草稿 with the confirm dialog `compose.discard.title` / `.body` / `common.cancel` / `compose.discard.confirm`, note `compose.note`, status line); `Notices({ warnings, blocked, conflict, latestFailed, onSendAnyway, onKeepMine, onUseLatest, onRetry, t })`.

- [ ] **Step 1: Failing tests:** `byteSize(512) === "512 B"`, `byteSize(1536) === "2 KB"`, `byteSize(26214400) === "25.0 MB"`; discard asks and only `compose.discard.confirm` calls `onDiscard`; each warning shows `发送前检查：<msg>` and `来自插件 <p>（message.beforeSend）`, and 仍然发送 calls `onSendAnyway`; conflict shows `草稿在别处被修改` with 保留我的版本 / 使用最新版本, or 重试 when `latestFailed`; blocked shows as a danger alert with its message and `来自插件 <p>（message.beforeSend）`; status line: `正在保存…`, `草稿已保存 · 21:58`, `未保存`.
- [ ] **Step 2–4:** run → FAIL; implement; run → PASS.
- [ ] **Step 5: Commit** `feat(web): compose attachments, send bar and notices`

---

### Task 6: The page assembled

**Files:**
- Modify: `web/src/compose/ComposePage.tsx`, `ComposePage.test.tsx`, `web/src/shared/styles.css` (title-bar drag region)

**Interfaces:**
- Consumes: Tasks 1–5. Plan 1's close handshake (`closing` gen → flush → `closed(gen, saved)`, freeze during the answer) is kept as is.

Layout: a top bar (`--app-region: drag`, left inset for the traffic lights, title = subject or `compose.new`, status) → Fields → Toolbar → Editor (grows) → signature and quote (collapsed) → Attachments → Notices → SendBar. Behaviour:
- Load → `makeT(state.text)`, fields, editor `initial = state.body`, `applyTheme`.
- Edits → `saver.edit`; any edit clears warnings.
- Account change → `saver.run(r => api.setAccount(id, r))` → signature text updates; on conflict or null the select reverts.
- 添加附件 → `saver.run(r => api.addAttachments(r))`; × → `saver.run(r => api.removeAttachment(id, r))`; attachments and revision from the result; `Failed` names show under the list.
- Send / ⌘Enter → `saver.run(r => api.send(r, digest ?? ""))` (null → nothing sent, the status explains) → `Sent`: stay frozen (Go closes the window); `Warnings`: show; `Blocked`: show; `Conflict`: conflict notice. A frozen edit toasts `compose.sending.locked`.
- Discard confirmed → `saver.run(…api.discard())` (Go closes the window).
- Conflict: 保留我的版本 → `api.latest()`, adopt latest's account, signature, quote and attachments (not editable here), then `saver.keepMine(latest.revision)`; 使用最新版本 → `api.latest()`, refill everything, `saver.useLatest(latest.revision)`; read failure → 重试.

- [ ] **Step 1: Failing tests** (mocked client, as plan 1):
  - `send flushes first and freezes` (Review Focus 1): type, click 发送 at once → `save` with the typed body is called before `send(savedRevision, "")`; typing during the send is refused and the toast shows 「正在发送，暂时不能修改草稿」.
  - `send aborts on failed save`: `save` rejects → `send` never called, form editable.
  - `cmd-enter commits pending address`: text `x@y.z` still in the To input, ⌘Enter → the saved edit has it in `to`.
  - `send during composition`: a composition that never ends → `send` not called.
  - `send warnings then send anyway`: `send` resolves warnings → list shows; 仍然发送 → `send(digest)`.
  - `account switch keeps body`: type, switch account → `save` then `setAccount(id, savedRevision)`; signature text updates.
  - `keep mine after conflict`: save resolves conflict → notice; 保留我的版本 → `latest`, then `save` with the latest revision and the local body.
  - `use latest after conflict`: 使用最新版本 → fields and editor show latest's content.
  - `discard asks then discards`.
  - plan 1's tests still pass (close handshake, IME, StrictMode loads).
- [ ] **Step 2–4:** run → FAIL; implement; run → PASS; `mise run build`.
- [ ] **Step 5: Commit** `feat(web): the full compose page`, then `mise run web:check`.

- [ ] **Step 6: Manual (macOS)**, `mise run dev`, Mail › 写邮件（网页窗口）. Check each of these:
  - **Typing:** Chinese IME.
  - **Formats:** bold and italic with ⌘B / ⌘I; a link through the popover; both list types; a quote.
  - **Paste:** from a web page and from Word or Pages.
  - **Recipients:** chips; an invalid address.
  - **Account:** switch accounts and check the signature changes.
  - **Attachments:** add two, one of them over 25 MB; remove one.
  - **Send:** ⌘Enter sends to yourself and the window closes; then send with a test plugin warning, then 仍然发送.
  - **Discard.**
  - **Conflict:** edit the same draft in the old compose page.
  - **Empty draft:** close an empty window and check the drafts list has no new entry.
  - **Deleted elsewhere:** delete the draft from the drafts list while its window is open, and check the window closes.
  - **Paste makes no request:** with the inspector's Network tab open, paste HTML that has remote images; check that no request goes out.
  - **Window:** dark mode; the title bar can be dragged.

  Record the results in the PR body.

---

## Self-review notes

- Spec coverage:
  - §B1:
    - Window, title, title bar and cascade → Task 2.
    - Close, empty-draft deletion and deleted elsewhere → Tasks 1 and 2.
    - Quit is unchanged from plan 1.
    - The entry-point switch → plan 3 (ruled).
  - §B2: every non-Wit method → Task 1. `latest` replaces `resolveConflict` (spec updated). `openLink` and the `draftChanged` event move to plan 3 (Ruling): links in the editor are edited, never followed (navigation is denied), and the only other writers of an open draft — the native page until plan 3 — are caught by the revision checks as conflicts, while its send/discard close the window (Task 2).
- Codex review (plan v1) folded in: flush returns success and gates every operation; one queue for save, account, attachments, send, discard and the close answer, unfrozen in finally; pending addresses and IME settled first; send pinned to the saved revision (core `SendRevision`); atomic empty-draft deletion after in-flight calls; attachment results re-read and partial outcomes reported; resource-loading markup stripped before Plate parses, request-freeness checked by hand in WebKit; explicit keep-mine / use-latest; `<br>` and nested lists; native send/discard close the window; race tests with cgo; real `Compose` in window tests; web:check after commit; Plate APIs verified first.
  - §B3: fields → Task 4; Plate and HTML → Task 3; quote and signature, toolbar, attachments and send area → Tasks 3, 5 and 6; shortcuts → Tasks 3 and 6.
  - §B4: plan 3.
- Spec differs from the native page, and the spec wins:
  - ⌘Enter sends.
  - Esc closes popovers (Base UI default).
- Copy follows the survey's exact native strings, e.g. 「正在发送，暂时不能修改草稿」, not the spec's paraphrase.
