# 原生插件 P3：翻译接入 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 设置里的「翻译方式」可以选内置模型或任一声明了 `translate` 能力的已启用插件（原生或脚本）；阅读页原有的「翻译」按钮与选区翻译走选中的渠道，「无需翻译」、缓存、错误展示与模型路径一致。

**Architecture:** core 里一个小接口 `translator`，两个适配器：`nativeTranslator`（`NativeProcess.Call("translate")`）与 `jsTranslator`（goja 里 `ctx.translators.register` 注册的函数）。`runSkill` 在 `name == "translate"` 且 `Settings.Translator` 指向插件时，在解析模型之前分流；模型路径原样不动（它是流式的）。

**Spec:** `docs/superpowers/specs/2026-10-14-native-plugins-design.md`（§2.7、3.3、3.4、4.4 translate、5 的 settings / models / aiservice / translators 行、6 P3）。P1、P2 已完成，在同一分支上。

## Global Constraints

- `CGO_ENABLED=0` 下 `go build ./...`、`go test ./...` 通过；vet、gofmt 干净；不加依赖。
- 翻译契约（原生与脚本相同）：请求 `{"texts": [...], "source": "", "target": "<BCP 47>"}`，响应 `{"texts": [...], "source": "<BCP 47>"}`；`texts` 与请求等长，否则算插件失败。长文本由插件自己按其服务的限制拆分（写进 `sdk/native/godwit_plugin.h` 与 `sdk/godwit.d.ts`）。
- `Settings.Translator`：`""`（内置模型）或 `"plugin/<name>"`。界面与 `context` 事件里插件渠道的模型位写 `plugin/<name>`，现有 `modelOf` 切出 `<name>`。
- 正文：`body_text`，为空则 `mail.HTMLToText(body_html)`；按 `defaultMailBudget.PerMessage` 截断；按空行切段，丢掉空段，译文用 `"\n\n"` 连回。选区用 `t.Text`。
- 「无需翻译」：插件返回的 `source` 与阅读语言或 `NoTranslate` 任一项的基础语言（`-` 之前，大小写不敏感）相同 → 输出空串（与模型路径的 `NO_TRANSLATION` 一样缓存为空）。
- 缓存键：`("translate-plugin", name, 运行实例的 source hash, t.Kind, t.ID, members, lang, keep)`；只缓存 message / thread（与模型路径一致）。
- 失败计数：原生插件只有 `plugins.IsNativeFailure(err)`（退出、ABI、超时）与「`texts` 长度不对」计入 `pluginFailed`；插件返回的错误信封（`*NativeError`）原样作为错误展示、不计数。脚本插件沿用现有规则：抛错或结果不合法都计数（同 Action）。成功调用 `pluginSucceeded`。
- 脚本插件翻译的外发：`KindTranslate` 拿到 manifest 声明的全部主机、不逐次审批（spec 2.7，已与用户确认）；调用前 `markHoldsMail`；审计照旧 `gateway.Record`。
- 子进程环境：`PATH` 加上存在的代理变量 `HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY`、`ALL_PROXY` 及其小写形式。
- 原生插件启动时限 `nativeStart` 从 30 秒降到 10 秒。

## Review Focus

1. **翻译插件停用、卸载或换成别的插件后**，按钮仍在，点了给出明确错误（`ErrTranslatorUnavailable`），不会退回模型、也不会卡住。→ Task 1 `TestPluginTranslateUnavailable`。
2. **没配任何模型的用户**选了插件翻译仍能用（分流在 `resolveTask` 之前）。→ Task 1 `TestPluginTranslateWithoutModel`。
3. **纯 HTML 邮件、空正文、超长正文**：插件收到非空、截断过的段落；空正文不调用插件。→ Task 1 `TestPluginTranslateBodies`。
4. **插件返回的段数不对或崩溃**：用户看到错误，连续三次后插件停用；限流之类的错误信封不让插件停用。→ Task 1 `TestPluginTranslateFailures`。
5. **脚本插件声明了 `translate` 却没注册（或反过来）**：加载失败并说明原因。→ Task 2 `TestTranslatorRegistrationMatchesManifest`。

---

## File Structure

