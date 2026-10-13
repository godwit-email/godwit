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
- `messages.labeled_at` 三种值：`NULL` = 新邮件待自动分类（只在 `Settings.AutoLabel` 开时取）；`0` = 回扫目标（不论开关都取）；`> 0` = 已分类的时间。回扫的进度和停止只看 `labeled_at = 0`。
- 多条语句的写操作一律走 `s.db.Transaction(ctx, stmts)`（见 `core/accounts.go:188`）；标签的创建、改名、删除、排序另持 `s.labelsMu`，重名检查与写入在同一把锁内。
- 名称重名比较在 Go 里做：`strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))`（SQLite 没有 ICU，`lower()` 不处理非 ASCII）。

## Review Focus

1. **分类途中标签被删或邮件被删/移出收件箱**：写入不能因外键失败而让整批反复重试——写 `message_labels` 用 `INSERT … SELECT … WHERE EXISTS(label) AND EXISTS(message)`。测试在 Task 5。
2. **分类器与手动修改同时写同一行**：分类器的 upsert 只能覆盖 `source='auto'` 的行（`ON CONFLICT DO UPDATE … WHERE source = 'auto'`），手动行不被覆盖。测试在 Task 5。
3. **用户把标签全删光**：没有题可问时不调用 judge（`ValidateQuestions` 会报 no questions），直接写 `labeled_at`。测试在 Task 5。
4. **只有 HTML 正文或正文极长的邮件**：`body_text` 为空时用 `mail.HTMLToText` 的结果；超过 4000 字截断。测试在 Task 5。
5. **名字只差大小写或首尾空格**（「账单 」与「账单」、`Work` 与 `work`、`Équipe` 与 `équipe`）：视为重名，返回 `ErrLabelExists`。测试在 Task 3。
6. **模型作答期间，邮件被停止回扫、移出收件箱或重新回扫**：作答前读到的 `labeled_at` 与收件箱条件在提交时重新校验，不符就丢弃这次结果。测试在 Task 5。
7. **读信页从搜索或平铺列表进入**（`r.msgs` 只剩一封）：标签操作仍作用于整个会话的全部邮件。测试在 Task 9。

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
- Modify: `core/service.go`（`New` 里迁移后调用 `s.seedLabels(ctx)`；`Service` 加 `labelsMu sync.Mutex`；`Options` 加未导出字段 `noLabeler bool`，供 Task 5 的包内测试关掉后台分类器）
- Modify: `core/mailcontext_test.go`（新增 `newLabelService(t, dir, lang string) *Service`：用给定 DataDir 与 `SystemLanguage`、`noLabeler: true` 调 `New`，并插入与 `newMailContextService` 相同的账户和收件箱；`dir` 为空用 `t.TempDir()`）
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
- [ ] **Step 2: 写失败测试**（用 `newLabelService`；除注明外语言为 `"zh-CN"`）：
  - `TestSeedLabelsChinese`：`newLabelService(t, dir, "zh-CN")`，`ListLabels` 得 7 个，slug 依次 `otp bill order travel calendar notice newsletter`，`[0].Name == "验证码"`、`.Color == "red"`、`.Icon == "shield-keyhole"`、`.Builtin`；关掉重开同一 DataDir 仍是 7 个；删掉一个后重开是 6 个（不再补种）。
  - `TestSeedLabelsEnglish`：`newLabelService(t, "", "")`，`[0].Name == "Codes"`、`[1].Name == "Bills"`；`Create{Name: "BILLS"}` 返回 `ErrLabelExists`。
  - `TestLabelCRUD`：Create `{Name: " 家人 ", Color: "pink", Icon: "heart-3"}` 得 `Name == "家人"`、`Slug == "u8"`（7 个预置之后的 id）、`Position == 7`；Update 改名为「家庭」后 Slug 仍 `u8`；`Create{Name: "账单 "}` 返回 `ErrLabelExists`；先建 `Équipe` 再建 `équipe` 返回 `ErrLabelExists`；两个 goroutine 同时 `Create{Name: "旅行"}`，恰一个成功、一个 `ErrLabelExists`；空名、`Color: "teal"`、`Icon: "nope"` 各返回非 nil 错误；`ReorderLabels` 倒序后 `ListLabels` 顺序随之改变；少传一个 id 返回错误；Delete 后其 `message_labels` 行消失（先手插一行再删）。
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

  名称：`strings.TrimSpace` 后非空、≤ 32 rune；重名比较见 Global Constraints（持 `labelsMu` 读出全部名字比较）。`CreateLabel` 用一个事务两条语句：插入时 slug 为临时值 `'tmp-' || hex(randomblob(8))`，紧接着 `UPDATE labels SET slug = 'u' || id WHERE id = last_insert_rowid()`。
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
  - `TestLabelView`：`ListMessages(View{LabelID: otp})` 只返回 `active=1` 的那封，`active=0` 的不返回；`View{LabelID: otp, Unified: "inbox"}` 与 `View{LabelID: otp, FolderID: 1}` 都返回错误。
  - `TestSetLabelAtomic`：`SetLabel([A, 999999], otp, true)`（999999 不存在）返回错误，且 A 也没有写入。
  - `TestSearchWithFilters` 末尾加：`ids(SearchQuery{LabelID: otp}) == []int64{带 otp 的那封}`；`SearchQuery{LabelID: 1}.Filtered() == true`。
