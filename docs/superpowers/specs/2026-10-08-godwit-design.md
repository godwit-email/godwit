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
Accounts   List / Add / Update / Remove / Test / Reauthorize / SyncNow
           （Account 含发件人名称、侧栏显示名称、签名、是否默认发件、同步范围、登录方式与状态 ok / reauth_required / error）
Settings   Get / Set（外观、界面语言、阅读语言、回复语言、免翻译语言、启动与窗口、通知、即时回复开关）
Folders    List(account)
Messages   List(folder, cursor, limit) / Get(id) / Search(query)
           SetFlags(ids, add, remove) / Move(ids, folder) / Delete(ids)
Compose    SaveDraft / Send(draftID) / GetAttachment(id)
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

## 7. 界面与交互

设计稿：[Godwit UI 画布](https://claude.ai/artifact/LESLwyFigHSD8Yfvfimruq)（以画布为准，这里记录决定和规则）。

### 7.1 窗口与布局

- 风格：shadcn Nova（紧凑）+ neutral 灰阶；字体 Geist / Geist Mono，中文 Noto Sans SC。实现时前端直接使用 shadcn 组件。
- 布局参考 Codex，三层颜色：窗口底色（顶栏 + 左侧图标栏）`#ECECEC` → 侧栏 `#F9F9F9`（左上圆角）→ 内容区白色，侧栏与内容区连成一块面板，贴住窗口右边和下边。
- 左侧图标栏：邮件、Wit；设置固定在底部。扩展只从「设置 › 扩展」进入。没有用户体系，不放头像。
- 顶栏：系统窗口按钮 + 收起侧栏、后退、前进；内容区上方显示当前标题（如「全部收件箱 · 16 封未读」）和同步状态。
- 系统窗口按钮（mygo `TitleBarHiddenInset` / `TitleBarHeight` / `--mygo-titlebar-inset-*` / `--app-region: drag`）：
  - macOS：红绿灯嵌在左上角；背景 `VibrancySidebar`。
  - Windows 11：右上角最小化、最大化、关闭，标题栏高 40；窗口圆角 8px；背景 `VibrancyMica`。
  - Linux：按钮由 GTK 绘制，位置取决于桌面设置，只能靠 inset 变量让位。

### 7.2 邮件

- 侧栏是账户分组树：顶部是跨账户视图（全部收件箱、已标星、智能视图），下面每个账户可折叠，展开后是各自的文件夹，最后是「添加账户」。只有一个账户时不分组。
- 写邮件的发件人默认用当前选中的账户；在跨账户视图里写邮件用默认账户。
- 阅读区自上而下：标题与发件人 → AI 总结（可开关，只显示要点，不带操作按钮）→ 插件 Card → 远程图片拦截提示 → 正文 → 附件 → 即时回复。
- 翻译不单独占区域：正文直接替换为译文，上方一行「已翻译 · 显示原文」。
- 即时回复：在收到的邮件底部给出最多 3 条完整回复草稿（只显示一行预览，Tab 切换），选中后进入撰写页继续改。会额外消耗模型额度，设置里可以关闭。

### 7.3 撰写

- 草稿和发信是同一个撰写页，内容自动保存为草稿；发送后草稿变为发件箱里的操作。
- 撰写区直接铺满内容区（不套卡片）；编辑区工具栏只有格式和附件。
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

面板元素统一：用户消息、工具调用记录（可折叠）、「待确认」卡片（接受 / 拒绝）、引用邮件和模型选择、底部输入框。疑似 prompt 注入的来源会被标出，并说明「只作为数据处理」。

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

**做**：IMAP/SMTP 账户；同步和离线操作；文件夹和邮件列表、阅读、搜索（FTS5）；撰写、回复、转发、草稿；托盘常驻；OpenAI 兼容和 Anthropic 兼容模型服务；内置总结、翻译、调整（语气 / 长度 / 校对）skill；即时回复；Wit；`Ask`；Skill 和 Plugin 的加载、配置与权限；两个 Hook；Action、Card、Tool；`--headless` 下的 JSON-RPC 和 MCP（只读，加上草稿）。

**不做**：模型服务插件和订阅登录（见 6.2）；Gmail API、JMAP、Outlook Graph 后端；日历和联系人；PGP；插件市场（先手动安装）；规则编辑器 UI；DuckDB 分析。

## 9. 测试

- `core` 的各模块用单元测试覆盖；`sync` 和 `send` 跑在本地 IMAP/SMTP 测试服务器上（go-imap v2 自带服务端实现，可以搭内存服务器）。
- 插件运行时：用一组夹具插件验证权限拦截、超时、崩溃隔离。
- Service 层：对 mygo `Bind()` 和 JSON-RPC 两个出口跑同一套契约测试，确保两边行为一致。
- 安全：用恶意 HTML 和 prompt 注入邮件样本做回归测试。

## 10. 待定

- QuickJS-on-wazero 和 goja 二选一，原型阶段测一下性能和兼容性再定。
- mygo 托盘、通知、开机自启在三个平台上的实际表现，原型阶段验证。
- Microsoft OAuth 应用注册（Outlook / Hotmail 必需），以及 Gmail 以后是否申请 OAuth 审核。
- 深色模式的三层颜色还没有设计稿。
- 尽快注册：域名 `godwit.email`、npm org `godwit`、GitHub org `godwitmail`（2026-10-08 查询时均未被占用）。
