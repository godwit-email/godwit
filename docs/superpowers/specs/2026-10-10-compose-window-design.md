# 撰写窗口：网页实现的设计

日期：2026-10-10 · 状态：待评审 · 上位文档：`2026-10-08-godwit-design.md`

## 1. 目标与范围

把撰写从主窗口里的一个页面（`PageCompose`，自研编辑器）改成**每封草稿一个独立窗口**，窗口整体用网页实现：React + shadcn/ui + TanStack Query，正文编辑器用 Plate。主窗口保持 mygo 原生界面。

**为什么**：自研编辑器在中文输入、粘贴、拼写检查、撤销、表格图片、无障碍等方面很难做到成熟编辑器的水准；独立窗口能同时写多封、对照原邮件。

**做**：独立撰写窗口；网页版的发件人、收件人、主题、附件、格式工具栏、正文、引用与签名、自动保存与冲突处理、发送（含插件警告与拦截）、丢弃；Wit 撰写面板与正文内的修订（逐词修订、整体重写对比、已应用可撤销、⌘K 改写选区）；主题与原生界面一致；前端工程、构建、类型化的后台调用、测试。

**不做（本期）**：阅读区改成网页（另立项目）；联系人自动补全（core 还没有联系人能力）；新增格式（标题、下划线、颜色、图片等，保持现有能力：粗体、斜体、链接、有序/无序列表、引用）；拖放附件（可作为后续小项）。

**行为对等**：现有撰写的全部行为都要保留，以 §9 的对照表为准，旧测试覆盖的每条行为在新实现里都有对应测试。

## 2. 关键决定

| 决定 | 选择 | 理由 |
|---|---|---|
| 窗口形态 | 每封草稿一个窗口；同一草稿再次打开时聚焦已有窗口 | 可同时写多封、对照原邮件；和 Mail.app、Outlook 一致 |
| 窗口实现 | mygo 现有的网页窗口（`WindowOptions{URL}`），整窗是网页 | 弹出层与正文同属一个网页，没有「网页压住原生弹出层」的问题；不依赖 mygo PR #200，继续用正式版 mygo |
| 前端栈 | React 19、TypeScript、Vite、shadcn/ui（Tailwind v4）、TanStack Query | 和后续网页界面统一；我们的 token 本来就照 shadcn 定义 |
| 编辑器 | Plate（Slate） | 组件即 shadcn 风格；自带「建议」（修订）与 AI 菜单，对应 Wit 的修订与 ⌘K；Safari 内核下中文输入已实测正常 |
| 包管理与构建 | Bun；`web/` 目录；产物 `web/dist` 用 `mygo.SetFrontend` 嵌入 | 本机已有；mygo 文档推荐 |
| 后台调用 | `mygo.Bind` 绑定一个窄的 `Compose` 服务；`mygo generate` 生成 TypeScript 客户端并提交 | 类型两端都检查 |
| 草稿内容的权威 | Go 端（core 草稿）。网页只是编辑视图 | 沿用现有的保存、版本、冲突、Wit 审批机制 |

## 3. 窗口与生命周期

- **打开**：现在所有导航到 `PageCompose` 的地方（新建、回复、全部回复、转发、快速回复、插件动作返回草稿、mailto、草稿列表、菜单与托盘的「写邮件」⌘N）改为调用 `App.OpenCompose(draftID)`：已有该草稿的窗口就聚焦，否则新建窗口。`PageCompose` 路由与主窗口里的撰写视图删除。
- **窗口**：标题为主题（空时「新邮件」）；默认 860×720，最小 640×480，位置和大小按 `StateKey: "compose"` 记忆；新窗口相对上一个错开。macOS 用透明标题栏，网页自己画顶栏（`--app-region: drag`），与主窗口观感一致。
- **关闭**（⌘W 或红灯）：先让网页提交未保存的输入并等保存完成；保存失败时询问「有修改没有保存」（关闭 / 返回）。发送成功或丢弃后窗口自动关闭。
- **退出应用**：沿用 `flushDrafts` 的语义，等所有撰写窗口保存完；有失败时询问。
- **窗口与草稿的绑定**：Go 端维护 `window ID → draftID`。草稿被删除（别处丢弃）时关闭其窗口。

## 4. 网页与 Go 的接口

只绑定一个服务 `Compose`。**每个方法都只作用于调用者窗口绑定的草稿**：用 `mygo.CallerWindow(ctx)` 查到草稿，页面不传 draftID，无法越权操作别的草稿或别的数据。

