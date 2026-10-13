# 邮件标签 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给邮件打标签：预置 + 自建，模型自动分类，用户手动改，自动/手动显示有别；出现在列表、读信页、侧栏、搜索、设置。

**Architecture:** core 新增 `labels`、`message_labels` 两张表和 `messages.labeled_at`；一个后台分类器按 hooks 调度器的写法取收件箱里待分类的邮件，每封一次 `ai.Judge` 调用（每个标签一道 YesNo 题）。app 端新增 `kit.LabelChip`，列表、读信页、侧栏、搜索筛选、设置页都用它。

**Tech Stack:** Go（mygo 原生 UI）、SQLite（`core/store`）、`core/ai` 的 Judge、Remix Icon（`web/icons.go`）、Tailwind v4 色板（`internal/palette`）。

**Spec:** `docs/superpowers/specs/2026-10-13-email-labels-design.md`；设计稿：Godwit UI 画布「标签」页（`LabelChip`、`LabelChips`、`MainLabels*`、`SettingsLabels*` 画板）。

## Global Constraints

- Go 命令一律 `mise exec -- go …`（Go 1.27.1，见 `mise.toml`）。worktree 里先跑一次 `mise run web:install`，否则 `web/icons.go` 的 embed 找不到 `node_modules/remixicon`。
- 只存本地，不同步到服务器；`labels.slug` 建好不改。
- 分类只看收件箱：`m.folder_id IN (SELECT id FROM folders WHERE role = 'inbox')`（与 `viewCond` 的 unified inbox 相同）。
- 模型阈值：概率 ≥ 0.5 为是；拒答当作否；正文取前 4000 字（rune）。
- 手动永远优先：`source='manual'` 的行分类器不出题、不改、不删；回扫也不动。
- 颜色名固定 8 个，顺序：`gray red orange amber green blue violet pink`。
- 芯片：高 20、圆角 `Radius.Sm`(4)、左右内边距 6、图标与文字间距 4、字号 `Text.Sm`、图标 12。手动 = `Fill` 底 + `Fill` 边；自动 = 透明底 + `Edge` 边；文字和图标 = `Text`。
- 所有尺寸用 token（`th.Space(n)`、`t.Radius.*`、`t.Text.*`），不写中间值。
- Godwit 未发布：迁移直接加新的一组，不考虑旧数据兼容。
- 新增界面文案中英两份都要有（`internal/i18n/zh.go`、`en.go`），键名照各任务给出的。

## Review Focus

1. **分类途中标签被删或邮件被删/移出收件箱**：写入不能因外键失败而让整批反复重试——写 `message_labels` 用 `INSERT … SELECT … WHERE EXISTS(label) AND EXISTS(message)`。测试在 Task 5。
2. **分类器与手动修改同时写同一行**：分类器的 upsert 只能覆盖 `source='auto'` 的行（`ON CONFLICT DO UPDATE … WHERE source = 'auto'`），手动行不被覆盖。测试在 Task 5。
3. **用户把标签全删光**：没有题可问时不调用 judge（`ValidateQuestions` 会报 no questions），直接写 `labeled_at`。测试在 Task 5。
4. **只有 HTML 正文或正文极长的邮件**：`body_text` 为空时用 `mail.HTMLToText` 的结果；超过 4000 字截断。测试在 Task 5。
5. **名字只差大小写或首尾空格**（「账单 」与「账单」、`Work` 与 `work`）：视为重名，返回 `ErrLabelExists`。测试在 Task 3。

---

### Task 1: 标签颜色 token

**Files:**
- Modify: `internal/palette/palette.go`、`internal/palette/palette_test.go`
- Modify: `internal/theme/theme.go`
- Test: `internal/theme/theme_test.go`

**Interfaces:**
- Produces: `theme.LabelColor struct{ Dot, Fill, Edge, Text ui.Color }`；`Tokens.Labels map[string]LabelColor`（键为 8 个颜色名）。

