# 邮件标签：设计

日期：2026-10-13 · 状态：待评审 · 设计稿：Godwit UI 画布的「标签」页

给邮件打标签：预置一组，用户可以自己加；模型自动分类，用户可以手动改；自动打的和手动改的在显示上有细微区别。

## 1. 已定的决定

| 问题 | 决定 |
|---|---|
| 存在哪 | 只存本地。每个标签有不变的 `slug`，以后同步成 IMAP 关键字时用；这次不做同步 |
| 每封几个 | 可以多个 |
| 模型凭什么判断 | 标签的名称 + 一句可选说明 |
| 分哪些邮件 | 收件箱里的新邮件，启动时补齐同步范围内还没分过的；已发送、草稿、垃圾邮件不分 |
| 回扫 | 用户手动触发：选最近几天，选「只补没有标签的」或「全部重新判断」 |
| 手动和自动的关系 | 手动的永远优先：手动加的不会被删，手动去掉的不会被加回来，回扫也一样 |
| 出现在哪 | 列表行、读信页、侧栏「标签」组、搜索的「标签」筛选、设置里的「标签」页 |

**不做**：同步到服务器；按发件人等确定规则打标签；改了标签后自动回扫；给已发送等其他文件夹分类。

## 2. 数据

新增一组迁移（`core/store/schema.go`）：

```sql
CREATE TABLE labels (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  slug TEXT NOT NULL UNIQUE,          -- 建好不改；预置的用英文名，如 otp
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  color TEXT NOT NULL,                -- gray red orange amber green blue violet pink
  icon TEXT NOT NULL,                 -- Godwit 图标名，见 §6
  builtin INTEGER NOT NULL DEFAULT 0,
  position INTEGER NOT NULL);
CREATE TABLE message_labels (
  message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  label_id INTEGER NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
  source TEXT NOT NULL,               -- auto | manual
  active INTEGER NOT NULL,            -- 0 = 用户手动去掉
  PRIMARY KEY (message_id, label_id));
CREATE INDEX message_labels_label ON message_labels(label_id, active);
ALTER TABLE messages ADD COLUMN labeled_at INTEGER;  -- 分类过的时间；NULL = 待分类
```

- 一封邮件「有」某标签 = 有一行 `active=1`。`source` 只决定显示样式。
- `source=manual, active=0` 是用户手动去掉的记录，分类器据此不再加回来。
- 预置标签在迁移里插入（§7），用户可以改名、改颜色、改说明，也可以删除；删了就不再出现。
- 删除标签会连带删除 `message_labels` 里的行（外键级联）。

## 3. 分类器

core 里一个后台 goroutine，和现有的 hooks 调度器同一种写法：同步存入新邮件后唤醒它，另外每 30 秒自己查一次。

**取活**：收件箱（`folders.role = 'inbox'`）里 `labeled_at IS NULL` 的邮件，按日期从新到旧，每批 20 封。设置里「自动给新邮件打标签」关掉时不取。

**一封邮件的分类**：
1. 每个标签出一道 `ai.YesNo` 题，题名 = `slug`。说明 = 固定的引导语 + 「这封邮件是否属于『名称』：说明」。凡是这封邮件已有 `source=manual` 行的标签都跳过，不出题。
2. 用 `s.resolveJudge(ctx)`（`core/models.go`）一次问完：有判断模型用判断模型，没有就用生成模型代答。邮件内容作为 state 传入：发件人、主题、正文的前 4000 字。按 `ai.Judge` 的约定，这部分属于不可信数据。
3. 概率 ≥ 0.5 的写入 `source=auto, active=1`，低于 0.5 的删掉已有的 auto 行。最后写 `labeled_at = now`。以上在一个事务里完成。

**失败**：
- 没有可用模型：不写 `labeled_at`，等以后配好模型再分。
- 请求失败：按 `hookBackoff` 退避后重试这一批。
- 某道题被拒答：这个标签当作「否」处理，其余照常写入。

## 4. 回扫

`Service.RescanLabels(ctx, days int, all bool) (count int, err error)`，范围是 `date >= now - days`、在收件箱里的邮件：

- **只补没有标签的**（`all=false`）：把范围内没有任何 `active=1` 行的邮件的 `labeled_at` 清空。
- **全部重新判断**（`all=true`）：删掉范围内所有 `source=auto` 的行，再清空这些邮件的 `labeled_at`。`manual` 行不动。

清空之后由分类器照常处理，进度 = 范围内 `labeled_at IS NULL` 的邮件数。「停止」把这些邮件的 `labeled_at` 写回现在的时间，没分完的就不再分。

## 5. 手动修改

`Service.SetLabel(ctx, messageIDs []int64, labelID int64, on bool)` 给每封邮件写入 `source=manual` 和对应的 `active`。读信页显示的是会话，勾选菜单和芯片上的 × 都对会话里的全部邮件调用它；会话的标签是其中各封邮件标签的并集，只要有一封是手动打上的就按手动显示。

## 6. 界面

设计稿见画布的「标签」页。尺寸、间距一律用 kit 的 token。

**颜色**：8 种，取自 Tailwind 色板，放进 `theme.Tokens.Labels`（按颜色名查），深色主题有另一组值：

| 用途 | 浅色 | 深色 |
|---|---|---|
| 手动标签的底色 | 100 | 950 |
| 自动标签的描边 | 200 | 800 |
| 文字和图标 | 700 | 300 |
| 色板上的圆点 | 500 | 500 |