```
core/translators.go             translator 接口、nativeTranslator、jsTranslator、ListTranslators、translateWithPlugin
core/aiservice.go               runSkill 的插件分流
core/settings.go                Settings.Translator 与校验
core/models.go                  TaskModel("translate") 的插件分支、ErrTranslatorUnavailable
core/extensions.go              nativeStart 10 s；startPlugins 无关
core/service.go                 nativeEnv 带代理变量
core/plugins/native_fake.go     translate 返回 "[<target>] <text>"
core/plugins/host.go、types.go  ctx.translators.register、KindTranslate、EgressFor、Plugin.Translate、能力与注册一致
core/plugins/testdata/          脚本翻译夹具插件
sdk/godwit.d.ts、sdk/native/godwit_plugin.h   类型与说明
rpcbind/bind.go                 ErrTranslatorUnavailable 的 wire code
app/settings_general.go         「翻译方式」下拉
app/settings_extensions.go      权限卡片里 translate 能力的一行
app/errors.go、internal/i18n      文案
```

---

### Task 1: 原生插件翻译与渠道选择

**Files:** Create `core/translators.go`、`core/translators_test.go`；Modify `core/aiservice.go`、`core/settings.go`、`core/models.go`、`core/extensions.go`、`core/service.go`、`core/plugins/native_fake.go`、`core/plugins/native_test.go`（`TestNativeCall` 的期望）、`rpcbind/bind.go`、`rpcbind/bind_test.go`

**Interfaces — Produces:**
- `Settings.Translator string`；`validate`：`""` 或 `"plugin/"` + `skills.ValidName` 的名字。
- `var ErrTranslatorUnavailable = errors.New("the translation plugin is not running")`；rpc code `translator_unavailable`。
- `type TranslatorInfo struct { ID, Name string; Native, Running bool }`；`func (s *Service) ListTranslators(ctx) ([]TranslatorInfo, error)`：第一项 `{ID: ""}`（内置模型），然后每个声明了 `translate` 的已安装插件，`ID = "plugin/<name>"`，`Running` = 正在运行。
- `type translateRequest struct { Texts []string; Source, Target string }`（JSON `texts`、`source`、`target`）；`type translateResult struct { Texts []string; Source string }`。
- `type translator interface { translate(ctx context.Context, req translateRequest) (translateResult, error) }`；`nativeTranslator{n *plugins.NativeProcess}`。
- `func (s *Service) pluginTranslator(name string) (translator, *runningPlugin, error)`：持 `pluginsMu` 读锁取运行实例；没有运行或没有 `translate` 能力 → `ErrTranslatorUnavailable`。
- `TaskModel("translate")`：`Translator` 指向插件时返回 `"plugin/<name>"`；未运行时返回 `ErrTranslatorUnavailable`（`refreshTasks` 不把它当成「没模型」，按钮仍显示）。
- `runSkill`：`name == "translate"` 且插件渠道 → `s.runPluginTranslate(ctx, end, name, t, st)`，返回与模型路径相同形状的事件流（`context` → 至多一个 `text` → `done`，或 `error`）。
- `FakeNativeOpener` 的 `translate` 在设了 `record` 时把请求追加写到 `record + ".translate"`；配置时 `recordEnv` 为真则 record 里多一个 `env` 字段（`os.Environ()`）；`translateError` 非空时返回该错误码的信封；`translate` 改为返回 `"[<target>] " + text`，`source` 取假库 JSON 的 `"source"` 字段（缺省 `"en"`）；`"translateWrongCount": true` 时少返回一段。
- `nativeEnv` 加代理变量；`nativeStart = 10 * time.Second`。