| 方法（TS 名） | 作用 | 对应 core |
|---|---|---|
| `load(): ComposeState` | 账户列表、草稿字段、可编辑正文 HTML、引用与签名（纯文本）、附件、revision、设置（是否有模型、语言） | `GetDraft`、`SplitDraft`、`ListAccounts` |
| `save(edit): SaveResult` | 保存字段与正文，带 revision；返回新 revision 或冲突 | `SaveDraft`（`ErrStaleDraft` → 冲突） |
| `resolveConflict(keepMine)` | 保留我的版本 / 使用最新版本 | 现有 `KeepMine` / `useLatest` 语义 |
| `setAccount(id)` | 切换发件账户，换签名 | `SetDraftAccount` |
| `addAttachments()` | 弹原生多选文件对话框（挂在本窗口），逐个添加 | `plat.OpenFiles`、`AddAttachment` |
| `removeAttachment(id)` | 删除附件 | `RemoveAttachment` |
| `send(digest?)` | 发送；带 digest 即「仍然发送」 | `Send` / `SendAcknowledged` |
| `discard()` | 丢弃草稿 | `DeleteDraft` |
| `assist(input)`、`skill(name, lang?)`、`rewriteSelection(sel, instruction)` | Wit；返回 `Channel<WitEvent>` 流式事件 | `Assist`、`AssistSkill`、`RewriteSelection` |
| `decide(approvalID, accept)`、`applyEdited(approvalID, html)`、`undoAssist()`、`stop()` | 审批与撤销 | `ResolveApproval`、`ApplyEditedProposal`、`UndoLastAssist`、`CancelRun` |
| `openLink(url)` | 用户确认后用系统浏览器打开 | 现有链接确认 |

**事件（Go → 网页）**：`draftChanged`（Wit 应用或别处保存后，网页按现有 reload 规则决定是否重载）、`theme`（主题、字号、密度变化）、`closing`（请网页立即提交输入）。

**保存规则**照搬 `draftsaver`：停止输入 1 秒后保存；输入法合成中不保存；写入按草稿串行；关闭和发送前先 flush；发送时冻结编辑并提示「正在发送，暂不能修改」。这些放在网页端的一个小状态机里，由 Go 端的 revision 校验兜底。

## 5. 编辑器

- **Plate 插件**：段落、粗体、斜体、链接、有序/无序列表、引用块，和 `FromComposeHTML` 保留的格式一一对应；不启用其他格式。
- **HTML 进出**：载入时用 Plate 的 HTML 反序列化（在 `DOMParser` 生成的惰性文档上进行，不执行任何脚本）；保存时序列化为 HTML 交给 Go，Go 用 `SanitizeCompose` 清洗后存。粘贴走同一条反序列化路径。
- **引用与签名**：和现在一样不进编辑器，作为只读纯文本显示在正文下方（引用默认折叠）。纯文本显示意味着不可信的原邮件 HTML 永远不会进入页面 DOM。
- **快捷键**：⌘B / ⌘I / ⌘K（有选区且配置了模型时打开 Wit 改写，和现在一样；否则打开插入链接）、⌘Enter 发送、⌘W 关闭、Esc 关闭弹出层。撤销、复制、粘贴走系统「编辑」菜单（整窗网页无 PR #200 的问题，仍需实测）。
- **工具栏**：shadcn 的 ToggleGroup，与现有工具栏同样的按钮；链接用 Popover 输入。

## 6. Wit

- **面板**：网页里的侧栏（有模型服务时显示），对应现有面板：上下文标签、调整 ▾、翻译 ▾、自由输入、流式回答、审批卡片。
- **修订展示**：用 Plate 的「建议」能力在正文里显示插入与删除。
  - 是「逐词修订」还是「整体重写」**由 Go 端判定**，沿用现有 `newProposal` 的规则（`worddiff`，块类型不变且改动比例 ≤ 0.5 为逐词），结果随审批一起发给网页，保证和现在一致。
  - 逐词修订：每处可单独接受或拒绝；全部接受 / 全部拒绝走 `decide`，部分接受时网页生成结果 HTML 走 `applyEdited`。
  - 整体重写：正文只读显示新版本，可切换「对比原文」，整体接受或拒绝。
- **⌘K 改写选区**：core 现在用「草稿 HTML 在某个 revision 下的字节偏移」定位选区。网页先保存，再把**带两个选区标记的正文 HTML** 发给 Go；Go 去掉标记、清洗后算出偏移，调用 `RewriteSelection`。标记用不会被清洗规则保留的私有元素，计算在 Go 端完成，网页不需要理解字节偏移。
- **运行中锁定**、**已应用可撤销**、**重载规则**（新读覆盖旧读、本地有编辑时保留本地）都照搬现有逻辑。

## 7. 主题

- Go 端从 `internal/theme` 生成 shadcn 的 CSS 变量（`--background`、`--foreground`、`--muted-foreground`、`--border`、`--input`、`--ring`、`--primary`、`--radius-*`、阴影档位等），浅色与深色各一套，加上字号与密度。
- `load()` 带上当前一套，`theme` 事件推送变化。外观跟随应用设置（不是只跟随系统），与主窗口始终一致。
- 字体与原生一致：Geist + 系统中文字体。

## 8. 安全