`gray` 用 neutral 色阶，圆点用 primary。

这组颜色也要加进设计系统的颜色表（Godwit design system 的 `tokens.json`）。

**图标**：在 `web/icons.go` 的 `RemixIcons` 里加 40 个 `label-*` 图标，顺序和设计稿的图标网格一致：

> shield-keyhole bill truck plane calendar-event notification-3 newspaper shopping-bag-3 · folder-3 money-dollar-circle book-2 graduation-cap pencil quill-pen braces terminal-box · music-2 movie-2 tools palette stethoscope first-aid-kit seedling briefcase-4 · bar-chart-box run sticky-note scales-3 earth global hammer footprint · flask brain heart-3 plant team home-4 key-2 chat-3

另加 `price-tag-3`（line 和 fill 两种）、`draggable`、`check`，用于设置侧栏和菜单。`labels.icon` 存去掉 `label-` 前缀后的名字。

**标签芯片**（`internal/kit`，列表、读信页、设置共用）：
- 尺寸：高 20，圆角 4，左右内边距 6，图标和文字间距 4，字号 12，图标 12。
- 手动标签：浅底填充 + 彩色文字。
- 自动标签：同样的颜色，但只有描边、没有底色。
- 悬停提示：自动的显示「由模型标记，点按可修改」，手动的显示「手动标记」。
- 可选的 × 按钮（读信页用）。

**列表行**：芯片放在预览文字下面，最多显示 2 个，多出的显示成 `+N`。顺序按标签的 `position`。

**读信页**：主题下面显示全部芯片，每个带 ×，最后是「+ 标签」按钮。点按钮弹出菜单：
- 顶部是搜索框，输入的名字没有对应标签时，菜单多一项「新建『xx』」。
- 列出所有标签，带勾选状态；自动打上的在右侧标注「模型标记」。
- 底部是「管理标签…」。

**侧栏**：在账户分组前面加「标签」组，只列出有邮件的标签。每行是彩色图标、名称和未读数，点一下打开一个跨账户的视图。`core.View` 加 `LabelID int64` 字段，`ListThreads` 遇到它就按 `message_labels` 过滤。

**搜索**：筛选芯片那一行加「标签 ▾」。`SearchQuery` 加 `LabelID int64` 字段，`Filtered()` 也要算上它。

**设置**：在应用分组「模型服务」后面新加「标签」一节。
- **标签列表**：可以拖动排序。每行是手柄、芯片、说明、「预置」标记、邮件数和编辑按钮。页面右上角是「新建标签」。
- **编辑 / 新建弹层**：`kit.Dialog` md 宽度。顶上是手动和自动两种样式的预览，下面依次是名称、说明（可选）、8 个色点、图标网格（每行 8 个，选中的格子用标签自己的颜色）。底栏左边是「删除标签」，右边是取消和保存（新建时是「创建」）。删除前再弹一次确认，提示「已打上的 N 封邮件会去掉这个标签」。
- **自动标记**：一个开关（`Settings` 加 `AutoLabel` 字段，默认开），加上「判断模型」一行，链接到模型服务。
- **回扫已有邮件**：范围（最近 7 / 30 / 90 天，或同步范围内全部）、方式（分段选择），再加「开始回扫」按钮。回扫进行时显示进度条和「停止」。

名称不能为空，也不能和已有标签重名（不区分大小写），否则保存按钮不可用，并在输入框下面说明原因。

## 7. 预置标签

| slug | 名称 | 颜色 | 图标 | 说明 |
|---|---|---|---|---|
| otp | 验证码 | red | shield-keyhole | 登录、注册、支付时收到的一次性验证码或登录链接 |
| bill | 账单 | amber | bill | 账单、收据、发票和扣款通知 |
| order | 订单与物流 | orange | truck | 网购订单确认、发货和快递进度 |
| travel | 航班与出行 | blue | plane | 机票、火车票、酒店预订和值机提醒 |
| calendar | 日程邀请 | green | calendar-event | 会议邀请、日程变更和活动报名确认 |
| notice | 通知 | gray | notification-3 | 系统或服务自动发出的提醒，不需要回复 |
| newsletter | 订阅与资讯 | gray | newspaper | 新闻简报、产品更新和营销邮件 |

预置标签的名称和说明按首次启动时的界面语言写入，之后不随语言切换而改变，因为它们已经是用户的数据。用户新建标签时，`slug` 由 `u` 加 id 生成。

## 8. 测试

- **core**：
  - 迁移和预置数据写入正确。
  - 分类器用假的 judge 测：已有 manual 行的标签不出题；概率阈值；拒答当作「否」；失败时不写 `labeled_at`；没有模型时跳过。
  - 回扫两种方式在有 manual 行时的结果。
  - `SetMessageLabel` 写入正确。
  - `View.LabelID`、`SearchQuery.LabelID` 的过滤。
- **app**：
  - 芯片两种样式渲染出的颜色和描边，深浅主题都测。
  - 列表行最多 2 个，加 `+N`。
  - 读信页菜单勾选后调用 `SetMessageLabel`。
  - 侧栏只列出有邮件的标签。
  - 设置页：新建、编辑、重名校验、删除确认、回扫按钮的状态。
  - 设置搜索的索引要覆盖新的设置项，沿用现有的覆盖测试。
- **i18n**：新增的文案中英都有，`i18n_test` 照常通过。
