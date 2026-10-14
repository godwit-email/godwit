# 原生插件 P4：重启后重新配置、空闲退出、草稿翻译 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 原生插件的子进程无论因为崩溃、超时还是空闲退出而重启，都先核对 `describe` 并重新 `configure`，再接受调用；空闲 10 分钟的子进程自己退出；撰写页的「翻译草稿」在选了插件翻译时走插件，结果照旧以修改提案的形式让用户决定。

**Architecture:** `NativeProcess` 加 `OnStart`（每个新进程在第一个调用之前跑一次，失败则这次调用失败、进程结束）与 `Idle`（没有在途调用满这么久就关 stdin）。core 把 describe 核对与 configure 都放进 `OnStart`，配置取自运行实例上的快照，`SetPluginConfig` 更新快照；这样不在 `pluginsMu` 里读配置。草稿翻译：`drive` 的模型调用改成可替换的运行函数，`assistSkill` 在 `translate-draft` 且插件渠道时传一个「插件翻译」的运行函数，其余（提案、审批、撤销）不变；翻译按富文本块进行。

**Spec:** `docs/superpowers/specs/2026-10-14-native-plugins-design.md`（§3.4.5、6 P4）。P1–P3 已完成，在同一分支上。

## Global Constraints

- `CGO_ENABLED=0` 下 `go build ./...`、`go test ./...` 通过；vet、gofmt 干净；不加依赖。
- 秘密只在 describe 核对通过之后才交给新进程（`OnStart` 里先 describe 后 configure）。
- 空闲时限 10 分钟（`nativeIdle`，包级变量，测试可改）；有在途调用时不计时。
- 草稿翻译：只在用户选了插件翻译（`Settings.Translator`）时走插件；目标语言为 `SkillOptions.TargetLanguage`，为空则阅读语言；输出交给现有的 `proposeUpdate`（整篇用 `cleanHTML`，选区用 `spliceSelection`）。
- 富文本块的翻译：段落、标题、引用、列表项一块一段；表格每格一段；`Pre`、分隔线、图片不翻译。块内所有片段样式（粗斜体、链接、颜色等）相同时保留该样式，否则这一块变成无样式文本（用户在提案里能看到并拒绝）。
- 插件翻译失败按 P3 的规则计数；错误以 `error` 事件结束这次运行（`ai.Error{Code: CodeInternal}`，消息为插件错误经 `cleanError`）。

## Review Focus

1. **崩溃或空闲退出后的第一个调用**拿到的进程已经配置好（含 secret），且在配置完成前不会有别的调用先到。→ Task 1 `TestNativeOnStartGatesCalls`、`TestNativeReconfiguredAfterRestart`。
2. **重启后的库被换成了 describe 不符的文件**：不配置、不交出 secret，调用失败。→ Task 1 `TestNativeRestartChecksDescribe`。
3. **空闲计时与在途调用**：长调用期间不会被空闲关闭。→ Task 1 `TestNativeIdleWaitsForCalls`。
4. **草稿里有链接、列表、表格**：结构保留，链接在样式一致的块里保留。→ Task 2 `TestTranslateDraftWithPlugin`。
5. **选区翻译草稿**、**草稿在提案生成后被改动**：沿用现有的 revision 校验。→ Task 2 `TestTranslateDraftSelectionWithPlugin`。

---

### Task 1: OnStart、Idle 与重新配置

**Files:** Modify `core/plugins/native.go`、`core/plugins/native_test.go`、`core/extensions.go`、`core/export_test.go`；Test `core/native_test.go`

**Interfaces:**
- `NativeOptions.OnStart func(ctx context.Context, call func(ctx context.Context, method string, params any) (json.RawMessage, error)) error`：每个新进程跑一次，在任何 `Call` 之前；它返回错误时这次 `Call` 返回该错误、进程被结束，下次 `Call` 再起新进程重试。等待它完成的并发调用在它完成后才发出。
- `NativeOptions.Idle time.Duration`：0 不退出；否则最后一个调用结束后满 `Idle` 且期间没有新调用，关 stdin（进程自己退出），下次 `Call` 重启。
- `func (n *NativeProcess) Start(ctx context.Context) error`：确保进程在跑且 `OnStart` 已完成（`loadNative` 用它）。
- core：`runningPlugin.cfg atomic.Pointer[map[string]any]`（原生插件的配置快照）；`loadNative` 设快照、`OnStart` 里 describe 核对（P1 的 `startNative` 逻辑）后 `configure {config: *cfg}`；`SetPluginConfig` 更新快照后对运行中的进程 `configure`（现有行为）；`var nativeIdle = 10 * time.Minute`；`SetNativeIdleForTest(t, d)` 放 `core/export_test.go`。
- [ ] **Step 1: 失败的测试**
  - `core/plugins`：`TestNativeOnStartGatesCalls`（`OnStart` 里先 sleep 300 ms 再记录「started」；并发 4 个 `describe` 都在 started 之后返回、`OnStart` 只跑一次）；`TestNativeOnStartAfterCrash`（`probe.crash` 后下一次 `Call` 之前 `OnStart` 第二次运行）；`TestNativeOnStartError`（返回错误 → `Call` 返回它；下一次 `Call` 再跑 `OnStart`）；`TestNativeIdleExit`（`Idle: 200ms`，调用后 600 ms 内进程退出，再调用时 `OnStart` 第二次运行）；`TestNativeIdleWaitsForCalls`（`Idle: 200ms`，一个 `probe.sleep {ms: 600}` 成功返回，期间 `OnStart` 只跑一次）。
  - `core`：`TestNativeReconfiguredAfterRestart`（`SetNativeIdleForTest` 200 ms；启用 → 存配置 → 删 record → 等 600 ms → 走一次翻译 → record 重新出现且含刚存的配置值）；`TestNativeRestartChecksDescribe`（假库加 `"restartDescribe": {…}`：假库每次初始化往 `record + ".starts"` 追加一行，已有记录时 `describe` 返回这一份；设成版本 `9.9.9` → 空闲退出后的调用失败，record 里仍是第一次的配置，没有被第二个进程改写）。
