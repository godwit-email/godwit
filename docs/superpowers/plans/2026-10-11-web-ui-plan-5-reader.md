# Web UI Plan 5: Message Bodies on the Web — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every expanded message body in the reader becomes an embedded web page (`reader.html`, role `reader`). The body sits in a sandboxed `srcdoc` iframe:
- HTML mail as written, on a white sheet; plain text in the theme.
- Selectable and copyable.
- Its height drives the native layout, and wheels it can't use are handed to the reader's native scroll.
- Links go through the native confirmation, and remote images come through Go only after 加载图片.

Everything around the body stays native.

**Architecture:**
- **One web view per expanded message.** The reader creates a `*mygo.WebView` per expanded message (through a new Platform seam) and destroys it on fold or reset. Each page is registered with role `reader` and bound to its message.
- **Bound `ReaderPage` (role `reader`).** Scoped to the caller's own message, it serves:
  - the sanitized body;
  - height reports;
  - wheel deltas;
  - link opens;
  - remote image fetches, which return `data:` URLs from a shared fetcher (moved out of `app/images.go`, with the same SSRF guard and budgets).
- **No custom protocol.** The iframe stays fully offline under the existing CSP (`img-src 'self' data: blob:`).

**Tech Stack:** Go 1.27, mygo 0.4.0 (`NewWebView`, `ui.WebView`, `CallerPage`, `EmitPage`, `ui.ScrollState`), React 19, Vite (new `reader` input).

