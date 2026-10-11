# Godwit 插件运行时与 MCP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让第三方 TS 插件和用户配置的 MCP server 接入 Godwit：manifest 与配置声明、安装与授权、goja 沙箱、`ctx` API、两个 Hook 的可靠投递、Action / Card / Tool，以及 MCP 工具接入 Wit。完成后不需要 GUI，就能通过 Go 测试安装夹具插件、跑通 Hook、在 Wit 里调用插件工具和 MCP 工具，并验证权限拦截、超时、崩溃隔离与外发审批。

**Architecture:** 新包 `core/plugins`：manifest、安装快照与编译（esbuild）、goja 虚拟机、`ctx` 宿主绑定；宿主能力通过 `Host` 接口注入，每次调用携带一个 `Invocation`（调用种类、是否允许写、允许外发的主机）。`core`：安装 / 配置 / 授权的持久化、Hook 调度、beforeSend、Action / Card，以及一个统一的「外部执行」审批路径，供插件工具和 MCP 工具使用。每个插件一个 goja 实例、一个 owner goroutine、同一时刻只执行一个顶层调用。MCP 用官方 Go SDK 作为客户端。

**Tech Stack:** 前三份计划的全部依赖；新增 `github.com/dop251/goja`（锁定 `v0.0.0-20260923012348-67663e7a3b97`）、`github.com/evanw/esbuild v0.28.2`、`github.com/modelcontextprotocol/go-sdk v1.8.0`（Task 13 第一步先做最小编译验证，确认 transport / 调用 / 关闭 API）。

**Spec:** `docs/superpowers/specs/2026-10-08-godwit-design.md`（第 4 节 Extensions / AI.RunAction、5.1、5.3、5.4、5.5、6.3 第 1–2 条、7.4、7.6）

**基线:** 从 `feat/ai-wit`（计划 3，PR godwit-email/godwit#4）拉出 `feat/plugins`；前序 PR 合并后改基于 `main`。

**依据:** 运行时选型来自 2026-10-09 的原型对比（goja 取代 QuickJS-on-wazero；spec 第 2、5.5 节已更新）。原型验证过的写法：

- 宿主函数一律包一层：`defer func(){ if r := recover(); r != nil { panic(vm.NewGoError(fmt.Errorf("%v", r))) } }()`（goja 里普通 `panic` 会逃逸成 Go panic）。
- 每个 VM 一个 owner goroutine；`vm.NewPromise` 的 resolve / reject 只在 owner goroutine 上调用。
- 中断：`stop := context.AfterFunc(ctx, func(){ vm.Interrupt(ctx.Err()); close(done) })`；结束后 `if !stop() { <-done; vm.ClearInterrupt() }`（先等中断落地再清除）。`Interrupt` 打断不了正在执行的原生 Go 函数（含内建函数），宿主函数自己要响应 ctx。
- `vm.SetMaxCallStackSize(10000)`；`goja.Compile` 一次、`RunProgram` 运行。
- esbuild：`Format: IIFE, GlobalName: "plugin", Target: ES2022, Platform: Neutral, Supported: {"async-generator": false, "for-await": false}`，入口 `plugin.default(ctx)`；`import type` 被擦除，值导入 `@godwit/sdk` 解析失败（保留为防护）。
- goja 默认不暴露任何全局对象；不引入 goja_nodejs。

### 规格解释（本计划采用，已同步到 spec）

- **外发审批的粒度**：插件代码长期驻留、可用 `ctx.storage` 跨调用保存数据，所以「这次调用是否读过邮件」无法可靠追踪。规则按调用种类定：Hook 只能外发到 manifest 声明且安装时授权的主机，不再逐次审批（spec 5.5 原文）；Card 渲染不能外发；Action 和 Tool 如果插件同时拥有 `mail:read` 与 `net` 权限，每次调用前需要一次外发审批，卡片列出插件声明的全部主机；没有 `mail:read` 的插件，Tool 在 Wit 运行已读过邮件时同样需要审批（参数里可能带着邮件内容）。插件初始化（`default(ctx)`）不能外发。
- **邮件来源标记跨运行、跨调用保留**（终审修正）：对话持久化 `conversations.read_mail`，任何一次运行读过邮件，此后该对话的新运行与恢复都从「已读邮件」开始（历史里还带着那些邮件），重启后同样。插件持久化 `plugins.holds_mail`：插件只要收到过邮件内容（`ctx.mail.get` / `search` 结果、`message.received` 负载、selection 目标、`beforeSend` 草稿、卡片的邮件、来自已读邮件运行的工具调用），在交给插件之前就标记，此后它的 Action / Tool 外发按拥有 `mail:read` 处理（每次审批），它的工具结果也把运行标为已读邮件；只有卸载才清除，更新、去掉 `mail:read`、重新授权都不清除。
- **MCP**：MCP server 是用户自己配置并授权运行的本机程序或远端服务，Godwit 不为它提供发信能力，但无法约束它自身的行为。所有 MCP 工具调用默认每次审批；用户可以把某个工具标记为「信任的只读工具」（与工具定义的哈希绑定，定义变化即失效），之后它只在 Wit 运行读过邮件时才需要审批（外发）。`readOnlyHint` 只作为界面提示，不作为安全依据。
- **插件写操作的保证范围**：插件写工具获批后在审批事务之外执行（插件副作用不在我们的事务里）；审批绑定插件代码哈希、权限哈希、配置版本与参数，三者任一变化即失效；执行中崩溃的调用标为「结果不确定，可能已经执行」，不自动重放。
- **Assist 的插件工具**：撰写页的 Wit 只接入插件的只读工具（spec 7.4：撰写页只改当前草稿）；写工具只在 Ask 里出现。
- **非秘密的插件配置**存 SQLite（`plugin_config`），作为应用的配置存储；secret 存钥匙串。

## Global Constraints