- [ ] **Step 1: 补色板。** 从 `web/node_modules/tailwindcss/theme.css` 原样抄 oklch（百分比 L 换成 0–1）到 `palette.go`：red 100/500；orange 100/200/300/500/700/800/950；amber 100/200/700；green 500；blue 100/200/300/700/800/950；violet 100/200/300/500/700/800/950；pink 100/200/300/500/700/800/950。已有的不重复。`palette_test.go` 加三条：`{"orange-500", Orange500, OKLCH{0.705, 0.213, 47.604}}`、`{"violet-700", Violet700, OKLCH{0.491, 0.27, 292.581}}`、`{"pink-950", Pink950, OKLCH{0.284, 0.109, 3.907}}`。
- [ ] **Step 2: 写失败测试** `TestLabelColors`（theme_test.go）：对 `Light()` 与 `Dark()`，`len(t.Labels) == 8`；`Light().Labels["red"] == LabelColor{Dot: p.Red500, Fill: p.Red100, Edge: p.Red200, Text: p.Red700}`（各 `.Color()`）；`Dark().Labels["red"] == {Red500, Red950, Red800, Red300}`；`Light().Labels["gray"].Dot == Light().Primary`、`.Fill == Neutral100`、`.Edge == Neutral200`、`.Text == Neutral700`；`Dark().Labels["gray"] == {Dark().Primary, Neutral800, Neutral700, Neutral300}`。
- [ ] **Step 3: 跑** `mise exec -- go test ./internal/theme -run TestLabelColors`，预期 FAIL（`Labels` 未定义）。
- [ ] **Step 4: 实现。** 在 `status()` 里按亮/暗填 `t.Labels`：浅色 Dot 500 / Fill 100 / Edge 200 / Text 700；深色 Dot 500 / Fill 950 / Edge 800 / Text 300；gray 用 neutral，Dot 取 `t.Primary`（所以 gray 在 `Light()`/`Dark()` 构造后补，Primary 此时已定）。
- [ ] **Step 5: 跑** `mise exec -- go test ./internal/palette ./internal/theme`，预期 PASS。
- [ ] **Step 6: Commit** `feat(theme): label color tokens`

### Task 2: 标签图标

**Files:**
- Modify: `web/icons.go`（`RemixIcons` 与 `//go:embed` 列表）
- Test: `internal/icons/icons_test.go`

**Interfaces:**
- Produces: 图标名 `label-<remix 名>` 共 40 个（顺序见 Step 1），另有 `tag`、`tag-fill`、`grip`。`check`、`plus`、`x` 已存在。

- [ ] **Step 1: 写失败测试** `TestLabelIcons`：对下面 40 个名字逐个 `Get("label-"+n)` 不 panic；`Get("tag")`、`Fill("tag")`、`Get("grip")` 不 panic。40 个名字（也是设计稿网格顺序）：
  `shield-keyhole bill truck plane calendar-event notification-3 newspaper shopping-bag-3 folder-3 money-dollar-circle book-2 graduation-cap pencil quill-pen braces terminal-box music-2 movie-2 tools palette stethoscope first-aid-kit seedling briefcase-4 bar-chart-box run sticky-note scales-3 earth global hammer footprint flask brain heart-3 plant team home-4 key-2 chat-3`
- [ ] **Step 2: 跑** `mise exec -- go test ./internal/icons -run TestLabelIcons`，预期 FAIL（panic: no icon）。
- [ ] **Step 3: 实现。** `RemixIcons` 加 `"label-<n>": "node_modules/remixicon/icons/<目录>/<n>-line.svg"`（目录用 `ls web/node_modules/remixicon/icons/*/<n>-line.svg` 查）；`"tag"` → `Business/price-tag-3-line.svg`、`"tag-fill"` → `price-tag-3-fill.svg`、`"grip"` → `Editor/draggable.svg`；对应 `//go:embed` 行各加一条。
- [ ] **Step 4: 跑** `mise exec -- go test ./internal/icons ./web`，预期 PASS。
- [ ] **Step 5: Commit** `feat(icons): label icons`

### Task 3: 标签的存储与增删改

**Files:**
- Modify: `core/store/schema.go`（`Migrations` 末尾追加一组）
- Create: `core/labels.go`
- Modify: `core/events/bus.go`（加 `LabelsChanged = "labels.changed"`）
- Modify: `core/service.go`（`New` 里迁移后调用 `s.seedLabels(ctx)`）
- Test: `core/labels_test.go`（package core）

**Interfaces:**
- Produces:
  ```go
  var LabelColors = []string{"gray", "red", "orange", "amber", "green", "blue", "violet", "pink"}
  var LabelIcons  []string // Task 2 的 40 个名字，同顺序
  var ErrLabelExists = errors.New("a label with this name exists")
  type Label struct {
      ID int64; Slug, Name, Description, Color, Icon string
      Builtin bool; Position int
      Count, Unread int // active=1 的邮件数、其中未读数（全部文件夹）
  }
  type LabelInput struct{ Name, Description, Color, Icon string }
  func (s *Service) ListLabels(ctx) ([]Label, error)            // 按 position
  func (s *Service) CreateLabel(ctx, in LabelInput) (Label, error) // slug = "u"+id，position 排最后
  func (s *Service) UpdateLabel(ctx, id int64, in LabelInput) (Label, error) // slug 不变
  func (s *Service) DeleteLabel(ctx, id int64) error
  func (s *Service) ReorderLabels(ctx, ids []int64) error       // ids 须恰为全部标签
  ```
  每个写操作成功后 `s.bus.Publish(events.Event{Type: events.LabelsChanged})`。