- [ ] **Step 2:** `CGO_ENABLED=0 go test ./core/plugins -run 'OnStart|Idle' ./core -run 'Reconfigured|RestartChecksDescribe'` → FAIL
- [ ] **Step 3:** 实现
- [ ] **Step 4:** `CGO_ENABLED=0 go test ./core/...`、`go test -race ./core/plugins ./core` → PASS
- [ ] **Step 5:** Commit `fix(plugins): a restarted native plugin is checked and configured again; idle ones exit`

### Task 2: 草稿翻译走插件

**Files:** Modify `core/conversations.go`（`drive` 的运行函数参数）、`core/assist.go`、`core/translators.go`、所有 `s.drive(` 调用方；Test `core/translators_test.go`

**Interfaces:**
- `type runFunc func(ctx context.Context, req ai.Request) (<-chan ai.Event, func() ai.Result)`；`drive(job, end, run runFunc, req, out)`，现有调用方传 `e.Run`。
- `func (s *Service) translateDraftHTML(ctx, plugin string, src, target string) (string, error)`：`richtext.FromComposeHTML(src)` → 按上面的块规则收集文本 → `pluginTranslator` → 一次 `translate` → 回填 → `richtext.ToHTML`。失败计数同 P3。
- `assistSkill`：`name == "translate-draft"` 且 `Settings.Translator` 指向插件时不解析模型；`claimRun` 的模型位写 `plugin/<name>`；运行函数调用 `translateDraftHTML`，以 `ai.Result{Messages: [{Role: "model", Text: html}]}` 结束（出错时 `ai.Result{Err}`）；`job.host` 照旧生成提案。插件不可用时 `assistSkill` 直接返回 `ErrTranslatorUnavailable`。
- [ ] **Step 1: 失败的测试**（`newAssistEnv` + P3 的 `useNativeTranslator`）
  - `TestTranslateDraftWithPlugin`：草稿 `<p>Hello <a href="https://x.example">there</a></p><ul><li>One</li><li>Two</li></ul>` → `AssistSkill("translate-draft", {TargetLanguage: "fr"})` → `interrupted`，一个 `update_draft` 提案；接受后草稿的可编辑部分含 `[fr] Hello there`（混合样式的块变成纯文本）、`<li>[fr] One</li>`、`<li>[fr] Two</li>`；引用与签名不变；没有模型请求。另一篇只有 `<p><a href="https://x.example">Docs</a></p>` 的草稿翻译后链接保留。
  - `TestTranslateDraftSelectionWithPlugin`：选区「Two」→ 提案只改那一段；草稿在提案后被另存 → 接受返回 `ErrStaleDraft`（现有校验）。
  - `TestTranslateDraftPluginUnavailable`：插件停用 → `AssistSkill` 返回 `ErrTranslatorUnavailable`。
  - `TestTranslateDraftModelUnchanged`：`Translator == ""` 时仍走模型（现有 `TestAssistSkillTranslateDraft` 继续通过即可，不新增）。
- [ ] **Step 2:** `CGO_ENABLED=0 go test ./core -run 'TranslateDraft'` → FAIL
- [ ] **Step 3:** 实现
- [ ] **Step 4:** `CGO_ENABLED=0 go test ./...`、vet、gofmt、`go test -race ./core ./app` → PASS
- [ ] **Step 5:** Commit `feat(core): translate drafts with a plugin`

## 不在 P4

- 没有模型时撰写页的 Wit 面板不显示，所以也没有「翻译草稿」入口。只放出翻译的面板模式要改 React 面板，是新的产品决定，留给用户决定。
- 块内混合样式的逐片段翻译。
