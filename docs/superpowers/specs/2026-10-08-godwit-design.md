# Godwit 设计文档

- 日期：2026-10-08
- 状态：待审阅
- 官网：godwit.email
- 仓库：github.com/godwitmail/godwit
- 插件 SDK（npm）：`@godwit/sdk`
- CLI 命令：`godwit`
- 命名由来：斑尾塍鹬（bar-tailed godwit）保持着鸟类不间断飞行的世界纪录，寓意可靠送达；开头的 Go 也呼应实现语言

## 1. 目标

做一个开源的邮件客户端，主要目的是打出名气。它开箱即用，同时可以扩展：内置的翻译、总结、润色等 AI 能力，全部通过与第三方插件相同的扩展机制实现。

定位参考 pi 在 agent 领域的做法：核心极小、可以随意改造，扩展机制是一等公民，核心本身是可以单独复用的库。

### 成功标准

- 普通用户：配好一个 IMAP 账户后，能收发邮件、搜索，并用内置 AI 能力翻译、总结、润色。
- 写 prompt 的用户：放进一个 `SKILL.md`，客户端里就多出一个按钮。
- 开发者：写一个 TS 插件，就能接入新的 Hook、Action、卡片或工具。
- agent 用户：通过 MCP 以只读方式访问邮箱，可以起草回复，发送需要人工批准。

## 2. 技术选型