- 前三份计划的 Global Constraints 全部继续有效（Go 1.27.1、不用 cgo、mygo sqlite Go API 单语句、迁移只追加、凭据只进钥匙串且不进日志 / 错误 / 事件、Genkit 类型只在 `core/ai`、所有交给 `ai.Run` 的工具经 `gateway.Wrap`、AI 不能发信）。
- **Godwit 不向任何扩展提供发信能力**：`ctx` 没有 send，插件工具与 MCP 工具都不经过 `Send`。用副作用计数测试证明（发件箱与 SMTP 没有新内容）。
- **权限**：`mail` 只有 `read`、`write`（改标记、移动、创建与修改草稿）；`net` 是主机列表；`llm` 是布尔值。每次调用的写能力与外发能力由宿主给出的 `Invocation` 决定，不由插件自己的注册声明决定；调用未获许可的 API 直接抛 `PermissionError`（信息不含宿主路径、Go 栈或 secret）。权限哈希变化后必须重新授权才能启用。
- **网络**：插件 `ctx.net.fetch` 与 HTTP 类 MCP 用专用 `http.Transport`：不使用环境代理；只允许 `https`；主机与端口规范化后精确匹配声明（大小写不敏感，默认端口 443；声明里带端口时端口也要匹配）；拒绝 URL userinfo 与 IP 字面量；连接时校验实际 IP（拒绝回环、私有、链路本地、未指定、组播、CGNAT `100.64.0.0/10`、IPv4 映射的这些地址）；插件提供的 `Cookie`、`Host`、`Proxy-*`、`Authorization`（除非插件自己设置且目标主机是声明主机）头按规则处理（`Cookie`、`Host`、`Proxy-*` 一律删除）；重定向每一跳重新校验，跨主机重定向时删除 `Authorization`；最多 5 跳；响应体最多 5 MB（超出报错，不返回半截）；单次 30 秒。测试通过注入 resolver / dialer 覆盖，不依赖真实 DNS。
- **调用时限**：初始化、Hook、Action、Tool 单次 30 秒；Card 的 `match` + `render` 合计 5 秒；`beforeSend` 5 秒；MCP 单次 60 秒。时钟与期限可注入，测试不用真实长等待。
- **失败与停用**：插件连续 3 次出错 / 超时 / 被看门狗中断后自动停用（`disabled_reason`），发 `events.PluginsChanged`。
- **内存（软限制）**：goja 没有单实例上限。宿主侧数据上限：传给插件的单封正文 256 KB、`ctx.storage` 单值 64 KB、单插件合计 1 MB、`llm.complete` 输入 64 KB / 输出 64 KB、fetch 请求体 1 MB / 响应 5 MB、插件返回给宿主的 JSON 1 MB。堆看门狗是尽力而为：进程级统计无法归因到插件，只在调用期间对**当前**插件生效，测试只验证策略（注入计量器与时钟），真实分配只做宽裕时限的冒烟测试。
- **秘密**：插件 `secret` 配置项与 MCP 的 Env / Header 值存钥匙串，读取时不回显；值长度至少 8 个字符（更短的拒绝保存，因为无法安全地按值脱敏）。所有这些值加入 Service 的动态秘密集合（线程安全，加载与修改时加入；被替换或删除的旧值在本次 Service 生命周期内继续脱敏，因为运行中的实例或在途调用可能仍持有它，重启后才不再加载），Engine 的脱敏、插件错误、MCP stderr、审计与事件都用它。
- **审计**：每次插件 / MCP 的网络外发与被拒绝的外发、每次审批执行都写审计，字段：来源（plugin / mcp）、扩展标识（插件名或 MCP server ID）、调用或审批 ID、目的主机（只有主机和端口）、决定、拒绝原因代码。不记录请求正文、完整 URL、查询串、认证头与秘密值。
- 插件文件在 `DataDir/plugins/<name>/`（安装快照）；编译产物在 `DataDir/plugins/<name>/.build/`。插件名规则同 skill：小写字母、数字、连字符。
- 测试全部在本机：夹具插件在 `core/plugins/testdata/`，假 HTTP 服务用 httptest，假 MCP server 用 go-sdk 的内存 transport 与测试二进制自身作为 stdio 子进程。

## Review Focus

1. **插件绕开外发限制**：重定向、DNS 重绑定到内网、IP 字面量、环境代理、`ctx.storage` 跨调用转存后在另一次调用里外发 —— 都被拦截或纳入审批，并有审计。→ Task 7、Task 8。
2. **插件谎报只读**：注册为只读的工具在 `run` 里调用写 API —— 抛错，没有任何写入。→ Task 5、Task 12。
3. **插件死循环、顶层死循环、迟到的异步回调**：被中断，不影响其他插件，迟到回调不会在下一次调用里执行。→ Task 4。
4. **新邮件到达时应用退出、首次同步大量旧邮件**：重启后每个启用的插件仍会收到新邮件的 `message.received`（至少一次）；首次同步与重建导入的旧邮件不触发 Hook。→ Task 9。
5. **`beforeSend` 插件出错、超时或在检查期间写邮件**：用户仍可确认后发送，不会死锁，不会发出未经检查的新版本。→ Task 10。

---

## File Structure

```
core/plugins/types.go         共享 DTO：MailMessage、MailSummary、DraftInput、DraftUpdate、FetchRequest/Response、Invocation、错误
core/plugins/manifest.go      Manifest、Permissions、ConfigField 解析与校验
core/plugins/install.go       安装快照（受限复制）与 esbuild 编译、缓存
core/plugins/vm.go            VM：owner goroutine、顶层调用队列、调用 ID、Promise 桥接、宿主函数包装
core/plugins/watchdog.go      堆看门狗（可注入计量器与时钟）
core/plugins/host.go          Host 接口与 ctx 对象构造、注册表
core/plugins/net.go           安全的 HTTP 客户端
core/plugins/testdata/        夹具插件
core/secrets.go               动态秘密集合
core/extensions.go            安装、列表、启停、授权、配置、卸载
core/pluginhost.go            Host 实现（mail / llm / storage / config / fetch）
core/external.go              外部执行的工具描述与审批路径（插件工具、MCP 工具共用）
core/hooks.go                 Hook 事件与调度器、beforeSend
core/actions.go               ListActions、RunAction、PluginCards
core/mcp.go                   MCP server 配置、授权、连接与工具
core/store/schema.go          迁移 7
sdk/godwit.d.ts               @godwit/sdk 类型定义（只有类型）
```

迁移 7（每项一条语句）：

