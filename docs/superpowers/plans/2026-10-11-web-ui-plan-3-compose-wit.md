# Web UI Plan 3: Wit in the Compose Window, then the Switch-over — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The compose window gets the native page's Wit: the side panel (chips, 调整 ▾, 翻译 ▾, free input, streamed answers, stop, retry), the review of Wit's edits in the body (inline per-change accept/reject or whole rewrite with compare), first-generation auto-apply, undo, and ⌘K selection rewrite. Then every entry point opens the compose window, and the native compose page, `internal/editor` and `app/draftsaver.go` are deleted.

**Architecture:**
- **Go.** Core already runs Wit, judges edits and applies decisions. `webui` exposes it per calling window, as bound methods plus a per-window event stream.
- **Review.** The review diff stays in Go: `app/proposal.go`'s logic moves to `webui/review.go` on `internal/worddiff`. The page renders Go's structured diff and returns the per-change decisions; Go builds the result HTML as the native page does.
- **Chat.** The chat uses AI SDK v7's `useChat` with our own `WitTransport` over the bound methods. Answers render with Streamdown, hardened per spec §A5. The chat components are generated shadcn ones (`message`, `message-scroller`, `input-group`, `marker`, `spinner`, `collapsible`, `card`, `hover-card`, `dropdown-menu`, `badge`), and the panel is built only from them.

**Tech Stack:** Go 1.27, mygo 0.4.0, React 19, AI SDK `ai` / `@ai-sdk/react` (v7 / v4 — exact current versions), `streamdown` (exact current), shadcn Base UI (generated), Plate v53 (plan 2).

**Spec:** `docs/superpowers/specs/2026-10-10-web-ui-design.md` §B4, §D1, §D2, §A5; §B1 and §F for the switch-over. Plans 1 and 2 are on this branch's base. Branch `feat/compose-wit`, stacked on `feat/compose-full` (PR #15).

## Global Constraints

- **Scope.** Every bound method acts only on the calling window's draft; the page never sends a draft ID. Wit methods join `ComposePage` (it stays the only bound type).
- **Read-only model output.** Model output is untrusted: Streamdown renders no images and no raw HTML. Links render only for `http`, `https` and `mailto`, and clicking one opens the page's confirmation (`link.title` / `link.copy` / `link.open`) before the bound `OpenLink` runs. Suspect sources show 「来源 %s 中包含疑似指令。它只被当作邮件内容处理，没有执行。」 (`wit.suspect`) (spec §A5 rule 2).
- **Same behaviour as the native page.** The Wit panel shows only when a model is configured (`Settings.DefaultModel != ""`). The body is locked while a run is in progress, an edit is pending, a reload is in flight, or the draft is sending. Locked input toasts `wit.compose.locked` (or `compose.sending.locked` when sending) once. Every run and ⌘K first flushes through the saver queue (plan 2), and nothing runs if the flush fails.
- **Same rules as the native page:**
  - First generation is applied by core (`applyInitial`) and shown as applied, with 撤销 (`UndoLastAssist`).
  - Reload: the newest read wins. It is skipped while a save or operation is busy, in conflict, when the stored revision is not newer, or when the page has an unsaved local edit.
  - Inline vs. rewrite uses `rewriteRatio = 0.5` on `worddiff.Ratio` with shapes kept.
- **Copy.** All copy comes from the native i18n tables: add every `wit.*`, `link.*`, `error.ai.*`, `error.run_active`, `error.invalid_state`, `error.stale_approval`, `error.ai_not_configured` key the page uses to `webui/strings.go`.
- **Generated code.** Generated components and `web/src/mygo.ts` are never hand-edited. npm versions are pinned exactly.
- **Streams.** mygo 0.4.0 bound methods cannot return a `Channel`; Wit events go on the `WitStream` event emitted to the calling window (`Event.Emit(window, …)`). The page registers its listener and buffers BEFORE calling any run-starting method, and never filters by run ID (one subscription per window).
- **Subscribe first.** Every subscription is taken BEFORE the action that may produce events (run start, decision, follow), as `app/wit_run.go` does; a resumed run's events attach to the same assistant message.
- **Drafts in use are never empty.** A draft with an active run or a pending approval is never deleted as empty (core, atomically).
- **The switch-over is the last task, and a ruling:** it lands only when Tasks 1–5 pass review and manual testing (spec §F as revised).
- **Commits.** End with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Go doc comments on exported names.