- [ ] **Step 2: 跑** `mise exec -- go test ./core -run 'TestSetLabelManual|TestThreadLabels|TestLabelView|TestSearchWithFilters'`，预期 FAIL。
- [ ] **Step 3: 实现。** `viewCond` 开头先校验 `FolderID`、`Unified`、`LabelID` 恰有一个非零值，否则返回错误；新增分支 `v.LabelID != 0` → `EXISTS (SELECT 1 FROM message_labels ml WHERE ml.message_id = m.id AND ml.label_id = ? AND ml.active = 1)`；错误信息里补上 LabelID。`SearchWith` 同条件追加。`SetLabel` 先查全部 id 都存在（否则返回错误、不写），再用一个 `Transaction` 对每封执行 `INSERT … ON CONFLICT(message_id, label_id) DO UPDATE SET source='manual', active=excluded.active`。
- [ ] **Step 4: 跑**同上，预期 PASS。
- [ ] **Step 5: Commit** `feat(core): applied labels, label view and search filter`

### Task 5: 后台分类器

**Files:**
- Create: `core/labeler.go`
- Modify: `core/settings.go`（`Settings.AutoLabel bool`，`defaultSettings` 设 true）
- Modify: `core/service.go`（`New` 里 `s.startJanitor()` 之后、`!opts.noLabeler` 时 `s.startLabeler()`；`syncAccount` 里 `s.notifyHooks()` 旁加 `s.wakeLabeler()`；`Service` 加 `labelWake chan struct{}`（容量 1）和 `labelJudge func(context.Context) (ai.Judge, error)`，`New` 里默认设为调用 `s.resolveJudge` 只取 judge）
- Test: `core/labeler_test.go`（package core，全部用 Task 3 的 `newLabelService`，它已关掉后台循环）

**Interfaces:**
- Consumes: `s.resolveJudge(ctx)`、`s.mailForModel(ctx, id)`（返回 `Message`）、`mailHeader(MessageSummary)`、`ai.Untrusted`、`ai.InjectionQuote`、`mail.HTMLToText`、`hookBackoff`、`s.hookWait`、`s.hookClock`。
- Produces:
  ```go
  func (s *Service) classifyBatch(ctx) (done int, err error) // 取一批（20 封）分完；没有模型返回 (0, errNoLabelModel)
  func (s *Service) runLabeler(ctx)                           // 后台循环本体，startLabeler 在 goroutine 里调用；调度测试直接调用
  func (s *Service) wakeLabeler()
  ```
  测试把 `s.labelJudge` 换成假 judge（按 Service，不用包级变量）。每批写完发 `LabelsChanged`。