- `plugins(name TEXT PRIMARY KEY, version TEXT NOT NULL, manifest TEXT NOT NULL, source_hash TEXT NOT NULL, permission_hash TEXT NOT NULL, config_version INTEGER NOT NULL DEFAULT 1, enabled INTEGER NOT NULL DEFAULT 0, granted_hash TEXT, failures INTEGER NOT NULL DEFAULT 0, disabled_reason TEXT NOT NULL DEFAULT '', installed_at INTEGER)`
- `plugin_config(plugin TEXT NOT NULL REFERENCES plugins(name) ON DELETE CASCADE, key TEXT NOT NULL, value TEXT NOT NULL, PRIMARY KEY(plugin, key))`
- `plugin_kv(plugin TEXT NOT NULL REFERENCES plugins(name) ON DELETE CASCADE, key TEXT NOT NULL, value TEXT NOT NULL, PRIMARY KEY(plugin, key))`
- `DROP TRIGGER messages_ai_event`（计划 1 的触发器对所有插入都写事件，包括首次同步与重建导入；改为由同步代码显式写入）
- `ALTER TABLE events_log ADD COLUMN subject TEXT` / `ADD COLUMN from_addr TEXT` / `ADD COLUMN msg_date INTEGER`（各一条）
- `plugin_cursors(plugin TEXT PRIMARY KEY REFERENCES plugins(name) ON DELETE CASCADE, seq INTEGER NOT NULL)`
- `mcp_servers(id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, transport TEXT NOT NULL, command TEXT, args TEXT NOT NULL DEFAULT '[]', env_keys TEXT NOT NULL DEFAULT '[]', url TEXT, header_keys TEXT NOT NULL DEFAULT '[]', version INTEGER NOT NULL DEFAULT 1, enabled INTEGER NOT NULL DEFAULT 0, granted_version INTEGER NOT NULL DEFAULT 0, created_at INTEGER)`
- `mcp_tool_trust(server_id INTEGER NOT NULL REFERENCES mcp_servers(id) ON DELETE CASCADE, tool TEXT NOT NULL, definition_hash TEXT NOT NULL, PRIMARY KEY(server_id, tool))`
- `ALTER TABLE audit_log ADD COLUMN extension TEXT` / `ADD COLUMN call_id TEXT` / `ADD COLUMN destination TEXT` / `ADD COLUMN reason TEXT`（各一条）
- `ALTER TABLE approvals ADD COLUMN exec_mode TEXT NOT NULL DEFAULT 'tx'`（`tx` / `external`）

---

### Task 1: 共享类型、秘密集合与 manifest

**Files:**
- Create: `core/plugins/types.go`, `core/plugins/manifest.go`, `core/secrets.go`, `core/plugins/testdata/`（manifest 夹具）, `sdk/godwit.d.ts`
- Modify: `core/models.go`（`NewEngine` 传入秘密集合，替换现在的 `nil`）
- Test: `core/plugins/manifest_test.go`, `core/secrets_test.go`

**Interfaces:**
- Produces（`core/plugins/types.go`，后续任务共用）：
  - `type MailMessage struct { ID int64; Subject, From string; To, Cc []string; Date time.Time; Text, HTML string; Truncated bool; Attachments []AttachmentMeta }`；`AttachmentMeta{Name, Type string; Size int64}`；`type MailSummary struct { ID int64; Subject, From string; Date time.Time; Snippet string }`
  - `type DraftInput struct { AccountID int64; To, Cc []string; Subject, HTML string }`；`type DraftUpdate struct { DraftID, Revision int64; HTML string }`
  - `type FetchRequest struct { URL, Method string; Headers map[string]string; Body string }`；`type FetchResponse struct { Status int; Headers map[string]string; Body string }`
  - `type InvocationKind string`：`init` / `hook` / `beforeSend` / `action` / `card` / `tool`；`type Invocation struct { ID string; Kind InvocationKind; Write bool; EgressHosts []string }`（`EgressHosts` 为空表示不能外发）
  - 错误：`ErrPermission`（`PermissionError{Perm string}`）、`ErrTimeout`、`ErrMemoryLimit`、`ErrClosed`、`ErrTooLarge`
- Produces（manifest，规则见上一版评审后的要求）：`Permissions{Mail []string; Net []string; LLM bool}`；`Text{Default string; ByLang map[string]string}` 与 `For(lang)`；`Option{Value string; Label Text}`；`When{Key string; Equals, NotEquals json.RawMessage}`（与被引用字段同类型比较）；`ConfigField{Key, Type string; Label Text; Options []Option; AllowCustom, Required bool; Default json.RawMessage; When *When}`；`Manifest{Name, Version, Main string; Permissions; Config []ConfigField}`；`ParseManifest(data []byte, dirName string) (Manifest, error)`（规则：名字与目录同名且合规；`main` 是相对 `.ts` / `.js` 且不含 `..`；`net` 每项是主机名或 `主机:端口`，不能是 IP、通配或含路径；config key 唯一、`when` 只引用前面的字段且比较值与该字段类型一致、select 选项不重复、默认值与类型一致、secret 不能有默认值）；`(Manifest).PermissionHash() string`
- Produces（`core/secrets.go`）：`type secretSet struct{…}`；`Add(owner string, values ...string)`、`RemoveOwner(owner string)`、`Values() []string`；Service 持有一个，`NewEngine` 的 `secrets` 回调返回 `Values()` 加模型服务 key
- `sdk/godwit.d.ts`：spec 5.3 的 `Ctx` 类型（`on`、`actions`、`cards`、`tools`、`config`、`mail`、`llm`、`net`、`storage`）与 Card / Action / Tool 的类型；只有类型声明

- [ ] **Step 1: 写失败的测试**：`TestParseManifestValid`（spec 5.3 的 deepl 示例原样通过）；`TestParseManifestRejects`（表驱动：名字不符、`mail: ["send"]`、`net: ["*.x.com"]`、`net: ["1.2.3.4"]`、`net: ["x.com/path"]`、`main: "../x.ts"`、key 重复、`when` 引用后面的字段、`when` 比较值类型不符、select 选项重复、secret 带默认值、数字字段默认值是字符串）；`TestWhenTyped`（boolean 与 number 字段的条件）；`TestPermissionHashStable`；`TestSecretSet`（并发 Add / RemoveOwner 无竞争；Engine 日志里出现插件 secret 时被替换）；`TestSDKTypesCompile`（用 esbuild 编译一个只 `import type` 的夹具，验证类型文件语法正确）。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行，确认通过** → PASS
- [ ] **Step 5: Commit**：`git commit -m "feat(plugins): shared types, manifest validation and a dynamic secret set"`

---

### Task 2: 安装快照、编译、授权与启停

**Files:**
- Create: `core/plugins/install.go`, `core/extensions.go`
- Modify: `core/store/schema.go`（迁移 7）、`go.mod`（esbuild）
- Test: `core/plugins/install_test.go`, `core/extensions_test.go`