- [ ] **Step 1: 迁移。** 追加一组，内容即 spec §2 的 SQL，但列名用 `active`（不是 `on`），索引 `message_labels_label ON message_labels(label_id, active)`，外加 `CREATE INDEX messages_labeled ON messages(folder_id, labeled_at)`。
- [ ] **Step 2: 写失败测试**（用 `newMailContextService`）：
  - `TestSeedLabelsChinese`：`Options.SystemLanguage = "zh-CN"` 新建，`ListLabels` 得 7 个，slug 依次 `otp bill order travel calendar notice newsletter`，`[0].Name == "验证码"`、`.Color == "red"`、`.Icon == "shield-keyhole"`、`.Builtin`；关掉重开同一 DataDir 仍是 7 个；删掉一个后重开是 6 个（不再补种）。
  - `TestSeedLabelsEnglish`：`SystemLanguage = ""`，`[0].Name == "Codes"`、`[1].Name == "Bills"`。
  - `TestLabelCRUD`：Create `{Name: " 家人 ", Color: "pink", Icon: "heart-3"}` 得 `Name == "家人"`、`Slug == "u8"`（7 个预置之后的 id）、`Position == 7`；Update 改名为「家庭」后 Slug 仍 `u8`；`Create{Name: "账单 "}` 与 `Create{Name: "BILLS"}`（英文种子下）返回 `ErrLabelExists`；空名、`Color: "teal"`、`Icon: "nope"` 各返回非 nil 错误；`ReorderLabels` 倒序后 `ListLabels` 顺序随之改变；少传一个 id 返回错误；Delete 后其 `message_labels` 行消失（先手插一行再删）。
  - `TestLabelCounts`：两封邮件（一封带 `\Seen`）各插 `active=1` 行，一封插 `active=0`，`Count == 2`、`Unread == 1`。
- [ ] **Step 3: 跑** `mise exec -- go test ./core -run 'TestSeedLabels|TestLabelCRUD|TestLabelCounts'`，预期 FAIL。
- [ ] **Step 4: 实现** `core/labels.go`。种子：`settings` 表 key `labels_seeded` 不存在时插入 7 个预置并写该 key（同一事务）；语言取 `cmp.Or(设置里的 UILanguage, s.opts.SystemLanguage)`，以 `zh` 开头用中文，否则英文。预置表：

  | slug | zh 名 | en 名 | color | icon | zh 说明 | en 说明 |
  |---|---|---|---|---|---|---|
  | otp | 验证码 | Codes | red | shield-keyhole | 登录、注册、支付时收到的一次性验证码或登录链接 | One-time codes or sign-in links for logging in, signing up or paying |
  | bill | 账单 | Bills | amber | bill | 账单、收据、发票和扣款通知 | Bills, receipts, invoices and payment notices |
  | order | 订单与物流 | Orders | orange | truck | 网购订单确认、发货和快递进度 | Order confirmations, shipping and delivery updates |
  | travel | 航班与出行 | Travel | blue | plane | 机票、火车票、酒店预订和值机提醒 | Flights, train tickets, hotel bookings and check-in reminders |
  | calendar | 日程邀请 | Invites | green | calendar-event | 会议邀请、日程变更和活动报名确认 | Meeting invites, schedule changes and event sign-ups |
  | notice | 通知 | Notices | gray | notification-3 | 系统或服务自动发出的提醒，不需要回复 | Automatic notices from systems or services that need no reply |
  | newsletter | 订阅与资讯 | Newsletters | gray | newspaper | 新闻简报、产品更新和营销邮件 | Newsletters, product updates and promotions |

  名称：`strings.TrimSpace` 后非空、≤ 32 rune；重名比较用 `lower(trim(name))`。
- [ ] **Step 5: 跑**同上，预期 PASS；再跑 `mise exec -- go test ./core/...` 确认迁移没破坏其他测试。
- [ ] **Step 6: Commit** `feat(core): labels table, presets and CRUD`

### Task 4: 邮件上的标签、按标签看和搜

**Files:**
- Modify: `core/labels.go`、`core/messages.go`（`viewCond`、`SearchQuery`、`Filtered`、`SearchWith`）、`core/threads.go`（`View`）
- Test: `core/labels_test.go`、`core/search_test.go`

**Interfaces:**
- Consumes: Task 3 的表与 `Label`。
- Produces:
  ```go
  type AppliedLabel struct{ LabelID int64; Auto bool }
  func (s *Service) SetLabel(ctx, messageIDs []int64, labelID int64, on bool) error // 写 source='manual', active=on；发 LabelsChanged
  func (s *Service) MessageLabels(ctx, ids []int64) (map[int64][]AppliedLabel, error)
  func (s *Service) ThreadLabels(ctx, threadIDs []int64) (map[int64][]AppliedLabel, error)
  // View 加 LabelID int64；SearchQuery 加 LabelID int64
  ```
  返回的切片按标签 `position` 排序。会话的规则：会话里任一邮件 `active=1` 即有该标签；只要其中有一行 `manual active=1`，`Auto=false`，否则 `Auto=true`。