- [ ] **Step 1: 写失败测试**（假 judge：记录收到的 state 和题目，按题名返回预设概率；可设为返回错误或对某题 `Refused`）：
  - `TestClassifyAppliesAuto`：收件箱邮件 A，假 judge 对 `otp` 给 0.5、`bill` 给 0.4999、其余 0；`classifyBatch` 后 `MessageLabels(A) == [{otp, Auto:true}]`，`labeled_at > 0`；judge 收到 7 道题，题名即 slug，`Instructions` 含「验证码」和它的说明（中文种子）。
  - `TestClassifyDropsStaleAuto`：A 已有 `auto bill active=1`，`labeled_at = 0`；judge 给 bill 0.1 → bill 行被删。
  - `TestClassifySkipsManual`：A 已有 `manual otp active=0` 和 `manual bill active=1`；judge 收到的题里没有 `otp`、`bill`；结果 otp 仍 `active=0`，bill 仍 manual。
  - `TestClassifyOnlyInbox`：已发送文件夹（role `sent`）里的邮件不被取。
  - `TestClassifyRefusedIsNo`：`otp` 拒答、`bill` 0.8 → 只有 bill；`labeled_at` 已写。
  - `TestClassifyErrorKeepsPending`：judge 返回错误 → `classifyBatch` 返回错误，`labeled_at` 仍为 NULL，无 `message_labels` 行。
  - `TestClassifyNoModel`：用默认 `labelJudge` 且未配任何模型服务 → 返回 `errNoLabelModel`，`labeled_at` 仍为 NULL。
  - `TestClassifyAutoOff`：`Settings.AutoLabel = false`，A 的 `labeled_at` 为 NULL、B 为 0 → 只有 B 被分类（judge 只被调一次），A 仍为 NULL。
  - Review Focus 6 `TestClassifyDiscardsStale`：三个子测试，假 judge 作答时分别 (a) 把 B 的 `labeled_at` 从 0 改成当前时间（即 Task 6 停止回扫做的事），(b) 把 A 移到已发送文件夹，(c) 把 A 的 `labeled_at` 改成别的值 → 三种情况下这封邮件都没有新的 `message_labels` 行，`labeled_at` 保持被改后的值。
  - `TestLabelerBackoffIgnoresWake`：`hookClock` 用假时钟（同 `core/hooks_test.go` 的做法，不改那里的断言），judge 一直报错，`go s.runLabeler(ctx)`；第一次失败后连续 `wakeLabeler()` 三次，judge 调用次数仍为 1；把假时钟推进 `hookBackoff(1)` 后变为 2。
  - Review Focus 1 `TestClassifyVanishedRows`：假 judge 在作答时删掉标签 otp 并删掉邮件 B（同批 A、B）；`classifyBatch` 返回 nil，A 的其余标签写入、无外键错误。
  - Review Focus 2 `TestClassifyKeepsConcurrentManual`：假 judge 作答时对 A 调 `SetLabel([A], otp, false)`，judge 给 otp 0.9；结束后 otp 行仍是 `manual active=0`。
  - Review Focus 3 `TestClassifyNoLabels`：删光全部标签 → judge 未被调用，`labeled_at` 已写。
  - Review Focus 4 `TestClassifyBodyText`：`body_text` 为空、`body_html = "<p>你的验证码是 123456</p>"` → state 含「你的验证码是 123456」；`body_text` 为 5000 个「字」→ state 中正文部分恰为 4000 个「字」。