**Interfaces:**
- Produces:
  - `plugins.Snapshot(src string, dst string) (sourceHash string, err error)`：把插件目录复制到 `dst`（先复制到同级临时目录再改名）；拒绝符号链接、`..`、绝对路径、设备文件；跳过 `.build`；单文件 5 MB、合计 20 MB 上限；`sourceHash` 是快照内全部文件（路径排序）的 SHA-256
  - `plugins.Build(snapshotDir, main string) ([]byte, error)`：只从快照编译——esbuild 的 `OnResolve` / `OnLoad` 插件把所有相对导入解析到快照目录内部并拒绝越界、拒绝裸模块名（`@godwit/sdk` 的值导入保持失败）；产物缓存键 = `sourceHash` + esbuild 版本 + 编译选项哈希；编译器以接口注入，测试可计数
  - `type PluginInfo struct { Name, Version string; Permissions plugins.Permissions; Enabled, NeedsGrant bool; DisabledReason, Error string; Config []plugins.ConfigField }`
  - `(*Service).InstallPlugin(ctx, srcDir string) (PluginInfo, error)`：读 manifest → 快照到临时位置 → 从快照编译（失败则不安装）→ 原子替换 `DataDir/plugins/<name>/` → 写行。更新已启用插件时：先停止旧 VM（Task 5 之后）、替换文件、写行；权限哈希变了 → `enabled = 0`；不变 → 重新加载新 VM。启动时如果发现目录与行的 `source_hash` 不一致（更新中途崩溃），该插件标为停用并在 `Error` 里说明
  - `ListPlugins`、`GrantPermission(name)`（`granted_hash = permission_hash`）、`SetPluginEnabled(name, enabled)`（未授权 → `ErrNeedsGrant`；启用清零失败计数）、`UninstallPlugin(name)`（停 VM、删目录与行；secret 在 Task 3 接入后一起删）；变化发 `events.PluginsChanged`

- [ ] **Step 1: 写失败的测试**：`TestSnapshotRejects`（符号链接、超大文件）；`TestBuildFromSnapshotOnly`（`import "../../etc/x"` 失败；快照后修改原目录不影响已安装代码）；`TestBuildCache`（同快照第二次不调用编译器；换编译选项后重新编译）；`TestInstallListGrantEnable`；`TestUpdateWithNewPermissionNeedsRegrant`；`TestUpdateSamePermissionsReloads`；`TestCrashMidUpdateDetected`；`TestBrokenPluginListed`；`TestUninstall`；`TestMigration7`（含 `messages_ai_event` 触发器已删除、`events_log` 新列存在）。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行，确认通过** → PASS
- [ ] **Step 5: Commit**：`git commit -m "feat(core): plugin install snapshots, build cache, grant and enable"`

---

### Task 3: 插件配置

**Files:**
- Modify: `core/extensions.go`
- Test: `core/pluginconfig_test.go`

**Interfaces:**
- Produces:
  - `type ConfigValue struct { Key string; Value json.RawMessage; Set bool }`（secret 的 `Value` 永远为空）
  - `GetPluginConfig(ctx, name)`；`SetPluginConfig(ctx, name, values map[string]json.RawMessage) error`：按声明校验（number 是有限数、boolean、select 是选项之一除非 `AllowCustom`、secret 至少 8 个字符）；`when` 不满足的字段忽略且不要求；可见的 `Required` 缺失 → 错误；secret 传 `""` 保持、`null` 清除；整组校验通过后才写入；写入成功 `config_version + 1`；secret 写钥匙串（`<install-id>/plugin:<name>:<key>`）并更新秘密集合；卸载时删除
  - 内部 `pluginConfig(ctx, name) (map[string]any, int64, error)`（含 secret 明文与默认值，以及当前 `config_version`）

- [ ] **Step 1: 写失败的测试**：`TestPluginConfigRoundTrip`；`TestPluginConfigValidation`；`TestWhenHiddenFieldNotRequired`；`TestSecretKeepClearAndMinLength`；`TestSecretNeverInErrors`；`TestConfigAtomic`；`TestConfigVersionBumps`；`TestUninstallRemovesSecrets`。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行，确认通过** → PASS
- [ ] **Step 5: Commit**：`git commit -m "feat(core): plugin configuration with keychain secrets and versions"`

---

### Task 4: goja 虚拟机与看门狗

**Files:**
- Create: `core/plugins/vm.go`, `core/plugins/watchdog.go`
- Modify: `go.mod`（goja）
- Test: `core/plugins/vm_test.go`

**Interfaces:**
- Produces:
  - `plugins.NewVM(name string, program *goja.Program, opts VMOptions) *VM`：只创建运行时与 owner goroutine，不执行任何插件代码；`VMOptions{Clock, Watchdog, Redact func(string) string}`
  - `(*VM).Call(ctx context.Context, inv Invocation, fn func(c *CallScope) (any, error)) (any, error)`：顶层调用**排队串行**；每次调用分配一个调用 ID；在 owner goroutine 上执行 `fn`；若得到 Promise，只处理**本调用**的异步完成任务直到 settle、ctx 结束或调用超时；结果在 owner goroutine 内导出为普通 Go 值（`map[string]any` / `[]any` / 基本类型，最多 1 MB 的 JSON）后返回——`goja.Value` 不离开 owner goroutine；结束时把该调用标为已结束
  - `(*CallScope).Async(fn func(ctx context.Context) (any, error)) goja.Value`：宿主异步函数用它返回 Promise；`fn` 在单独的 goroutine 里运行并继承**本调用**的 ctx；完成后把 resolve / reject 投回 owner goroutine，**带调用 ID**；调用已结束时丢弃（迟到回调不会执行，也不会出现在下一次调用里）
  - `(*CallScope).Invocation() Invocation`
  - `(*VM).HostFunc(name string, fn func(c *CallScope, call goja.FunctionCall) goja.Value) …`：recover → `NewGoError`；错误信息经 `Redact`，不含 Go 栈、文件路径
  - 初始化：`(*VM).Init(ctx, inv Invocation)` 用同一个 `Call` 运行 IIFE 与 `plugin.default(ctx)`（受时限、看门狗与中断约束；顶层死循环会被中断）
  - `(*VM).Close()`：拒绝新调用（`ErrClosed`），取消进行中的调用并等待它结束，丢弃所有待处理回调，停止 owner goroutine
  - 中断用「依据」里的写法；`SetMaxCallStackSize(10000)`
  - 看门狗：`Watchdog{Meter func() uint64; Clock; Interval time.Duration; Threshold uint64}`；`Watch(vm, callID) (release func())`：只在调用期间、只对该调用生效；`release` 之后到达的超限不会中断后续调用；默认 `Meter` 读 `runtime.MemStats.HeapAlloc`，默认 10 ms / 256 MB