- [ ] **Step 1: 写失败测试**：
  - `TestSetLabelManual`：邮件 A 先手插 `auto active=1`（label otp），`SetLabel([A], otp, false)` 后行为 `manual, active=0`，`MessageLabels([A])[A]` 为空；`SetLabel([A,B], bill, true)` 后两封都有 `{bill, Auto:false}`。
  - `TestThreadLabels`：同一会话两封，一封 `auto otp`、另一封 `manual otp`，结果 `[{otp, Auto:false}]`；只有 auto 时 `Auto:true`；顺序随 `position`。
  - `TestLabelView`：`ListMessages(View{LabelID: otp})` 只返回 `active=1` 的那封，`active=0` 的不返回；`View{LabelID: otp, Unified: "inbox"}` 返回错误。
  - `TestSearchWithFilters` 末尾加：`ids(SearchQuery{LabelID: otp}) == []int64{带 otp 的那封}`；`SearchQuery{LabelID: 1}.Filtered() == true`。
- [ ] **Step 2: 跑** `mise exec -- go test ./core -run 'TestSetLabelManual|TestThreadLabels|TestLabelView|TestSearchWithFilters'`，预期 FAIL。
- [ ] **Step 3: 实现。** `viewCond` 新增分支 `v.LabelID != 0 && v.FolderID == 0 && v.Unified == ""` → `EXISTS (SELECT 1 FROM message_labels ml WHERE ml.message_id = m.id AND ml.label_id = ? AND ml.active = 1)`；错误信息里补上 LabelID。`SearchWith` 同条件追加。`SetLabel` 用 `INSERT … ON CONFLICT(message_id, label_id) DO UPDATE SET source='manual', active=excluded.active`。
- [ ] **Step 4: 跑**同上，预期 PASS。
- [ ] **Step 5: Commit** `feat(core): applied labels, label view and search filter`

### Task 5: 后台分类器

**Files:**
- Create: `core/labeler.go`
- Modify: `core/settings.go`（`Settings.AutoLabel bool`，`defaultSettings` 设 true）
- Modify: `core/service.go`（`New` 里 `s.startJanitor()` 之后 `s.startLabeler()`；`syncAccount` 里 `s.notifyHooks()` 旁加 `s.wakeLabeler()`；`Service` 加 `labelWake chan struct{}`（容量 1））
- Test: `core/labeler_test.go`（package core）

**Interfaces:**
- Consumes: `s.resolveJudge(ctx)`、`s.mailForModel(ctx, id)`、`mailHeader`、`ai.Untrusted`、`ai.InjectionQuote`、`mail.HTMLToText`、`hookBackoff`、`s.hookWait`。
- Produces:
  ```go
  var testLabelJudge ai.Judge // 测试用：非 nil 时代替 resolveJudge
  func (s *Service) classifyBatch(ctx) (done int, err error) // 取一批（20 封）分完；没有模型返回 (0, errNoLabelModel)
  func (s *Service) wakeLabeler()
  ```
  每批写完发 `LabelsChanged`。

- [ ] **Step 1: 写失败测试**（假 judge：记录收到的 state 和题目，按题名返回预设概率；可设为返回错误或对某题 `Refused`）：
  - `TestClassifyAppliesAuto`：收件箱邮件 A，假 judge 对 `otp` 给 0.9、`bill` 给 0.2；`classifyBatch` 后 `MessageLabels(A) == [{otp, Auto:true}]`，`labeled_at` 非空；judge 收到 7 道题，题名即 slug，`Instructions` 含「验证码」和它的说明。
  - `TestClassifySkipsManual`：A 已有 `manual otp active=0` 和 `manual bill active=1`；judge 收到的题里没有 `otp`、`bill`；结果 otp 仍 `active=0`，bill 仍 manual。
  - `TestClassifyOnlyInbox`：已发送文件夹（role `sent`）里的邮件不被取。
  - `TestClassifyRefusedIsNo`：`otp` 拒答、`bill` 0.8 → 只有 bill；`labeled_at` 已写。
  - `TestClassifyErrorKeepsPending`：judge 返回错误 → `classifyBatch` 返回错误，`labeled_at` 仍为 NULL，无 `message_labels` 行。
  - `TestClassifyNoModel`：`testLabelJudge = nil` 且未配模型 → 返回 `errNoLabelModel`，`labeled_at` 仍为 NULL。
  - `TestClassifyAutoOff`：`Settings.AutoLabel = false` → 返回 `(0, nil)`，judge 未被调用。
  - Review Focus 1 `TestClassifyVanishedRows`：假 judge 在作答时删掉标签 otp 并删掉邮件 B（同批 A、B）；`classifyBatch` 返回 nil，A 的其余标签写入、无外键错误。
  - Review Focus 2 `TestClassifyKeepsConcurrentManual`：假 judge 作答时对 A 调 `SetLabel([A], otp, false)`，judge 给 otp 0.9；结束后 otp 行仍是 `manual active=0`。
  - Review Focus 3 `TestClassifyNoLabels`：删光全部标签 → judge 未被调用，`labeled_at` 已写。
  - Review Focus 4 `TestClassifyBodyText`：`body_text` 为空、`body_html = "<p>你的验证码是 123456</p>"` → state 含「你的验证码是 123456」；`body_text` 为 5000 个「字」→ state 中正文部分恰为 4000 个「字」。
