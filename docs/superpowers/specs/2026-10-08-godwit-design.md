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
| 工具链版本 | mise（`mise.toml`）固定 Go 1.27.1、Bun 1.4.2 | mygo 要求 Go 1.27+；Bun 只用于开发和构建期的前端工具链，不进入最终产物 |
| GUI | mygo，webview 模式 | 纯 Go，不需要 cgo，可交叉编译；渲染 HTML 邮件必须用 webview |
| IMAP | `github.com/emersion/go-imap/v2` | 成熟，支持 IDLE 和 CONDSTORE |
| SMTP | `github.com/emersion/go-smtp` | |
| MIME | `github.com/emersion/go-message` | |
| HTML 清洗 | `github.com/microcosm-cc/bluemonday` | |
| 存储 | mygo sqlite 插件的 Go API（`github.com/egoist/mygo/plugins/sqlite`） | 原生 SQLite 3.53，通过 purego 加载，不需要 cgo，已开启 FTS5 |
| 凭据 | `golang.org/x/oauth2`，`github.com/zalando/go-keyring` | 存进系统钥匙串，不需要 cgo |
| 插件运行时 | esbuild（Go 库）把 TS 转成 JS，再用 QuickJS-on-wazero 运行（如 `fastschema/qjs`） | 纯 Go，支持 ES2023 和 async；备选 goja |

不采用的方案：pimalaya（Rust 库，没有 C ABI，接入要么需要 cgo，要么要单独跑一个进程）；DuckDB（面向分析查询，不适合邮件这种频繁读写单条记录的场景，Go 驱动还需要 cgo）；Go `plugin` 包（不支持 Windows，且要求和主程序用同一个 Go 版本编译）。

### 存储相关约束

- 只在 Go 侧使用 sqlite，**不调用** `mygo.Use(sqlite.Plugin)`，前端碰不到数据库。
- 迁移用 `PRAGMA user_version` 加一个按顺序执行的 SQL 列表。
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
| `ai` | 模型服务：内置 OpenAI 兼容与 Anthropic 兼容两种接口，统一的流式 `Complete` 接口 |
| `skills` | 加载 `SKILL.md`，解析 UI 绑定 |
| `plugins` | JS 运行时、manifest、权限、Hook 分发 |
| `events` | 事件总线，推送给 GUI、Hook 和外部订阅者 |

## 4. Service API（v1）

```
Accounts   List / Add / Update / Remove / Test
Folders    List(account)
Messages   List(folder, cursor, limit) / Get(id) / Search(query)
           SetFlags(ids, add, remove) / Move(ids, folder) / Delete(ids)
Compose    SaveDraft / Send(draftID) / GetAttachment(id)
Models     ListServices / AddService / UpdateService / RemoveService / ListModels(service)
AI         RunSkill(name, target) → stream / RunAction(id, target) / Ask(question) → stream
Extensions ListSkills / ListPlugins / SetEnabled / GrantPermission / GetConfig / SetConfig
Events     Subscribe() → stream
```

原则：

1. **本地优先**：所有修改先写进 SQLite 和操作队列，界面立刻响应，`sync` 在后台推送到服务器。
2. **稳定的内部 ID**：不暴露 IMAP UID，因为 UIDVALIDITY 变化后 UID 会全部失效。
3. **`Delete` 只是移到废纸篓**，Service 不提供硬删除。
4. **HTML 在 core 层清洗**，所有出口拿到的都是安全内容。
5. **一套 Service，三个出口**：mygo `Bind()`、JSON-RPC、MCP。MCP 只开放子集，默认只读，`Send` 必须经过用户批准。

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
  godwit-action-output: "panel"           # panel | replace-draft
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
- **Card** 是声明式数据（标题、字段、按钮），由客户端负责渲染，插件不能注入 HTML。按钮动作只能是 `copy`、`open-url`、`run-action` 之一。

### 5.4 配置声明

插件在 `plugin.json` 的 `config` 里声明需要用户填写的字段，Godwit 统一渲染设置表单并在 Go 侧校验，插件不写 UI。设计参考 aio-proxy 的 `ConfigSpec`，但校验在 Go 侧完成，所以用 JSON 声明而不是 Zod。

- 字段类型：`text`、`secret`、`number`、`boolean`、`select`（可选 `allowCustom`，允许手动输入）。
- `when: { key, equals | notEquals }` 控制条件显示，只能引用排在前面的字段。
- `label` 等文案支持多语言：`string | { default, "zh-CN": ..., ... }`。
- `secret` 字段存进系统钥匙串，其余存进配置文件；编辑时 `secret` 留空表示保持原值；界面和日志永不回显 secret。
- 宿主严格校验声明本身：key 唯一、`when` 引用合法、`select` 选项不重复；不合法的插件拒绝加载。

### 5.5 运行时与安全

- 每个插件运行在独立的 QuickJS 实例里，有内存上限和超时限制（`beforeSend` 最多 5 秒）。插件崩溃时自动禁用并通知用户。
- 调用没有声明权限的 `ctx` API 时直接抛错。
- **插件和 MCP 都不能直接发信**，只能创建草稿，发送永远要用户自己点按钮。
- **防 prompt 注入**：邮件内容交给 LLM 时统一标记为不可信数据；agent 在处理邮件内容的过程中，如果想调用有写操作的工具，必须由用户批准。
- 邮件 HTML 放在沙箱 iframe 里显示，禁止脚本，默认不加载远程图片。
- 日志按值动态脱敏：收集所有 secret 和凭据的字符串值，输出前替换，不依赖字段名。

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

## 7. v1 范围

**做**：IMAP/SMTP 账户；同步和离线操作；文件夹和邮件列表、阅读、搜索（FTS5）；撰写、回复、转发、草稿；托盘常驻；OpenAI 兼容和 Anthropic 兼容模型服务；内置翻译、总结、润色这三个 skill；`Ask`；Skill 和 Plugin 的加载、配置与权限；两个 Hook；Action、Card、Tool；`--headless` 下的 JSON-RPC 和 MCP（只读，加上草稿）。

**不做**：模型服务插件和订阅登录（见 6.2）；Gmail API、JMAP、Outlook Graph 后端；日历和联系人；PGP；插件市场（先手动安装）；规则编辑器 UI；DuckDB 分析。

## 8. 测试

- `core` 的各模块用单元测试覆盖；`sync` 和 `send` 跑在本地 IMAP/SMTP 测试服务器上（go-imap v2 自带服务端实现，可以搭内存服务器）。
- 插件运行时：用一组夹具插件验证权限拦截、超时、崩溃隔离。
- Service 层：对 mygo `Bind()` 和 JSON-RPC 两个出口跑同一套契约测试，确保两边行为一致。
- 安全：用恶意 HTML 和 prompt 注入邮件样本做回归测试。

## 9. 待定

- QuickJS-on-wazero 和 goja 二选一，原型阶段测一下性能和兼容性再定。
- mygo 托盘、通知、开机自启在三个平台上的实际表现，原型阶段验证。
- 尽快注册：域名 `godwit.email`、npm org `godwit`、GitHub org `godwitmail`（2026-10-08 查询时均未被占用）。
