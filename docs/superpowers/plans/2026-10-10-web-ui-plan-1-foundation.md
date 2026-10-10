# Web UI, Plan 1: Frontend Foundation and Compose Window Skeleton — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `web/` frontend that builds into the binary, a native UI on system fonts, and a compose window — one mygo web window per draft — that loads a draft and saves a plain-text body, reachable from a dev-only entry.

**Architecture:** `web/` is a Vite multi-entry React project built with Bun. The mygo CLI (pinned as a Go tool, `go tool mygo …`, configured by a root `mygo.json`) runs the Vite dev server in development, builds `web/dist` and embeds it in builds, and generates the TypeScript client. Native icons embed straight from `web/node_modules/remixicon`, so `bun install` precedes every Go build and test. A new Go package `webui` owns the compose windows (draft ↔ window registry), the bound `Compose` service (every method scoped to the calling window's draft via `mygo.CallerWindow`) and the theme variables generated from `internal/theme`. `cmd/godwit` binds the service before `App.Run` and wires it to the `app` package through the existing `Platform` interface.

**Tech Stack:** Go 1.27 (mise), mygo v0.3.7 and its CLI as a Go tool (no web view embedding needed here), Bun 1.4, Vite, React 19, TypeScript, Tailwind CSS v4, shadcn/ui (Base UI flavour, generated), Vitest + Testing Library.

**Spec:** `docs/superpowers/specs/2026-10-10-web-ui-design.md` (§A1–A7, §B1–B2, §F row 1).

## Global Constraints

- Generated shadcn components are never edited by hand; they are produced by `web/scripts/components.ts` and CI regenerates and diffs them (spec §A2, §A6).
- Fonts: no font is defined. Web pages use `system-ui` (mono `ui-monospace`); the native UI uses the system font (`Theme.Font = ""`) and `"monospace"` for code (spec §A4).
- Builds go through the mygo CLI (`go tool mygo dev` / `build` / `generate`, pinned by a `tool` directive in `go.mod`); `bun install` in `web/` precedes any `go build`, `go test` or `go vet` — locally through mise tasks, in CI as the first step (spec §A6).
- The generated client `web/src/mygo.ts` is committed; CI checks it matches the Go code (spec §A6). `web/dist` is not committed.
- Every bound method acts only on the draft bound to `mygo.CallerWindow(ctx)`; the page never sends a draft ID. Only `ComposePage` (`Load`, `Save`, `Closed`) is bound; window management lives on unbound types (spec §B2).
- A compose window never closes with unsaved or unconfirmed input: no timeout closes it. A failed or conflicting final save asks 「有修改没有保存」 (关闭 / 返回), as the native app does; ⌘Q goes through the same flush (spec §B1).
- Pages never navigate away and never open windows: `OnWillNavigate` denies anything but the page's own entry, `SetWindowOpenHandler` denies all, for built, dev-server and fallback pages alike (spec §A5 rule 3).
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
5. **Two compose windows of different drafts save at the same time** → each saves only its own draft (Task 4 `TestComposeTwoWindowsIsolated`).

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
| `mygo.json` | mygo CLI config: main package, dev server, build command, `frontendDist`, `bindings` |
| `web/icons.go` | Go package `web`: embeds the used Remix icons from `node_modules` (Task 7) |
| `webui/theme.go` | `ThemeVars` |
| `webui/compose.go` | `Compose` (sessions, unbound) and `ComposePage` (the bound facade) |
| `webui/windows.go` | compose window registry: open / focus / close / flush |
| `cmd/godwit/gui.go`, `cmd/godwit/platform.go` | bind, set frontend, dev menu item, `Platform` methods |
| `app/app.go`, `app/settings_general.go`, `app/mail_sidebar.go` | `Platform.OpenCompose`, `Platform.SettingsChanged`, `App.ComposeInWindow` |
| `mise.toml`, `.github/workflows/ci.yml`, `.gitignore`, `web/biome.json` | tools, tasks, lint, CI |
| `internal/icons/` | reads its icons from package `web`; `svg/` and its licence copy are deleted (Task 7) |

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

Migrate the existing tests that pin Geist: `TestThemeApplyOf` (`internal/theme/theme_test.go:84`) now expects `Font == ""`; the tests calling `RegisterFonts` (`:98`, `:101`) are deleted with it.

- [ ] **Step 2: Run them to see them fail**

Run: `CGO_ENABLED=0 go test ./internal/theme ./app -run 'TestSystemFonts|TestNoGeist|TestThemeApplyOf'`
Expected: FAIL (`Mono` undefined / `Theme.Font = "Geist"`).

- [ ] **Step 3: Implement**

`Apply`: `th.Font = ""`; delete the font embed, `RegisterFonts`, `registerFonts` and `internal/theme/fonts/`; add `const Mono = "monospace" // code-like text: the system's monospace font`; remove the call in `gui.go`; replace the ten `Font("Geist Mono")` with `Font(theme.Mono)`. Update the doc comment of `Apply` (it mentions Geist).

- [ ] **Step 4: Run the whole suite**

Run: `CGO_ENABLED=0 go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

`feat(theme): the native UI uses the system's fonts, not an embedded Geist`

---

### Task 2: The frontend project and the mygo CLI

**Files:**
- Create: `mygo.json`, `web/package.json`, `web/bun.lock`, `web/tsconfig.json`, `web/tsconfig.node.json`, `web/vite.config.ts`, `web/vitest.config.ts`, `web/components.json`, `web/biome.json`, `web/compose.html`, `web/scripts/components.ts`, `web/src/shared/styles.css`, `web/src/compose/main.tsx`, `web/src/compose/ComposePage.tsx` (placeholder), `web/src/vite-env.d.ts`
- Modify: `go.mod` (`tool github.com/egoist/mygo/cmd/mygo`), `mise.toml`, `.gitignore`, `.github/workflows/ci.yml`
- Test: `web/src/compose/ComposePage.test.tsx`

**Interfaces:**
- Produces: mise tasks `web:install`, `dev`, `build`, `web:test`, `web:lint`, `web:components`, `web:check`; generated components importable as `@/components/ui/<name>`; `web/dist` (Vite's output, `mygo.json`'s `frontendDist`).

- [ ] **Step 1: The mygo CLI and its config**

`go get -tool github.com/egoist/mygo/cmd/mygo@v0.3.7` (adds the `tool` directive at the version Godwit already uses). `mygo.json`:
```json
{
  "name": "Godwit",
  "main": "./cmd/godwit",
  "devUrl": "http://localhost:5173",
  "devCommand": "cd web && bun run dev",
  "buildCommand": "cd web && bun install --frozen-lockfile && bun run build",
  "frontendDist": "web/dist",
  "bindings": "web/src/mygo.ts"
}
```
(The CLI runs them through `sh -c` / `cmd /s /c`, so `cd web && …` works everywhere.) `go.mod` also gets `ignore ./web/node_modules` (Go 1.25+), so `./...` never walks the npm tree; `go:embed` still reads files under it. `go tool mygo --help` runs.

- [ ] **Step 2: Scaffold the web project**

- `package.json`: `"type": "module"`, scripts `dev` (vite), `build` (`tsc -b && vite build`), `test` (`vitest run`), `typecheck` (`tsc -b --noEmit`), `lint` (`biome check src scripts`), `components` (`bun scripts/components.ts`). Dependencies: `react`, `react-dom`, `@tanstack/react-query`, `mygo-runtime` (the version matching mygo v0.3.7), `remixicon` `4.8.0`; dev: `vite`, `@vitejs/plugin-react`, `typescript`, `tailwindcss`, `@tailwindcss/vite`, `vitest`, `jsdom`, `@testing-library/react`, `@testing-library/user-event`, `@types/react`, `@types/react-dom`, `@biomejs/biome`. Exact versions.
- `vite.config.ts` (commands run inside `web/`, so no `root`): `build.outDir` `dist`, `emptyOutDir: true`, `rollupOptions.input = { compose: fileURLToPath(new URL("compose.html", import.meta.url)) }`, alias `@` → `fileURLToPath(new URL("src", import.meta.url))`, plugins react + tailwind; a small plugin adds the CSP meta (Global Constraints) to built HTML only (`apply: "build"`), so HMR keeps working in dev.
- `compose.html`: `<div id="root">`, `<script type="module" src="/src/compose/main.tsx">`, `lang="zh-CN"`.
- `styles.css`: `@import "tailwindcss";` plus shadcn's variable wiring as `shadcn init` writes it; `body { font-family: system-ui, -apple-system, "PingFang SC", "Microsoft YaHei", sans-serif }`, code `ui-monospace`.
- `components.json`: shadcn config for the Base UI flavour, aliases `@/components/ui`, `@/lib/utils`.
- `biome.json`: recommended rules; ignores `src/components/ui`, `src/mygo.ts`, `dist`.
- `.gitignore`: `web/node_modules/`, `web/dist/`.

- [ ] **Step 3: The component generator** `web/scripts/components.ts`

Runs the shadcn CLI (exact version pinned in the script) non-interactively for `["button", "input", "textarea", "label", "alert"]` with overwrite. Run it; commit what it writes, untouched. The registry is remote, so a rerun is not guaranteed byte-identical: `web:check` reruns it and fails on any change (tracked or untracked) under `web/`, which surfaces registry drift as a deliberate regenerate-and-commit rather than silent edits.

- [ ] **Step 4: Failing then passing smoke test** `ComposePage.test.tsx`

```tsx
test("renders the compose page shell", () => {
  render(<ComposePage />);
  expect(screen.getByRole("main", { name: "撰写" })).toBeInTheDocument();
});
```
Placeholder `ComposePage` returns `<main aria-label="撰写" />`. Run `mise run web:test` → PASS.

- [ ] **Step 5: mise and CI**

`mise.toml`: `[tools] bun = "1.4.2"`; tasks (`dir = "web"` for the `web:*` ones; all but `web:install` depend on it):
- `web:install` = `bun install --frozen-lockfile`
- `dev` = `go tool mygo dev` (repo root)
- `build` = `go tool mygo build -skip-dmg -skip-notarize` (repo root)
- `web:test` = `bun run test`; `web:lint` = `bun run lint`; `web:components` = `bun run components`
- `web:check` = `bun run typecheck && bun run lint && bun run test && bun run components && test -z "$(git status --porcelain -- .)"`

CI `test` job (three OS): after `mise-action`, `mise run web:install` first; gofmt becomes `test -z "$(gofmt -l $(git ls-files '*.go'))"`; the Go steps follow unchanged; then `mise run build` on all three (spec §A6: every platform must package). New job `web` (ubuntu-latest): `mise run web:check`.

- [ ] **Step 6: Verify**

`mise run web:install && CGO_ENABLED=0 go build ./... && go vet ./...` → pass; `mise run build` → a build in `dist/` (mygo's `out`; add `dist/` to `.gitignore` if the CLI writes there).

- [ ] **Step 7: Commit**

`feat(web): the frontend project, built and served by the mygo CLI`

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

### Task 4: The compose service and its bound facade

**Files:**
- Create: `webui/compose.go`, `webui/text.go`
- Test: `webui/compose_test.go`, `webui/text_test.go`

**Interfaces:**
- Consumes: `core.Service` (`GetDraft`, `SaveDraft`, `ListAccounts`, `GetSettings`), `mail.SplitDraft` / `mail.JoinDraft`, `ThemeOf` (Task 3).
- Produces:
```go
// Compose keeps each compose window's session: its draft and the parts of
// the draft the page does not edit. It is not bound: pages reach it only
// through ComposePage.
type Compose struct{ /* svc, sessions, closed hook */ }

func NewCompose() *Compose
func (c *Compose) SetService(svc *core.Service)   // in WhenReady, once the data is open
func (c *Compose) Attach(winID int, draftID int64) // Windows calls it as it opens a window
func (c *Compose) Detach(winID int)
func (c *Compose) OnClosed(fn func(winID int, saved bool)) // the page's final flush: saved reports success

// ComposePage is what compose pages call (mygo.Bind); every method acts on
// the calling window's draft only.
type ComposePage struct{ /* c *Compose; winOf func(context.Context) (int, bool) */ }

func NewComposePage(c *Compose, winOf func(context.Context) (int, bool)) *ComposePage // winOf: mygo.CallerWindow(ctx).ID() in production
func (p *ComposePage) Load(ctx context.Context) (ComposeState, error)
func (p *ComposePage) Save(ctx context.Context, e ComposeEdit) (SaveResult, error)
func (p *ComposePage) Closed(ctx context.Context, saved bool) error

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
var (
	ErrNoDraft    = errors.New("this window has no draft")
	ErrNotReady   = errors.New("the data is not open yet")
	ErrNoRevision = errors.New("a save needs the revision it edits") // core skips the conflict check for revision 0 (core/compose.go:188)
)

// text.go
func bodyHTML(text string) string // one <p> per line, escaped; spaces kept as written (runs of spaces become &nbsp; pairs so HTML keeps them); an empty line is <p></p>
func bodyText(editable string) string // inverse for bodyHTML's output exactly; other markup (from the old compose page) falls back to mail.HTMLToText per top-level element
```
`Save` refuses `Revision == 0` with `ErrNoRevision`; keeps the quote and signature HTML the session loaded and joins them after `bodyHTML(e.Body)`; `core.ErrStaleDraft` becomes `SaveResult{Conflict: true}`, not an error; on success the session's revision moves to the result. Quote and signature shown to the page use `mail.HTMLToText`.

- [ ] **Step 1: Failing tests**

`text_test.go`, `TestBodyRoundTrip`: for each of `"一行"`, `"a\n\nb"`, `"\n首行空\n"`, `"  缩进两格\n\t制表"`, `"<b>x</b> & y"`, `"末尾空行\n\n"`: `bodyText(bodyHTML(s)) == s`.

`compose_test.go` (`coretest.New(t)`, an account, `NewDraft(ctx, acc.ID, "new", 0)`, `winOf` returning a per-test window ID); all names start with `TestCompose` so `-run TestCompose` selects them:
- `TestComposeLoadOwnDraft`: `Attach(1, d.ID)`; `Load` from window 1 → `DraftID == d.ID`, the accounts, `Revision == d.Revision`, `Theme.Mode == "system"`.
- `TestComposeSaveThenLoad`: `Save{Body: "第一行\n\n  第三行", Subject: "Hi", To: ["a@example.com"], Revision: r}`, then a fresh `Load` → the same body, subject and recipients; `GetDraft` agrees.
- `TestComposeConsecutiveSaves`: two saves, the second with the first's returned revision → both stored, no conflict.
- `TestComposeSaveKeepsQuoteAndSignature`: a `"reply"` draft to a seeded message keeps its quote and signature HTML byte for byte after `Save`.
- `TestComposeStaleRevisionConflicts` (Review Focus 3): an external `SaveDraft`, then `Save` with the old revision → `Conflict`, no error, stored body unchanged.
- `TestComposeZeroRevisionRefused`: `Save{Revision: 0}` → `ErrNoRevision`, nothing stored.
- `TestComposeCallerWithoutDraftRefused` (Review Focus 4): from window 2 (not attached) `Load`, `Save`, `Closed` → `ErrNoDraft`; after `Detach(1)`, window 1 gets `ErrNoDraft` too.
- `TestComposeNotReady`: before `SetService`, `Load` → `ErrNotReady`.
- `TestComposeTwoWindowsIsolated` (Review Focus 5): two drafts attached to windows 1 and 2; concurrent `Save`s (goroutines) with different bodies → each draft holds its own body.
- `TestComposeBoundSurface`: the exported methods of `*ComposePage` are exactly `Closed`, `Load`, `Save` (reflect), so binding it exposes nothing else.

- [ ] **Step 2: Run** `CGO_ENABLED=0 go test ./webui -run 'TestCompose|TestBody'` → FAIL.

- [ ] **Step 3: Implement**; sessions map guarded by a mutex.

- [ ] **Step 4: Run** → PASS; `go vet ./webui`.

- [ ] **Step 5: Commit**

`feat(webui): compose sessions and the bound ComposePage, scoped to the calling window's draft`

---

### Task 5: Compose windows, closing, quitting and wiring

**Files:**
- Create: `webui/windows.go`
- Modify: `cmd/godwit/gui.go` (bind, dev menu item, quit hook), `cmd/godwit/platform.go` (`OpenCompose`, `SettingsChanged`, the mygo window adapter), `app/app.go` (`Platform` gains two methods), `app/settings_general.go` (`settingsEffects` calls `SettingsChanged`), `app/mail_sidebar.go` (`ComposeInWindow`), the fake platforms in `app/*_test.go`
- Create: `web/src/mygo.ts` (generated)
- Test: `webui/windows_test.go`, `cmd/godwit/compose_window_test.go`, `app/compose_window_test.go`

**Interfaces:**
- Consumes: `Compose.Attach/Detach/OnClosed` (Task 4), `ThemeOf` (Task 3).
- Produces:
```go
// Windows opens one compose window per draft and lets none close or quit
// with input unsaved.
type Windows struct{ /* compose, newWindow, ask, by draft, pending closes */ }

type Window interface {
	ID() int
	Focus()
	Destroy()     // closes without asking again
	EmitClosing() // tells the page to flush and call Closed
}
func NewWindows(c *Compose, newWindow func(draftID int64) Window, ask func(w Window) bool) *Windows // ask: 「有修改没有保存」, true = close anyway
func (w *Windows) Open(draftID int64)
func (w *Windows) RequestClose(winID int) // from the window's OnClose (always prevented): EmitClosing, then Destroy once the page reports saved, or ask on a failed save
func (w *Windows) Closed(winID int)       // the window went: Detach
func (w *Windows) Count() int
func (w *Windows) CloseAll(done func(ok bool)) // for quit: flush every window as RequestClose does; ok = all closed (none was kept by the user)
```
No timeout destroys a window. A page that never answers keeps its window open; the user can still choose 关闭 when `ask` runs after 5 s without an answer.

mygo adapter (`cmd/godwit`): `mygo.NewWindow(WindowOptions{URL: "/compose.html", Width: 860, Height: 720, MinWidth: 640, MinHeight: 480, StateKey: "compose", Title: "新邮件"})`; `OnClose` → `PreventDefault` + `RequestClose`; `OnClosed` → `Closed`; `OnWillNavigate` denies every URL but the page's own entry; `SetWindowOpenHandler` denies all.
(mygo resolves `/compose.html` against the dev server under `go tool mygo dev`, else the embedded `web/dist`.)
Quit: one coordinated hook. mygo runs every `OnBeforeQuit` listener even after `PreventDefault`, so the existing native hook (`gui.go`, `saveThen` + `a.Close()`) and a new one must not both register. Replace it with a single listener: if `Windows.Count() > 0`, prevent and `CloseAll`; on `ok` call `Quit` again (the next pass finds no windows); on `!ok` stop — the native flush never ran, `a.Close()` was not called, background work keeps going. With no compose windows, run today's native `saveThen` / `a.Close()` unchanged. Write it as `quitHook(windows interface{ Count() int; CloseAll(func(bool)) }, native func(e *mygo.QuitEvent)) func(*mygo.QuitEvent)` so it is testable.
Events (package `webui`): `var Closing = mygo.NewEvent[struct{}]("closing")`, `var ThemeChanged = mygo.NewEvent[Theme]("theme")`.
`app.Platform` gains `OpenCompose(draftID int64)` and `SettingsChanged(st core.Settings)`; `(*App).ComposeInWindow()` makes a draft as `mailPage.compose` does and calls `OpenCompose`.

- [ ] **Step 1: Failing tests** (`webui/windows_test.go`, fake `Window`s and `ask`; names start with `TestWindows`):
  - `TestWindowsOpenFocusesExisting` (Review Focus 2): `Open(7)` twice → one window made, focused once.
  - `TestWindowsCloseWaitsForSave` (Review Focus 1): `RequestClose` → `EmitClosing`, not destroyed; the page's `Closed(saved=true)` → destroyed; reading the draft back (real `Compose` + `coretest`) shows the text saved just before.
  - `TestWindowsCloseFailedSaveAsks`: `Closed(saved=false)` → `ask` called; `ask` false → window kept; `ask` true → destroyed.
  - `TestWindowsSlowPageAsksNotCloses`: no answer for 5 s (injected clock) → `ask` called, window not destroyed unless `ask` is true.
  - `TestWindowsClosedDetaches`: after `Closed`, the page's `Load` → `ErrNoDraft`; `Open` of the same draft makes a new window.
  - `TestWindowsCloseAllForQuit`: two windows, one saves, one fails and the user keeps it → `done(false)`, one window left; both save → `done(true)`.
  - `cmd/godwit` `TestQuitHookWebThenNative` (Review Focus 1): windows open and `CloseAll` reports `false` → event prevented, `native` not called, `Quit` not called again; `CloseAll` reports `true` → `Quit` called; with no windows → `native` called once.
  - `cmd/godwit` `TestComposeWindowNavigation`: the adapter's navigation predicate allows the entry URL and denies `https://example.com/`, `file:///etc/passwd` and `mygo://localhost/other.html`.
  - `app` `TestComposeInWindowOpensWindow`, `TestSettingsChangedReachesPlatform`: the fake platform records `OpenCompose(newDraftID)` and `SettingsChanged`.

- [ ] **Step 2: Run** `CGO_ENABLED=0 go test ./webui ./cmd/godwit ./app -run 'TestWindows|TestQuitHook|TestComposeWindowNavigation|TestComposeInWindow|TestSettingsChanged'` → FAIL.

- [ ] **Step 3: Implement** `Windows`, the adapter, the `Platform` methods (`SettingsChanged` → `ThemeChanged.Broadcast(ThemeOf(st))`), `mygo.Bind(NewComposePage(compose, winOf))` in `gui()` **before** `App.Run`, `SetService` in `WhenReady`, the quit hook, and the dev-only Mail menu item 「写邮件（网页窗口）」 under `if mygo.IsDev()` calling `a.ComposeInWindow()`.

- [ ] **Step 4: Generate the client**

mise task `generate` = `go tool mygo generate` (writes `bindings`, `web/src/mygo.ts`; `go tool mygo dev` regenerates it on Go changes too). (mygo's `RequestSingleInstanceLock` already returns true under `MYGO_GENERATE`, and `App.Run` returns before `WhenReady`, so no data is opened.) Run it; commit `web/src/mygo.ts`; check it contains `ComposePage.load`, `.save`, `.closed` and no `attach`/`detach`/`setService`. CI: on `macos-latest` in the `test` job, `mise run generate && git diff --exit-code -- web/src/mygo.ts`.

- [ ] **Step 5: Run** `CGO_ENABLED=0 go test ./... && go vet ./...` → PASS.

- [ ] **Step 6: Commit**

`feat(webui): compose windows that never close or quit with input unsaved, from a dev-only menu item`

---

### Task 6: The compose page (plain text)

**Files:**
- Modify: `web/src/compose/ComposePage.tsx`, `web/src/compose/main.tsx`
- Create: `web/src/compose/saver.ts`
- Test: `web/src/compose/saver.test.ts`, `web/src/compose/ComposePage.test.tsx`

**Interfaces:**
- Consumes: the generated `ComposePage` client and events `closing`, `theme` (`web/src/mygo.ts`), `applyTheme` (Task 3), generated `Input`, `Textarea`, `Label`, `Alert`.
- Produces: `createSaver(save: (e: ComposeEdit) => Promise<SaveResult>, opts?: {delay?: number}): Saver` with `edit(e: ComposeEdit)`, `setComposing(on: boolean)`, `flush(): Promise<void>`, `status: "saved" | "saving" | "unsaved" | "conflict"`, `onChange(fn)`. 1000 ms default delay; never saves while composing; saves serially (one in flight, the latest edit wins); a `conflict` result stops further saves.

- [ ] **Step 1: Failing saver tests** (fake timers):
  - `saves 1 s after the last edit` — two edits 500 ms apart → one save, 1000 ms after the second, with the second edit's body.
  - `waits while composing` — `setComposing(true)`, edit, advance 5 s → no save; `setComposing(false)` → save 1 s later.
  - `flushes on demand` — edit then `flush()` → saved immediately, no second save later.
  - `one save in flight` — a slow first save; edits meanwhile → exactly one follow-up save with the latest body.
  - `stops on conflict` — first save resolves `{conflict: true}` → status `conflict`, later edits don't save.

- [ ] **Step 2: Failing page tests** (mocked client module):
  - `loads the draft` — subject and body from `Compose.load()` show in their fields; the quote shows as text below the body, collapsed behind 「显示引用的原邮件」.
  - `flushes on closing` (Review Focus 1) — type, emit `closing` → `ComposePage.save` called with the typed body, then `ComposePage.closed(true)`; a rejected save → `closed(false)`; a conflict → `closed(false)`.
  - `shows conflict` (Review Focus 3) — save resolves conflict → an alert 「草稿已在别处修改，本窗口不再保存」.
  - `first save carries the loaded revision` — the first save sends `state.revision`, each later one the previous result's.
  - `IME` — typing between `compositionstart` and `compositionend` doesn't save until after the end.

- [ ] **Step 3: Run** `mise run web:test` → FAIL; **implement** the saver and the page (recipient fields are plain comma-separated inputs in plan 1; status line 正在保存… / 草稿已保存 · HH:mm / 未保存; `applyTheme(state.theme)` on load and on the `theme` event; the window title follows the subject via `currentWindow.setTitle`, 「新邮件」 when empty); **run** → PASS.

- [ ] **Step 4: End-to-end by hand (macOS)**

`mise run dev` (`go tool mygo dev`), Mail › 写邮件（网页窗口）: type Chinese, wait for 草稿已保存, close with ⌘W right after typing, quit with ⌘Q with the window open and text just typed, reopen the draft from the Drafts list in the old page → the text is there; switch Settings › 外观 to dark → the window follows; ⌘Z / ⌘C / ⌘V work in the body (spec §G: Edit menu in a full web window). Note results in the PR body.

- [ ] **Step 5: Commit**

`feat(web): the compose page — load, autosave a plain-text body, flush on close`

---

### Task 7: Native icons embedded from the `remixicon` package

**Files:**
- Create: `web/icons.go` (package `web`), `web/icons_test.go`
- Modify: `internal/icons/icons.go` (reads from package `web`)
- Delete: `internal/icons/svg/` (73 files and `LICENSE-remixicon.txt`; the package ships its licence)
- Test: `internal/icons/icons_test.go` (unchanged names, must stay green)

**Interfaces:**
- Produces (package `github.com/godwit-email/godwit/web`):
```go
// RemixIcons are the Remix Icon SVGs Godwit uses, from the remixicon
// package (pinned at 4.8.0, its last Apache-2.0 release), by Godwit name.
var RemixIcons map[string]string // name → path under the embedded FS, e.g. "trash-2" → "node_modules/remixicon/icons/System/delete-bin-line.svg"
var remix embed.FS               // //go:embed lists exactly the paths in RemixIcons, plus node_modules/remixicon/License
func RemixIcon(name string) ([]byte, bool)
```
The table carries today's mapping (each current `internal/icons/svg/*.svg` names its Remix id on its first line; find each id's category directory under `node_modules/remixicon/icons/`). `internal/icons` parses `web.RemixIcon(name)` where it read its own files; names, `Get`, `Fill` and the 24×24 size stay as they are.

- [ ] **Step 1: Failing test** `web/icons_test.go`:
```go
func TestRemixIconsEmbedded(t *testing.T) {
	for name, path := range RemixIcons {
		b, ok := RemixIcon(name)
		if !ok || !bytes.Contains(b, []byte("<svg")) {
			t.Errorf("%s (%s) not embedded", name, path)
		}
	}
	if len(RemixIcons) != 73 {
		t.Errorf("%d icons, want the 73 internal/icons has today", len(RemixIcons))
	}
}
```
Run `mise run web:install && CGO_ENABLED=0 go test ./web` → FAIL.
- [ ] **Step 2: Implement** `web/icons.go`, point `internal/icons` at it, delete `internal/icons/svg/`. Run `CGO_ENABLED=0 go test ./web ./internal/icons ./internal/kit ./app` → PASS (the parsed icons draw as before: the kit pixel tests for rail and sidebar icons stay green).
- [ ] **Step 3: Commit** `build(icons): native icons embed straight from the remixicon package, pinned at 4.8.0`

## Self-review notes

- Spec coverage: §A2 stack/components (Task 2), §A4 theme and fonts (Tasks 1, 3), §A5 CSP and caller scoping (Tasks 2, 4), §A6 build/CI/generated client (Tasks 2, 5), §A7 tests (each task), §B1 window lifecycle (Task 5), §B2 load/save/saver (Tasks 4, 6). Not in plan 1, by §F: rich text, fields' full behaviour, attachments, send, Wit, embedded web views.
- Deviation, ruled above: §F says the main-window entry points open the new window in plan 1; they stay on the old page until plan 2.
- Codex review (plan v1) folded in: bound facade only (`ComposePage`), no close timeout and quit coordination, Geist tests migrated, navigation/popup denial, CI install/lint/dev task, whitespace-preserving body text, test filters and read-back, zero-revision refusal.
- Added at the user's request: Task 7, native icons embedded straight from the `remixicon` npm package; builds go through the mygo CLI (Task 2), which builds and embeds the frontend and generates the client — replacing the plan's own `web/dist` embed package, `SetFrontend` call, generate task and frontend-missing page.
- Deferred to plan 2: §B1 「草稿被删除时关闭其窗口」.
- Codex review (mygo CLI revision) folded in: one coordinated quit hook (mygo fires every listener), Vite paths relative to its config, `go.mod` `ignore ./web/node_modules`, `mise run build` on all three platforms, component regeneration checked for untracked changes, no single-instance special case.