## Review Focus

1. **A run finishes while the user is typing in the body (unlocked after a decision)** → the reload never overwrites the local edit; the page's next save wins (Task 4 `reload skips a local edit`).
2. **⌘K on a selection right after typing** → the selection is computed on the saved revision; if the body changed in between, the run is refused as stale, never applied to the wrong range (Task 2 `TestComposeRewriteSelectionStale`, Task 4 `cmd-k saves first`).
3. **Window closed during a run** → the run keeps going in core and its approval waits; reopening the draft shows the pending review (Task 2 `TestComposeHistoryPendingEdit`; Task 3 `reconnects to a live run`).
4. **A model answer with a `javascript:` link, an image, or raw HTML** → no script, no request, and the link is not clickable (Task 3 `answer hardening`).
5. **Partial accept after the user edited the body during the review** → refused as stale with the conflict notice, no silent overwrite (Task 2 `TestComposeFinishPartialStale`).

---

## File Structure

| Path | Responsibility |
|---|---|
| `webui/wit.go` | bound Wit methods on `ComposePage`, the per-window `WitStream` event, wire types |
| `webui/wit_history.go` | conversation → UI messages (question/answer grouping, citations, tool calls), as `app/wit_run.go` `witMsgs` |
| `webui/review.go` | moved from `app/proposal.go`: diff, inline/rewrite, changes, link notes, result HTML from decisions |
| `webui/selection.go` | ⌘K: sentinel-marked HTML → `core.Selection` byte offsets |
| `webui/strings.go` | + Wit, link and error keys |
| `web/src/wit/` | `transport.ts` (`WitTransport`), `Panel.tsx`, `Response.tsx`, `Answer.tsx`, `Citation.tsx`, `menus.tsx` |
| `web/src/compose/review/` | `Review.tsx` (inline marks + per-change accept/reject, rewrite + compare), `CmdK.tsx` |
| `web/src/compose/ComposePage.tsx` | panel slot, lock rules, reload, applied/undo |
| app / cmd (Task 6) | entry points → `OpenCompose`; native compose deleted |

---

### Task 0: Pin and verify the web dependencies

**Files:** `web/package.json`, `web/bun.lock`; findings in the ledger.

- [ ] Install `ai`, `@ai-sdk/react`, `streamdown` at exact current versions. From the installed packages record: the `ChatTransport` signature; the UIMessageChunk names used below (`text-start/-delta/-end`, `tool-input-available`, `tool-output-available`, `tool-output-error` (field `errorText`), `tool-approval-request`, data parts `data-<name>`, `finish`, `error` with `errorText`); how `useChat` is kept from auto-sending after approvals; Streamdown's options for raw HTML off, custom `a` / `img` renderers, allowed link protocols, image handling. Adjust names in Tasks 3–4 to the installed APIs.
- [ ] Commit `build(web): pin the AI SDK and Streamdown`.

---

### Task 1: Go — Wit runs, stream and history for the calling window

**Files:** Create `webui/wit.go`, `webui/wit_history.go`, tests `webui/wit_test.go`; modify `webui/strings.go`, `webui/compose.go` (`ComposeState.Wit`).