- [ ] **Step 2: 跑** `mise exec -- go test ./core -run TestClassify`，预期 FAIL。
- [ ] **Step 3: 实现。**
  - 取活：`SELECT m.id FROM messages m WHERE m.labeled_at IS NULL AND <收件箱条件> ORDER BY m.date DESC, m.id DESC LIMIT 20`。
  - 题目：每个没有 manual 行的标签一题，`Kind: ai.YesNo`，`Name: slug`，`Instructions: fmt.Sprintf("Answer yes if this email belongs to the user's label %q. %s", name, desc)`（desc 为空则省略）。没有题 → 只写 `labeled_at`。
  - state：`ai.Untrusted(fmt.Sprintf("message:%d", id), mailHeader(m)+"\n"+body, ai.InjectionQuote(text) != "")`，body 为 `Text`，空则 `mail.HTMLToText(HTML)`，截到 4000 rune。
  - 写入（一个事务）：概率 ≥ 0.5 → `INSERT INTO message_labels SELECT ?, ?, 'auto', 1 WHERE EXISTS(SELECT 1 FROM labels WHERE id=?) AND EXISTS(SELECT 1 FROM messages WHERE id=?) ON CONFLICT DO UPDATE SET active=1 WHERE source='auto'`；否则 `DELETE … WHERE source='auto'`；最后 `UPDATE messages SET labeled_at = unixepoch() WHERE id = ?`。
  - 循环 `startLabeler`：`s.begin` 取 ctx；`for s.hookWait(ctx, wait, s.labelWake)`，有活且成功 → wait 0 继续；无活 → `hookPoll`；出错 → `hookBackoff(连续失败次数)`；`errNoLabelModel` → `hookPoll`。
- [ ] **Step 4: 跑**同上，预期 PASS；`mise exec -- go test ./core/...` 全绿。
- [ ] **Step 5: Commit** `feat(core): classify inbox mail into labels`

### Task 6: 回扫

**Files:**
- Modify: `core/labeler.go`
- Test: `core/labeler_test.go`

**Interfaces:**
- Produces:
  ```go
  func (s *Service) RescanLabels(ctx, days int, all bool) (int, error) // days 0 = 收件箱全部；返回待分类数；唤醒分类器
  func (s *Service) StopRescan(ctx) error                               // 把收件箱里 labeled_at IS NULL 的写成现在
  func (s *Service) PendingLabels(ctx) (int, error)                     // 收件箱里 labeled_at IS NULL 的数量
  ```

- [ ] **Step 1: 写失败测试**：
  - `TestRescanMissing`：三封已分类（A 有 auto otp，B 只有 `manual otp active=0`，C 无行），`RescanLabels(30, false)` 返回 2（B、C），A 的 `labeled_at` 不变。
  - `TestRescanAll`：A 有 auto otp 和 manual bill；`RescanLabels(30, true)` 后 A 的 auto 行被删、manual 行保留，返回 3。
  - `TestRescanRange`：40 天前的一封不被 `RescanLabels(30, …)` 清空；`RescanLabels(0, true)` 清空它。
  - `TestStopRescan`：`RescanLabels` 后 `StopRescan`，`PendingLabels == 0`。
- [ ] **Step 2: 跑** `mise exec -- go test ./core -run 'TestRescan|TestStopRescan'`，预期 FAIL。
- [ ] **Step 3: 实现。** 日期条件 `m.date >= ?`（`now - days*86400`）；「只补」= 没有任何 `active=1` 行的邮件。
- [ ] **Step 4: 跑**同上，预期 PASS。
- [ ] **Step 5: Commit** `feat(core): rescan labels`

### Task 7: kit.LabelChip

**Files:**
- Create: `internal/kit/label.go`
- Test: `internal/kit/kit_test.go`

**Interfaces:**
- Consumes: `theme.LabelColor`（Task 1）、`label-*` 图标（Task 2）。
- Produces:
  ```go
  type LabelChipOpts struct {
      Text, Icon, Tip string   // Icon 不带 "label-" 前缀；Tip 为悬停提示
      Color theme.LabelColor
      Auto bool
      OnRemove func()          // 非 nil 时尾部有 × 按钮，RemoveLabel 为其无障碍标签
      RemoveLabel string
  }
  func LabelChip(c *ui.Context, o LabelChipOpts) ui.Element
  ```

- [ ] **Step 1: 写失败测试**（沿用 kit_test 的渲染方式）：
  - `TestKitLabelChipManual`：高 20；背景为 `Color.Fill`；文字色为 `Color.Text`。
  - `TestKitLabelChipAuto`：背景透明、边框色为 `Color.Edge`，尺寸与手动相同。
  - `TestKitLabelChipRemove`：有 `OnRemove` 时能按 `RemoveLabel` 找到按钮，点击调用回调；悬停芯片出现 `Tip` 文本（同 `TestKitTooltipOnFocus` 的做法）。