- [ ] **Step 2: 跑** `mise exec -- go test ./core -run TestClassify`，预期 FAIL。
- [ ] **Step 3: 实现。**
  - 取活：`SELECT m.id, m.labeled_at FROM messages m WHERE (m.labeled_at = 0 OR (? AND m.labeled_at IS NULL)) AND <收件箱条件> ORDER BY m.labeled_at IS NULL, m.date DESC, m.id DESC LIMIT 20`（`?` 为 `AutoLabel`；回扫目标优先）。
  - 题目：每个没有 manual 行的标签一题，`Kind: ai.YesNo`，`Name: slug`，`Instructions: fmt.Sprintf("Answer yes if this email belongs to the user's label %q. %s", name, desc)`（desc 为空则省略）。没有题 → 只写 `labeled_at`。
  - state：`text := mailHeader(m.MessageSummary) + "\n" + body`，`ai.Untrusted(fmt.Sprintf("message:%d", m.ID), text, ai.InjectionQuote(text) != "")`；body 为 `m.Text`，空则 `mail.HTMLToText(m.HTML)`，截到 4000 rune。
  - 写入（每封一个 `Transaction`）：每条语句都带守卫 `g` = `EXISTS(SELECT 1 FROM messages WHERE id = ? AND labeled_at IS ? AND <收件箱条件>)`，`labeled_at` 用取活时读到的值。概率 ≥ 0.5 → `INSERT INTO message_labels SELECT ?, ?, 'auto', 1 WHERE g AND EXISTS(SELECT 1 FROM labels WHERE id = ?) ON CONFLICT DO UPDATE SET active = 1 WHERE source = 'auto'`；否则 `DELETE FROM message_labels WHERE message_id = ? AND label_id = ? AND source = 'auto' AND g`；最后 `UPDATE messages SET labeled_at = unixepoch() WHERE id = ? AND labeled_at IS ? AND <收件箱条件>`。守卫不成立时整封无写入，算作完成。
  - `runLabeler`：成功且本批有活 → 立即再取；无活或 `errNoLabelModel` → `s.hookWait(ctx, hookPoll, s.labelWake)`；出错 → `s.hookWait(ctx, hookBackoff(连续失败次数), nil)`（与 `dispatchHooks` 一样，失败退避不被同步唤醒打断）。
- [ ] **Step 4: 跑**同上，预期 PASS；`mise exec -- go test ./core/...` 全绿。
- [ ] **Step 5: Commit** `feat(core): classify inbox mail into labels`

### Task 6: 回扫

**Files:**
- Modify: `core/labeler.go`
- Test: `core/labeler_test.go`

**Interfaces:**
- Produces:
  ```go
  func (s *Service) RescanLabels(ctx, days int, all bool) (int, error) // 范围内收件箱邮件的 labeled_at 设为 0；days 0 = 收件箱全部；返回本次目标数；唤醒分类器
  func (s *Service) StopRescan(ctx) error                               // labeled_at = 0 的改成现在
  func (s *Service) PendingLabels(ctx) (int, error)                     // labeled_at = 0 的数量
  ```

- [ ] **Step 1: 写失败测试**：
  - `TestRescanMissing`：三封已分类（A 有 auto otp，B 只有 `manual otp active=0`，C 无行），`RescanLabels(30, false)` 返回 2（B、C 的 `labeled_at` 变 0），A 不变。
  - `TestRescanAll`：A 有 auto otp 和 manual bill；`RescanLabels(30, true)` 后 A 的 auto 行被删、manual 行保留，返回 3。
  - `TestRescanRange`：40 天前的一封不被 `RescanLabels(30, …)` 改动；`RescanLabels(0, true)` 改动它。
  - `TestStopRescan`：另有一封新邮件 N（`labeled_at` NULL）和一封 40 天前的 O（`labeled_at` NULL）；`RescanLabels(30, true)` 后 `StopRescan` → `PendingLabels == 0`，N、O 仍为 NULL。
