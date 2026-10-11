# Web UI Plan 4: Embedded Pages and the Wit Page — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:**
- **Embedded-page base.** A page role registry with per-role refusal, a target-agnostic Wit event plumbing, `ui.WebView` hosting conventions, and shortcut forwarding.
- **First consumer: the Wit page's content area.** It becomes an embedded web page (`wit.html`, role `wit`) with the conversation, approvals, cited mail, floating input and model chip. The left conversation sidebar, the title bar and the no-model state stay native.

**Architecture:**
- **One web view.** The main window gets one `*mygo.WebView` for the Wit page, created once after the window. The native `askPage` places it with `ui.WebView(c, v).Grow(1)` in its content area. When not shown, it hides and keeps its page.
- **Roles.** Bound methods identify the page with `mygo.CallerPage(ctx)`. A small registry maps registered pages to roles (`wit` now, `reader` in plan 5), and every method checks the role first.
- **Events.** The Wit event plumbing built for compose windows becomes keyed by a *target* (a window or a page), so the Wit page streams through the same `follow` / `hold` / reconcile code.
- **Native callbacks.** Native-only actions — open a cited message, model settings, a new conversation for the sidebar, back and forward — go through callbacks posted to the UI thread.

**Tech Stack:** Go 1.27, mygo 0.4.0 (`Window.NewWebView`, `ui.WebView`, `CallerPage`, `Event.EmitPage`), React 19, AI SDK v7 (as plan 3), Streamdown (as plan 3), generated shadcn.

**Spec:** `docs/superpowers/specs/2026-10-10-web-ui-design.md`: §C (embedded pages), §D1–§D3 (Wit components, transport, Wit page), §A3 and §A5 (roles, trust). §F revised: old plans 4 and 5 merge into this one (Ruling below); the reader is plan 5.