**Interfaces (produced):**
```go
var WitStream = mygo.NewEvent[WitEvent]("wit") // emitted to the calling window only
type WitEvent struct {
	Conversation int64; Run int64
	Kind string                 // "context" | "text" | "tool_call" | "tool_result" | "interrupted" | "done" | "error"
	Text string                 // text delta
	Call *WitCall; Result *WitResult
	Interrupts []WitCall        // the calls an interrupt waits on (a skill's has ID "skill" and no earlier tool_call)
	Citations []Citation; Approvals []Approval
	Error string                // i18n key (error.ai.<code> / error.*), "" for cancel
}
type WitCall struct{ ID, Name, Args string }   // Args JSON text
type WitResult struct{ CallID, Name, Output, Error string }
type Citation struct{ N int; MessageID int64; Subject, From string; Date int64; Suspect bool }
type Approval struct{ ID, Run int64; CallID, Tool, Summary, State, Error string; Destinations []string } // CallID ties it to its tool call; State: pending/executing/done/failed/rejected/stale
type WitInfo struct {
	Enabled bool                // a model is configured
	Skills []Skill              // 调整: tone-formal, tone-friendly, shorter, longer, proofread (label from wit.skill.<id>)
	Languages []Language        // 翻译: reading language first, then zh-CN, zh-TW, en, ja (as app/compose_wit.go languages())
	Chips []string              // 原邮件, attachment names, 与 %s 的往来 %d 封 — as compose_wit.go:56-79
}
type Skill struct{ ID, Label string }
type Language struct{ Tag, Name string }
type WitHistory struct{ Conversation int64; Messages []WitMessage; Running int64; Approvals []Approval } // every approval of the conversation, any state
type WitMessage struct{ ID, Role, Text string; Calls []WitCall; Results []WitResult; Citations []Citation }
```
Bound methods (append to `ComposePage`):
```go
func (p *ComposePage) WitInfo(ctx) (WitInfo, error)
func (p *ComposePage) Ask(ctx, input string) (int64, error)            // run ID; events flow on WitStream
func (p *ComposePage) Skill(ctx, id, language string) (int64, error)   // AssistSkill; translate-draft takes language
func (p *ComposePage) Stop(ctx, run int64) error                       // CancelRun, only a run of this draft's conversations
func (p *ComposePage) Follow(ctx) (WitHistory, error)                   // subscribe FIRST (buffering), then read and return the history as of after subscribing (openConversation's rule picks the conversation); a run that ended in between shows as ended
```

Behaviour:
- **Starting a run** is the native `start` (`app/compose_wit_run.go:141-192`): pick or create the untitled assist conversation, subscribe **before** running, and drain the direct channel. Events are translated to `WitEvent` and emitted to the window. The subscription ends on a terminal event (`ends()` as `app/wit_run.go:212`), on `Detach`, or on `Release`.
- **First generation:** an `interrupted` whose approvals are all already done (auto-applied `initial`) is not terminal (`ends()`); keep following the resume.
- **One subscription per window.** A new run or `Follow` replaces the previous one.
- **Run context.** `context.WithoutCancel` of the service context; only `Stop` cancels (as native).
- **Errors map to keys** like `app/errors.go` `humanize`: canceled → `""`, ai codes → `error.ai.<code>`, known → `errorKeys`, else `error.unknown`.

- [ ] **Step 1: Failing tests** (core test model from `core/coretest`, as `app/compose_wit_test.go` uses — read its helpers):
  - `TestComposeWitInfo`: model set → `Enabled`, five skills with zh labels, languages start with the reading language; a reply draft → chip 原邮件 and 与 <name> 的往来 N 封 after a run with context citations. No model → `Enabled == false`.
  - `TestComposeAskStreams`: `Ask("hi")` → the window's emitter receives `text` deltas then `done`; another window gets nothing.
  - `TestComposeAskRunActive`: a second `Ask` during a run → error key `error.run_active`.
  - `TestComposeStop`: `Stop` mid-run → a terminal event, no error text.
  - `TestComposeStopOtherDraftRefused`: a run ID of another draft → `ErrNoDraft`-class error, nothing cancelled.
  - `TestComposeHistoryPendingEdit` (Review Focus 3): a run that ended `interrupted` with a pending `update_draft`, window closed and reopened → `Follow` returns that conversation, its messages (with tool results), and the pending approval with its CallID.
  - `TestComposeFollowRaceEnded`: a run ends between subscribing and reading → `Follow`'s state shows it ended.
  - `TestComposeFirstGenerationFollows`: an initial edit is auto-applied → events continue through the resume to `done`.
  - `TestComposeSkillInterruptCall`: a skill run's interrupt has `ID == "skill"` and the approval's `CallID == "skill"`.
  - core `TestDeleteDraftIfEmptyKeepsActiveRun` + webui `TestComposeCloseEmptyReplyMidRun`: an empty reply with a running Wit run or a pending approval is kept on release.
  - `TestComposeWitHistoryGrouping`: user, model, tool, model → one question and one answer, text joined with "\n\n", calls listed, citations numbered from 1 by first appearance.
  - `TestComposeStringsExist` still passes with the added keys.