- [ ] **Step 1: 写失败的测试**（注入时钟与计量器）：`TestVMNoGlobals`；`TestInitTopLevelLoopInterrupted`；`TestCallInterruptLoopVMReusable`；`TestAsyncHostFunc`（另一个 goroutine 完成 → `await` 得到结果；`-race`）；`TestLateCallbackDropped`（第一次调用超时后它的异步完成才到 → 第二次调用不受影响，回调里的副作用计数为 0）；`TestCallsSerialized`（两个 goroutine 同时 `Call` → 不交错，执行顺序可观察）；`TestCloseDuringCall`（`Close` 取消并等待，之后 `Call` 返回 `ErrClosed`）；`TestHostPanicSanitized`；`TestRecursionLimited`；`TestWatchdogPolicy`（注入计量器：调用期间超限 → `ErrMemoryLimit`；`release` 后超限不影响下一次调用）；`TestWatchdogSmoke`（真实无限分配，10 秒内结束，`-short` 时跳过）；`TestExportStaysInOwner`（返回值已是 Go 值）。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行**：`go test -race -count=5 ./core/plugins/` 与全量 → PASS
- [ ] **Step 5: Commit**：`git commit -m "feat(plugins): goja VM with serialized calls, call-scoped async and a soft heap watchdog"`

---

### Task 5: ctx 宿主 API 与注册

**Files:**
- Create: `core/plugins/host.go`, `core/pluginhost.go`
- Modify: `core/extensions.go`（启用时加载、停用 / 卸载 / `Close` 时关闭）
- Test: `core/plugins/host_test.go`, `core/pluginhost_test.go`

**Interfaces:**
- Produces:
  - `type plugins.Host interface { Config(ctx) (map[string]any, error); StorageGet(ctx, key string) (string, bool, error); StorageSet(ctx, key, value string) error; StorageDelete(ctx, key string) error; MailGet(ctx, id int64) (MailMessage, error); MailSearch(ctx, q string, limit int) ([]MailSummary, error); MailSetFlags(ctx, ids []int64, add, remove []string) error; MailMove(ctx, ids []int64, folderID int64) error; MailCreateDraft(ctx, d DraftInput) (int64, error); MailUpdateDraft(ctx, u DraftUpdate) (int64, error); LLMComplete(ctx, prompt string) (string, error); Fetch(ctx, inv Invocation, req FetchRequest) (FetchResponse, error) }`
  - `plugins.Load(ctx, vm *VM, m Manifest, h Host) (*Plugin, error)`：构造 `ctx` 对象并运行初始化（`Invocation{Kind: init}`：不能写、不能外发）；记录注册
  - 每个 `ctx` 方法在调用时检查 manifest 权限 **和** 当前调用的 `Invocation`：写 API（`setFlags`、`move`、`createDraft`、`updateDraft`）要求 manifest 有 `mail:write` 且 `Invocation.Write`；`net.fetch` 要求目的主机在 `Invocation.EgressHosts` 里（Task 7、8 决定这个集合）；`llm.complete` 要求 `llm: true`
  - `ctx.on(event, handler)`（`message.received` / `message.beforeSend`）、`ctx.actions.register({id, label, on, run})`、`ctx.cards.register({match, render})`（要求 `mail:read`）、`ctx.tools.register({name, description, schema, write, run})`（`write` 缺省 true；`name` 符合 `^[a-z][a-z0-9_]{0,40}$`）；重复抛错；只能在初始化期间注册
  - `mail.get` 正文各最多 256 KB 并标 `truncated`；`search` limit ≤ 50；`createDraft` / `updateDraft` 的 HTML 经 `SanitizeCompose`（删 `data-godwit` 标记）；`updateDraft` 用 `Draft.Revision` 做 CAS，不符抛 `StaleDraft`
  - `llm.complete(prompt)`：输入最多 64 KB；prompt 作为不可信块（`ai.Untrusted("plugin:<name>", prompt, ai.SuspectInjection(prompt))`）放进用户消息，system 为固定说明 + `UntrustedSystemNote` + 阅读语言；输出最多 64 KB；审计
  - `storage`：字符串值，单值 64 KB、单插件合计 1 MB
  - 失败计数：`pluginFailed` / `pluginSucceeded`，3 次连续失败停用
  - 「不能发信」测试：`ctx` 的能力白名单与 Host 方法集合固定；夹具插件尝试一切可用 API 后，发件箱与假 SMTP 都没有新内容

- [ ] **Step 1: 写失败的测试**：`TestCtxManifestPermissions`；`TestReadOnlyInvocationCannotWrite`（有 `mail:write` 的插件，在 `Write=false` 的调用里调 `setFlags` → `PermissionError`，库里无变化）；`TestInitCannotWriteOrFetch`；`TestCtxMailTruncation`；`TestUpdateDraftCAS`；`TestLLMCompleteUntrusted`（假模型收到的用户消息里 prompt 在不可信块内，可疑时带 `suspect="true"`）；`TestStorageLimits`；`TestRegisterOnlyDuringInit`；`TestLoadFailureCounts`；`TestNoSendCapability`。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行，确认通过** → PASS
- [ ] **Step 5: Commit**：`git commit -m "feat(plugins): invocation-scoped ctx host API and registrations"`

---

### Task 6: 外部执行的审批路径

**Files:**
- Create: `core/external.go`
- Modify: `core/conversations.go`（工具注册保留来源、允许「只读但需审批」的工具）、`core/approvals.go`（外部执行模式、恢复）、`core/gateway/gateway.go`（审计新字段、nil scope）
- Test: `core/external_test.go`

**Interfaces:**
- Produces:
  - `type toolDesc struct { Spec ai.ToolSpec; Source string; Extension string; Write bool; Egress func(scope *gateway.Scope) []string; Mode string; Prepare func(ctx, conv Conversation, args json.RawMessage) (prepared, error); Run func(ctx context.Context, a Approval) (result any, err error) }`：`Mode` ∈ `tx`（计划 3 的事务执行器）/ `external`；注册时同名工具报错而不是覆盖；`Wrap` 时使用描述里的 `Source`（不再固定为 `wit`）；Gate 组合：先网关（deny 优先）、再描述自身的审批需求
  - 只读且不需审批的工具照常在运行内执行；需要审批的工具（写，或外发需审批）都走审批，无论读写
  - 外部执行：批准时在短临界区内把审批从 `pending` 领取为 `executing`（`exec_mode = 'external'`）并释放全局锁；在锁外按工具时限执行 `Run`（ctx 继承 Service 生命周期）；完成后在一个事务里写 `done` / `failed` + 结果 + 审计；`Run` 成功但落库失败 → 返回错误并记日志，审批保持 `executing`
  - 恢复：`exec_mode = 'external'` 且停在 `executing` 的审批 → `failed`，结果「结果不确定，可能已经执行」（不重放）；`tx` 模式保持计划 3 的规则
  - 审批快照绑定：`prepared` 增加 `Binding string`（插件：`source_hash` + `permission_hash` + `config_version`；MCP：server `version` + 工具定义哈希）；执行前重新计算，不同 → `stale`
  - 审计：`gateway.Record` / `Audit` 接收 `extension`、`call_id`、`destination`、`reason`；`Check` 对 nil scope 视为「未读邮件」而不是解引用
  - 审批的外发目的地：`Approval` 增加 `Destinations []string`（结构化字段，GUI 用它显示目的主机），不再拼进 `Summary`