- [ ] **Step 1: 失败的测试**（`core/translators_test.go`，`coretest.New` + 一个账户 + 投递邮件的现有夹具写法，原生夹具来自 `coretest.NativePluginFixture`，`installEnabled`）
  - `TestPluginTranslate`：设 `ReadingLanguage: "zh-CN"`、`Translator: "plugin/dlx"`；投递一封两段正文（`"Hello\n\nSee you"`）的邮件 → `RunSkill("translate", message)` → 事件依次是 `context`（`Model == "plugin/dlx"`）、一个 `text` 等于 `"[zh-CN] Hello\n\n[zh-CN] See you"`、`done`。停用插件后再跑一次：命中缓存，输出相同（缓存键不含运行状态，但查缓存在调用插件之前）。
  - `TestPluginTranslateNoTranslation`：假库 `source: "zh"`，阅读语言 `zh-CN` → `text` 事件缺省、`done`；`NoTranslate: ["en"]` 与假库 `source: "en-US"` 同样空输出。
  - `TestPluginTranslateWithoutModel`：没有任何模型服务，仍成功。
  - `TestPluginTranslateUnavailable`：选了 `plugin/dlx` 但插件停用 → `TaskModel("translate")` 返回 `ErrTranslatorUnavailable`；`RunSkill` 同样错误；没有模型调用发生（不退回模型）。
  - `TestPluginTranslateBodies`：只有 HTML 正文的邮件 → 插件收到纯文本段落；正文为空 → `done` 且没有调用插件（`FakeNativeOpener` 的 `translate` 在设了 `record` 时，把每次请求原文追加一行写到 `record + ".translate"`）；正文 30000 字 → 插件收到的总字数 ≤ 24000。
  - `TestPluginTranslateSelection`：`Target{Kind: "selection", Text: "Hi"}` → `"[zh-CN] Hi"`，不缓存（第二次调用后 `.translate` 记录有两条）。
  - `TestPluginTranslateFailures`：假库 `translateWrongCount: true` → 错误，连续三次后插件 `DisabledReason == "failures"`；另一个假库 `translateError: "rate_limited"`（`translate` 返回该错误信封）→ 错误文本是插件的 message，连续十次后仍启用。
  - `TestListTranslators`：内置一项 + 声明 translate 的原生插件一项，`Running` 随启停变化。
  - `TestNativeEnvProxy`：设 `HTTPS_PROXY` 后新建的 Service，原生插件子进程的环境里有它（假库新增 `"recordEnv": true`：configure 时把 `os.Environ()` 写进 record）。
- [ ] **Step 2:** `CGO_ENABLED=0 go test ./core -run 'PluginTranslate|ListTranslators|NativeEnvProxy'` → FAIL
- [ ] **Step 3:** 实现
- [ ] **Step 4:** `CGO_ENABLED=0 go test ./core/... ./rpcbind ./cmd/...`、`go test -race ./core` → PASS
- [ ] **Step 5:** Commit `feat(core): translate with a native plugin`

---

### Task 2: 脚本插件翻译

**Files:** Modify `core/plugins/host.go`、`core/plugins/types.go`、`core/plugins/egress_test.go`、`core/translators.go`、`sdk/godwit.d.ts`、`sdk/native/godwit_plugin.h`；Create `core/plugins/testdata/translate-demo/{plugin.json,index.ts}`；Test `core/plugins/host_test.go`、`core/translators_test.go`

**Interfaces:**
- Consumes: Task 1 的 `translator`、`translateRequest/Result`、`pluginTranslator`。
- Produces:
  - `KindTranslate InvocationKind = "translate"`；`EgressFor(KindTranslate, …)` 返回声明的全部主机、`needsApproval == false`（未授权时与其他种类一样为空）。
  - `ctx.translators.register({ translate(req) })`：只能在初始化时调用；manifest 没声明 `translate` 时抛 `TypeError("translators.register: plugin.json does not declare the translate capability")`；重复注册报错。
  - `Load`：manifest 声明了 `translate` 但初始化后没注册 → 错误 `plugin %s declares translate but registered no translator`。
  - `func (p *Plugin) Translate(ctx, inv Invocation, req any) (any, error)`。
  - `jsTranslator{s *Service; name string; rp *runningPlugin}`：`markHoldsMail` → `Invocation{ID: "translate:"+rand, Kind: KindTranslate, EgressHosts, Config: rp.host.snapshot()}` → `gateway.Record`（`Source: SourcePlugin, Tool: "plugin:" + name + "/translate", Decision: "run"`）→ `Translate` → 结果按 `translateResult` 解码（不合法即错误）。
  - SDK：`TranslateRequest`、`TranslateResult`、`Ctx.translators: { register(t: { translate(req: TranslateRequest): MaybePromise<TranslateResult> }): void }`，注释写明长文本由插件拆分、声明 `capabilities: ["translate"]`。
- [ ] **Step 1: 失败的测试**
  - `core/plugins`：`TestEgressForTranslate`；`TestTranslatorRegistrationMatchesManifest`（声明未注册 → `Load` 错误；注册未声明 → 错误；正常 → `Translate` 返回插件的结果）。
  - `core`：`TestScriptPluginTranslate`：安装 `translate-demo`（它的 `translate` 返回 `texts.map(t => t.toUpperCase())`、`source: "en"`），授权启用，设 `Translator: "plugin/translate-demo"` → `RunSkill` 得到大写译文；`holds_mail` 被置位（`core/export_test.go` 加 `HoldsMailForTest(s, name) bool` 读出）；`ListTranslators` 含它且 `Native == false`。