- [ ] **Step 2:** Run `CGO_ENABLED=0 go test ./webui ./core -run 'TestComposeWit|TestComposeAsk|TestComposeStop|TestComposeHistory|TestComposeFollow|TestComposeFirst|TestComposeSkill|TestComposeCloseEmpty|TestDeleteDraftIfEmpty'` → FAIL.
- [ ] **Step 3: Implement.** `core.DeleteDraftIfEmpty` also keeps the draft when any of its assist conversations has an active run or a pending approval, checked under the same locks as the delete. Run `mise run generate`.
- [ ] **Step 4:** Run `go test -race ./webui` → PASS; `go vet ./...`.
- [ ] **Step 5: Commit** `feat(webui): Wit runs, stream and history for compose windows`

---

### Task 2: Go — review, decisions, undo, ⌘K and links

**Files:** Create `webui/review.go` (moved from `app/proposal.go`, which stays until Task 6), `webui/selection.go`, tests; modify `webui/wit.go`.

**Interfaces (produced):**
```go
type Review struct {
	Approval int64; Tool string
	Inline bool; Ratio float64; Exact bool   // Exact = the diff completed within budget (else rewrite_plain)
	Blocks []ReviewBlock                     // for rendering marks
	Old, New string                          // editable HTML before / after, for the rewrite compare
}
type ReviewBlock struct{ Kind string; Level int; Ordered bool; Added, Removed bool; Whole int; Ops []ReviewOp } // Added/Removed from BlockDiff.Old/New == -1; Whole = change index of a whole added/removed block (empty ones too), else -1
// Review also has Changes int; Finish validates len(decisions) == Changes and each ∈ {1,-1}.
type ReviewOp struct{ Kind string; Change int; Tokens []ReviewToken; Link string } // Kind same|del|ins; Change = index of the change it belongs to, -1 for same; Link = linkNote
type ReviewToken struct{ Text string; Bold, Italic bool; Href string }
func (p *ComposePage) Review(ctx, approval int64) (Review, error)            // only a pending draft edit of this draft
type BodyChange struct{ Change; Body string } // re-read after the action + the stored editable HTML; the page adopts it atomically
func (p *ComposePage) Decide(ctx, approval int64, accept bool) (BodyChange, error)    // subscribe this window FIRST, then ResolveApproval; the resumed run streams on WitStream
func (p *ComposePage) Finish(ctx, approval int64, decisions []int) (BodyChange, error) // same; all 1 → accept, all -1 → reject, mixed → ApplyEditedProposal(result HTML)
func (p *ComposePage) Undo(ctx) (BodyChange, error)                                  // UndoLastAssist
type RunStart struct{ Run, Revision int64; Conflict bool }
func (p *ComposePage) Rewrite(ctx, revision int64, text string, start, end int, instruction string) (RunStart, error) // ⌘K by plain-text offsets
func (p *ComposePage) OpenLink(ctx, url string) error                         // http/https → plat OpenURL; mailto → new draft + OpenCompose
```
`Change` is plan 2's type: Revision, Conflict, Signature, Attachments, UpdatedAt, re-read after the decision.