**Spec:** `docs/superpowers/specs/2026-10-10-web-ui-design.md` §E, §C2, §A5; §G (performance risk). **Branch:** `feat/reader-web`, stacked on `feat/web-embed` (PR #17). **Survey:** `.superpowers/sdd/2026-10-11-web-ui-plan-5-reader/survey.md`.

## Global Constraints

- **Isolation (spec §E3):**
  - The iframe is `<iframe sandbox="allow-same-origin" srcdoc=…>`: no `allow-scripts`, no forms, no popups.
  - The parent page never injects script into it. Mail CSS can't reach the page.
  - `mail.Sanitize` still runs in Go first; the iframe is the second line.
- **No remote request ever leaves the page or the iframe.** The CSP is unchanged.
  - **Remote images:** without 加载图片, Go sanitises with `loadRemote=false` (src removed, as today). After it, Go sanitises with `loadRemote=true` (http(s) srcs kept), and the parent page swaps each such src for a `data:` URL from `ReaderPage.Image(url)`.
  - **`Image` is refused** for a message the user hasn't allowed, and for a URL that is not in that message's sanitised body.
  - **Inline `cid:` images** already arrive as `data:` from core.
- **Scope:** every `ReaderPage` method checks the role and acts only on the caller page's own message (registered with the page). A reader page can't read another message.
- **Navigation:** each reader page is locked like the others: entry-only navigation, no window opens.
- **Links:** sanitized links carry `data-href` only. The parent page intercepts clicks in the iframe and calls `OpenLink`, which runs the native `App.openLink`: http/https → the native confirmation dialog; mailto → compose window.
- **Appearance:** HTML mail sits on a white sheet with no inversion in dark mode (spec §E3). Plain text follows the theme, at the body font size. Add `BodyFontSize` to the page's state and theme events.
- **Lifecycle:** web views exist only for expanded messages, are destroyed on fold, reset or thread change, and are unregistered from the role registry on destroy.
- **Wheel hand-off (spec §C2):** the page listens on the iframe's window. A wheel the body can't use (it is fitted to its content, so vertical wheels) is summed per animation frame and sent once per frame to `Wheel(dy)`, which moves the reader's `ui.ScrollState`.
- **Out of scope:** ⌘F find in the body (not present today; mygo's find doesn't search iframes), printing, and always allowing images per sender. Translation keeps its native rendering: the web view hides while the translation shows.
- **Code and commits:** generated components and `web/src/mygo.ts` are never hand-edited. Commits end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Go doc comments on exported names.

## Review Focus

1. **A hostile mail:**
   - Input: `<script>`, `<meta http-equiv=refresh>`, `<form>`, a CSS `url()` background, an `<img src=https://tracker>` without 加载图片, and `<a href=javascript:…>`.
   - Expected: nothing runs, nothing loads, and nothing navigates. With 加载图片, only listed http(s) images arrive, as `data:`.
   - Tests: Task 1 `TestReaderImageRefused*`; Task 3 `hostile body stays inert`.
2. **Images loading after first layout, and window resizes** → the height follows (ResizeObserver plus image load events). No clipped body, no gap (Task 3 `height follows content`).
3. **A long thread with many unread messages, all expanded** → each gets a web view. Folding destroys it, switching threads destroys all of them, and nothing leaks (Task 2 `TestReaderWebViewsLifecycle`; manual memory check in Task 4).
4. **Scrolling the reader with the pointer over a body** → the reader scrolls smoothly; one Go call per frame (Task 3 `wheel coalesces per frame`; Task 2 `TestReaderWheelMovesScroll`).
5. **One reader page asks for another message's image or body** → refused (Task 1 `TestReaderPageScoped`).

---

### Task 1: Go — the `ReaderPage` service and the shared image fetcher

**Files:**
- Create: `internal/imagefetch/` (moved from `app/images.go`: client with publicIP guard, budgets, redirect rules, `decode`; app keeps its loader on top of it); `webui/reader.go`; `webui/reader_strings.go`; tests.
- Modify: `app/images.go`.

**Interfaces (produced, bound as `ReaderPage`):**
```go
type ReaderBody struct{ Message int64; HTML, Text string; Remote bool; Theme Theme; BodyFontSize float32; Text map[string]string } // HTML sanitized (loadRemote = Remote); Text when the mail has no HTML
func (p *ReaderPage) Load(ctx) (ReaderBody, error)              // the caller page's message
func (p *ReaderPage) Height(ctx, px int) error                  // → Reader.OnHeight(message, px)
func (p *ReaderPage) Wheel(ctx, dy float64) error               // → Reader.OnWheel(dy)
func (p *ReaderPage) OpenLink(ctx, url string) error            // → Reader.OnLink(url) (native App.openLink)
func (p *ReaderPage) Image(ctx, url string) (string, error)     // data: URL; refused unless the message is allowed AND url appears in its sanitized HTML
var ReaderReload = mygo.NewEvent[ReaderBody]("readerBody")      // Go → that page: body changed (加载图片, retry)
// unbound: Reader{ SetService; Bind(pageKey any, message int64); Unbind(pageKey); Allow(message int64) /* re-sanitize with remote and emit ReaderReload */; deps OnHeight/OnWheel/OnLink }
```
- `Theme` gains `BodyFontSize` (from settings).
- Image fetching uses `internal/imagefetch`:
  - Same limits as today (maxImageBytes 5 MB, a per-message 20 MB budget, 50 images, 15 s, no cookies or Referer, public IPs only).
  - Formats: png / jpeg / gif / webp.
  - Returns `data:<type>;base64,…`.

- [ ] **Step 1: Failing tests:**
  - `TestReaderPageLoad`: a registered page for message M gets M's sanitized HTML, `Remote == false`, and the blocked count.
  - `TestReaderPageScoped` (Review Focus 5): a page bound to M can't get N's body or image; an unregistered or other-role caller → `ErrWrongPage`.
  - `TestReaderImageRefusedUntilAllowed`: `Image` before `Allow(M)` → refused, and no request reaches the test server.
  - `TestReaderImageAfterAllow`: after `Allow(M)`, `Image(url in body)` → a `data:` URL; the server sees one request with no Cookie and no Referer.
  - `TestReaderImageRefusedNotInBody`: a URL not in the body → refused.
  - `TestReaderAllowReloads`: `Allow(M)` emits `ReaderReload` with `Remote == true` to M's page only.
  - `app` image tests (`TestImageBudgets`, `TestImageTotalBudget`, `TestInlineImageBudgets`) still pass on the moved fetcher.
- [ ] **Step 2–4:** Run → FAIL; implement; `go test -race ./webui ./internal/imagefetch ./app`; `mise run generate`.
- [ ] **Step 5: Commit** `feat(webui): the reader page service; image fetching shared`

---

### Task 2: Native — one web view per expanded message

**Files:**
- Modify: `app/reader.go`, `app/app.go` (Platform seam), `cmd/godwit/gui.go`, `cmd/godwit/platform.go`, and the app test harness.

**Interfaces:**
- `Platform.NewBodyView(message int64) (view ui.NativeWebView, close func(), err error)`. In `cmd/godwit` it:
  - calls `win.NewWebView({URL: "/reader.html"})`;
  - locks the page;
  - registers the role `reader`;
  - binds the page to `message` (`Reader.Bind`).

  `close` unbinds, unregisters and destroys.

**Reader changes:**
- `msgBody` gains `view`, `close`, `height`.
  - `expand` / `loadBody` create the view once the body is ready.
  - `toggle` (fold), `reset` and thread change call `close`.
- `message()` renders `ui.WebView(c, b.view).Height(b.height)` (a minimum of one line until the first report) in place of `richtext.Render`. While the translation shows, the native translation renders instead, and the view isn't built, so it hides.
- `OnHeight(message, px)` → `b.height = px` (on the UI thread).
- **Wheel:** `ui.Scroll(...).TrackScroll(&r.scroll)`. `OnWheel(dy)` → `r.scroll.Y = clamp(r.scroll.Y + dy)` on the UI thread.
- `OnLink(url)` → `a.openLink(url)` on the UI thread.
- **加载图片:** `Reader.Allow(m.ID)` instead of `loadBody(remote)`. The native notice hides once the page reports that it was allowed.
- The truncated notice, attachments, AI area, cards and quick replies are unchanged.

- [ ] **Step 1: Failing tests** (fake `NativeWebView` and fake Platform seam, as `ask_test.go`):
  - `TestReaderWebViewsLifecycle` (Review Focus 3): open a thread with two unread messages and one read → two views; fold one → closed; switch thread → all closed; no leftover registrations.
  - `TestReaderHeightApplied`: `OnHeight(M, 640)` → that body's element height is 640.
  - `TestReaderWheelMovesScroll` (Review Focus 4): `OnWheel(120)` → `scroll.Y` +120, clamped.
  - `TestReaderLoadImagesAllows`: 加载图片 → `Allow(M)` called; the notice hides.
  - `TestLinkConfirm` (retargeted): `OnLink("https://…")` → the native dialog; 打开 → `OpenURL`.
  - **Retarget** `TestReaderRemoteImages` to the new flow (the Go fetch assertions live in Task 1).
  - **Keep** the tests that don't touch body rendering. Drop or retarget the ones that asserted body text through native richtext, with a test mapping in the report.
- [ ] **Step 2–4:** Run → FAIL; implement; `go test ./...`, `go vet ./...`, `go test -race ./app ./webui ./cmd/godwit`.
- [ ] **Step 5: Commit** `feat: reader bodies are embedded web pages (native side)`

---

### Task 3: Web — `reader.html`

**Files:** Create `web/reader.html`, `web/src/reader/main.tsx`, `Body.tsx`, `frame.ts` (iframe document builder), `wheel.ts`, and tests; modify `web/vite.config.ts` (`reader` input).

**Behaviour:**
- **Startup.** Register `readerBody` and `theme` listeners first, then `ReaderPage.load()`.
- **The iframe document.**
  - The parent builds a `srcdoc`: `<!doctype html><meta charset=utf-8>`, a `<base target=_self>`, our own base CSS, then the body.
  - **HTML mail:** white background, `#0a0a0a` text default, `img{max-width:100%;height:auto}`, wrapping tables, `word-break: break-word`. It is not inverted in dark mode.
  - **Plain text:** `white-space: pre-wrap`, theme colours and the body font size.
- **The iframe element.** `sandbox="allow-same-origin"`; `style="border:0;width:100%;display:block"`; `scrolling="no"`.
- **Height.** After load, measure `contentDocument.documentElement.scrollHeight` and call `height(px)` when it changes. Re-measure on ResizeObserver over the iframe document's root, on each `<img>` load or error, and on window resize. Debounce to one call per animation frame.
- **Links.** On a click in the iframe document, find the closest element with `data-href`; call `preventDefault` and then `openLink(href)`. Any other click that would navigate is prevented.
- **Images (remote allowed).** For each `<img src^="http">` in the iframe: set `src` to empty first, so no request goes out (the CSP blocks it anyway), then `image(url)` and set the returned `data:`. Failures leave the alt text.
- **Wheel.** Listen on `iframe.contentWindow` with `{passive:false}`. A wheel whose vertical part outweighs the horizontal → `preventDefault`, add to `pending`, and once per `requestAnimationFrame` call `wheel(pending)`. Horizontal wheels over wide content stay in the iframe, which may scroll horizontally.
- **Selection and copy** work natively. The Edit menu reaches the page.

- [ ] **Step 1: Failing tests** (jsdom; `frame.ts` is unit-tested on strings):
  - `hostile body stays inert` (Review Focus 1): the built `srcdoc` has no `<script>`, no `<meta http-equiv>`, no `on*` attributes, and no `javascript:`. The iframe has exactly `sandbox="allow-same-origin"` (Go already sanitises; this asserts the parent adds nothing unsafe).
  - `height follows content` (Review Focus 2): a mocked document whose `scrollHeight` changes → `height` is called with each new value, at most once per frame.
  - `wheel coalesces per frame` (Review Focus 4): three wheel events in one frame → one `wheel(sum)`.
  - `link click opens via Go`: a click on an element with `data-href` → `openLink(url)`; navigation is prevented.
  - `remote images swapped after allow`: a `readerBody` with `remote: true` → each http img gets the `data:` from `image()`.
  - `plain text uses theme and body size`.
- [ ] **Step 2–4:** Run → FAIL; implement; `mise run web:test`; `mise run build`.
- [ ] **Step 5: Commit**, then `mise run web:check`. Message: `feat(web): message bodies as pages`

---

### Task 4: Manual (macOS) and performance check

**Steps** (`mise run dev`):
1. Open a newsletter HTML mail: it shows as written on white; in dark mode the sheet stays white.
2. Click 加载图片: images appear. Check that the inspector's Network tab shows no request from the page or the iframe.
3. Scroll the reader with the pointer over bodies: it moves smoothly.
4. Select text and copy it.
5. Click a link: the native dialog appears.
6. Click a `mailto` link: a compose window opens.
7. Resize the window: heights follow.
8. Open a 20-message thread and expand all: note memory (Activity Monitor) and creation time. If it is unacceptable, record it in the ledger and the PR (spec §G fallback: create only for visible bodies).

Record the results in the PR body.

---

## Self-review notes

- **Spec coverage:**
  - §E1 split → Task 2; §E2 → Tasks 1–3 (find deferred); §E3 isolation and appearance → Tasks 1 and 3; §C2 wheel → Tasks 2–3; §G performance → Task 4.
- **Ruling: images via a bound method returning `data:` URLs, not a custom `godwit-img://` scheme.**
  - *Why:* mygo treats documents from handled schemes as trusted, and Windows maps schemes to `http://<scheme>.localhost`. A scheme would also need a CSP opening. The bound method keeps the CSP offline and reuses the existing fetcher's guards.
  - *Cost if wrong:* base64 overhead on large images (capped at 5 MB each).
  - Update spec §E3.
- **Ruling: ⌘F find deferred.** *Why:* it is not present today, and mygo's FindInPage doesn't search iframes. *Cost:* none versus today. Update spec §E2.
- **Ruling: translation stays native.** The web view hides while it shows.