- [ ] **Step 1: 写失败的测试**（测试专用描述符）：`TestReadToolNeedingApproval`（只读但需外发审批的工具 → 中断并产生审批，批准后执行一次）；`TestExternalRunOutsideLock`（外部执行期间另一个对话可以开始运行与批准）；`TestExternalCrashUncertain`（执行中模拟崩溃 → 重启后 `failed` 且不重放）；`TestExternalBindingStale`；`TestDuplicateToolName`；`TestSourcePreserved`（审计行来源为 `plugin` / `mcp`）；`TestAuditDestinationNoSecrets`；`TestMixedTxAndExternalResume`（一轮里一个 `tx` 写、一个 `external` 写都批准 → 续写一次，两个结果都在）。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行**：`go test -race -count=5 ./core/ -run 'External|Approv'` 与全量 → PASS
- [ ] **Step 5: Commit**：`git commit -m "feat(core): external execution mode for approvals, preserved sources and structured audit"`

---

### Task 7: 安全的网络访问

**Files:**
- Create: `core/plugins/net.go`
- Modify: `core/pluginhost.go`
- Test: `core/plugins/net_test.go`

**Interfaces:**
- Produces:
  - `plugins.NewClient(opts NetOptions) *Client`；`NetOptions{Resolver interface{ LookupIPAddr(ctx, host string) ([]net.IPAddr, error) }; Dial func(ctx, network, addr string) (net.Conn, error); AllowHTTP, AllowLoopback bool; MaxBody int64; Timeout time.Duration}`（后两个开关只用于测试）
  - `(*Client).Do(ctx, allowed []string, req FetchRequest) (FetchResponse, string /*normalized host:port*/, error)`：按 Global Constraints 的网络规则执行；自己解析域名并直接拨号到校验过的 IP（防 DNS 重绑定，TLS SNI 仍用主机名）；不使用环境代理；拒绝原因为稳定代码（`host_not_allowed`、`scheme`、`ip_literal`、`userinfo`、`private_address`、`too_many_redirects`、`too_large`、`timeout`）
  - Host 的 `Fetch`：用当前 `Invocation.EgressHosts` 作为 `allowed`；每次成功与拒绝都写审计（目的地只有主机与端口，加 `reason`）
  - JS 侧：`await ctx.net.fetch(url, {method, headers, body})` → `{status, headers, text()}`

- [ ] **Step 1: 写失败的测试**：`TestFetchAllowed`；`TestFetchRejects`（表驱动：未声明主机、端口不符、`http://`、IP 字面量、`user:pass@`、重定向到未声明主机、重定向到 `http`、6 跳、注入 resolver 把声明主机解析到 `127.0.0.1` / `10.0.0.1` / `::ffff:127.0.0.1`、响应 6 MB、请求体 2 MB）；`TestFetchIgnoresProxyEnv`（设置 `HTTPS_PROXY` 后请求仍直连）；`TestFetchStripsHeaders`（插件设置的 `Cookie`、`Host`、`Proxy-Authorization` 不到达服务器；跨主机重定向后 `Authorization` 被删除）；`TestFetchAudited`（成功与拒绝各一行，无 URL 路径与查询串）。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行，确认通过** → PASS
- [ ] **Step 5: Commit**：`git commit -m "feat(plugins): hardened network client with egress audit"`

---

### Task 8: 各调用种类的外发规则

**Files:**
- Modify: `core/pluginhost.go`、`core/extensions.go`
- Test: `core/egress_test.go`

**Interfaces:**
- Produces（`Invocation.EgressHosts` 的决定规则，见「规格解释」）：
  - `init`、`card`、`beforeSend`：空（不能外发）
  - `hook`：插件声明且已授权的全部主机
  - `action`：插件同时有 `mail:read` 与 `net` 时，`RunAction` 需要外发审批：没有 `RunActionOptions.ApproveEgress` 时返回 `*EgressApprovalRequired{Plugin string; Hosts []string}`，GUI 确认后带上它重试；批准后为声明的全部主机；插件没有 `mail:read` 时直接为声明的全部主机
  - `tool`：插件有 `net` 且（有 `mail:read`，或 Wit 运行的 scope 已读邮件）时，工具调用需要审批（Task 6 的路径，`Destinations` = 声明主机），批准后为声明的全部主机；否则直接为声明的全部主机
- 每次调用的 `Invocation.ID` 写进审计的 `call_id`

- [ ] **Step 1: 写失败的测试**：`TestHookFetchDeclaredHost`；`TestCardCannotFetch`；`TestInitCannotFetch`；`TestActionNeedsEgressApproval`（有 `mail:read` + `net` 的插件：第一次 `RunAction` 返回 `EgressApprovalRequired`，带 `ApproveEgress` 后成功）；`TestToolEgressApproval`（同一插件的工具在 Wit 里调用 → 审批，`Destinations` 正确）；`TestStorageLaunderingCovered`（Hook 把邮件存进 storage、之后 Tool 外发 → 该 Tool 调用需要审批）；`TestNetOnlyPluginToolNoMail`（没有 `mail:read`、Wit 未读邮件 → 不需审批；Wit 已读邮件 → 需要）。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行，确认通过** → PASS
- [ ] **Step 5: Commit**：`git commit -m "feat(core): egress rules per invocation kind"`

---

### Task 9: message.received 的可靠投递

**Files:**
- Create: `core/hooks.go`
- Modify: `core/mailsync/messages.go`、`core/mailsync/fetch.go`、`core/service.go`
- Test: `core/hooks_test.go`