- [ ] **Step 2:** `CGO_ENABLED=0 go test ./core/plugins ./core -run 'Translat'` → FAIL
- [ ] **Step 3:** 实现
- [ ] **Step 4:** `CGO_ENABLED=0 go test ./core/...`、`go test -race ./core/plugins ./core` → PASS
- [ ] **Step 5:** Commit `feat(plugins): script plugins can be translators`

---

### Task 3: 设置与阅读页

**Files:** Modify `app/settings_general.go`、`app/settings_extensions.go`、`app/errors.go`、`internal/i18n/zh.go`、`internal/i18n/en.go`；Test `app/settings_general_test.go`、`app/reader_ai_test.go`

**Interfaces:**
- Consumes: Task 1 的 `ListTranslators`、`Settings.Translator`、`ErrTranslatorUnavailable`。
- 「翻译方式」放在设置 › 通用 › 语言区块，「无需翻译的语言」之前：`settingSelect`，选项为「模型（默认）」+ 每个 `TranslatorInfo`（标签 `<name>`，未运行的追加「（已停用）」）；当前值不在列表里（插件已卸载）时显示一项 `<name>（已卸载）`。列表在打开设置页时读一次（`settingsPage.refresh` 的 `hints == nil` 分支），插件变化事件时再读。
- 扩展页权限卡片：声明了 `translate` 的脚本插件多一行 `perm("translate", …)`：「会收到你要翻译的邮件内容，并可发往上面列出的地址」。原生插件的风险卡片已覆盖，不加。
- `app/errors.go`：`ErrTranslatorUnavailable` →「翻译插件没有运行，到设置 › 扩展里启用它，或换回模型翻译。」
- 文案键：`settings.translator`「翻译方式」/ "Translate with"；`settings.translator.help`「插件需要在扩展里启用」/ "Plugins need to be enabled in Extensions"；`settings.translator.model`「模型（默认）」/ "Model (default)"；`settings.translator.off`「%s（已停用）」/ "%s (off)"；`settings.translator.gone`「%s（已卸载）」/ "%s (uninstalled)"；`settings.ext.perm.translate`「会收到你要翻译的邮件内容，并可发往上面列出的地址」/ "Gets the mail you translate and may send it to the addresses above"；`error.translator_unavailable` 如上。
- [ ] **Step 1: 失败的测试**
  - `TestTranslatorSetting`（`settings_general_test.go`）：装并启用原生夹具 `dlx` → 打开通用设置 → 选「dlx」→ 等到 `Settings.Translator == "plugin/dlx"`；停用插件后重新打开 → 看到「dlx（已停用）」。
  - `TestReaderTranslatesWithPlugin`（`reader_ai_test.go`，照现有 reader 翻译测试的写法）：`Translator: "plugin/dlx"`、阅读语言 `zh-CN`、没有任何模型服务 → 打开邮件 → 「翻译」按钮出现 → 点 → 正文显示 `[zh-CN] …`，标签含 `dlx`。
  - `TestReaderTranslatorOff`：选了插件但插件停用 → 按钮仍在 → 点 → 看到「翻译插件没有运行」。
  - `TestScriptTranslatorPermissionLine`：待授权的 `translate-demo` 卡片里有「会收到你要翻译的邮件内容」。
- [ ] **Step 2:** `CGO_ENABLED=0 go test ./app -run 'Translator|TranslatesWithPlugin|ScriptTranslatorPermission'` → FAIL
- [ ] **Step 3:** 实现
- [ ] **Step 4:** `CGO_ENABLED=0 go test ./...`、vet、gofmt、`go test -race ./app ./core/...` → PASS
- [ ] **Step 5:** Commit `feat(app): choose the translator, and translate mail with a plugin`

---

## 收尾验证（手动）

用 P0 的真实 DLX 动态库（`tmp/native-p0/out/dlx.dylib`，`describe` 为 `dlx 0.0.0-p0`），签名后的 `godwit` headless：安装、授权、启用、`SetSettings` 选 `plugin/dlx`、投递或读取一封英文邮件、`RunSkill translate`，得到中文译文。没有可用邮件账户时，用 `selection` 目标直接翻一句。

## 不在 P3

- 草稿翻译（`translate-draft`）、子进程空闲退出（P4）。
- 把原生插件启动挪出 `pluginsMu`：改为把时限降到 10 秒（spec 原计划 P3 移出锁；结构改动风险大于收益，记为裁定）。
- 插件翻译的流式输出。