**⌘K offsets** (`selection.go`) — Ruling (Codex v1): plain-text offsets, not sentinels (a mark changes `toHTML`'s `<br>` decisions, so a serialized comparison can't hold).
- Contract shared by both sides, tested on both: blocks joined by `"\n"`; a soft break is `"\n"`; a trailing soft break (`<br><br>`) counts once; entities decoded; offsets in UTF-16 code units.
- The page, after saving at `revision`, sends `text` and `[start, end)`.
- Go walks the stored editable at `revision` with the html tokenizer, producing the same text plus a map to byte offsets at rune starts outside tags and entities (an offset inside an entity snaps outward). `text` differs → `Conflict`. `start >= end` → error. Then `core.Selection` and `RewriteSelection`; events on `WitStream`.

- [ ] **Step 1: Failing tests:**
  - Port these tests of `app/compose_wit_test.go` to `webui` as `TestComposeReview*` against `Review` / `Finish`:
    - `TestInlineProposalAcceptAll`
    - `TestInlineProposalPartial` (mixed decisions → `ApplyEditedProposal` with the expected HTML)
    - `TestLinkOnlyChangeShown` (`Link` set)
    - `TestRewriteProposalCompare` (`Inline == false`, `Ratio`, `Old` / `New`)
  - Also port the pure tests of `app/proposal.go`, if any exist.
  - `TestComposeFinishPartialStale` (Review Focus 5): a save after the proposal → `Finish` mixed → `Conflict`, approval stale.
  - `TestComposeUndo`: after an applied first generation → `Undo` restores the old body and the revision moves forward.
  - `TestComposeRewriteSelection`: table-driven — plain, CJK, emoji, `&amp;`, bold/link boundaries, a trailing soft break, a selection across two paragraphs, a list item — each selecting a known substring → `Start`/`End` slice exactly that HTML range and pass core's `checkSelection`.
  - `TestComposeRewriteSelectionStale` (Review Focus 2): `text` differing from the stored editable's → `Conflict`, no run.
  - `TestComposeDecideResumes`: accept and reject → the resumed run's events reach the window through to `done`; `BodyChange.Body` is the stored editable after the decision.
  - `TestComposeFinishValidates`: wrong length or a 0 → error, nothing decided.
  - `TestComposeReviewEmptyBlock`: an added empty paragraph is a whole change with its own index.
  - `TestComposeOpenLink`: `https://x.org` → fake `OpenURL` called; `javascript:x` → error; `mailto:a@b.c` → a new draft with To `a@b.c`, and `OpenCompose` called.
  - `TestComposeReviewOtherDraftRefused`.