- [ ] **Step 2: 跑** `mise exec -- go test ./internal/kit -run TestKitLabelChip`，预期 FAIL。
- [ ] **Step 3: 实现**，尺寸见 Global Constraints；有 × 时右内边距改为 `th.Space(0.5)`，× 为 16×16、图标 10。
- [ ] **Step 4: 跑**同上，预期 PASS。
- [ ] **Step 5: Commit** `feat(kit): label chip`

### Task 8: app 数据接线、列表行、侧栏标签视图

**Files:**
- Modify: `app/app.go`（`a.labels []core.Label`，随账户一起加载；收到 `LabelsChanged` 重新加载并刷新列表）
- Create: `app/labels.go`（芯片工厂和查表）
- Modify: `app/mail_list.go`（`mailItem.Labels []core.AppliedLabel`；`fetchItems` 末尾按会话/邮件补标签；`row`）
- Modify: `app/mail_sidebar.go`（「标签」组）、页面标题处（标签视图显示标签名）
- Modify: `internal/i18n/zh.go`、`en.go`
- Test: `app/labels_test.go`

**Interfaces:**
- Consumes: Task 3/4 的 `ListLabels`、`ThreadLabels`、`MessageLabels`、`View.LabelID`；Task 7 的 `kit.LabelChip`。
- Produces:
  ```go
  func (a *App) label(id int64) (core.Label, bool)
  func (a *App) labelChip(c *ui.Context, l core.AppliedLabel, onRemove func()) ui.Element // 取 a.labels 与 theme.Of(c).Labels，Tip 用 label.tip.auto / label.tip.manual
  ```
- i18n：`label.tip.auto` 由模型标记，点按可修改 / Labeled by the model; click to change；`label.tip.manual` 手动标记 / Labeled by you；`mail.labels` 标签 / Labels；`label.more` +%d / +%d。

- [ ] **Step 1: 写失败测试**（`newHarness` + 往 core 插邮件和 `message_labels`）：
  - `TestListRowChips`：一个会话有 3 个标签 → 行里看到前两个标签名和「+1」；auto 的芯片悬停出现「由模型标记，点按可修改」。
  - `TestCoreLabelsHaveTokens`：`core.LabelColors` 每个都在 `theme.Light().Labels` 与 `theme.Dark().Labels` 里；`core.LabelIcons` 每个 `icons.Get("label-"+n)` 不 panic。
  - `TestSidebarLabels`：只有有邮件的标签出现在「标签」组；点「验证码」后路由为 `View{LabelID: otp}`，列表只剩该会话，顶部标题为「验证码」。
  - `TestLabelsRefreshOnEvent`：后台 `SetLabel` 后（发 `LabelsChanged`），行内芯片随之出现。
- [ ] **Step 2: 跑** `mise exec -- go test ./app -run 'TestListRowChips|TestCoreLabelsHaveTokens|TestSidebarLabels|TestLabelsRefreshOnEvent'`，预期 FAIL。
- [ ] **Step 3: 实现。** 行内芯片放在摘要下方，`Gap(th.Space(1))`，上间距 `th.Space(1)`，最多 2 个加 `label.more`。侧栏组放在「已标星」之后、账户之前，组标题样式同 settings 的 `group`；每行用 `kit.SidebarItem`，图标 `label-<icon>` 着 `Labels[color].Text` 色，右侧未读数。
- [ ] **Step 4: 跑**同上，预期 PASS。
- [ ] **Step 5: Commit** `feat(app): label chips in the list and label views`

### Task 9: 读信页的标签行与菜单

**Files:**
- Modify: `app/reader.go`（`view` 里主题下面一行）
- Create: `app/reader_labels.go`
- Modify: `internal/i18n/zh.go`、`en.go`
- Test: `app/reader_labels_test.go`

**Interfaces:**
- Consumes: `ThreadLabels`、`SetLabel`、`CreateLabel`（Task 3/4）、`a.labelChip`（Task 8）。
- Produces: `func (r *reader) labelRow(c *ui.Context)`；会话的邮件 id 取 `r.msgs`。
- i18n：`reader.labels.add` 标签 / Label；`reader.labels.search` 搜索或新建标签 / Search or create a label；`reader.labels.create` 新建「%s」 / Create “%s”；`reader.labels.manage` 管理标签… / Manage labels…；`reader.labels.auto` 模型标记 / By model；`reader.labels.remove` 去掉标签「%s」 / Remove label “%s”。