**Interfaces:**
- Produces:
  - 事件写入：由同步代码在导入新邮件的同一个 `Transaction` 里显式写 `events_log(type, account_id, message_id, subject, from_addr, msg_date, created_at)`；`message_id` 用按唯一服务器位置查询的子查询（`SELECT id FROM messages WHERE account_id=? AND server_folder_id=? AND server_uidvalidity=? AND server_uid=?`），不用 `last_insert_rowid()`；**只**对新到达的邮件写：文件夹已经同步过（拉取前 `last_uid > 0`）且不是 UIDVALIDITY 重建后的重新导入、不是认领回原邮件；首次同步与回填（计划 2 的 SyncLimit 回填）不写
  - 调度器：每个启用且注册了 `message.received` 的插件一个 goroutine；从 `plugin_cursors.seq` 之后按序取（每批 50）；handler 参数 `{id, accountId, subject, from, date, deleted}`（来自事件快照；邮件已被删除时 `deleted: true`，`id` 仍给出），第二个参数 `{idempotencyKey: "<plugin>:<seq>"}`；`Invocation{Kind: hook, Write: manifest 有 mail:write}`；成功后推进游标（**至少一次，重复次数没有上限**——执行后、推进前崩溃会再次投递）
  - 失败退避 2、4、8… 秒（上限 5 分钟，时钟可注入），3 次连续失败停用，游标停在原处；重新启用后从那里继续
  - 订阅边界：插件第一次启用且注册了 `message.received` 时，游标设为当时的最大 `seq`（在同一事务里读取并写入）；停用期间到达的事件在重新启用后投递
  - 清理：`seq` 小于所有**存在游标的**插件的最小游标、且早于 7 天的事件删除；没有任何游标时只按 7 天删除；清理与调度用同一把锁读写游标
  - `Close` 停止调度器并等待

- [ ] **Step 1: 写失败的测试**：`TestHookReceivesNewMail`；`TestInitialSyncNoHooks`（新账户首次同步 50 封 → 没有事件）；`TestUIDValidityReimportNoHooks`；`TestBackfillNoHooks`；`TestHookAtLeastOnceAcrossRestarts`（两次在推进前崩溃 → 投递三次，幂等键相同）；`TestHookDeletedMessage`（事件之后邮件被删 → handler 收到 `deleted: true` 与快照字段，游标推进）；`TestHookBackoffAndDisable`（注入时钟）；`TestHookResumeAfterReenable`；`TestHookCleanupRace`（清理与投递并发，`-race`）；`TestCloseStopsDispatchers`。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行**：`go test -race -count=5 ./core/ -run Hook` 与全量 → PASS
- [ ] **Step 5: Commit**：`git commit -m "feat(core): at-least-once message.received hooks for new arrivals"`

---

### Task 10: message.beforeSend

**Files:**
- Modify: `core/hooks.go`、`core/compose.go`
- Test: `core/beforesend_test.go`

**Interfaces:**
- Produces:
  - `type SendCheck struct { DraftID, Revision int64; Plugins []string /*name@source_hash，按名字排序*/; Warnings []SendWarning; Digest string }`；`SendWarning{Plugin, Message string}`
  - `Send` 的新流程：在 `editMu` 下读草稿快照（revision）与启用插件列表 → **释放锁**，在锁外按插件名顺序调用 `beforeSend`（`Invocation{Kind: beforeSend, Write: false}`，不能写、不能外发；每个 5 秒）→ `block` 立即返回 `*SendBlockedError{Plugin, Message}`；出错 / 超时视为警告「插件 <name> 没有响应」并计一次失败 → 有警告时把 `SendCheck` 存进 Service 内存（按 `Digest`，10 分钟过期）并返回 `*SendWarningsError{Check SendCheck}` → 没有警告时重新取 `editMu`，确认 revision 与插件列表未变后入发件箱（变了就从头重新检查一次；连续两次变化返回 `ErrStaleDraft`）
  - `Digest` = SHA-256(草稿 ID、revision、插件列表、警告列表)
  - `(*Service).SendAcknowledged(ctx, draftID int64, digest string) error`：找到该 `SendCheck`；在 `editMu` 下确认 revision 与插件列表仍与检查时一致 → 直接入发件箱（**不重跑** beforeSend）；不一致或找不到 → 重新走 `Send` 流程（可能再次返回警告）。`block` 永远无法确认
  - `warn` / `block` 文本最多 500 字符，去掉控制字符
  - beforeSend 不能写邮件，所以检查期间不会与 `editMu` 死锁

- [ ] **Step 1: 写失败的测试**（注入时钟）：`TestBeforeSendWarnThenAcknowledge`（确认时插件没有被再次调用）；`TestBeforeSendBlock`；`TestBeforeSendTimeoutIsWarning`；`TestAcknowledgeAfterEditRechecks`（确认前改了草稿 → 重新检查）；`TestAcknowledgeAfterPluginUpdateRechecks`；`TestBeforeSendCannotWrite`（插件在 beforeSend 里调 `setFlags` → 抛错，不死锁）；`TestEditDuringCheck`（检查期间用户保存草稿 → 用新版本重新检查，不会发出未检查的版本）；`TestBeforeSendNoPlugins`。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行，确认通过** → PASS
- [ ] **Step 5: Commit**：`git commit -m "feat(core): beforeSend checks outside the edit lock with bound acknowledgements"`

---

### Task 11: Action 与 Card

**Files:**
- Create: `core/actions.go`
- Test: `core/actions_test.go`

**Interfaces:**
- Produces:
  - `type ActionInfo struct { ID, Plugin string; Label plugins.Text; On []string }`；`ListActions(ctx, kind string)`（`ID` = `<plugin>/<id>`）
  - `type RunActionOptions struct { ApproveEgress bool }`；`RunAction(ctx, id string, t Target, opts RunActionOptions) (ActionResult, error)`；`ActionResult{Text, HTML string; DraftID int64}`；`Invocation{Kind: action, Write: manifest 有 mail:write, EgressHosts: Task 8}`；target 对象 `{kind, id, text}`（邮件内容不预先放进去）；返回字符串或 `{text?, html?, draftId?}`；HTML 经阅读清洗；30 秒；失败计数
  - `type Card struct { Plugin, Title string; Fields []CardField; Buttons []CardButton }`；`CardField{Label, Value string}`；`CardButton{Label, Kind, Value string}`，`Kind` ∈ `copy` / `open-url`（只允许 `https` / `http`）/ `run-action`（同一插件已注册的 action）
  - `PluginCards(ctx, messageID int64) ([]Card, error)`：每个注册了 card 的启用插件，`match(message)` 为真时 `render(message)`，两者合计 5 秒；`Invocation{Kind: card, Write: false}`；不合法的 card 丢弃并计一次失败；字段文本各最多 1000 字符、最多 20 个字段与 5 个按钮

- [ ] **Step 1: 写失败的测试**：`TestListAndRunAction`；`TestActionReturnsDraft`；`TestActionHTMLSanitized`；`TestCardsRendered`；`TestCardInvalidButtonDropped`；`TestCardCannotWrite`；`TestCardTimeout`。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行，确认通过** → PASS
- [ ] **Step 5: Commit**：`git commit -m "feat(core): plugin actions and declarative cards"`

---

### Task 12: 插件工具接入 Wit

