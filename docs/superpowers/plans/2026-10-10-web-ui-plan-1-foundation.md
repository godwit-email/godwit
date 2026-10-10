# Web UI, Plan 1: Frontend Foundation and Compose Window Skeleton — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `web/` frontend that builds into the binary, a native UI on system fonts, and a compose window — one mygo web window per draft — that loads a draft and saves a plain-text body, reachable from a dev-only entry.

**Architecture:** `web/` is a Vite multi-entry React project built with Bun; its output lands in `web/dist/app`, embedded by the Go package `web/dist` and served with `mygo.SetFrontend`. A new Go package `webui` owns the compose windows (draft ↔ window registry), the bound `Compose` service (every method scoped to the calling window's draft via `mygo.CallerWindow`) and the theme variables generated from `internal/theme`. `cmd/godwit` binds the service before `App.Run` and wires it to the `app` package through the existing `Platform` interface.

**Tech Stack:** Go 1.27 (mise), mygo v0.3.7 (unchanged; this plan needs no web view embedding), Bun 1.4, Vite, React 19, TypeScript, Tailwind CSS v4, shadcn/ui (Base UI flavour, generated), Vitest + Testing Library.

**Spec:** `docs/superpowers/specs/2026-10-10-web-ui-design.md` (§A1–A7, §B1–B2, §F row 1).

## Global Constraints

- Generated shadcn components are never edited by hand; they are produced by `web/scripts/components.ts` and CI regenerates and diffs them (spec §A2, §A6).
- Fonts: no font is defined. Web pages use `system-ui` (mono `ui-monospace`); the native UI uses the system font (`Theme.Font = ""`) and `"monospace"` for code (spec §A4).
- `web/dist/app` is not committed except a placeholder; `go build` must succeed with the frontend unbuilt, and the compose window then shows 「前端未构建」 (spec §A6).
- The generated client `web/src/mygo.ts` is committed; CI checks it matches the Go code (spec §A6).
- Every `Compose` method acts only on the draft bound to `mygo.CallerWindow(ctx)`; the page never sends a draft ID (spec §B2).
- CSP of built pages: `default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'self'` — no remote request (spec §A5).
- Saving: 1 s after the last input; never while an IME composition is active; flush before the window closes (spec §B2, from `draftsaver`).
- **Ruling (no regression between PRs):** every existing entry point keeps opening the old compose page. The new window opens only from a dev-only Mail menu item 「写邮件（网页窗口）」 shown when `mygo.IsDev()`. Plan 2 switches the entry points over. *Why:* plan 1's body is plain text; users must not lose formatting or Wit in the meantime. *Cost if wrong:* one extra menu item to remove in plan 2.
- Commits end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; Go code follows the repo's comment style (doc comments on every exported name, one-line rationale where a choice is non-obvious).
- Exact npm versions are pinned (no `^`/`~`) at the versions current when Task 2 runs; `bun.lock` is committed.

## Review Focus

1. **Closing the window while typing within the 1 s debounce** → the last keystrokes are saved before the window goes (Task 5 `TestCloseFlushesPendingInput`, Task 6 `flushes on closing`).
2. **Opening the same draft twice** (menu twice, or a second path) → the existing window comes to the front; no second window, no second session (Task 5 `TestOpenFocusesExistingWindow`).
3. **The draft changed elsewhere** (old compose page, Wit) after the window loaded → save reports a conflict and does not overwrite; the page says so (Task 4 `TestSaveStaleRevisionConflicts`, Task 6 `shows conflict`).
4. **A page calls `Compose` from a window with no draft, or after its window closed** → refused with `ErrNoDraft`; no other draft is touched (Task 4 `TestCallerWithoutDraftRefused`).
5. **Frontend not built** (fresh clone, `go build` only) → the window shows 「前端未构建，运行 mise run web:build」 instead of a blank page (Task 5 `TestFrontendMissingPage`).

---

## File Structure

| Path | Responsibility |
|---|---|
| `internal/theme/theme.go` | drop Geist: `Font = ""`, delete `RegisterFonts`, add `Mono = "monospace"` |
| `internal/theme/fonts/` | deleted |
| `app/*.go` (10 call sites) | `Font("Geist Mono")` → `Font(theme.Mono)` |
| `web/package.json`, `bun.lock`, `tsconfig*.json`, `vite.config.ts`, `vitest.config.ts`, `components.json` | the frontend project |
| `web/compose.html` | the compose entry, with the CSP meta |
| `web/scripts/components.ts` | generates the shadcn components (the only way they change) |
| `web/src/components/ui/` | generated shadcn components (never hand-edited) |
| `web/src/shared/theme.ts` | applies the Go theme variables to the document |
| `web/src/shared/styles.css` | Tailwind entry, shadcn variable wiring, system fonts |
| `web/src/compose/` | the compose page: `main.tsx`, `ComposePage.tsx`, `saver.ts` |
| `web/src/mygo.ts` | generated client (committed) |
| `web/dist/dist.go`, `web/dist/app/.gitkeep` | Go package embedding the build output |
| `webui/theme.go` | `ThemeVars` |
| `webui/compose.go` | the bound `Compose` service and its types |
| `webui/windows.go` | compose window registry: open / focus / close / flush |
| `cmd/godwit/gui.go`, `cmd/godwit/platform.go` | bind, set frontend, dev menu item, `Platform` methods |
| `app/app.go`, `app/settings_general.go`, `app/mail_sidebar.go` | `Platform.OpenCompose`, `Platform.SettingsChanged`, `App.ComposeInWindow` |
| `mise.toml`, `.github/workflows/ci.yml`, `.gitignore` | tools, tasks, CI |

---

### Task 1: Native UI on system fonts

**Files:**
- Modify: `internal/theme/theme.go` (`Apply` sets `th.Font`; `RegisterFonts`, `registerFonts`, `//go:embed fonts/*.ttf`)
- Delete: `internal/theme/fonts/`
- Modify: `cmd/godwit/gui.go:49-51` (the `RegisterFonts` call)
- Modify: `app/reader_ai.go:592`, `app/wit.go:160,201`, `app/settings_extensions.go:247,448,542`, `app/settings_models.go:377,381`, `app/compose_wit.go:127`, `app/settings.go:271`
- Test: `internal/theme/theme_test.go`, `app/kit_guard_test.go`

**Interfaces:**
- Produces: `theme.Mono = "monospace"` (const), used for code-like text in native UI.

- [ ] **Step 1: Write the failing tests**

In `internal/theme/theme_test.go`:
```go
// TestSystemFonts: the native UI uses the system's fonts, as the web pages do.
func TestSystemFonts(t *testing.T) {
	var font string
	ui.NewTester(func(c *ui.Context) { Apply(c, Light()); font = c.Theme().Font }, 10, 10)
	if font != "" {
		t.Errorf("Theme.Font = %q, want the system's (\"\")", font)
	}
	if Mono != "monospace" {
		t.Errorf("Mono = %q", Mono)
	}
}
```
In `app/kit_guard_test.go` add `TestNoGeist`: walk `*.go` in `app/` (skip `_test.go`) and fail on any file containing `"Geist`.

- [ ] **Step 2: Run them to see them fail**

Run: `CGO_ENABLED=0 go test ./internal/theme ./app -run 'TestSystemFonts|TestNoGeist'`
Expected: FAIL (`Mono` undefined / `Theme.Font = "Geist"`).

- [ ] **Step 3: Implement**

`Apply`: `th.Font = ""`; delete the font embed, `RegisterFonts`, `registerFonts` and `internal/theme/fonts/`; add `const Mono = "monospace" // code-like text: the system's monospace font`; remove the call in `gui.go`; replace the ten `Font("Geist Mono")` with `Font(theme.Mono)`. Update the doc comment of `Apply` (it mentions Geist).

- [ ] **Step 4: Run the whole suite**

Run: `CGO_ENABLED=0 go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

`feat(theme): the native UI uses the system's fonts, not an embedded Geist`

---

### Task 2: The frontend project

**Files:**
- Create: `web/package.json`, `web/bun.lock`, `web/tsconfig.json`, `web/tsconfig.node.json`, `web/vite.config.ts`, `web/vitest.config.ts`, `web/components.json`, `web/compose.html`, `web/scripts/components.ts`, `web/src/shared/styles.css`, `web/src/compose/main.tsx`, `web/src/compose/ComposePage.tsx` (placeholder), `web/src/vite-env.d.ts`
- Create: `web/dist/dist.go`, `web/dist/app/.gitkeep`
- Modify: `mise.toml`, `.gitignore`, `.github/workflows/ci.yml`
- Test: `web/src/compose/ComposePage.test.tsx`, `web/dist/dist_test.go`

**Interfaces:**
- Produces: `dist.FS() (fs.FS, bool)` in package `github.com/godwit-email/godwit/web/dist` — the built files rooted at the output directory, and whether `compose.html` exists in it.
- Produces: mise tasks `web:install`, `web:build`, `web:test`, `web:check`, `web:components`; generated components importable as `@/components/ui/<name>`.

- [ ] **Step 1: Write the failing Go test** `web/dist/dist_test.go`:
```go
func TestFSWithoutBuild(t *testing.T) {
	fsys, built := FS()
	if fsys == nil {
		t.Fatal("no FS")
	}
	if _, err := fs.Stat(fsys, "compose.html"); built != (err == nil) {
		t.Errorf("built = %v but compose.html stat err = %v", built, err)
	}
}
```
Run `CGO_ENABLED=0 go test ./web/dist` → FAIL (package missing).

- [ ] **Step 2: Implement `web/dist/dist.go`**

`//go:embed all:app` into `var files embed.FS`; `FS()` returns `fs.Sub(files, "app")` and whether `compose.html` exists. Add `web/dist/app/.gitkeep`; `.gitignore` gets `web/dist/app/*`, `!web/dist/app/.gitkeep`, `web/node_modules/`. Run the test → PASS.

- [ ] **Step 3: Scaffold the web project**

- `package.json`: `"type": "module"`, scripts `dev` (vite), `build` (`tsc -b && vite build`), `test` (`vitest run`), `typecheck` (`tsc -b --noEmit`), `components` (`bun scripts/components.ts`). Dependencies: `react`, `react-dom`, `@tanstack/react-query`; dev: `vite`, `@vitejs/plugin-react`, `typescript`, `tailwindcss`, `@tailwindcss/vite`, `vitest`, `jsdom`, `@testing-library/react`, `@testing-library/user-event`, `@types/react`, `@types/react-dom`. Exact versions.
- `vite.config.ts`: `root` = `web/`, `build.outDir` = `dist/app`, `emptyOutDir: true`, `rollupOptions.input = { compose: "compose.html" }`, alias `@` → `src`, plugins react + tailwind. A small plugin adds the CSP meta (Global Constraints) to built HTML only (`apply: "build"`, `transformIndexHtml`), so the dev server's HMR keeps working.
- `compose.html`: `<div id="root">`, `<script type="module" src="/src/compose/main.tsx">`, `lang="zh-CN"`.
- `styles.css`: `@import "tailwindcss";` plus shadcn's variable wiring as `shadcn init` writes it, `body { font-family: system-ui, -apple-system, "PingFang SC", "Microsoft YaHei", sans-serif }`, code `ui-monospace`.
- `components.json`: shadcn config for the Base UI flavour, aliases `@/components/ui`, `@/lib/utils`.

- [ ] **Step 4: The component generator** `web/scripts/components.ts`

Runs the shadcn CLI (exact version pinned in the script) non-interactively for the list `["button", "input", "textarea", "label", "alert", "sonner"]` with overwrite, so a rerun reproduces `src/components/ui/` byte for byte. Run `mise run web:components`; commit what it writes, untouched.

- [ ] **Step 5: Failing then passing smoke test** `ComposePage.test.tsx`

```tsx
test("renders the compose page shell", () => {
  render(<ComposePage />);
  expect(screen.getByRole("main", { name: "撰写" })).toBeInTheDocument();
});
```
Placeholder `ComposePage` returns `<main aria-label="撰写" />`. Run `mise run web:test` → PASS.

- [ ] **Step 6: mise and CI**

`mise.toml`: `[tools] bun = "1.4.2"`; tasks:
- `web:install` = `bun install --frozen-lockfile` (dir `web`)
- `web:build` = depends `web:install`, `bun run build`
- `web:test` = `bun run test`
- `web:components` = `bun run components`
- `web:check` = `bun run typecheck && bun run test && bun run components && git diff --exit-code -- src/components/ui`

CI `test` job: after `mise-action`, run `mise run web:build` before the Go steps. New job `web` (ubuntu-latest): `mise run web:check`.

- [ ] **Step 7: Verify**

Run: `mise run web:build && CGO_ENABLED=0 go test ./web/dist -v` → `TestFSWithoutBuild` passes with `built = true`; `rm -rf web/dist/app/* && git checkout web/dist/app/.gitkeep && CGO_ENABLED=0 go build ./...` → builds.

- [ ] **Step 8: Commit**

`feat(web): the frontend project — Vite multi-entry, Bun, generated shadcn (Base UI), embedded build`

---

### Task 3: Theme variables from the native tokens

**Files:**
- Create: `webui/theme.go`, `web/src/shared/theme.ts`
- Test: `webui/theme_test.go`, `web/src/shared/theme.test.ts`

**Interfaces:**
- Produces (Go): `type Theme struct { Mode string \`json:"mode"\`; Light map[string]string \`json:"light"\`; Dark map[string]string \`json:"dark"\`; FontSize float32 \`json:"fontSize"\` }` and `func ThemeOf(st core.Settings) Theme` — `Mode` is `st.Theme` (`"system" | "light" | "dark"`), `Light` / `Dark` are `ThemeVars(theme.FromSettings(...))` for each appearance.
- Produces (Go): `func ThemeVars(t theme.Tokens) map[string]string` — keys are shadcn's CSS variables.
- Produces (TS): `applyTheme(t: Theme, root = document.documentElement): () => void` — sets the variables of the effective appearance (for `"system"`, follows `matchMedia("(prefers-color-scheme: dark)")` and keeps following it), toggles the `dark` class, sets `font-size`; returns a cleanup.

- [ ] **Step 1: Failing Go test**

```go
func TestThemeVars(t *testing.T) {
	l := theme.Light()
	v := ThemeVars(l)
	want := map[string]string{
		"--background": css(l.Background), "--foreground": css(l.Foreground),
		"--card": css(l.Background), "--popover": css(l.Background),
		"--primary": css(l.Primary), "--primary-foreground": css(l.PrimaryText),
		"--secondary": css(l.Hover), "--muted": css(l.Hover), "--muted-foreground": css(l.Muted),
		"--accent": css(l.Hover), "--accent-foreground": css(l.Foreground),
		"--destructive": css(l.Danger), "--border": css(l.Border), "--input": css(l.Input), "--ring": css(l.Ring),
		"--radius": "8px",
	}
	for k, w := range want {
		if v[k] != w {
			t.Errorf("%s = %q, want %q", k, v[k], w)
		}
	}
	if css(ui.RGBA(10, 20, 30, 128)) != "rgb(10 20 30 / 0.502)" {
		t.Errorf("css = %q", css(ui.RGBA(10, 20, 30, 128)))
	}
}
```
(`--radius` is `Radius.Lg`, so shadcn's derived `sm/md/lg/xl` land on our 4/6/8/12; add `--card-foreground`/`--popover-foreground`/`--secondary-foreground` = foreground, `--destructive-foreground` = primary text.) Run → FAIL.

- [ ] **Step 2: Implement `ThemeVars`, `ThemeOf` and `css(ui.Color) string`** (`rgb(r g b / a)`, alpha with three decimals, `1` when opaque). Run → PASS.

- [ ] **Step 3: Failing TS test** `theme.test.ts`: with a stub `matchMedia` reporting dark, `applyTheme({mode: "system", light: {"--background": "rgb(255 255 255 / 1)"}, dark: {"--background": "rgb(10 10 10 / 1)"}, fontSize: 13})` sets `--background` to the dark value and the `dark` class; changing the stub to light updates it; `mode: "light"` ignores the media query; cleanup removes the listener. Run → FAIL.

- [ ] **Step 4: Implement `applyTheme`**; run → PASS.

- [ ] **Step 5: Commit**

`feat(webui): theme variables for web pages from the native tokens`

---

### Task 4: The `Compose` service

**Files:**
- Create: `webui/compose.go`
- Test: `webui/compose_test.go`

**Interfaces:**
- Consumes: `core.Service` (`GetDraft`, `SaveDraft`, `ListAccounts`, `GetSettings`), `mail.SplitDraft` / `mail.JoinDraft`, `ThemeOf` (Task 3).
- Produces:
```go
// Compose is bound for compose windows' pages; each method acts on the
// draft of the calling window only.
type Compose struct { /* svc, sessions, winOf */ }

func NewCompose(winOf func(context.Context) (int, bool)) *Compose // winOf: the calling window's ID (mygo.CallerWindow in production)
func (c *Compose) SetService(svc *core.Service)                  // after the data opens, in WhenReady
func (c *Compose) Attach(winID int, draftID int64)               // Task 5 calls it as it opens a window
func (c *Compose) Detach(winID int)
func (c *Compose) Load(ctx context.Context) (ComposeState, error)
func (c *Compose) Save(ctx context.Context, e ComposeEdit) (SaveResult, error)
func (c *Compose) Closed(ctx context.Context) error // the page flushed: Task 5 may close the window

type Account struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
}
type ComposeState struct {
	DraftID   int64     `json:"draftId"`
	Accounts  []Account `json:"accounts"`
	From      int64     `json:"from"`
	To        []string  `json:"to"`
	Cc        []string  `json:"cc"`
	Bcc       []string  `json:"bcc"`
	ReplyTo   string    `json:"replyTo"`
	Subject   string    `json:"subject"`
	Body      string    `json:"body"`      // the editable part as plain text (plan 1)
	Quote     string    `json:"quote"`     // plain text
	Signature string    `json:"signature"` // plain text
	Revision  int64     `json:"revision"`
	Theme     Theme     `json:"theme"`
}
type ComposeEdit struct {
	To       []string `json:"to"`
	Cc       []string `json:"cc"`
	Bcc      []string `json:"bcc"`
	ReplyTo  string   `json:"replyTo"`
	Subject  string   `json:"subject"`
	Body     string   `json:"body"`
	Revision int64    `json:"revision"`
}
type SaveResult struct {
	Revision int64 `json:"revision"`
	Conflict bool  `json:"conflict"` // the draft changed elsewhere: nothing was saved
}
var ErrNoDraft = errors.New("this window has no draft")
var ErrNotReady = errors.New("the data is not open yet")
```
`Save` keeps the quote and signature the session loaded (the stored HTML's parts at `Load`), joins them after the new editable part, and calls `SaveDraft` with `Revision`; `core.ErrStaleDraft` becomes `SaveResult{Conflict: true}`, not an error. Plain text ↔ HTML: `textToHTML` makes one `<p>` per line, HTML-escaped (`html.EscapeString`), an empty line `<p></p>`; the editable HTML, quote and signature become text with `mail.HTMLToText` (`core/mail/build.go`).

- [ ] **Step 1: Failing tests** (`coretest.New(t)`, an account, a `NewDraft(... "new", 0)`; `winOf` returns a window ID set per test):
  - `TestLoadOwnDraft`: `Attach(1, d.ID)`; `Load` with window 1 returns `DraftID == d.ID`, the account list, `Revision == d.Revision`, `Theme.Mode == "system"`.
  - `TestSaveRoundTrip`: `Save{Body: "第一行\n\n<b>x</b>", Subject: "Hi", To: ["a@example.com"], Revision: r}` → `GetDraft` has subject and recipients, and `mail.SplitDraft(html)` editable part is `<p>第一行</p><p></p><p>&lt;b&gt;x&lt;/b&gt;</p>`; the result's `Revision` is the stored one.
  - `TestSaveKeepsQuoteAndSignature`: a reply draft (`"reply"` to a seeded message) keeps its quote and signature parts byte for byte after `Save`.
  - `TestSaveStaleRevisionConflicts` (Review Focus 3): save once externally with `SaveDraft`, then `Save` with the old revision → `Conflict == true`, no error, stored body unchanged.
  - `TestCallerWithoutDraftRefused` (Review Focus 4): `winOf` returns window 2 (not attached) → `Load`, `Save`, `Closed` return `ErrNoDraft`; after `Detach(1)`, window 1's calls return `ErrNoDraft` too.
  - `TestNotReady`: before `SetService`, `Load` returns `ErrNotReady`.

- [ ] **Step 2: Run** `CGO_ENABLED=0 go test ./webui -run Compose` → FAIL.

- [ ] **Step 3: Implement**; sessions map guarded by a mutex; `Closed` only records that the page flushed (Task 5 consumes it through a hook `onClosed func(winID int)` set by `Windows`).

- [ ] **Step 4: Run** → PASS; `go vet ./webui`.

- [ ] **Step 5: Commit**

`feat(webui): the Compose service, scoped to the calling window's draft`

---

### Task 5: Compose windows and wiring

**Files:**
- Create: `webui/windows.go`
- Modify: `cmd/godwit/gui.go` (bind, `SetFrontend`, dev menu item, `MYGO_GENERATE` path), `cmd/godwit/platform.go` (`OpenCompose`, `SettingsChanged`), `app/app.go` (`Platform` gains two methods), `app/settings_general.go` (`settingsEffects` calls `SettingsChanged`), `app/mail_sidebar.go` (`ComposeInWindow`), `app/*_test.go` fake platform
- Create: `web/src/mygo.ts` (generated)
- Test: `webui/windows_test.go`, `app/compose_window_test.go`

**Interfaces:**
- Consumes: `Compose.Attach/Detach/Closed` hook (Task 4), `dist.FS` (Task 2), `ThemeOf` (Task 3).
- Produces:
```go
// Windows opens one compose window per draft.
type Windows struct { /* compose, open func(draftID) window, by draft */ }
func NewWindows(c *Compose, newWindow func(draftID int64) Window) *Windows
type Window interface { ID() int; Focus(); Close(); EmitClosing() }
func (w *Windows) Open(draftID int64)     // focuses an open window of the draft, else makes one and Attaches it
func (w *Windows) Closing(winID int) bool // the window asks to close: false the first time (the page is told to flush), true once the page called Closed or 3 s passed
func (w *Windows) Closed(winID int)       // the window went: Detach
```
mygo adapter (in `cmd/godwit`): `mygo.NewWindow(WindowOptions{URL: "/compose.html", Width: 860, Height: 720, MinWidth: 640, MinHeight: 480, StateKey: "compose", Title: "新邮件"})`; `OnClose` prevents and calls `Closing` until it returns true; `OnClosed` → `Closed`. When `dist.FS` reports unbuilt, the window instead `Page().LoadHTML` the message 「前端未构建，运行 mise run web:build」.
- Events: `var Closing = mygo.NewEvent[struct{}]("closing")`, `var ThemeChanged = mygo.NewEvent[Theme]("theme")` (package `webui`).
- `app.Platform` gains `OpenCompose(draftID int64)` and `SettingsChanged(st core.Settings)`; `(*App).ComposeInWindow()` makes a draft as `mailPage.compose` does and calls `OpenCompose`.

- [ ] **Step 1: Failing tests**
  - `TestOpenFocusesExistingWindow` (Review Focus 2): `Open(7)` twice with a fake `newWindow` → one window made, `Focus` called once on it.
  - `TestCloseFlushesPendingInput` (Review Focus 1): `Closing(id)` first returns false and calls `EmitClosing`; after `Compose.Closed` from that window, `Closing` returns true; with no `Closed`, it returns true after 3 s (inject the clock or a `time.After` hook).
  - `TestClosedDetaches`: after `Closed(id)`, `Compose.Load` from that window → `ErrNoDraft`; `Open` of the same draft makes a new window.
  - `TestFrontendMissingPage` (Review Focus 5): the adapter's page-choice function `pageFor(built bool) (url, html string)` returns the compose URL when built, else the HTML containing 「前端未构建，运行 mise run web:build」.
  - `app/compose_window_test.go` `TestComposeInWindowOpensWindow`: the fake platform records `OpenCompose` with the new draft's ID; `TestSettingsChangedReachesPlatform`: changing the theme setting records `SettingsChanged`.

- [ ] **Step 2: Run** → FAIL.

- [ ] **Step 3: Implement** `Windows`, the adapter, the `Platform` methods (`SettingsChanged` → `ThemeChanged.Broadcast(ThemeOf(st))`), binding in `gui()` **before** `App.Run` (`mygo.Bind(compose)` with `winOf` from `mygo.CallerWindow`), `SetService` in `WhenReady`, `mygo.SetFrontend` from `dist.FS`, and the dev-only Mail menu item 「写邮件（网页窗口）」 under `if mygo.IsDev()` calling `a.ComposeInWindow()`.

- [ ] **Step 4: Generate the client**

mise task `generate`: `MYGO_GENERATE=web/src/mygo.ts go run ./cmd/godwit --data-dir "$(mktemp -d)"` (a non-default data dir skips the single-instance lock, so a running Godwit doesn't swallow it). Run it; commit `web/src/mygo.ts`. Add to `web:check` a step that reruns `generate` and `git diff --exit-code -- src/mygo.ts`, on the macOS CI runner only (it starts the app up to `App.Run`).

- [ ] **Step 5: Run** `CGO_ENABLED=0 go test ./... && go vet ./...` → PASS.

- [ ] **Step 6: Commit**

`feat(webui): compose windows, one per draft, opened from a dev-only menu item`

---

### Task 6: The compose page (plain text)

**Files:**
- Modify: `web/src/compose/ComposePage.tsx`, `web/src/compose/main.tsx`
- Create: `web/src/compose/saver.ts`
- Test: `web/src/compose/saver.test.ts`, `web/src/compose/ComposePage.test.tsx`

**Interfaces:**
- Consumes: the generated `Compose` client and events `closing`, `theme` (`web/src/mygo.ts`), `applyTheme` (Task 3), generated `Input`, `Textarea`, `Label`, `Alert`.
- Produces: `createSaver(save: (e: ComposeEdit) => Promise<SaveResult>, opts?: {delay?: number}): Saver` with `edit(e: ComposeEdit)`, `setComposing(on: boolean)`, `flush(): Promise<void>`, `status: "saved" | "saving" | "unsaved" | "conflict"`, `onChange(fn)`. 1000 ms default delay; never saves while composing; saves serially (one in flight, the latest edit wins); a `conflict` result stops further saves.

- [ ] **Step 1: Failing saver tests** (fake timers):
  - `saves 1 s after the last edit` — two edits 500 ms apart → one save, 1000 ms after the second, with the second edit's body.
  - `waits while composing` — `setComposing(true)`, edit, advance 5 s → no save; `setComposing(false)` → save 1 s later.
  - `flushes on demand` — edit then `flush()` → saved immediately, no second save later.
  - `one save in flight` — a slow first save; edits meanwhile → exactly one follow-up save with the latest body.
  - `stops on conflict` — first save resolves `{conflict: true}` → status `conflict`, later edits don't save.

- [ ] **Step 2: Failing page tests** (mocked client module):
  - `loads the draft` — subject and body from `Compose.load()` show in their fields; the quote shows as text below the body, collapsed behind 「显示引用的原邮件」.
  - `flushes on closing` (Review Focus 1) — type, emit `closing` → `Compose.save` called with the typed body, then `Compose.closed()`.
  - `shows conflict` (Review Focus 3) — save resolves conflict → an alert 「草稿已在别处修改，本窗口不再保存」.
  - `IME` — typing between `compositionstart` and `compositionend` doesn't save until after the end.

- [ ] **Step 3: Run** `mise run web:test` → FAIL; **implement** the saver and the page (recipient fields are plain comma-separated inputs in plan 1; status line 正在保存… / 草稿已保存 · HH:mm / 未保存; `applyTheme(state.theme)` on load and on the `theme` event; the window title follows the subject via `currentWindow.setTitle`, 「新邮件」 when empty); **run** → PASS.

- [ ] **Step 4: End-to-end by hand (macOS)**

`mise run web:build && MYGO_DEV=1 go run ./cmd/godwit`, Mail › 写邮件（网页窗口）: type Chinese, wait for 草稿已保存, close with ⌘W right after typing, reopen the draft from the Drafts list in the old page → the text is there; switch Settings › 外观 to dark → the window follows; ⌘Z / ⌘C / ⌘V work in the body (spec §G: Edit menu in a full web window). Note results in the PR body.

- [ ] **Step 5: Commit**

`feat(web): the compose page — load, autosave a plain-text body, flush on close`

---

## Self-review notes

- Spec coverage: §A2 stack/components (Task 2), §A4 theme and fonts (Tasks 1, 3), §A5 CSP and caller scoping (Tasks 2, 4), §A6 build/CI/generated client (Tasks 2, 5), §A7 tests (each task), §B1 window lifecycle (Task 5), §B2 load/save/saver (Tasks 4, 6). Not in plan 1, by §F: rich text, fields' full behaviour, attachments, send, Wit, embedded web views.
- Deviation, ruled above: §F says the main-window entry points open the new window in plan 1; they stay on the old page until plan 2.
- Deferred to plan 2: §B1 「草稿被删除时关闭其窗口」 and the close prompt on a failed save — in plan 1 a failed or conflicting save shows in the page, and the old compose page remains the safe path.