- [ ] **Step 2: 跑** `mise exec -- go test ./core -run 'TestRescan|TestStopRescan'`，预期 FAIL。
- [ ] **Step 3: 实现。** 日期条件 `m.date >= ?`（`now - days*86400`）；「只补」= 没有任何 `active=1` 行的邮件；`all` 时删除 auto 行与置 0 用同一个 `Transaction`。
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
      OnClick func()           // 非 nil 时芯片可点（读信页用它打开改标签菜单）
      OnRemove func()          // 非 nil 时尾部有 × 按钮，RemoveLabel 为其无障碍标签
      RemoveLabel string
  }
  func LabelChip(c *ui.Context, o LabelChipOpts) ui.Element
  ```

- [ ] **Step 1: 写失败测试**（沿用 kit_test 的渲染方式）：
  - `TestKitLabelChipLooks`：对 `theme.Light()`、`theme.Dark()` 各渲染一次（照 `TestKitDarkRenders` 的方式切主题），每种主题下 red 的手动芯片高 20、背景 `Labels["red"].Fill`、边框 `Fill`、文字 `Text`；自动芯片背景透明、边框 `Edge`、文字 `Text`，宽高与手动相同。
  - `TestKitLabelChipRemove`：有 `OnRemove` 时能按 `RemoveLabel` 找到按钮，点击调用回调且不触发 `OnClick`；悬停芯片出现 `Tip` 文本（同 `TestKitTooltipOnFocus` 的做法）。
  - `TestKitLabelChipClick`：有 `OnClick` 时点芯片调用它；没有时芯片不可聚焦。
- [ ] **Step 2: 跑** `mise exec -- go test ./internal/kit -run 'TestKitLabelChip'`，预期 FAIL。
- [ ] **Step 3: 实现**，尺寸见 Global Constraints；有 × 时右内边距改为 `th.Space(0.5)`，× 为 16×16、图标 10。
- [ ] **Step 4: 跑**同上，预期 PASS。
- [ ] **Step 5: Commit** `feat(kit): label chip`

### Task 8: app 数据接线、列表行、侧栏标签视图

**Files:**
- Modify: `app/app.go`（`a.labels []core.Label`，随账户一起加载；收到 `LabelsChanged` 重新加载并刷新列表）
- Create: `app/labels.go`（芯片工厂和查表）
- Modify: `app/mail_list.go`（`mailItem.Labels []core.AppliedLabel`；`fetchItems` 末尾按会话/邮件补标签；搜索在 `SearchWith` 之后用 `MessageLabels` 给结果补标签（`app/mail_list.go:1083` 一带）；`row` 与 `resultRow` 都画芯片）
- Modify: `app/mail_sidebar.go`（「标签」组）、页面标题处（标签视图显示标签名）
- Modify: `internal/i18n/zh.go`、`en.go`
- Test: `app/labels_test.go`

**Interfaces:**
- Consumes: Task 3/4 的 `ListLabels`、`ThreadLabels`、`MessageLabels`、`View.LabelID`；Task 7 的 `kit.LabelChip`。
- Produces:
  ```go
  func (a *App) label(id int64) (core.Label, bool)
  func (a *App) labelChip(c *ui.Context, l core.AppliedLabel, onClick, onRemove func()) ui.Element // 取 a.labels 与 theme.Of(c).Labels，Tip 用 label.tip.auto / label.tip.manual
  ```
- i18n：`label.tip.auto` 由模型标记 / Labeled by the model；`label.tip.manual` 手动标记 / Labeled by you；`mail.labels` 标签 / Labels；`label.more` +%d / +%d。

- [ ] **Step 1: 写失败测试**（`newHarness` + 往 core 插邮件和 `message_labels`）：
  - `TestListRowChips`：一个会话有 3 个标签 → 行里看到前两个标签名和「+1」；auto 的芯片悬停出现「由模型标记」。
  - `TestSearchRowChips`：搜索命中这封邮件 → 结果行同样看到前两个标签名和「+1」。
  - `TestCoreLabelsHaveTokens`：`core.LabelColors` 每个都在 `theme.Light().Labels` 与 `theme.Dark().Labels` 里；`core.LabelIcons` 每个 `icons.Get("label-"+n)` 不 panic。
  - `TestSidebarLabels`：只有有邮件的标签出现在「标签」组；点「验证码」后路由为 `View{LabelID: otp}`，列表只剩该会话，顶部标题为「验证码」。
  - `TestLabelsRefreshOnEvent`：后台 `SetLabel` 后（发 `LabelsChanged`），行内芯片随之出现。
- [ ] **Step 2: 跑** `mise exec -- go test ./app -run 'TestListRowChips|TestSearchRowChips|TestCoreLabelsHaveTokens|TestSidebarLabels|TestLabelsRefreshOnEvent'`，预期 FAIL。
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
- Produces: `func (r *reader) labelRow(c *ui.Context)`；`reader` 加 `threadIDs []int64`，在 `app/reader.go:142` 裁剪 `msgs` 之前取 `th.Messages` 的全部 id。标签的读取（`ThreadLabels`）和修改（`SetLabel`）一律用 `threadIDs`，不用 `r.msgs`（它在指定了 MessageID 时只剩一封）。
- i18n：`reader.labels.add` 标签 / Label；`reader.labels.search` 搜索或新建标签 / Search or create a label；`reader.labels.create` 新建「%s」 / Create “%s”；`reader.labels.manage` 管理标签… / Manage labels…；`reader.labels.auto` 模型标记 / By model；`reader.labels.remove` 去掉标签「%s」 / Remove label “%s”。

- [ ] **Step 1: 写失败测试**：
  - `TestReaderLabelRow`：会话有 auto otp → 主题下见「验证码」芯片和「标签」按钮。
  - `TestReaderLabelRemove`：点 `去掉标签「验证码」` → core 中该会话所有邮件 otp 为 `manual active=0`，芯片消失。
  - Review Focus 7 `TestReaderLabelFromSearch`：两封同会话邮件只有第二封有 auto otp；从搜索结果打开第一封（路由带 MessageID）→ 仍见「验证码」芯片；点 × 后两封的 otp 都为 `manual active=0`。
  - `TestReaderChipOpensMenu`：点「验证码」芯片本身 → 改标签菜单打开。
  - `TestReaderLabelMenu`：点「标签」→ 菜单列出全部 7 个预置；otp 一项带「模型标记」；点「账单」→ 会话邮件 bill 为 `manual active=1`。
  - `TestReaderLabelCreate`：菜单搜索框输入「家人」→ 出现「新建「家人」」，点它 → 新标签被创建（颜色 gray、图标 folder-3）并打到会话上；输入「账单」不出现新建项。
  - 「管理标签…」导航到 `Route{Page: PageSettings, Settings: "labels"}`。
- [ ] **Step 2: 跑** `mise exec -- go test ./app -run 'TestReader(Label|Chip)'`，预期 FAIL。
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
- Modify: `app/settings_search_test.go`（`TestSettingsIndexCoversRows` 的页面表在第 30 行写死，加 `"labels": "labels"`）
- Modify: `internal/i18n/zh.go`、`en.go`
- Test: `app/settings_labels_test.go`

**Interfaces:**
- Consumes: Task 3 的 CRUD、Task 6 的 `RescanLabels`/`StopRescan`/`PendingLabels`、Task 5 的 `Settings.AutoLabel`、`a.changeSettings`、Task 7/8 的芯片。
- Produces: `func (p *settingsPage) labelsView(c *ui.Context)`。
- i18n（zh / en）：
  `settings.labels` 标签 / Labels；`settings.labels.sub` 模型会给新邮件自动打标签，你也可以随时手动改。手动改过的不会被覆盖。 / The model labels new mail for you, and you can change labels any time. Your changes are never overwritten.；`settings.labels.new` 新建标签 / New label；`settings.labels.list` 标签 / Labels；`settings.labels.list.foot` 拖动调整顺序，侧栏和菜单按这个顺序排列 / Drag to reorder; the sidebar and menus follow this order；`settings.labels.builtin` 预置 / Built-in；`settings.labels.count` %d 封 / %d；`settings.labels.no_desc` 没有说明，只按名称判断 / No description; judged by name only；`settings.labels.edit` 编辑「%s」 / Edit “%s”；`settings.labels.auto` 自动标记 / Auto-labeling；`settings.labels.auto.on` 自动给新邮件打标签 / Label new mail automatically；`settings.labels.auto.on.help` 收到新邮件后在后台分类，只看收件箱 / Sorts new mail in the background, Inbox only；`settings.labels.model` 判断模型 / Judge model；`settings.labels.model.help` 没有判断模型时由生成模型代答，会慢一些 / Without a judge model the generation model answers, more slowly；`settings.labels.model.value` 跟随「模型服务」 / From Model services；`settings.labels.model.go` 去设置 / Open；`settings.labels.rescan` 回扫已有邮件 / Rescan existing mail；`settings.labels.rescan.sub` 新建或改了标签后，按现在的标签重新给收件箱里的邮件分类 / After adding or changing labels, sort Inbox mail again with the current labels；`settings.labels.rescan.range` 范围 / Range；`settings.labels.rescan.days` 最近 %d 天 / Last %d days；`settings.labels.rescan.all_range` 同步范围内全部 / Everything synced；`settings.labels.rescan.mode` 方式 / Mode；`settings.labels.rescan.missing` 只补没有标签的 / Unlabeled only；`settings.labels.rescan.missing.help` 已经有标签的邮件不动 / Mail with labels stays as is；`settings.labels.rescan.redo` 全部重新判断 / Judge all again；`settings.labels.rescan.redo.help` 模型打的标签重新判断，手动改过的保留 / Model labels are judged again; yours are kept；`settings.labels.rescan.start` 开始回扫 / Rescan；`settings.labels.rescan.running` 正在回扫 · %d / %d 封 / Rescanning · %d of %d；`settings.labels.rescan.progress` 回扫进度 / Rescan progress；`settings.labels.rescan.stop` 停止 / Stop；`settings.labels.dialog.new` 新建标签 / New label；`settings.labels.dialog.edit` 编辑标签 / Edit label；`settings.labels.dialog.sub` 模型根据名称和说明判断哪些邮件该打这个标签。 / The model uses the name and description to decide which mail gets this label.；`settings.labels.preview.manual` 手动标记 / By you；`settings.labels.preview.auto` 模型标记 / By model；`settings.labels.name` 名称 / Name；`settings.labels.name.placeholder` 比如：家人 / e.g. Family；`settings.labels.name.exists` 已经有同名的标签 / A label with this name exists；`settings.labels.desc` 说明（可选） / Description (optional)；`settings.labels.desc.placeholder` 写清楚什么样的邮件算，比如：爸妈和孩子学校发来的邮件 / Say which mail counts, e.g. mail from my parents and my kid's school；`settings.labels.color` 颜色 / Color；`settings.labels.icon` 图标 / Icon；`settings.labels.delete` 删除标签 / Delete label；`settings.labels.delete.confirm` 删除「%s」？ / Delete “%s”?；`settings.labels.delete.confirm.help` 已打上的 %d 封邮件会去掉这个标签。 / It will be removed from %d messages.；`settings.labels.create` 创建 / Create；颜色名 `label.color.gray` 灰 / Gray … `label.color.pink` 粉 / Pink（8 个）。

- [ ] **Step 1: 写失败测试**：
  - `TestSettingsLabelsList`：`h.openSettings("labels")` 见 7 行预置，每行有「预置」和「编辑「验证码」」按钮；侧栏「标签」位于「模型服务」之后。
  - `TestSettingsLabelsCreate`：点「新建标签」，输入「家人」，选粉色、`heart-3`，点「创建」→ `ListLabels` 末尾为 `{家人, pink, heart-3}`；输入「账单」时「创建」不可用并显示「已经有同名的标签」。
  - `TestSettingsLabelsEdit`：编辑「验证码」改说明后保存 → core 中说明更新、slug 仍为 `otp`。
  - `TestSettingsLabelsDelete`：编辑 → 「删除标签」→ 确认框文本含「删除「验证码」？」和邮件数 → 确认后行消失。
  - `TestSettingsLabelsReorder`：把第 1 行拖到第 3 行位置 → `ListLabels` 顺序为 `bill order otp …`。
  - `TestSettingsLabelsAutoSwitch`：关掉开关 → `a.settings.AutoLabel == false`。
  - `TestSettingsLabelsRescan`：收件箱 4 封；选「全部重新判断」点「开始回扫」→ 调用 `RescanLabels(30, true)`（范围默认最近 30 天），显示「正在回扫 · 0 / 4 封」、「停止」和按 `回扫进度` 找到的进度条（宽度占比 0）；手动把其中一封的 `labeled_at` 写成现在并推进 2 秒 → 文本变为「1 / 4」、进度条占比 0.25；点「停止」后 `PendingLabels == 0` 并回到「开始回扫」。
  - `TestSettingsLabelsRescanWithAutoOff`：关掉自动开关后仍能开始回扫，按钮可用。
  - `TestSettingsIndexCoversRows` 通过（含新加的 `labels` 页）；`TestSettingsSearchFindsLabels`：设置侧栏搜索「回扫」和「自动标记」都只剩「标签」一页。
- [ ] **Step 2: 跑** `mise exec -- go test ./app -run 'TestSettingsLabels|TestSettingsIndexCoversRows|TestSettingsSearchFindsLabels'`，预期 FAIL。
- [ ] **Step 3: 实现。** 布局照设计稿 `SettingsLabels`：`pageHeading`（右侧「新建标签」）+ 三个 `settingsSection`。列表行：`grip` 图标；整行元素调 `.Draggable(&p.labelDrag[id], ui.DraggableOptions{Axis: ui.Vertical, Bounds: 列表范围})`（元素方法，见 mygo `ui/draggable.go:77`），每行的 offset 存在 `settingsPage.labelDrag map[int64]*ui.Point`；当该元素 `Dragging()` 由真变假时，新位置 = 原位置 + round(offset.Y / 行高)，夹在 [0, n-1]，调 `ReorderLabels` 后把 offset 归零、宽 `th.Space(28)` 的芯片列、说明（单行省略）、`kit.Badge` 预置、数量、编辑图标按钮。弹层 `kit.Dialog` md：预览两枚芯片（`t.Sidebar` 底、`Radius.Lg`）、`kit.Field`+`kit.Input`/`kit.TextArea`、8 个 24×24 圆形色点（选中描 2 px 背景色 + 2 px 圆点色外环）、图标网格 8 列、格高 36、选中格用该色 `Fill` 底和 `Text` 色。回扫区：`settingSelect`（7/30/90 天、全部，默认 30）、分段选择（`t.Hover` 轨道）、开始按钮；开始时记下 `RescanLabels` 返回的总数 total，进行中每 2 秒读 `PendingLabels`，显示 `total-pending / total` 和高 4、`Radius.Full` 的进度条（`t.Hover` 轨道、`t.Primary` 填充，标签为 `回扫进度`）；pending 为 0 时回到空闲。
- [ ] **Step 4: 跑**同上，预期 PASS；再跑 `mise exec -- go test ./...` 全绿。
- [ ] **Step 5: Commit** `feat(app): labels settings`

---

**设计系统同步（不在本仓库）：** 把 Task 1 的 8 组颜色以 `label-<color>-dot/fill/edge/text` 命名加入 Godwit design system 画布的 `tokens.json`，亮暗两套值与 `theme.Labels` 一致。