**Files:**
- Modify: `core/wit.go`、`core/assist.go`、`core/pluginhost.go`
- Test: `core/plugintools_test.go`

**Interfaces:**
- Produces:
  - 工具名 `plugin_<name>_<tool>`（`-` 换成 `_`），与已有工具冲突时报错（Task 6 的注册规则）；schema 经 `ai.NormalizeSchema`、`Strict = false`
  - Ask：接入启用插件的全部工具；Assist：只接入只读工具（`write: false`）
  - 只读工具：`Invocation{Kind: tool, Write: false, EgressHosts: Task 8}`；运行内执行（30 秒）；需要外发审批时走 Task 6 的外部执行路径
  - 写工具：总是审批；`Prepare` 记录参数与 `Binding`（`source_hash` + `permission_hash` + `config_version`）；批准后外部执行，`Invocation{Kind: tool, Write: true, EgressHosts: Task 8}`
  - 所有工具结果（成功、错误、审批结果）都经 `ai.Untrusted("plugin:<name>", …, suspect)` 包装后交给模型
  - 插件停用、卸载或更新后，它的工具不再出现在新运行里；已有审批按 `Binding` 变为 `stale`

- [ ] **Step 1: 写失败的测试**：`TestAskCallsPluginReadTool`（结果在不可信块里）；`TestLyingReadToolCannotWrite`（声明 `write: false` 的工具在 `run` 里调 `setFlags` → 工具结果是错误，库里无变化）；`TestPluginWriteToolApproval`（批准后执行一次，执行时 `Write` 为 true）；`TestPluginToolStaleAfterUpdate`；`TestPluginToolStaleAfterConfigChange`；`TestAssistOnlyReadTools`；`TestPluginToolTimeout`；`TestPluginToolErrorWrapped`。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行，确认通过** → PASS
- [ ] **Step 5: Commit**：`git commit -m "feat(core): plugin tools in Wit with invocation-scoped writes"`

---

### Task 13: MCP server 接入

**Files:**
- Create: `core/mcp.go`
- Modify: `go.mod`（go-sdk）、`core/wit.go`、`core/assist.go`
- Test: `core/mcp_test.go`

**Interfaces:**
- **Step 0（先做）**：在 `core/mcp.go` 里写最小的客户端连接、`ListTools`、`CallTool`、关闭代码并编译通过（内存 transport + stdio + Streamable HTTP），确认 go-sdk v1.8.0 的 API；与本节描述不符时以 SDK 为准，把差异写进报告
- Produces:
  - `type MCPServerInput struct { Name, Transport, Command string; Args []string; Env map[string]string; URL string; Headers map[string]string }`；`Transport` ∈ `stdio` / `http`；Env 与 Headers 的值存钥匙串（`<install-id>/mcp:<id>:env:<k>` / `…:header:<k>`，至少 8 个字符，进秘密集合），表里只存键名
  - `MCPServer{ID int64; Name, Transport, Command string; Args, EnvKeys, HeaderKeys []string; URL string; Version int64; Enabled, Granted bool; Tools []MCPTool; Error string}`；`MCPTool{Name, Description string; ReadOnlyHint, Trusted bool; DefinitionHash string}`
  - CRUD：`AddMCPServer`、`UpdateMCPServer`（任何修改 `version + 1`、关闭旧连接、清除授权与信任、相关待审批变 `stale`）、`RemoveMCPServer`、`ListMCPServers`；`GrantMCPServer(ctx, id)`（`granted_version = version`）；`SetMCPServerEnabled`（未授权当前版本 → `ErrNeedsGrant`）；`RefreshMCPTools(ctx, id)`（同样要求已授权当前版本——**任何启动进程或建立连接的路径都先检查授权**）；`SetMCPToolTrusted(ctx, id, tool string, trusted bool)`（绑定当前定义哈希）
  - `http`：URL 必须 `https`（或回环地址的 `http`），用 Task 7 的专用 transport 的同一套 IP 与代理规则；不跟随跨源重定向；Headers 只发给配置的源
  - `stdio`：这是用户授权运行的本机程序（不是沙箱）；子进程环境只包含 `PATH` 与配置的 Env；放进单独的进程组；调用超时、停用、删除、`Close` 时先关闭 stdin 并等待 2 秒，再结束整个进程组并回收；stderr 最多保留最近 64 KB，经秘密集合脱敏，只用于错误信息
  - 工具接入 Ask 与 Assist：名字 `mcp_<server id>_<规范化工具名>_<定义哈希前 6 位>`（无碰撞）；**每次调用需要审批**，除非该工具被用户信任且定义哈希一致；信任的工具仍按外发规则：Wit 运行已读邮件时需要审批（`Destinations` = `["mcp:<server name>"]`）；Assist 只接入被信任的工具；执行走 Task 6 的外部路径，`Binding` = server `version` + 定义哈希
  - 结果：文本内容拼接（非文本只记类型），最多 64 KB，成功、错误与审批结果都经 `ai.Untrusted("mcp:<name>", …, suspect)` 包装
  - 测试：内存 transport 的假 server（一个只读工具、一个写工具）；stdio 用测试二进制自身（`os.Args[0]` + 环境变量切换为 MCP server 模式）

- [ ] **Step 1: 写失败的测试**：`TestMCPSecretsInKeychain`；`TestMCPNoProcessBeforeGrant`（未授权时 `RefreshMCPTools` 不启动子进程）；`TestMCPUpdateRevokes`；`TestMCPEveryCallApprovedByDefault`（即使 `readOnlyHint: true`）；`TestMCPTrustedToolNoApprovalUntilMailRead`；`TestMCPTrustInvalidatedOnDefinitionChange`；`TestMCPResultWrappedAndSuspect`；`TestMCPInjectionStillNeedsApproval`（MCP 结果里写着「创建草稿」，假模型照做 → `create_draft` 仍是审批）；`TestMCPToolNamesNoCollision`；`TestMCPStdioEnvIsolated`；`TestMCPStdioKilledOnClose`（进程组里的子进程也被回收）；`TestMCPStderrRedacted`。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行**：`go test -race ./...`、`CGO_ENABLED=0 go test ./...`、`CGO_ENABLED=0 go build ./...`、`GOOS=windows go vet ./...` → 全部通过（进程组相关代码按平台分文件：Unix 用 `Setpgid` + `syscall.Kill(-pgid)`，Windows 用 Job Object 或 `taskkill /T`，不可用时退化为结束单个进程并在报告中说明）
- [ ] **Step 5: Commit**：`git commit -m "feat(core): MCP servers as Wit tools with grants, trust and egress approvals"`