**Branch:** `feat/web-embed`, stacked on `feat/compose-wit` (PR #16). **Survey of the native page and the mygo API:** `.superpowers/sdd/2026-10-11-web-ui-plan-4-wit-page/survey.md`.

## Global Constraints

- **Roles.** Every method of a page-role service refuses a caller that is not a registered page of that role. Compose methods keep refusing callers without a compose window: an embedded page's `CallerWindow` is the main window, which has no compose session (spec §A3, §A5 rule 4).
- **No navigation.** Embedded pages never navigate or open windows, exactly as compose windows do: `OnWillNavigate` allows only the entry document, and `SetWindowOpenHandler` denies (spec §A5 rule 3).
- **Answer hardening.** Answers render through plan 3's hardened `Response`. Links use the page's confirmation and then the bound `OpenLink`: http/https go to the system browser, `mailto` makes a draft from the default account and opens its compose window (spec §A5 rule 2).
- **The Wit page's scope.** It acts only on `ask` conversations. A conversation ID it sends is checked to be kind `ask`, and so is any approval or run it decides or stops.
- **Native callbacks.** They run on the UI thread, through the App's posting function.
- **Shortcuts.** The page forwards ⌘[ / ⌘] to Go, because native `c.Shortcut` handlers don't see keys while the page has focus. Menu accelerators (⌘N, ⌘W, Edit menu) need no forwarding.
- **Run lifetimes.** Runs started by a page use a context detached from the page call's context (as `ComposePage.start`, `WithoutCancel`): a page reload cancels its calls, never a run; only Stop cancels.
- **Latest selection wins.** Conversation switches are guarded by a generation on both sides (Go's subscription replacement and the page's application of follow/ask results ignore anything older than the newest selection).
- **Snapshot + events.** The page registers its event listeners BEFORE `Load`; Go keeps the current conversation as a versioned snapshot that `Load` returns and `WitOpen` carries; the page applies only newer versions (mygo drops events that arrive before a listener exists).
- **Reconcile at the end.** On a terminal event the page re-reads the persisted history, approvals and citations and replaces what it streamed (native `reconcile`; core may drop text for slow subscribers).
- **No scroll hand-off.** The Wit page fills its area and scrolls itself, so wheels are not handed off (spec §C2).
- **Reuse:** components from `web/src/wit`, Streamdown settings from plan 3, theme via `ThemeOf` and the `theme` broadcast, and copy from the native tables (`ask.*`, `wit.*`, `link.*`, `error.*`) added to a key list.
- **Generated code.** Generated components and `web/src/mygo.ts` are never hand-edited. npm versions are pinned exactly.
- **Commits.** End with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Go doc comments on exported names.

## Review Focus

1. **A conversation is selected in the native sidebar while a run streams in the page** → the page switches to that conversation's history and follows it. The old run keeps going in core, and its events don't leak into the new view (Task 2 `TestWitPageOpenSwitches`, Task 3 `switching conversations drops the old stream`).
2. **First question in a new conversation** → Go creates the `ask` conversation, the native sidebar lists it and selects it, the route carries its ID, and back/forward work (Task 2 `TestWitPageAskCreatesConversation`).
3. **A compose window's page calls a `WitPage` method, or the Wit page calls a `ComposePage` method** → refused both ways (Task 1 `TestPageRoles*`).
4. **Window resized, sidebar collapsed, or theme switched while the page shows** → the page fills the content area, follows the theme, and keeps its scroll position (Task 3 manual check; Task 2 `TestAskPageHostsWebView`).
5. **A cited mail is clicked in the page** → the main window navigates to it in the reader, and back returns to the Wit conversation (Task 2 `TestWitPageOpenCited`).

---

## File Structure

| Path | Responsibility |
|---|---|
| `webui/pages.go` | page registry: register a page with a role; `roleOf(ctx)`; target IDs for pages |
| `webui/follow.go` | the subscription/hold/reconcile plumbing moved out of `wit.go`, keyed by target |
| `webui/witpage.go` | bound `WitPage` (role `wit`): Load, Open/Follow, Ask, Stop, Decide, DecideAll, OpenCited, OpenLink, OpenModels, Navigate |
| `webui/witpage_strings.go` | key list for the page (`ask.*`, `wit.*`, `link.*`, `error.*`, `mail.no_subject`, `mail.list_sep`) |
| `cmd/godwit/gui.go`, `platform.go` | create the web view after the window, lock its navigation, register its role, bind `WitPage`, wire callbacks |
| `app/ask.go` | content: `ui.WebView` (model configured) or the native no-model state; route ↔ page sync |
| `app/wit.go`, `app/wit_run.go` | deleted (their only user was `askPage`); `ask_test.go` retargeted or ported |
| `web/wit.html`, `web/src/wit-page/*` | the page: Panel (generalised), citations column (container query), floating input, model chip, shortcut forwarding |
| `web/vite.config.ts` | `wit` input |

---

### Task 1: Page registry, role checks and target-keyed Wit plumbing

**Files:** Create `webui/pages.go`, `webui/follow.go` (move), tests `webui/pages_test.go`; modify `webui/wit.go`, `webui/compose.go`.

**Interfaces (produced):**
```go
type Target struct{ Window int; Page int } // exactly one set; Page IDs come from the registry
type Pages struct{ /* page key → {id, role}; id → emitter */ }
func NewPages(pageOf func(ctx context.Context) any) *Pages // pageOf: mygo.CallerPage(ctx) as an opaque key (nil when not a web view page)
func (p *Pages) Register(key any, role string) int        // returns the page ID; Unregister(key) on destroy
func (p *Pages) Caller(ctx context.Context, role string) (int, error) // the caller's page ID, or ErrWrongPage
var ErrWrongPage = errors.New("this page may not call that")
```
- The follow plumbing (`follow`, `hold`, `endedWhileHeld`, `release`, `pump`, `ends`, `witEvent`, history, error keys) moves to `follow.go`, keyed by `Target`. The emitter dependency becomes `Emit func(Target, WitEvent)`. The compose code is unchanged in behaviour: windows are `Target{Window: id}`.

- [ ] **Step 1: Failing tests:**
  - `TestPageRolesRegistered`: a caller registered as `wit` → `Caller(ctx, "wit")` returns its ID; asking for `reader` → `ErrWrongPage`; an unregistered caller or a window caller → `ErrWrongPage`.
  - `TestPageRolesComposeFromPage` (Review Focus 3): a `ComposePage` method called with a context whose window has no compose session (the main window) → `ErrNoDraft`, nothing touched.
  - All existing `webui` tests stay green (compose follow behaviour unchanged).
- [ ] **Step 2–4:** Run → FAIL; implement; run `go test -race ./webui` → PASS.
- [ ] **Step 5: Commit** `refactor(webui): page roles and target-keyed Wit streams`

---

### Task 2: Go — the `WitPage` service, the web view, and the native host

**Files:**
- Create: `webui/witpage.go`, `webui/witpage_strings.go`, tests `webui/witpage_test.go`.
- Modify: `cmd/godwit/gui.go`, `cmd/godwit/platform.go`, `app/ask.go`, `app/app.go` (Platform gains the web view getter and the callback wiring), `app/shell.go` (Navigate hook).
- Delete: `app/wit.go`, `app/wit_run.go`, and their tests once ported.

**Interfaces (produced, bound as `WitPage`):**
```go
type WitPageState struct{ Conversation, Version int64; Model string; Text map[string]string; Theme Theme } // Model: label of Settings.DefaultModel; Version: of the conversation snapshot
func (p *WitPage) Load(ctx) (WitPageState, error)
func (p *WitPage) Follow(ctx, conversation int64) (WitHistory, error)   // conversation 0 → empty history; subscribe first (Task 1 plumbing)
func (p *WitPage) Ask(ctx, conversation int64, input string) (RunStart, error) // 0 → NewConversation("ask",0) first, then svc.Ask; returns Run, and the conversation via a new field RunStart.Conversation
func (p *WitPage) Stop(ctx, run int64) error
func (p *WitPage) Decide(ctx, approval int64, accept bool) error
func (p *WitPage) DecideAll(ctx, run int64, accept bool) error
func (p *WitPage) OpenCited(ctx, message int64) error   // → native navigate to the message in the reader
func (p *WitPage) OpenLink(ctx, url string) error       // http/https → OpenURL; mailto → default-account draft + OpenCompose
func (p *WitPage) OpenModels(ctx) error                 // → native Settings › models
func (p *WitPage) Navigate(ctx, dir int) error          // -1 back, +1 forward (⌘[ / ⌘])
type WitOpenMsg struct{ Conversation, Version int64 }
var WitOpen = mygo.NewEvent[WitOpenMsg]("witOpen")      // Go → page: show this conversation (0 = new), versioned
var WitState = mygo.NewEvent[WitPageState]("witState") // Go → page: model label / copy changed (settings), without touching the stream
func (p *WitPage) SetConversation(id int64)            // native → service: the route's conversation; bumps Version and emits WitOpen
// Ask's creation of a conversation carries the Version it was asked under; ConversationCreated(id, version) updates the route only if the native route still shows that version's empty conversation (else it only reloads the sidebar list).
```
`WitPageDeps{ OpenMessage func(id int64); OpenModels func(); Navigate func(dir int); ConversationCreated func(id, version int64); OpenURL func(string) error; OpenCompose func(int64) }` — set in `cmd/godwit`, where each App call is posted to the UI thread.

**Native side:**
- `cmd/godwit` creates the web view right after `mygo.NewWindow` with `WebViewOptions{URL: "/wit.html"}`, locks it like compose windows (entry-only navigation, no window opens), registers it as `wit`, and hands it to the App (a setter: `a.SetWitView(ui.NativeWebView)`).
- `askPage.Content`:
  - No model → the existing native no-model state.
  - Otherwise `ui.WebView(c, view).Grow(1)` inside the content column, with no extra padding (the page pads itself).
- **Route → page.** On `refresh(nil)` (navigation), native calls `SetConversation(route.ConversationID)` (version bump + `WitOpen`).
- **Settings → page.** `SettingsChanged` also emits `WitState` (model label) to the Wit page.
- **Kept in `ask.go`:** `convKey` (sidebar delete still chains on it); `Title` and sidebar selection read the route's conversation instead of `p.wit.conv`.
- **Page → route.** `ConversationCreated(id)` sets the route's ID in place (as native `Changed` did) and reloads the sidebar list.
- **ApprovalsChanged** needs nothing extra for the open conversation: the page's subscription carries it. The sidebar list still reloads on events.
- `askPage.Title()` is unchanged (title plus `ask.permissions`).
- `app/wit.go` and `app/wit_run.go` are deleted.

- [ ] **Step 1: Failing tests:**
  - `TestWitPageAskCreatesConversation` (Review Focus 2): `Ask(0, "hi")` → an `ask` conversation is created, `ConversationCreated(id)` fires, and events stream to the page target; a second `Ask(id, …)` reuses it.
  - `TestWitPageScopesAsk`: Follow, Ask, Decide and Stop on an `assist` conversation, or on its approval or run → refused.
  - `TestWitPageOpenSwitches` (Review Focus 1): following A while it runs, then `Follow(B)` → A's later events don't reach the page.
  - `TestWitPageOpenCited` (Review Focus 5): `OpenCited(id)` → the `OpenMessage(id)` callback fires.
  - `TestWitPageRoleRefused`: every method from a non-`wit` caller → `ErrWrongPage`.
  - **Ported from `app/ask_test.go`** to `webui/witpage_test.go` (the panel logic now lives here): `TestAskStreamsAnswer`, `TestAskCitationsAndSuspect`, `TestAskApproveDraft`, `TestApproveFastResume`, `TestDroppedStreamEventsReconciled`, `TestReopenConversationFollowsRun`, `TestAskStop`, `TestApprovalsChainedInOrder`, `TestContextCitationsMapToRunAnswer`, `TestToolDisclosurePersists`, `TestLanguageClueSuspectNoteStays`. Each keeps its intent as a Go-side assertion on events and history. Those that test rendering move to Task 3's web tests.
  - **Ported, not dropped:** `TestReopenRunningConversationStops` (it asserts the core run is cancelled by Stop after reopening). **Kept native and retargeted:** `TestAskConversationsList` (sidebar), `TestAskNoModel`.
  - `TestWitPageReloadKeepsRun`: the page's call context is cancelled (reload) mid-run → the run continues; a new Follow sees it, and Stop cancels it.
  - `TestWitPageSwitchGeneration`: Follow(A), then Follow(B) where A's history read is slow → B wins, and A's events never reach the page.
  - `TestWitPageCreatedVersion`: Ask(0) under version 3, then the route moves on (version 4) before creation finishes → `ConversationCreated(id, 3)` is reported and native leaves the route alone (sidebar reloads only).
  - `TestDroppedStreamEventsReconciled` (ported): the history after the run equals the saved text in full.
  - `app` `TestAskPageHostsWebView` (Review Focus 4): with a model, the content builds the web view element (fake `NativeWebView`); navigating to a conversation emits `WitOpen(id)`.
  - `app` `TestShellNavigation` keeps passing; `Navigate(-1)` from the page triggers back.
- [ ] **Step 2–4:** Run → FAIL; implement; run `go test ./...`, `go vet ./...`, `go test -race ./webui ./app ./cmd/godwit` → PASS; `mise run generate`.
- [ ] **Step 5: Commit** `feat: the Wit page's content is an embedded web page (Go side)`

---

### Task 3: Web — `wit.html`

**Files:** Create `web/wit.html`, `web/src/wit-page/main.tsx`, `WitPage.tsx`, `web/src/wit/Citations.tsx` (shared: one component, container query — right column when wide, cards under the answer when narrow), `web/src/shared/shortcuts.ts`, tests; modify `web/vite.config.ts` (input `wit`), `web/src/wit/Panel.tsx`, `Answer`/`Response` and `transport.ts`, `web/src/compose/ComposePage.tsx` (follows the generalisation), `webui/theme.go` (+ `--sidebar`, `--warning-bg/-border/-text`, `--shadow-float`, `--float-inset`).

**Generalisation (both hosts):**
- `onLink` is injected into `Panel`/`Answer` (compose → `ComposePage.openLink`, Wit page → `WitPage.openLink`); nothing in `web/src/wit` imports a host's service.
- The decide → resume protocol (record the run before deciding, keep early events, resume the run's message after; handle "answer already done before decide returns" and "other approvals still pending") moves from `ComposePage.tsx` into a shared hook in `web/src/wit/`.
- The end-of-run reconcile (re-read history and replace) lives in the transport/Panel for both hosts.
- Slots: `layout` (citations placement) and `input` (the floating box).
- `follow` / `ask` results carry the selection generation; stale ones are ignored.

**Behaviour** (native page per survey §1):
- **Load:** `WitPage.load()` → theme, copy (`makeT`), conversation. A `WitTransport` with the `WitPage` API (`ask(conv, input)` → `RunStart`, `follow(conv)`, `stop`), created before `follow`.
- **Startup order:** register `witOpen` / `witState` / `wit` listeners, THEN `load()`; apply only versions newer than the one held.
- **`witOpen` event:** switch conversation (newer version only). End the old SDK stream, reinitialise messages, then `follow(id)` under a new generation. The core run is not cancelled. Conversation 0 → empty.
- **`witState`:** update the model label and copy without resetting the stream or scroll.
- **Layout:**
  - The conversation is centred with max width 760 (`theme.WitMaxWidth`) and scrolls itself; it sticks to the bottom while near it.
  - Below it, the floating input box: radius 2xl; shadow: add `--shadow-float` to `ThemeVars` from `theme.ShadowFloat`, and for dark the inset edge (`FloatInset`, blur 1). The box holds a textarea (2–8 lines; Enter sends, Shift+Enter makes a newline, IME respected; placeholder `wit.placeholder`, label `wit.input`), the model chip (`state.model` + chevron, tooltip `wit.model`, click → `openModels`), and 发送 / 停止.
- **Citations** (spec §D1, container query):
  - Wide container → a right column, 320 px, sidebar background, left hairline, heading 引用的邮件. Each item shows 「[n] 发件人」, time, subject (or `mail.no_subject`), and 含疑似指令，已作为数据处理 with a warning border when suspect. Click → `openCited(id)`.
  - Narrow → the same cards below the answer.
- **Approvals:** plan 3's generic cards (允许 / 全部允许 / 拒绝, destinations, send note for `create_draft`) wired to `decide` / `decideAll`.
- **Shortcuts:** `shortcuts.ts` forwards ⌘[ / ⌘] (not while composing) to `navigate(-1 / 1)`.

- [ ] **Step 1: Failing tests:**
  - `switching conversations drops the old stream` (Review Focus 1); `out-of-order follow results` (A's late result is ignored after B); `ask returning after a switch` is ignored.
  - `witOpen before load resolves` and `load during navigation` (versions decide).
  - `reconcile after done replaces streamed text with the saved history`.
  - `decide then resume` (shared hook): resume after decide; the answer was already finished before decide returned; other approvals still pending.
  - `links go through the host's openLink` for both hosts.
  - `first ask creates and keeps the conversation` (Review Focus 2): `ask(0, …)` returns conversation 7, so later sends use 7.
  - `citations: both layouts render` (jsdom can't evaluate container queries — the query itself is checked in the manual step and in a real-browser probe page).
  - `cited click opens`.
  - `model chip opens settings`.
  - `cmd-[ forwards`.
  - `answer hardening` still holds through this page (reuse plan 3's cases).
- [ ] **Step 2–4:** Run → FAIL; implement; run `mise run web:test` → PASS; `mise run build`.
- [ ] **Step 5: Commit**, then `mise run web:check`: `feat(web): the Wit page`.
- [ ] **Step 6: Manual (macOS), `mise run dev`** — also: resize the content area across the citations breakpoint; reload the page (⌘R in the inspector) mid-run and see the run continue:
  - First question creates a conversation; the sidebar lists and selects it.
  - Switch conversations mid-run.
  - Stop.
  - An approval (`create_draft` via 「帮我起草一封回复给…」) → 允许 → the draft is created.
  - Click a cited mail → the reader; ⌘[ goes back.
  - A link asks before it opens.
  - Resize; collapse the sidebar; switch dark mode.
  - Popovers over the page (the title-bar menus and a toast) draw above it.

---

## Self-review notes

- **Spec coverage:**
  - §C1/§C2 → Tasks 1–2: create once, roles, navigation lock, shortcut forwarding. The wheel hand-off is not needed for a self-scrolling page; it comes with the reader (plan 5).
  - §D1 citations container query → Task 3.
  - §D2 transport → reused.
  - §D3 → Tasks 2–3: native sidebar and title, web content, no-model state native.
  - §A3/§A5 → Tasks 1–2.
- **Ruling: merge old plans 4 and 5.** *Why:* the embedded-page base has no other user, so it can't be tested end to end alone. *Cost if wrong:* one larger PR. Update spec §F and §C1 (mygo 0.4.0 includes #205, no fork).
- **Codex review (plan v1) folded in:** shared components get `onLink` injected; the decide→resume protocol and the end-of-run reconcile become shared; versioned snapshot + listener-first startup; latest-selection generations on both sides; creation callbacks carry their version; `convKey`/title/selection kept off the deleted panel; runs detached from page calls (reload-safe); `TestReopenRunningConversationStops` ported, not dropped; a model-label push; shared citations component; extra theme vars; container queries checked in a real browser.
- **Ruling: keep the no-model state native.** *Why:* it is one sentence and a button that navigates natively. *Cost if wrong:* a small native view to replace later.