| 领域 | 选择 | 理由 |
|---|---|---|
| 语言 | Go | 一种语言贯穿全项目，没有跨语言调用 |
| 工具链版本 | mise（`mise.toml`）固定 Go 1.27.1 | 原生 UI 不需要前端工具链；插件的 TS 由 Go 侧的 esbuild 编译 |
| GUI | mygo **原生 UI**（`ui` 包，GPU 渲染：Metal / Direct3D 11 / OpenGL） | 纯 Go、不需要 cgo；全部界面由 GPU 绘制。HTML 邮件正文先转成原生富文本显示（7.2），等 mygo 支持在原生界面里嵌入网页视图（[egoist/mygo#148](https://github.com/egoist/mygo/issues/148)）后再评估是否改用 |
| IMAP | `github.com/emersion/go-imap/v2` | 成熟，支持 IDLE 和 CONDSTORE |
| SMTP | `github.com/emersion/go-smtp` | |
| MIME | `github.com/emersion/go-message` | |
| HTML 清洗 | `github.com/microcosm-cc/bluemonday` | |
| 存储 | mygo sqlite 插件的 Go API（`github.com/egoist/mygo/plugins/sqlite`） | 原生 SQLite 3.53，通过 purego 加载，不需要 cgo，已开启 FTS5 |
| 凭据 | `golang.org/x/oauth2`，`github.com/zalando/go-keyring` | 存进系统钥匙串，不需要 cgo |
| Agent 框架 | Genkit Go（`github.com/firebase/genkit/go`）v1.13.1，锁定版本；插件 `anthropic`、`compat_oai` | 1.x 稳定版、体积最小、自带工具中断 / 恢复；选型依据见 6.3 |
| 插件运行时 | esbuild（Go 库）把 TS 转成 JS，再用 QuickJS-on-wazero 运行（如 `fastschema/qjs`） | 纯 Go，支持 ES2023 和 async；备选 goja |

不采用的方案：pimalaya（Rust 库，没有 C ABI，接入要么需要 cgo，要么要单独跑一个进程）；DuckDB（面向分析查询，不适合邮件这种频繁读写单条记录的场景，Go 驱动还需要 cgo）；Go `plugin` 包（不支持 Windows，且要求和主程序用同一个 Go 版本编译）。

### 存储相关约束

- 只在 Go 侧使用 sqlite，**不调用** `mygo.Use(sqlite.Plugin)`。
- 迁移用 `PRAGMA user_version` 加一个按顺序执行的 SQL 列表；mygo 的 `Execute` 一次只接受一条语句，所以列表里每项都是单条语句，一个版本的所有语句放进同一个 `Transaction`。
- 数据库版本高于当前程序认识的版本时（用户降级了程序）拒绝打开，提示升级，不做任何写入。
- 开启外键约束；草稿、操作队列、附件都通过外键挂在账户或邮件上，删除时级联清理。
- 同步时只拉邮件头、`BODYSTRUCTURE` 和正文的文本部分（纯文本与 HTML），存进数据库；附件只记录元数据，打开或保存时再按 MIME 段下载进缓存。原始 .eml 只在需要时（查看原文、作为附件转发）临时获取。
- 全文索引可以从邮件表完整重建（启动检查或设置里手动触发）。
- headless 发布时，要把 SQLite 动态库和二进制放在一起；离线环境通过 `MYGO_SQLITE_LIBRARY` 环境变量指定库文件路径。

## 3. 架构

核心是一个库，同一个二进制有两种运行方式。

```
core/          Go 库：所有业务逻辑，对外只暴露 Service
app/           mygo 托盘应用（默认）：进程内直接调用 core.Service
cmd/godwit/    入口；加 --headless 时把同一个 Service 通过 JSON-RPC（本地 socket）和 MCP 暴露出去
```

- 应用常驻托盘，关闭窗口只是隐藏，后台同步和 Hook 继续运行。
- **纪律**：GUI 只能通过 Service 访问核心，不能直接读数据库，也不能调用内部函数。mygo `Bind()` 生成的 TS 客户端，就是对外 API 的第一个使用者。

### 核心模块

| 模块 | 职责 |
|---|---|
| `store` | SQLite 和 FTS5：账户、文件夹、邮件头、标记；原始 .eml 文件放在磁盘上 |
| `sync` | 每个账户一个 goroutine；IDLE 推送，CONDSTORE 增量同步；本地操作队列 |
| `mail` | 解析、组装 MIME，清洗 HTML |
| `send` | SMTP 和发件箱队列 |
| `auth` | 钥匙串、OAuth |
| `ai` | 模型服务（OpenAI 兼容、Anthropic 兼容，基于 Genkit 插件）、Wit 的 agent 循环、工具注册与参数格式预处理、审批状态 |
| `skills` | 加载 `SKILL.md`，解析 UI 绑定 |
| `plugins` | JS 运行时、manifest、权限、Hook 分发 |
| `events` | 事件总线，推送给 GUI、Hook 和外部订阅者 |

## 4. Service API（v1）

```
Accounts   List / Add / Update / Remove / Test / Reauthorize / SyncNow
           （Account 含发件人名称、侧栏显示名称、签名、是否默认发件、同步范围、登录方式与状态 ok / reauth_required / error）
Settings   Get / Set（外观、界面语言、阅读语言、回复语言、免翻译语言、启动与窗口、通知、即时回复开关）
Folders    List(account)
Threads    List(view, cursor, limit) / Get(threadID)            （view：文件夹或跨账户视图）
Messages   List(view, cursor, limit)（关闭会话视图时用）/ Get(id, loadRemoteImages) / GetAttachment(id)（按需下载）
           Search(account, query)（account 为 0 时搜全部账户；只覆盖已同步的邮件，界面需提示）
           SetFlags(ids, add, remove) / Move(ids, folder) / Delete(ids)
Compose    NewDraft(mode, sourceMessageID)（mode：new / reply / reply_all / forward）
           SaveDraft / AddAttachment / RemoveAttachment / Send(draftID)
Outbox     List / Resolve(id, action)（处理「待核实」的发送：确认已发出 / 重新发送 / 丢弃）
Models     ListServices / AddService / UpdateService / RemoveService / ListModels(service)
AI         RunSkill(name, target) → stream / RunAction(id, target) / QuickReplies(messageID)
           Ask(conversationID, input) → stream / Assist(draftID, input) → stream / Resolve(proposalID, accept)
Extensions ListSkills / ListPlugins / SetEnabled / GrantPermission / GetConfig / SetConfig
Events     Subscribe() → stream
```

原则：

1. **本地优先**：所有修改先写进 SQLite 和操作队列，界面立刻响应，`sync` 在后台推送到服务器。
2. **稳定的内部 ID**：不暴露 IMAP UID，因为 UIDVALIDITY 变化后 UID 会全部失效。
3. **`Delete` 只是移到废纸篓**，Service 不提供硬删除。
4. **HTML 在 core 层清洗**，所有出口拿到的都是安全内容。
5. **一套 Service，三个出口**：mygo `Bind()`、JSON-RPC、MCP。MCP 只开放子集：只读，加上创建草稿；**没有 `Send`**。
6. **邮件的身份和位置分开**：本地 ID 在邮件的整个生命周期内不变。每封邮件同时记录两个位置：**期望位置**（用户看到的文件夹，本地修改立即生效）和**服务器位置**（最后确认的文件夹 + UIDVALIDITY + UID）。拉取按服务器位置去重，所以移动还没推送时，源文件夹里的这封不会被重复导入。移动成功且服务器返回 COPYUID 时直接更新服务器位置；不返回时进入「待认领」，下次拉取目标文件夹时，只有 Message-ID 非空、且与大小都唯一匹配的那一封才会被认回原来的本地邮件；有歧义时操作标为冲突，按新邮件导入，并提示用户。
7. **操作队列的安全规则**：每个操作记录它针对的「文件夹 + UIDVALIDITY + UID」。推送前先 SELECT 文件夹并核对 UIDVALIDITY，不一致的操作标为冲突、不再重放。同一封邮件的后续操作依赖前一个操作，按顺序执行。
8. **发送至多一次**：SMTP 无法保证「恰好一次」。发件箱状态机：排队 → 发送中 → 已发出 → 存档中 → 完成；另有「发送待核实」「存档待核实」「失败」。服务器是否已接受不确定时（提交后断线、进程崩溃）标为「发送待核实」，**不自动重发**。存档前先在已发送文件夹搜索这个 Message-ID（很多服务商会自动保存已发送邮件），`SEARCH HEADER` 是子串匹配，命中后还要取回邮件头逐字比对；已存在就不再 APPEND。APPEND 的结果不确定时同样进入「存档待核实」，下次先核对再决定。用户只能对「待核实」和「失败」的条目操作（确认已发出 / 重新发送 / 丢弃）。
9. **文件与数据库的一致性**：附件缓存和待发送的原文先写临时文件，再原子改名，最后写库；启动时清理孤立文件、补齐缺失的缓存记录。附件缓存可随时丢弃重建。
10. **资源预算**：正文文本和 HTML 各最多保存 2 MB（超出截断并标记，可按需取原文）；MIME 嵌套最多 20 层；附件缓存总量默认 2 GB，按最近使用淘汰。

## 5. 扩展机制

### 5.1 三层扩展

| 层 | 形式 | 能力 | 权限 |
|---|---|---|---|
| Skill | `SKILL.md`（Agent Skills 开放标准） | 让 LLM 执行任务，可以挂到 UI 上 | 只能读到当前目标的内容，不能联网 |
| Plugin | `plugin.json` 加 `index.ts` | 见 5.3 | 在 manifest 里声明，安装时由用户确认 |
| MCP | 配置项 | 给 agent 提供外部工具 | 逐个 server 授权，写操作每次都要批准 |

内置的翻译、总结、润色本身就是 skill，和第三方 skill 走同一条路。

### 5.2 Skill 的 UI 绑定

`metadata` 是 Agent Skills 规范定义的可选字段，专门留给客户端存放规范之外的属性。它只允许字符串到字符串的映射，规范还建议 key 取得足够独特以免冲突，所以统一加 `godwit-` 前缀：

```yaml
---
name: translate
description: Translate the email into the user's preferred language
metadata:
  godwit-action-on: "message,selection"   # message | thread | selection | draft
  godwit-action-output: "panel"           # panel | replace-body（只替换显示，不改原邮件，可切回原文）| replace-draft
---
```

没有 `godwit-action-*` 的 skill，不会出现在 UI 上，只由 `Ask` 里的 agent 按需调用。

其他约束：
- `name` 必须和 skill 所在目录同名，只能用小写字母、数字和连字符（规范要求）。
- 规范允许 skill 带 `scripts/` 目录，但 Godwit **不执行 skill 里的脚本**；要运行代码就写成 Plugin，走权限声明。`references/`、`assets/` 照常可以读取。

### 5.3 Plugin

```json
{
  "name": "deepl-translate",
  "version": "0.1.0",
  "main": "index.ts",
  "permissions": {
    "mail": ["read"],
    "net": ["api-free.deepl.com", "api.deepl.com"],
    "llm": false
  },
  "config": [
    { "key": "apiKey", "type": "secret", "label": "DeepL API Key" },
    { "key": "plan", "type": "select", "label": "套餐",
      "options": [{ "value": "free", "label": "Free" }, { "value": "pro", "label": "Pro" }],
      "default": "free" },
    { "key": "endpoint", "type": "text", "label": "自定义地址",
      "when": { "key": "plan", "equals": "pro" } }
  ]
}
```

`mail` 权限可取 `read`、`write`（改标记、移动、创建草稿）。**没有 `send` 权限。**

```ts
import type { Ctx } from "@godwit/sdk"   // 只有类型定义

export default function (ctx: Ctx) {
  ctx.on("message.received", async (msg) => {})
  ctx.on("message.beforeSend", async (draft) => ({ warn: "..." }))  // 也可以返回 { block: "..." }

  ctx.actions.register({ id, label, on: ["message"], run })
  ctx.cards.register({ match, render })                  // render 返回 Card 数据
  ctx.tools.register({ name, description, schema, run }) // 给 Ask 里的 agent 用

  ctx.config    // 用户填写的配置（含 secret），由宿主解析后注入，只读
  ctx.mail      // get / search / setFlags / move / createDraft
  ctx.llm       // complete，走用户选定的模型服务
  ctx.net       // fetch，只能访问 manifest 里声明的域名
  ctx.storage   // 每个插件独立的 KV 存储
}
```

- 扩展点按命名空间分组（`ctx.actions`、`ctx.cards`、`ctx.tools`），避免与邮箱一侧的「账户 / 服务商」概念混淆。
- v1 只做 `message.received` 和 `message.beforeSend` 两个 Hook。
- **Hook 可靠投递**：`message.received` 不走可丢弃的界面事件总线。新邮件入库时，在同一个事务里写入一张只增不减的事件日志（自增序号）；每个插件记录自己处理到的序号，每次调用带幂等 ID，插件或应用重启后从断点继续，每封邮件对每个插件至少投递一次。
- **权限矩阵**：`mail:read` 读邮件；`mail:write` 改标记、移动、创建和修改草稿；不存在发信权限。只有 `warn` 能被用户覆盖。
- **`warn` 和 `block` 的区别**：`warn` 显示提示，用户可以「仍然发送」；`block` 阻止发送，用户只能修改草稿或在扩展设置里停用该插件。
- **Card** 是声明式数据（标题、字段、按钮），由客户端负责渲染，插件不能注入 HTML。按钮动作只能是 `copy`、`open-url`、`run-action` 之一。

### 5.4 配置声明

插件在 `plugin.json` 的 `config` 里声明需要用户填写的字段，Godwit 统一渲染设置表单并在 Go 侧校验，插件不写 UI。设计参考 aio-proxy 的 `ConfigSpec`，但校验在 Go 侧完成，所以用 JSON 声明而不是 Zod。

- 字段类型：`text`、`secret`、`number`、`boolean`、`select`（可选 `allowCustom`，允许手动输入）。
- `when: { key, equals | notEquals }` 控制条件显示，只能引用排在前面的字段。
- `label` 等文案支持多语言：`string | { default, "zh-CN": ..., ... }`。
- `secret` 字段存进系统钥匙串，其余存进配置文件；编辑时 `secret` 留空表示保持原值；界面和日志永不回显 secret。
- 宿主严格校验声明本身：key 唯一、`when` 引用合法、`select` 选项不重复；不合法的插件拒绝加载。

### 5.5 运行时与安全

**统一权限网关**：插件、MCP 和 Wit 的所有工具调用都经过宿主的同一个网关，按「调用来源 + 数据范围 + 外发目的地」判断是否放行，网关记录审计日志。

- **任何扩展都不能发信**：插件、MCP、Wit 都只能创建或修改草稿，发送永远由用户在撰写页亲自点击。
- **防外泄**：网关为数据打上来源标记。任何外发（插件的 `ctx.net`、MCP 工具、Hook 内的联网）在**实际发出请求的那一刻**检查：本次调用链里是否读过邮件内容（包括工具内部自己读的、预先放进上下文的、Hook 收到的），以及目的地是否在批准范围内。读过邮件内容的外发需要用户审批，卡片上显示目的域名；插件 Hook 只能外发到 manifest 声明并经用户安装时同意的域名。
- **防 prompt 注入**：邮件内容交给模型时统一标记为不可信数据；写操作工具一律需要审批；审批绑定调用 ID、最终参数（修复后的参数）和目标对象的版本，参数或对象变化后审批失效。
- **审批只执行一次**：每个待审批调用持久化为「待审批 → 已批准 → 执行中 → 已完成 / 已拒绝」，带幂等键。宿主执行完后用 `Respond` + `WithToolResponses` 把结果交还 Genkit，**不用 `Restart`**（否则会再执行一次）；崩溃后停在「执行中」的调用，先按幂等键核对是否已生效，再决定交还结果还是标为失败。
- **插件沙箱**：每个插件一个 QuickJS 实例，设内存上限、单次调用的 CPU 时间上限（`beforeSend` 最多 5 秒），宿主侧调用可随插件超时一起取消；崩溃或连续超时的插件自动停用并通知用户。WASM 本身不限制宿主调用，所以网络限制在宿主的 `ctx.net` 实现里做：只允许 manifest 声明的域名，跟随重定向时每一跳都重新校验，DNS 解析后拒绝内网、回环和链路本地地址，限制响应大小。
- 调用没有声明权限的 `ctx` API 时直接抛错。
- 日志按值动态脱敏：收集所有 secret 和凭据的字符串值，输出前替换，不依赖字段名。

**邮件 HTML 显示契约**（原生渲染，没有 HTML 引擎）：

- core 层用白名单清洗：去掉脚本、事件属性、`javascript:` 等危险协议、`<form>`、`<iframe>`、`<object>`、`<base>`、`<meta http-equiv>`；CSS 只保留白名单属性，去掉 `url()`、`expression()`、`@import`。
- 清洗后的 HTML 转成 **Godwit 富文本模型**（段落、标题、粗体 / 斜体 / 下划线 / 删除线、颜色、链接、有序 / 无序列表、引用、代码、分隔线、图片、简单表格），再由原生界面绘制。模型表达不了的排版（多列布局、绝对定位、复杂嵌套表格）会被简化为从上到下的流式排版。
- 远程资源默认全部拦截（`img` 的 `src` 和 `srcset`、协议相对地址、CSS 背景图），被拦截的数量在清洗**之前**统计。用户选择加载后，由 Go 侧下载图片（不带 Cookie 和 Referer），CSS 背景图始终不加载。`cid:` 内联图片直接读本地附件，按文件内容实际识别格式，只允许 PNG、JPEG、GIF、WebP。
- 链接点击由宿主处理，用系统浏览器打开，并在打开前显示真实地址。
- 中西文混排时自动加间距（相当于 CSS 的 `text-autospace: normal`），在富文本模型渲染时处理，所有平台效果一致。
- 兜底：阅读区提供「在浏览器中查看原文」。把清洗后的 HTML 写入临时文件（文件里加上禁止远程资源的 CSP），用系统浏览器打开。
- 以后如果正文改用网页视图（mygo#148），沿用同样的清洗规则，并补上 iframe sandbox 和 CSP 的约束。

## 6. 模型服务

### 6.1 v1：两种兼容接口

v1 只内置两种模型服务，用户在设置里添加任意多个：

| 类型 | 配置项 |
|---|---|
| OpenAI 兼容 | 名称、base URL、API key、模型列表（可从 `/models` 拉取，也可手动填写） |
| Anthropic 兼容 | 名称、base URL、API key、模型列表 |

skill、Action、`Ask` 统一用 `服务名/模型 ID` 指定模型；设置里可以选默认模型，单个 skill 可以覆盖。

订阅账户（ChatGPT、Claude、Copilot 等）不在 Godwit 内处理。需要的用户可以自行运行 aio-proxy、magpie、CLIProxyAPI 之类的本地网关，再把 base URL 填进来。Godwit 不做自动发现。

### 6.2 v2：模型服务插件（`ctx.models`，暂不实现）

调研结论，供 v2 设计时参考：

- **aio-proxy**：协议细节归插件、用户交互归宿主（`AuthorizationPort`：device code、authorize URL、loopback 加粘贴回调 URL 兜底）；凭据由宿主存储，刷新走带 revision CAS 的 single-flight；多账户用 `fingerprint` 去重；`definePlugin` + 整数 `apiVersion`。插件产出 AI SDK 的 `ProviderV4`，依赖 Node API。
- **magpie**（Go，MIT）：按需下载固定版本的 Bun，以子进程运行插件 host，stdin/stdout 逐行 JSON-RPC，流式响应带 credit 流控；以 OpenCode 插件的 `auth` 钩子为通用契约，pi 扩展通过适配层包装成同样的形状。
- **CLIProxyAPI**（Go，MIT）：`sdk/cliproxy` 可无 cgo 嵌入并交叉编译，但会让二进制增加约 45MB，且内置执行器只能通过启动其 HTTP 服务使用；核心用途是把订阅登录当 API 用，存在服务条款风险。若要支持，做成可选构建（如 `-tags cpa`），不进默认版本。

v2 的倾向：模型插件跑在按需下载的 Bun 子进程里（与跑在 QuickJS 沙箱里、能访问邮箱的邮件插件分开），契约采用 OpenCode 的 `auth` 钩子，pi 通过适配层接入，aio-proxy 插件以后再加适配。

### 6.3 Agent 框架：Genkit Go

**选型过程**（2026-10-08，所有验证都用本地假服务器模拟两种接口的流式协议，`CGO_ENABLED=0`，Go 1.27.1）：

| | Fantasy v0.45.2 | Eino v0.9.21 | Genkit Go v1.13.1 |
|---|---|---|---|
| 流式输出 + 工具调用 + 审批（两种接口） | ✅ | ✅ | ✅ |
| 等待审批时取消 / 输出中途取消 | ✅ / ✅ | ✅ / ✅ | ✅ / ✅ |
| 复杂工具参数格式（`$defs`、`oneOf`） | 自带 agent 循环会重建参数格式 ❌；只用模型接入层 ✅ | ✅ | OpenAI ✅；Anthropic 丢失根级字段 ❌（可绕开，见下） |
| 审批状态保存、重启后恢复 | 需自己实现 | 有（未测） | ✅ 自带中断 / 恢复，已验证 |
| 二进制体积（两种接口，`-s -w`） | 50 MB | 28 MB | **22 MB** |
| 主要问题 | Anthropic 模块无条件引入 Bedrock / Vertex | 依赖的 sonic 不支持 Go 1.27；概念多 | Anthropic 插件丢根级字段；全局注册表，带 OpenTelemetry |

另外评估过：google/adk-go（无 Anthropic 支持）、langchaingo、自己基于官方 SDK 写循环（Codex / gpt-6-astra 独立评估估算 2–4 人周，协议差异需长期维护）。

**落地要求**：

1. **工具参数格式预处理**：插件和 MCP 的工具注册进来时统一处理：把 `#/$defs/*`、`#/definitions/*` 引用展开成内联定义并删掉根级定义；根级 `description` 合并进工具描述；根级 `anyOf` / `oneOf` / `allOf` 以及无法展开的自引用 `$ref` 记录警告。原因：Genkit 的 Anthropic 插件只保留根级的 `type`、`properties`、`required`。
2. **插件和 MCP 的工具一律 `ai.WithStrictSchema(false)`**；内置工具可以开启严格模式。
3. **审批用 Genkit 的工具中断**：需要审批的工具在未获批准时调用 `ToolContext.Interrupt`，不产生任何副作用；对话历史和待审批的调用序列化后存入 SQLite；用户批准后用 `ToolAction.Restart` + `ai.WithToolRestarts` 恢复执行，拒绝时用 `Respond` 返回「用户拒绝」。应用重启后待审批的调用仍可继续。
4. **多个待审批调用**：同时显示多张卡片（按调用顺序），支持逐个处理和「全部允许 / 全部拒绝」。Genkit 要求恢复时一次处理完所有被中断的调用，所以：用户批准一张卡片时，宿主**立即执行**这个工具的实际操作并记下结果；所有卡片都处理完后，再用这些结果一次性恢复 agent。修改同一封草稿的写操作排队执行，只读工具和不同对象上的写操作可以并行。关闭面板时取消所有未决调用。
5. **隔离框架**：`ai` 模块对外只暴露自己的接口（流式事件、工具定义、审批），不把 Genkit 的类型泄漏到 Service API，方便以后替换。
6. **上游**：给 Genkit 提 issue / PR，让 Anthropic 插件通过 SDK 的 `ExtraFields` 保留根级字段；合并后可以去掉第 1 条里的部分处理。

## 7. 界面与交互

设计稿：[Godwit UI 画布](https://claude.ai/artifact/LESLwyFigHSD8Yfvfimruq)（以画布为准，这里记录决定和规则）。

### 7.0 设计系统

- **实现方式**：全部界面用 mygo 原生 UI 绘制。组件外观**按 shadcn/ui 的 Nova 风格、neutral 基础色**来做：用 mygo 提供的无样式基础组件（`ButtonBase`、`SwitchBase`、`SelectBase`、`PopoverBase`、`DialogBase` 等）实现自己的一套组件，尺寸、圆角、间距、状态样式对齐 shadcn Nova。
- **色板**：只用 **Tailwind v4 默认色板**（oklch，用 `ui.Oklch` 绘制，P3 屏幕上和浏览器里一样鲜艳）。mygo PR [#104](https://github.com/egoist/mygo/pull/104)（`ui/palette`）合并前，项目里放一份同样从 Tailwind v4 `theme.css` 生成的 `internal/palette`，只收录用到的颜色；合并后改用 mygo 的。
- **主题变量**：在 mygo 主题（`c.Theme()`）之上定义 Godwit 的语义颜色，全部引用色板，不写自定义十六进制值：

  | 变量 | 浅色 | 用途 |
  |---|---|---|
  | `Window` | `neutral-100` 与 `neutral-200` 的 oklch 中点 | 顶栏和图标栏底色 |
  | `Sidebar` | `neutral-50` | 侧栏 |
  | `Background` | `white` | 内容区 |
  | `Foreground` / `Muted` / `Border` | `neutral-950` / `neutral-500` / `neutral-200` | 正文、次要文字、分隔线 |
  | `Primary` | `neutral-900` | 主按钮、开关打开、选中强调 |
  | 选中项 | 侧栏 `neutral-100`，图标栏 `neutral-200` | |
  | 状态 | 成功 `green-600`；提醒 `amber-50` 底 / `amber-300` 边 / `amber-800` 字；危险 `red-600` | |

  深色主题对应 shadcn neutral 的深色取值，三层颜色的深色值待设计稿确定（见待定）。设计稿里个别不在色板上的灰色（如 `#ECECEC`、`#F9F9F9`、`#DEDEDE`、`#EDEDED`）实现时按上表换成色板值。
- **图标**：Lucide 的 SVG，用 `ui.Icon` 显示，随文字颜色变化。
- **字体**：用 `ui.RegisterFont` 内嵌 Geist / Geist Mono；中文使用系统字体（苹方、微软雅黑、Noto Sans CJK），不内嵌，以免安装包过大。
- **布尔设置一律用 Switch**；表单、菜单、对话框、卡片都用上面那套组件。

### 7.1 窗口与布局

- 布局参考 Codex，三层颜色：窗口底色（顶栏 + 左侧图标栏）`#ECECEC` → 侧栏 `#F9F9F9`（左上圆角）→ 内容区白色，侧栏与内容区连成一块面板，贴住窗口右边和下边。
- 左侧图标栏：邮件、Wit；设置固定在底部。扩展只从「设置 › 扩展」进入。没有用户体系，不放头像。
- 顶栏：系统窗口按钮 + 收起侧栏、后退、前进；内容区上方显示当前标题（如「全部收件箱 · 16 封未读」）和同步状态。
- 系统窗口按钮（mygo `TitleBarHiddenInset` / `TitleBarHeight`；原生 UI 里用 `ui.Context.TitleBar` 得到要让出的位置，用 `Element.DragWindow` 标记可拖动区域）：
  - macOS：红绿灯嵌在左上角；背景 `VibrancySidebar`。
  - Windows 11：右上角最小化、最大化、关闭，标题栏高 40；窗口圆角 8px；背景 `VibrancyMica`。
  - Linux：按钮由 GTK 绘制，位置取决于桌面设置，只能靠 inset 变量让位。

### 7.2 邮件

- 侧栏是账户分组树：顶部是跨账户视图（全部收件箱、已标星、智能视图），下面每个账户可折叠，展开后是各自的文件夹，最后是「添加账户」。只有一个账户时不分组。
- 写邮件的发件人默认用当前选中的账户；在跨账户视图里写邮件用默认账户。
- **会话视图**：邮件列表按会话（thread）显示，一行一个会话，显示参与者、最新一封的摘要、封数和未读数；阅读区按时间顺序展示整个会话，较早的邮件默认折叠成一行，未读和最新一封展开。会话按 `References` / `In-Reply-To` / `Message-ID` 归并，同一账户内跨文件夹（含已发送）；被引用但还没收到的邮件也记下占位，后到的父邮件或同时引用两个会话的邮件会把它们合并（与到达顺序无关）。只有完全没有引用头时，才按「规范化后的主题 + 除自己以外的参与者有交集 + 7 天内」兜底归并。设置里可以关闭会话视图，改回逐封显示。
- 阅读区（单封邮件）自上而下：标题与发件人 → AI 总结（可开关，只显示要点，不带操作按钮）→ 插件 Card → 远程图片拦截提示 → 正文 → 附件 → 即时回复。
- 翻译不单独占区域：正文直接替换为译文，上方一行「已翻译 · 显示原文」。
- 即时回复：在收到的邮件底部给出最多 3 条完整回复草稿（只显示一行预览，Tab 切换），选中后进入撰写页继续改。会额外消耗模型额度，设置里可以关闭。

### 7.3 撰写

- 草稿和发信是同一个撰写页，内容自动保存为草稿；发送后草稿变为发件箱里的操作。
- 撰写区直接铺满内容区（不套卡片）；编辑区工具栏只有格式和附件。
- **HTML 撰写**：原生富文本编辑器，基于 mygo 的 `TextInputClient`（输入法、选区、撤销由 mygo 接入系统，文档模型和绘制由我们实现），文档就是 7.2 的 Godwit 富文本模型的子集（加粗、斜体、链接、列表、引用）。发出的邮件是 `multipart/alternative`（HTML + 自动生成的纯文本）。粘贴的 HTML 先清洗再转成模型。mygo#148 解决后，可以评估改用网页的 `contenteditable`。
- 收件人区支持抄送、密送、Reply-To；回复、全部回复（自动去掉自己的地址）、转发（正文引用原邮件，附件随转发带上）。
- 附件添加时复制进应用自己的目录（草稿快照），不引用外部路径；单个附件和整封邮件有大小上限（默认 25 MB，超出时提示），超限前提示用户。
- 右侧是Wit：多轮对话，结果直接写入左侧正文。输入框上方两个入口：「调整 ▾」（语气：正式 / 友好；长度：更短 / 更长；校对）和「翻译 ▾」。菜单项都是 skill。另有 ⌘K：选中一段只改这一段。
- 撰写状态：① 空白 ② 生成中（正文锁定，可停止）③ 局部修改待确认 ④ 整体重写待确认 ⑤ 已应用（最近一轮可撤销）⑥ ⌘K 改写一段 ⑦ 出错（正文不变，可重试）。
- 正文 diff 规则：
  - 第一次生成（正文为空）直接写入，不显示 diff。
  - 局部修改按词显示修订（删除线 + 高亮），可全部接受 / 拒绝，也可逐处处理。
  - 改动超过一半视为整体重写，不显示逐词 diff，提供「对比原文」左右对照。
  - 有待确认的修改时正文不可编辑，必须先接受或拒绝。
  - 引用的原邮件和签名对助手只读。
- 发送前 Hook（如附件检查）在点「发送」时提示，可「仍然发送」。

### 7.4 Wit（agent）

AI 助手统一叫 **Wit**（取自 God*wit*）。图标栏上的入口和撰写页右侧的面板是同一个 Wit：同一个 agent 运行时 + 同一个对话面板组件，按配置区分：

| 配置 | 撰写页里的 Wit | 图标栏里的 Wit |
|---|---|---|
| 上下文 | 原邮件、附件、与对方的往来 | 整个邮箱（搜索） |
| 工具权限 | 读邮件、`update_draft`（只改当前草稿） | 读邮件、`create_draft` |
| 结果去向 | 写入正文，带修订 | 显示在对话里，引用来源列在右侧 |

Wit 输出的 Markdown 也转成 Godwit 富文本模型显示。面板元素统一：用户消息、工具调用记录（可折叠）、「待确认」卡片（接受 / 拒绝）、引用邮件和模型选择、底部输入框。疑似 prompt 注入的来源会被标出，并说明「只作为数据处理」。

### 7.5 账户接入

- **首次启动**：没有账户时不显示图标栏和侧栏，窗口分左右两栏。左栏说明隐私承诺（密码只存系统钥匙串、邮件只存本机、AI 默认关闭）；右栏是接入流程。
- **流程以服务商为先**：输入邮箱地址自动识别服务商，或从列表选择（Gmail、Outlook、QQ、网易 163/126、腾讯企业邮、iCloud、Fastmail、其他 IMAP），每个服务商标明登录方式。
  - 授权码 / 应用专用密码类：说明「要用授权码，不是登录密码」，给出获取步骤和设置页链接；服务器配置折叠在「手动设置服务器」里。
  - 浏览器登录类（OAuth，XOAUTH2）：打开系统浏览器，登录完成后自动继续。
  - 连接检查逐项显示（服务器设置、IMAP、SMTP），失败时指出具体是哪一步、原因和下一步操作。
  - 成功后填写发件人名称、选择同步范围（默认每个文件夹最近 2000 封）。
- **v1 登录方式**：Outlook / Hotmail 个人邮箱已不支持密码登录，必须走 OAuth（需注册 Microsoft 应用，公开客户端 + PKCE）。Gmail 的 OAuth 需要申请受限权限并通过 Google 的审核和安全评估，对开源项目成本过高，v1 先用应用专用密码。
- **非首次添加**：只有一个入口，在「设置 › 账户」里，同一个流程以弹窗形式出现在设置页上。主界面侧栏的「+ 添加账户」直接跳到设置。
- 不提供明文连接选项（`SecurityNone` 只允许本机回环地址，仅用于测试）。

### 7.6 设置

- 设置侧栏分两组：**账户**（每个账户一项，带服务商标识和状态点：绿色正常、黄色需要处理；最后是「+ 添加账户」）和**应用**（通用、模型服务、扩展、隐私与安全、关于）。
- **账户页**（选中某个账户）：顶部是身份信息（服务商、地址、默认发件标记、状态），下面依次是：
  - 基本信息：发件人名称、侧栏显示名称、设为默认发件账户；
  - 签名：自动附在正文末尾，Wit只读；
  - 同步：状态与「立即同步」、同步范围、本地占用与清理附件缓存；
  - 登录与服务器：登录方式与凭据状态、更新授权码或重新登录、IMAP / SMTP 地址、编辑服务器；
  - 删除账户：只清除本机数据（缓存、草稿、钥匙串凭据），服务器上的邮件不受影响。
- **登录过期**：账户停止同步并显示提示；本地邮件仍可查看和搜索，发件箱里的邮件保留，重新登录后自动发出。
- **通用页**：
  - 外观：主题（跟随系统 / 浅色 / 深色）、界面密度（紧凑默认 / 标准）、字号、邮件正文字号。
  - 语言：界面语言；**阅读语言**（总结、翻译、Wit和Wit的回答，默认跟随界面语言）；**回复语言**（Wit和即时回复起草的邮件，默认跟随来信语言，新邮件按收件人以往来信判断；可改为总是用阅读语言）；不需要翻译的语言。
  - 启动与窗口：开机自动启动、关闭窗口后保持在后台、菜单栏显示未读数。
  - 通知：新邮件通知、只通知智能视图里的邮件、通知里是否显示预览（默认不显示）。
- 布尔设置一律用 Switch 组件。
- 模型服务：列表 + 编辑表单（接口类型、名称、Base URL、API Key 留空保持原值、拉取模型、默认模型、测试连接）。
- 扩展：Skill / 插件 / MCP 三个标签。插件详情列出请求的权限和「不包含」的能力（任何插件都不能发送）；配置表单由插件的 `config` 声明生成，支持 `when` 条件显示。

## 8. v1 范围

**做**：IMAP/SMTP 账户（含 Outlook OAuth）；同步和离线操作；会话视图；文件夹、阅读、搜索（FTS5）；HTML 撰写、回复、全部回复、转发、抄送 / 密送、草稿；托盘常驻；OpenAI 兼容和 Anthropic 兼容模型服务；内置总结、翻译、调整（语气 / 长度 / 校对）skill；即时回复；Wit；`Ask`；Skill 和 Plugin 的加载、配置与权限；两个 Hook；Action、Card、Tool；`--headless` 下的 JSON-RPC 和 MCP（只读，加上草稿）。

**发布节奏**：v1 内部按阶段交付，每个阶段都能独立使用：① 核心邮件（收发、会话、搜索、撰写）→ ② AI（总结、翻译、即时回复、Wit）→ ③ 插件与 MCP → ④ headless 与 JSON-RPC。

**不做**：完整的备份与恢复（v2；v1 只保证迁移安全和拒绝旧版本打开新数据库）；模型服务插件和订阅登录（见 6.2）；Gmail API、JMAP、Outlook Graph 后端；日历和联系人；PGP；插件市场（先手动安装）；规则编辑器 UI；DuckDB 分析。

## 9. 测试

- `core` 的各模块用单元测试覆盖；`sync` 和 `send` 跑在本地 IMAP/SMTP 测试服务器上（go-imap v2 自带服务端实现，可以搭内存服务器）。
- 插件运行时：用一组夹具插件验证权限拦截、超时、崩溃隔离。
- Service 层：对 mygo `Bind()` 和 JSON-RPC 两个出口跑同一套契约测试，确保两边行为一致。
- 安全：用恶意 HTML 和 prompt 注入邮件样本做回归测试；样本覆盖 CSS `url()`、`srcset`、协议相对地址、`<base>`、`<meta http-equiv="refresh">`、表单。
- 同步与发送的故障注入：IMAP 拉取中途断开、UIDVALIDITY 变化、服务器不支持 MOVE / UIDPLUS、SMTP 提交后断线、进程在各步骤之间崩溃。

## 10. 待定

- QuickJS-on-wazero 和 goja 二选一，原型阶段测一下性能和兼容性再定。
- mygo 托盘、通知、开机自启在三个平台上的实际表现，原型阶段验证。
- Microsoft OAuth 应用注册（Outlook / Hotmail 必需），以及 Gmail 以后是否申请 OAuth 审核。
- 深色模式的三层颜色还没有设计稿。
- 跟进 [egoist/mygo#148](https://github.com/egoist/mygo/issues/148)（原生界面嵌入网页视图）：解决后评估邮件正文和撰写编辑器是否改用网页视图。
- 跟进 [egoist/mygo#104](https://github.com/egoist/mygo/pull/104)（Tailwind 色板）：合并后用 mygo 的 `ui/palette` 替换 `internal/palette`。
- Genkit 的 OpenTelemetry 依赖能否在不影响功能的前提下关闭或裁剪。
- 深色模式下邮件正文的显示策略；键盘操作、屏幕阅读器、缩放；日期、时区、复数、从右到左语言的验收标准。
- 尽快注册：域名 `godwit.email`、npm org `godwit`、GitHub org `godwitmail`（2026-10-08 查询时均未被占用）。