- [ ] **Step 2–4:** Run → FAIL; implement (`webui` depends on `internal/worddiff` and `internal/richtext`; `proposal.go`'s UTF-16 helpers need `mygo/ui` — leave them; `OpenLink` needs a `PageDeps.OpenURL` and `PageDeps.OpenCompose`, wired in `cmd/godwit`); run `go test -race ./webui ./cmd/godwit` → PASS; `mise run generate`.
- [ ] **Step 5: Commit** `feat(webui): Wit edit review, decisions, undo, ⌘K and safe links for compose windows`

---

### Task 3: Web — the Wit panel (transport, answers, menus)

**Files:**
- (dependencies: Task 0)
- `web/scripts/components.ts`: add `message`, `message-scroller`, `bubble`, `input-group`, `marker`, `spinner`, `collapsible`, `card`, `hover-card`, `dropdown-menu` and `badge`, whichever are not already there.
- Create the `web/src/wit/*` files with tests.

**Interfaces (produced):**
```ts
export class WitTransport implements ChatTransport<WitUIMessage> // sendMessages → api.ask(text) / api.skill(id, lang); streams WitStream events of the run as UIMessageChunks; reconnectToStream → api.follow(conversation) when WitHistory.running
export function toChunks(ev: WitEvent, seen: Set<string>): UIMessageChunk[] // text → text-start/delta/end; tool_call → tool-input-available; tool_result → tool-output-available / -error (errorText); interrupted: each unseen interrupt (a skill's "skill") first gets a synthetic tool-input-available, then tool-approval-request only for approvals still pending (auto-applied initial ones show as output); citations → data-citations; done → finish; error → error (errorText = t(key))
export function historyToMessages(h: WitHistory): WitUIMessage[] // with tool results and every approval's state by CallID
// Approvals are decided only through the review UI (Decide/Finish), never addToolApprovalResponse; useChat has no auto-send.
export function Panel(props: { info: WitInfo; history: WitHistory; locked: boolean; onReview(a: Approval): void; t: T; slots?: { chips?: ReactNode; actions?: ReactNode } }): JSX.Element
export function Response(props: { text: string; onLink(url: string): void }): JSX.Element // Streamdown hardened
```
- Check the AI SDK v7 chunk names and the `ChatTransport` signature in the installed package. The names above follow its `index.d.ts`, and the installed package wins if they differ.
- The panel layout follows `web/src/compose` (right side, `WitPanelWidth` equivalent). It contains: the chips row; 调整 ▾ and 翻译 ▾ (`dropdown-menu`, disabled while locked); the message scroller with questions as bubbles, answers through `Response`, tool calls collapsed (`wit.tools.one/.other`), and citations as `hover-card` with `wit.suspect` when suspect; the input (`input-group` textarea; Enter sends, Shift+Enter makes a newline, IME respected; placeholder `wit.compose.placeholder`); Send, or 停止 while running; an error alert with 重试.
- The link confirmation is an `alert-dialog` with `link.title`, the address with its host in bold, and buttons 取消 / `link.copy` / `link.open`.

- [ ] **Step 1: Failing tests:**
  - `toChunks` for each kind.
  - `historyToMessages` grouping.
  - `answer hardening` (Review Focus 4): `![x](https://t/x.png)`, `![d](data:image/png;base64,AA)`, `<script>`, `<img onerror>`, benign `<b>x</b>` (shown as text), `[a](javascript:alert(1))`, `[f](file:///etc)`, `[b](https://ok.org)`, `[m](mailto:x@y.z)` → no img/script/raw element in the DOM; `a`/`f` plain text; `b` and `m` open the confirmation and only 打开 calls `onLink`. Streamdown configured explicitly (Task 0), not by defaults.
  - The panel sends with Enter and not during composition; Stop calls `api.stop(run)`; an error shows 重试, and retry repeats the last request (skill or text).
  - `reconnects to a live run` (Review Focus 3): `follow` returns `running` → deltas (even ones emitted before `follow` resolves) append to the open answer.
  - `listener before start`: events emitted before `ask` resolves are not lost.
  - `skill interrupt synthesizes a tool part`: interrupt "skill" + pending approval → a tool part exists before the approval request.
- [ ] **Step 2–4:** Run `mise run web:test` → FAIL; implement; run → PASS.
- [ ] **Step 5: Commit**, then run `mise run web:check`. Message: `feat(web): the Wit panel — transport, answers, menus`

---

### Task 4: Web — review in the body, locks, reload, undo, ⌘K

**Files:** Create `web/src/compose/review/Review.tsx`, `CmdK.tsx` + tests; modify `ComposePage.tsx`, `editor/Editor.tsx` (⌘K routing).

Behaviour (the native page's rules, from the survey in `.superpowers/sdd/2026-10-11-web-ui-plan-3-compose-wit/survey.md`):

- **Panel placement.** The panel mounts to the right of the body when `info.enabled`.
- **Saver accessors** (add to `saver.ts`, read-only): `revision`, `busy`, `dirty`. **Body operations** (`decide`, `finish`, `undo`) go through `saver.run` and the page adopts `BodyChange` atomically (`useLatest(revision, snapshot with body)`, editor remount). Run-starting calls are wrapped to `{revision, conflict}`.
- **Lock.** `locked = running || pendingDraftEdit || reloading || sending`. While locked the editor is read-only and the toast rule applies.
- **Inline review.** When a draft-edit approval is pending, the body is replaced by `Review`:
  - Same text renders as is. Del and Ins render with del/ins colours, which come from theme vars: add `--ins-bg`, `--ins-text`, `--del-bg`, `--del-text` to `ThemeVars` from the native tokens.
  - Hovering a change, or putting the caret in it, shows 接受 / 拒绝. A link note shows 「链接改为 %s」.
  - The banner shows 「%d 处修改待确认」 with 全部拒绝 / 全部接受.
  - When the last change is decided, the page calls `finish(decisions)`.
- **Rewrite review.**
  - Shows the new version read-only, with 对比原文 / 返回新版本 for a two-column compare (原文 | 新版本).
  - The banner shows 「已整体重写，改动约 %d%%」, or `rewrite_plain` when not exact.
  - 全部拒绝 / 全部接受 call `decide`.
- **Panel card for the same approval.** `wit.compose.review` plus the tool name, the summary text, and the same two buttons.
- **Running on a blank body.** Shows 「Wit 正在写…」 and three skeleton bars.
- **Reload** on every terminal event and every decision:
  - `api.latest()` with a reload counter; the newest read wins.
  - It is skipped (Review Focus 1) when the saver is busy or in conflict, when `latest.revision <= saver.revision`, or when there is a local unsaved edit since the run started.
  - Otherwise it calls `useLatest`, remounts the editor and sets `applied`.
  - A failure shows an alert with 重试.
- **Applied.** Shows 「已应用到正文」 with 撤销 → `saver.run(r => api.undo())`. Any local edit, run or ⌘K clears it.
- **⌘K.** Only with a non-empty selection, `info.enabled` and not locked; otherwise ⌘K keeps plan 2's link popover.
  - A popover under the selection holds the input 「修改选中的内容」, the line 「⌘K · 只修改选中的这一段」, an Esc button and 生成 ↵.
  - Submit runs `saver.run((r, flush) => …)`: the page computes `toText(value)` by Task 2's contract and the selection's `[start, end)` in it, then `api.rewrite(r, text, start, end, instruction)`.

- [ ] **Step 1: Failing tests** (mocked client):
  - `inline review accept one reject one` → `finish([1,-1])`.
  - `accept all` → `decide(id, true)`.
  - `rewrite compare toggles`.
  - `locked while running toasts once`.
  - `reload skips a local edit` (Review Focus 1).
  - `reload newest wins`.
  - `applied then undo`.
  - `cmd-k saves first` (Review Focus 2): typed text, ⌘K on a selection → `save` before `rewrite`, `text.slice(start, end)` equals the selection; `toText` cases mirror Task 2's table.
  - `undo adopts the body`: undo → the editor shows the returned body; no reload overwrites it.
  - `cmd-k without model keeps link popover`.
- [ ] **Step 2–4:** Run → FAIL; implement; run → PASS; `mise run build`.
- [ ] **Step 5: Commit**, then run `mise run web:check`. Message: `feat(web): Wit edits reviewed in the compose body; ⌘K rewrites a selection`

---

### Task 5: Manual check of Wit in the window (before the switch)

**Steps:** With a model service configured, run `mise run dev` and open Mail › 写邮件（网页窗口） on a reply. Check that:
- the chips show;
- 调整 › 更正式 gives an inline review; accept one change and reject one;
- 翻译 › English gives a rewrite with compare;
- a free question streams and Stop works;
- a first generation on an empty reply applies itself, then 撤销 works;
- ⌘K on a sentence works;
- an answer's link asks first;
- closing the window during a run and reopening shows the pending review.

Record the results in the ledger. This task gates Task 6.

---

### Task 6: The switch-over

**Files:** Per survey section 6.
- **Modify:** `app/mail_sidebar.go`, `app/notify.go`, `app/drafts.go`, `app/links.go`, `app/reader.go`, `app/reader_ai.go`, `app/app.go`, `app/shell.go`, `app/mail_list.go`, `app/errors.go`, `cmd/godwit/gui.go` (drop the dev-only item; `compose` no longer shows the main window), `cmd/godwit/platform.go` (drop `ClipboardHTML`).
- **Delete:** `app/compose.go`, `app/compose_wit.go`, `app/compose_wit_run.go`, `app/proposal.go`, `app/draftsaver.go`, `internal/editor/`, their tests, and the now-unused theme tokens (`WitPanelWidth`, `InsBg`, `DelBg`, `InsText`, `DelText`) and `richtext.ToHTMLOffsets` — but only if nothing else uses them.
- **Keep:** `internal/worddiff` (now used by `webui/review.go`).

- [ ] **Step 1: Failing tests** — map every test of the deleted files in the report (deleted / ported / retargeted):
  - `TestE2EOnboardToReply` keeps its real send: reader 回复 → `OpenCompose <id>` → the test drives `webui.ComposePage` for that draft (test window ID) with `Save` then `Send` → the message is in the outbox.
  - `app/compose_window_test.go` `TestDeleteDraftNotifiesPlatform`: rewritten without the deleted helpers.
  - Retarget to expect `OpenCompose <id>` on the fake platform:
  - `TestE2EOnboardToReply`, `TestE2EQuickReplyAndDraftsList`
  - `TestDraftsListRoute`, `TestLocalDraftsEntry`
  - `TestReaderReplyCreatesDraft`, `TestQuickReplies`
  - `TestComposeInWindowOpensWindow` (now the sidebar 写邮件)
  - a new `TestMailtoOpensWindow`
  - `TestBeforeClose*`: rewrite them without composers.
- [ ] **Step 2:** Run `go test ./app ./cmd/godwit` → FAIL.
- [ ] **Step 3: Implement** — every site calls `a.plat.OpenCompose(id)`; `PageCompose` and `Route.DraftID` go; `BeforeClose` no longer flushes drafts (the compose windows flush first, plan 1's quit hook).
- [ ] **Step 4:** Run `go test ./...`, `go vet ./...`, `go test -race ./webui ./cmd/godwit ./app`, `mise run build`, `mise run web:check` → PASS; check that `grep -r "PageCompose\|internal/editor\|draftsaver" --include=*.go` is empty.
- [ ] **Step 5: Commit** `feat: every compose entry point opens the compose window; the native compose page is gone`
- [ ] **Step 6: Manual check** of every entry point: sidebar, ⌘N, tray, reply / reply all / forward, quick reply, a plugin action that returns a draft, a mailto link in a message, and a draft row in the drafts list. Quit with windows open.

---

## Self-review notes

- **Spec coverage.**
  - §B4 → Tasks 2 and 4 (Ruling below on suggestions).
  - §D1 components → Task 3 (only the compose slots: the Wit page is plan 5, so the model picker slot is not built).
  - §D2 → Tasks 1 and 3. The event mapping table is followed; there is no `draft`, `account` or `model` event (the survey found none in Wit runs).
  - §A5 → Task 3. §B1 and §F switch-over → Task 6.
- **Ruling (spec §B4 says Plate's suggestion capability).** The review is rendered from Go's diff (`webui/review.go`, moved from the native `app/proposal.go` on `internal/worddiff`), and Go builds the partial result. *Why:* the proven logic is reused, and there is no second diff in TypeScript that could disagree with core's revision rules. *Cost if wrong:* the review is a read-only view instead of marks inside the live editor, which is what the native page does too. Update spec §B4 to match.
- **Ruling (⌘K offsets).** Plain-text offsets checked against Go's walk of the stored editable (Codex v1: sentinels fail because inserting a mark changes the serializer's `<br>` decisions). Update spec §B4.
- Codex review (plan v1) folded in: subscribe-before-decide with resumes on the same answer; first-generation not terminal; active runs/pending approvals block empty-draft deletion; interrupts and CallIDs carried, synthetic tool part for skill interrupts, approval requests only when pending; body ops return the stored body (`BodyChange`) adopted atomically, saver accessors explicit; plain-text ⌘K offsets; Follow subscribes then reads; listener before run start; history with results and all approval states; review blocks carry Added/Removed/Whole and a change count, decisions validated, UTF-16 helpers stay native; dependencies pinned and Streamdown configured explicitly first (Task 0); explicit switch-over test mapping with the reply send kept end to end; Channel return impossible in mygo 0.4.0 → per-window event.