- [ ] **Step 1: 写失败测试**：
  - `TestReaderLabelRow`：会话有 auto otp → 主题下见「验证码」芯片和「标签」按钮。
  - `TestReaderLabelRemove`：点 `去掉标签「验证码」` → core 中该会话所有邮件 otp 为 `manual active=0`，芯片消失。
  - `TestReaderLabelMenu`：点「标签」→ 菜单列出全部 7 个预置；otp 一项带「模型标记」；点「账单」→ 会话邮件 bill 为 `manual active=1`。
  - `TestReaderLabelCreate`：菜单搜索框输入「家人」→ 出现「新建「家人」」，点它 → 新标签被创建（颜色 gray、图标 folder-3）并打到会话上；输入「账单」不出现新建项。
  - 「管理标签…」导航到 `Route{Page: PageSettings, Settings: "labels"}`。
- [ ] **Step 2: 跑** `mise exec -- go test ./app -run TestReaderLabel`，预期 FAIL。
- [ ] **Step 3: 实现。** 行：`Wrap()`、`Gap(th.Space(1))`，与主题间距 `th.Space(2)`；「标签」按钮高 20、边框 `t.Border`、`Radius.Sm`、muted 文字；菜单用 `kit.Popover`，宽 240。
- [ ] **Step 4: 跑**同上，预期 PASS。
- [ ] **Step 5: Commit** `feat(app): label row and menu in the reader`

### Task 10: 搜索的标签筛选

**Files:**
- Modify: `app/mail_filters.go`（`searchFilters.labelID int64`；`searchQuery` 带上；`filterChips` 加一项）
- Modify: `internal/i18n/zh.go`、`en.go`（`mail.filter.label` 标签 / Label；`mail.filter.label.any` 全部标签 / Any label）
- Test: `app/mail_search_test.go`

- [ ] **Step 1: 写失败测试** `TestSearchLabelFilter`：打开搜索，点「标签」芯片，选「验证码」→ 芯片文字变为「验证码」并显示该标签图标，结果只剩带 otp 的邮件；选「全部标签」→ 恢复。
- [ ] **Step 2: 跑** `mise exec -- go test ./app -run TestSearchLabelFilter`，预期 FAIL。
- [ ] **Step 3: 实现**，照现有 `filterChip`/`filterItem` 写法；菜单项前画 `label-<icon>` 着色图标。
- [ ] **Step 4: 跑**同上，预期 PASS。
- [ ] **Step 5: Commit** `feat(app): filter search by label`

### Task 11: 设置 › 标签

**Files:**
- Modify: `app/settings.go`（`appSections` 在 `models` 后加 `labels`；`sectionIcons["labels"] = "tag"`；`Content` 加分支）
- Create: `app/settings_labels.go`
- Modify: `app/settings_search.go`（`settingsIndex["labels"]`）
- Modify: `internal/i18n/zh.go`、`en.go`
- Test: `app/settings_labels_test.go`

**Interfaces:**
- Consumes: Task 3 的 CRUD、Task 6 的 `RescanLabels`/`StopRescan`/`PendingLabels`、Task 5 的 `Settings.AutoLabel`、`a.changeSettings`、Task 7/8 的芯片。
- Produces: `func (p *settingsPage) labelsView(c *ui.Context)`。
- i18n（zh / en）：
  `settings.labels` 标签 / Labels；`settings.labels.sub` 模型会给新邮件自动打标签，你也可以随时手动改。手动改过的不会被覆盖。 / The model labels new mail for you, and you can change labels any time. Your changes are never overwritten.；`settings.labels.new` 新建标签 / New label；`settings.labels.list` 标签 / Labels；`settings.labels.list.foot` 拖动调整顺序，侧栏和菜单按这个顺序排列 / Drag to reorder; the sidebar and menus follow this order；`settings.labels.builtin` 预置 / Built-in；`settings.labels.count` %d 封 / %d；`settings.labels.no_desc` 没有说明，只按名称判断 / No description; judged by name only；`settings.labels.edit` 编辑「%s」 / Edit “%s”；`settings.labels.auto` 自动标记 / Auto-labeling；`settings.labels.auto.on` 自动给新邮件打标签 / Label new mail automatically；`settings.labels.auto.on.help` 收到新邮件后在后台分类，只看收件箱 / Sorts new mail in the background, Inbox only；`settings.labels.model` 判断模型 / Judge model；`settings.labels.model.help` 没有判断模型时由生成模型代答，会慢一些 / Without a judge model the generation model answers, more slowly；`settings.labels.model.value` 跟随「模型服务」 / From Model services；`settings.labels.model.go` 去设置 / Open；`settings.labels.rescan` 回扫已有邮件 / Rescan existing mail；`settings.labels.rescan.sub` 新建或改了标签后，按现在的标签重新给收件箱里的邮件分类 / After adding or changing labels, sort Inbox mail again with the current labels；`settings.labels.rescan.range` 范围 / Range；`settings.labels.rescan.days` 最近 %d 天 / Last %d days；`settings.labels.rescan.all_range` 同步范围内全部 / Everything synced；`settings.labels.rescan.mode` 方式 / Mode；`settings.labels.rescan.missing` 只补没有标签的 / Unlabeled only；`settings.labels.rescan.missing.help` 已经有标签的邮件不动 / Mail with labels stays as is；`settings.labels.rescan.redo` 全部重新判断 / Judge all again；`settings.labels.rescan.redo.help` 模型打的标签重新判断，手动改过的保留 / Model labels are judged again; yours are kept；`settings.labels.rescan.start` 开始回扫 / Rescan；`settings.labels.rescan.running` 正在回扫 · 还剩 %d 封 / Rescanning · %d left；`settings.labels.rescan.stop` 停止 / Stop；`settings.labels.dialog.new` 新建标签 / New label；`settings.labels.dialog.edit` 编辑标签 / Edit label；`settings.labels.dialog.sub` 模型根据名称和说明判断哪些邮件该打这个标签。 / The model uses the name and description to decide which mail gets this label.；`settings.labels.preview.manual` 手动标记 / By you；`settings.labels.preview.auto` 模型标记 / By model；`settings.labels.name` 名称 / Name；`settings.labels.name.placeholder` 比如：家人 / e.g. Family；`settings.labels.name.exists` 已经有同名的标签 / A label with this name exists；`settings.labels.desc` 说明（可选） / Description (optional)；`settings.labels.desc.placeholder` 写清楚什么样的邮件算，比如：爸妈和孩子学校发来的邮件 / Say which mail counts, e.g. mail from my parents and my kid's school；`settings.labels.color` 颜色 / Color；`settings.labels.icon` 图标 / Icon；`settings.labels.delete` 删除标签 / Delete label；`settings.labels.delete.confirm` 删除「%s」？ / Delete “%s”?；`settings.labels.delete.confirm.help` 已打上的 %d 封邮件会去掉这个标签。 / It will be removed from %d messages.；`settings.labels.create` 创建 / Create；颜色名 `label.color.gray` 灰 / Gray … `label.color.pink` 粉 / Pink（8 个）。