整窗网页是**可信页面**：它能调用绑定的 Go 方法。所以页面里绝不能执行来自邮件的内容。

1. **CSP**：只允许加载打包进来的脚本和样式（`script-src 'self'`，不允许内联脚本）；`img-src` 只允许 `data:`、`blob:` 和本地附件协议；不允许任何远程请求。
2. **不可信 HTML 不进 DOM**：引用的原邮件以纯文本显示；正文与粘贴内容只经过反序列化变成 Plate 的数据结构，渲染由 React 完成（自动转义），不使用 `dangerouslySetInnerHTML`。
3. **不跳转**：页面内导航一律拦截；正文链接只能经 `openLink` 在确认后用系统浏览器打开；`window.open` 拒绝。
4. **最小接口**：只绑定 `Compose`，且每个方法只作用于调用者窗口的草稿。主窗口是原生界面，没有页面。将来阅读区如果嵌入网页显示邮件，必须用不可信的嵌入网页视图（无绑定），不能复用这套可信页面。
5. **发送仍需亲手点击**：Wit 不能发送，`send` 只由页面的发送按钮或 ⌘Enter 触发。

## 9. 行为对照与测试

**Go 端**（`app` 包，core 测试环境）：`Compose` 服务按窗口绑定草稿、越权调用被拒；保存与冲突（保留我的 / 使用最新 / 读取失败重试）；切换账户换签名；附件（过大报错、部分失败不误报冲突）；发送（成功、插件警告后仍然发送、拦截、发送中拒绝修改、发送时 flush 最新输入）；丢弃（失败后继续保存）；关闭与退出（flush、失败时询问、等待进行中的写入）；⌘K 选区标记换算偏移；Wit 审批（全部接受、部分接受、整体重写、撤销、重载规则）。现有 `app/compose_test.go`、`app/compose_wit_test.go` 里的每个测试名都要在新测试中有对应项，计划里列出映射表。

**网页端**（Vitest + Testing Library，jsdom）：字段与收件人标签（逗号、回车、失焦提交，重复跳过，无效地址标红）、保存状态机（1 秒防抖、合成中不保存、flush）、工具栏、修订的接受与拒绝生成正确 HTML、⌘K 选区标记、主题变量应用。

**端到端**（macOS，可选的 CI 任务）：用真实窗口和 `mygo.EvalAs` 驱动：打开、输入中文、粘贴、⌘Z、发送。你在 Safari 里试过的输入法边界情况写成可重复的检查清单，升级 Plate 时人工过一遍。

## 10. 工程与构建

- **目录**：`web/`（Vite + React + TS），`web/src/mygo.ts` 为生成的客户端（提交入库），`web/dist` 为构建产物（不入库，保留占位文件让 `go build` 在未构建前端时也能编译，此时撰写窗口显示「前端未构建」）。
- **命令**：`mise` 任务 `web:dev`、`web:build`、`web:test`、`generate`（`mygo generate` 写客户端）；本地开发 `mygo dev` 或 `devUrl` 指向 Vite 开发服务器以便热更新。
- **CI**：三个平台的 Go 任务前先 `bun install && bun run build`；新增网页任务（类型检查、lint、Vitest）；检查生成的客户端与 Go 代码一致。
- **体积**：React、Plate、shadcn 打包后约 1 MB 级，嵌入二进制可以接受。

## 11. 交付拆分

按惯例每步一个 PR，叠在 `feat/ui-kit` 之后：

1. **前端工程与窗口骨架**：`web/` 工程、主题变量生成、`Compose` 服务的 `load` / `save`、独立窗口打开与关闭、纯文本正文；主窗口的入口改为打开窗口。
2. **完整撰写**：Plate 正文与工具栏、收件人与账户、附件、引用与签名、发送与插件警告、丢弃、冲突处理、关闭与退出；删除旧的撰写页与 `internal/editor`。
3. **Wit**：面板、修订展示、⌘K、撤销与重载。
4. **端到端与收尾**：macOS 端到端检查、文档、设计稿同步撰写窗口。

旧撰写页在第 2 步删除；第 1 步结束时新旧暂时并存（旧页可用、新窗口只有纯文本），第 2 步一次切换。

## 12. 风险与待验证

- **WebView2（Windows）与 WebKitGTK（Linux）上的 Plate 与输入法**：没有 Windows 机器实测，依赖 CI 的端到端检查和社区反馈；Plate 在 Chromium 上最成熟，风险较低。
- **系统「编辑」菜单在整窗网页里是否可用**：探针里的问题出在嵌入网页视图；整窗网页走 mygo 的常规路径，第 1 步先实测。
- **Plate 与 React 版本升级**：锁定版本，升级时过输入法检查清单。
- **生成客户端需要能运行到 `App.Run`**：`main` 在 `App.Run` 之前不能做迁移数据库等副作用，第 1 步核对 `cmd/godwit`。