- [ ] **Step 1: 写失败测试**：
  - `TestSettingsLabelsList`：`h.openSettings("labels")` 见 7 行预置，每行有「预置」和「编辑「验证码」」按钮；侧栏「标签」位于「模型服务」之后。
  - `TestSettingsLabelsCreate`：点「新建标签」，输入「家人」，选粉色、`heart-3`，点「创建」→ `ListLabels` 末尾为 `{家人, pink, heart-3}`；输入「账单」时「创建」不可用并显示「已经有同名的标签」。
  - `TestSettingsLabelsEdit`：编辑「验证码」改说明后保存 → core 中说明更新、slug 仍为 `otp`。
  - `TestSettingsLabelsDelete`：编辑 → 「删除标签」→ 确认框文本含「删除「验证码」？」和邮件数 → 确认后行消失。
  - `TestSettingsLabelsReorder`：把第 1 行拖到第 3 行位置 → `ListLabels` 顺序为 `bill order otp …`。
  - `TestSettingsLabelsAutoSwitch`：关掉开关 → `a.settings.AutoLabel == false`。
  - `TestSettingsLabelsRescan`：选「全部重新判断」点「开始回扫」→ 调用 `RescanLabels(30, true)`（范围默认最近 30 天）；有待分类时显示「正在回扫 · 还剩 N 封」和「停止」，点「停止」后 `PendingLabels == 0`。
  - `TestSettingsIndexCoversRows` 照常通过（`settingsIndex["labels"]` 覆盖本页所有行标签键）。
- [ ] **Step 2: 跑** `mise exec -- go test ./app -run 'TestSettingsLabels|TestSettingsIndexCoversRows'`，预期 FAIL。
- [ ] **Step 3: 实现。** 布局照设计稿 `SettingsLabels`：`pageHeading`（右侧「新建标签」）+ 三个 `settingsSection`。列表行：`grip` 图标（`ui.Draggable`，`Axis: ui.Vertical`，松手时按行高四舍五入算新位置再 `ReorderLabels`）、宽 `th.Space(28)` 的芯片列、说明（单行省略）、`kit.Badge` 预置、数量、编辑图标按钮。弹层 `kit.Dialog` md：预览两枚芯片（`t.Sidebar` 底、`Radius.Lg`）、`kit.Field`+`kit.Input`/`kit.TextArea`、8 个 24×24 圆形色点（选中描 2 px 背景色 + 2 px 圆点色外环）、图标网格 8 列、格高 36、选中格用该色 `Fill` 底和 `Text` 色。回扫区：`settingSelect`（7/30/90 天、全部，默认 30）、分段选择（`t.Hover` 轨道）、开始按钮；进行中每 2 秒读 `PendingLabels`，为 0 时回到空闲。
- [ ] **Step 4: 跑**同上，预期 PASS；再跑 `mise exec -- go test ./...` 全绿。
- [ ] **Step 5: Commit** `feat(app): labels settings`

---

**设计系统同步（不在本仓库）：** 把 Task 1 的 8 组颜色以 `label-<color>-dot/fill/edge/text` 命名加入 Godwit design system 画布的 `tokens.json`，亮暗两套值与 `theme.Labels` 一致。
