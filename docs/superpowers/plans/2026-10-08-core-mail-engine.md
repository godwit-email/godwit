# Godwit 核心邮件引擎 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现 `core` 库里与邮件相关的部分：存储、MIME 解析与 HTML 清洗、账户、IMAP 同步、会话归并、本地优先的修改、HTML 撰写与发送、事件。完成后不需要 GUI，就能通过 Go 测试完整地收发、搜索、整理邮件。

**Architecture:** 所有能力挂在 `core.Service` 上，这是唯一对外入口。数据存 mygo sqlite（Go API）：邮件头、正文文本和 HTML 存库，附件按需下载进可丢弃的缓存。每个账户一个同步 worker，同一账户的同步与推送串行。本地修改先写库和操作队列，再推到服务器；邮件的本地身份与服务器位置分开记录。发送走至多一次的发件箱状态机。

**Tech Stack:** Go 1.27.1；`github.com/egoist/mygo/plugins/sqlite`（mygo v0.3.0）；`github.com/emersion/go-imap/v2` v2.0.0-beta.8；`github.com/emersion/go-smtp` v0.25.0；`github.com/emersion/go-message` v0.18.2；`github.com/microcosm-cc/bluemonday` v1.0.27；`golang.org/x/net/html`；`github.com/zalando/go-keyring` v0.2.8；测试用 `github.com/stretchr/testify`。

**Spec:** `docs/superpowers/specs/2026-10-08-godwit-design.md`（重点：第 2 节存储约束、第 4 节原则 6–9、5.5 节邮件 HTML 显示契约、7.2 / 7.3 节会话与撰写）

### 计划拆分

按依赖顺序共六份计划，每份完成后都能单独测试：

1. **核心邮件引擎**（本文件）
2. 账户接入：服务商识别、Outlook OAuth（公开客户端 + PKCE、XOAUTH2、刷新互斥、撤销）、重新登录、钥匙串不可用时的行为
3. AI 与 Wit：Genkit、模型服务、Skill、总结 / 翻译 / 即时回复、Wit 与审批
4. 插件运行时：QuickJS 沙箱、权限网关、配置声明、Hook 可靠投递、actions / cards / tools、MCP
5. GUI：mygo 窗口、会话列表与阅读、富文本撰写、设置
6. Headless：`--headless` 下的 JSON-RPC 与 MCP

## Global Constraints

- Go 版本由 `mise.toml` 固定为 1.27.1；module 路径 `github.com/godwitmail/godwit`。
- 不用 cgo：`CGO_ENABLED=0 go build ./...` 和 `CGO_ENABLED=0 go test ./...` 必须通过（`go test -race` 需要 cgo，仅本机使用）。
- 存储只用 mygo sqlite 的 Go API，**不调用** `mygo.Use(sqlite.Plugin)`。`Execute` / `Query` 一次只接受**一条语句**；多条语句用 `Transaction([]sqlite.Statement)`，它在全部执行完才返回，中间拿不到结果，需要关联新插入的行时在 SQL 里用 `last_insert_rowid()`。
- 迁移用 `PRAGMA user_version` + 迁移列表，列表每项是单条语句；数据库版本高于程序认识的版本时返回 `store.ErrNewerDatabase`，不写入任何东西。
- 外键开启（`PRAGMA foreign_keys = ON`，每次打开连接时设置）。
- 对外只暴露内部 `int64` ID，不暴露 IMAP UID。
- `Delete` 只是移到 Trash 角色的文件夹，Service 不提供硬删除。
- HTML 在 core 层清洗，符合 spec 5.5「邮件 HTML 显示契约」。
- 密码只存系统钥匙串，不进 SQLite、日志或错误信息。
- 测试里 SQLite 原生库由 mygo 自动下载到 CLI 缓存；离线或 CI 环境设置 `MYGO_SQLITE_LIBRARY`。
- 附件和整封邮件的大小上限 `core.MaxMessageBytes = 25 << 20`。
- 与 spec 的有意差异：v1 先每次拉取已同步范围内的全部 FLAGS，CONDSTORE 作为以后的优化（代码里用 `ponytail:` 注释标出）。

## Review Focus

1. **只在正文出现的中文短词**：搜「发票」时，标题和发件人里都没有、只有正文有的邮件也必须命中。→ Task 7。
2. **服务器重建文件夹**：UIDVALIDITY 变化后，本地不能残留旧邮件，队列里针对旧 UID 的操作不能被重放到新邮件上。→ Task 6、Task 8。
3. **移动后紧接着改标记**：离线时先移动、再标记已读，联网后两个操作按顺序生效，本地 ID 不变，没有重复邮件；服务器不支持 UIDPLUS 时也一样。→ Task 8。
4. **发送中途断线或崩溃**：SMTP 提交后连接断开，不能自动重发；状态变为「待核实」，能在已发送文件夹里按 Message-ID 找到时自动确认。→ Task 9。
5. **非 UTF-8 编码**：GBK、GB2312、Big5、ISO-2022-JP 的标题和正文正确解码。→ Task 2。

---

## File Structure

```
go.mod
core/
  service.go          Service、Options、New、Close、SyncNow、Subscribe；组装各部分
  types.go            对外类型：Account、Folder、ThreadSummary、Thread、Message、Draft、OutboxItem 等
  accounts.go         账户增删查、校验、TestAccount
  threads.go          ListThreads、GetThread
  messages.go         GetMessage、Search、GetAttachment、SetFlags、Move、Delete
  compose.go          NewDraft、SaveDraft、AddAttachment、RemoveAttachment、Send
  outbox.go           ListOutbox、ResolveOutbox
  events/bus.go       界面事件总线（可丢弃）
  store/store.go      打开、迁移、版本保护
  store/schema.go     迁移列表（每项一条语句）
  store/query.go      Rows 取值辅助
  store/files.go      原子写文件（临时文件 + 改名）、启动时清理孤立文件
  mail/parse.go       MIME 解析
  mail/sanitize.go    HTML 清洗（阅读与撰写共用）
  mail/build.go       生成邮件原文（HTML + 纯文本、附件、Bcc 不入头）
  mail/reply.go       回复 / 全部回复 / 转发的收件人和引用正文
  mail/thread.go      会话归并键
  sync/worker.go      每账户 worker、账户级互斥
  sync/fetch.go       文件夹与邮件拉取
  sync/ops.go         操作队列推送
  send/smtp.go        SMTP 发送
  internal/testmail/  内存 IMAP 服务器、捕获型 SMTP 服务器、故障注入
```

---

### Task 1: 项目骨架与存储层

**Files:**
- Create: `go.mod`, `core/store/store.go`, `core/store/schema.go`, `core/store/query.go`, `core/store/files.go`
- Test: `core/store/store_test.go`, `core/store/files_test.go`

**Interfaces:**
- Produces:
  - `store.Open(ctx, path string) (*store.DB, error)`；`type DB struct{ *sqlite.DB }`
  - `store.Migrations [][]string`：第 i 项是版本 i+1 的语句列表，每条一句
  - `store.ErrNewerDatabase`
  - `store.Int(v any) int64`、`store.Str(v any) string`
  - `store.WriteFileAtomic(path string, data []byte) error`；`store.CleanOrphans(dir string, keep func(path string) bool) error`

- [ ] **Step 1: 初始化 module**：`go mod init github.com/godwitmail/godwit`，`go get github.com/egoist/mygo@v0.3.0 github.com/stretchr/testify`。

- [ ] **Step 2: 写失败的测试**

```go
func TestOpenMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "godwit.db")
	db, err := store.Open(ctx, path)
	require.NoError(t, err)
	rows, _ := db.Query(ctx, "PRAGMA user_version")
	assert.Equal(t, int64(len(store.Migrations)), store.Int(rows.Values[0][0]))
	rows, _ = db.Query(ctx, "PRAGMA foreign_keys")
	assert.Equal(t, int64(1), store.Int(rows.Values[0][0]))
	db.Close()
	db, err = store.Open(ctx, path) // 重新打开不重复迁移
	require.NoError(t, err)
	_, err = db.Execute(ctx, "INSERT INTO messages_fts(rowid, subject, from_addr, body) VALUES (1, '通知', 'a@b.c', '请查收本月报销单')")
	require.NoError(t, err)
	rows, _ = db.Query(ctx, "SELECT rowid FROM messages_fts WHERE messages_fts MATCH '报销单'")
	assert.Len(t, rows.Values, 1)
}
func TestRefuseNewerDatabase(t *testing.T) {
	// 打开后把 user_version 设为 len(Migrations)+1 → 再次 Open 返回 ErrNewerDatabase，且文件内容（大小与修改时间）不变
}
func TestForeignKeysCascade(t *testing.T) {
	// 插入 account → folder → message → attachment；删除 account 后四张表都为空
}
func TestWriteFileAtomic(t *testing.T) {
	// 写入后内容正确；目录里没有残留的临时文件；CleanOrphans 删除 keep 返回 false 的文件
}
```

- [ ] **Step 3: 运行，确认失败**：`go test ./core/store/` → FAIL

- [ ] **Step 4: 实现**

`Open`：确保父目录存在 → `sqlite.Open` → `PRAGMA foreign_keys = ON`、`PRAGMA journal_mode = WAL` → 读 `user_version`，大于 `len(Migrations)` 返回 `ErrNewerDatabase` → 剩余每个版本的语句加上 `PRAGMA user_version = N` 放进一个 `Transaction`。`WriteFileAtomic`：同目录写 `.tmp-*` 文件、`Sync`、`os.Rename`。

迁移 1 的表（列名即后续任务使用的名字；外键都 `ON DELETE CASCADE`，标注 SET NULL 的除外）：

| 表 | 列 |
|---|---|
| `accounts` | `id INTEGER PK`, `email TEXT NOT NULL UNIQUE`, `name`, `alias`, `signature`, `is_default INTEGER DEFAULT 0`, `sync_limit INTEGER DEFAULT 2000`, `auth_type TEXT DEFAULT 'password'`, `status TEXT DEFAULT 'ok'`, `username`, `imap_host`, `imap_port INTEGER`, `imap_security`, `smtp_host`, `smtp_port INTEGER`, `smtp_security`, `created_at INTEGER` |
| `folders` | `id INTEGER PK`, `account_id` → accounts, `name TEXT`, `role TEXT NOT NULL DEFAULT ''`, `uidvalidity INTEGER`, `last_uid INTEGER DEFAULT 0`；`UNIQUE(account_id, name)` |
| `threads` | `id INTEGER PK`, `account_id` → accounts, `subject TEXT`, `last_date INTEGER`；索引 `(account_id, last_date DESC)` |
| `messages` | `id INTEGER PK`, `account_id` → accounts, `folder_id` → folders, `thread_id` → threads, `uidvalidity INTEGER`, `uid INTEGER`（可空）, `message_id TEXT`, `in_reply_to TEXT`, `refs TEXT`（空格分隔）, `subject`, `from_addr`, `to_addrs`, `cc_addrs`, `reply_to`, `date INTEGER`, `flags TEXT NOT NULL DEFAULT ''`, `size INTEGER`, `snippet`, `body_text TEXT`, `body_html TEXT`, `has_attachments INTEGER`；`UNIQUE(folder_id, uid)`；索引 `(folder_id, date DESC, id DESC)`、`(thread_id)`、`(account_id, message_id)` |
| `attachments` | `id INTEGER PK`, `message_id` → messages, `part TEXT`（MIME 段路径，如 `2.1`）, `filename`, `content_type`, `size INTEGER`, `content_id`, `inline INTEGER`, `cache_path TEXT` |
| `messages_fts` | `fts5(subject, from_addr, body, tokenize='trigram')`，`rowid` = `messages.id` |
| `drafts` | `id INTEGER PK`, `account_id` → accounts, `mode TEXT`（new / reply / reply_all / forward）, `source_message_id` → messages（SET NULL）, `to_addrs`, `cc_addrs`, `bcc_addrs`, `reply_to`, `subject`, `body_html`, `body_text`, `updated_at INTEGER` |
| `draft_attachments` | `id INTEGER PK`, `draft_id` → drafts, `filename`, `content_type`, `size INTEGER`, `path TEXT`（应用目录内的快照） |
| `ops` | `id INTEGER PK`, `account_id` → accounts, `message_id` → messages（SET NULL）, `kind TEXT`（flags / move）, `folder_id INTEGER`, `uidvalidity INTEGER`, `uid INTEGER`, `payload TEXT`（JSON）, `depends_on INTEGER`（前一个操作的 id，可空）, `state TEXT DEFAULT 'pending'`（pending / done / conflict / failed）, `attempts INTEGER DEFAULT 0`, `last_error TEXT`, `created_at INTEGER` |
| `outbox` | `id INTEGER PK`, `account_id` → accounts, `message_id_hdr TEXT`, `raw_path TEXT`, `rcpts TEXT`（JSON）, `state TEXT`（queued / sending / sent / uncertain / done / failed）, `attempts INTEGER DEFAULT 0`, `last_error TEXT`, `created_at INTEGER` |

- [ ] **Step 5: 运行，确认通过**：`go test ./core/store/` → PASS；`CGO_ENABLED=0 go build ./...` 无输出。

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum core/store
git commit -m "feat(store): sqlite with single-statement migrations, version guard, FKs, atomic files"
```

---

### Task 2: MIME 解析与 HTML 清洗

**Files:**
- Create: `core/mail/parse.go`, `core/mail/sanitize.go`
- Test: `core/mail/parse_test.go`, `core/mail/sanitize_test.go`, `core/mail/testdata/*.eml`

**Interfaces:**
- Produces:
  - `mail.Parse(raw []byte) (*mail.Parsed, error)`
  - `type Parsed struct { MessageID, Subject, From, ReplyTo, InReplyTo string; To, Cc, References []string; Date time.Time; Text, HTML string; Attachments []Attachment }`
  - `type Attachment struct { Part, Filename, ContentType, ContentID string; Inline bool; Size int; Data []byte }`（`Part` 为 MIME 段路径；同步时 `Data` 为空）
  - `mail.ParseStructure(bs *imap.BodyStructure) (textParts []string, atts []Attachment)`：从 `BODYSTRUCTURE` 得出要拉取的正文段路径和附件元数据
  - `mail.Sanitize(html string, inline map[string]Attachment, loadRemote bool) (safe string, blockedRemote int)`：`inline` 以 Content-ID 为键
  - `mail.SanitizeCompose(html string) string`：撰写时粘贴内容的清洗（同一白名单，不允许任何远程资源）
  - `mail.Snippet(text string) string`：前 200 个字符，空白折叠

- [ ] **Step 1: 测试样本**：`testdata/` 下放 `alternative.eml`、`attachment.eml`（PDF）、`gbk.eml`（`=?GBK?B?...?=` 标题 + `charset=gbk` 正文）、`big5.eml`、`iso2022jp.eml`、`inline-image.eml`（`<img src="cid:logo">`，附件类型 `image/png`）、`cid-not-image.eml`（`cid:` 指向 `application/pdf`）、`hostile.eml`（`<script>`、`onerror`、`javascript:` 链接、`<form>`、`<iframe>`、`<base href>`、`<meta http-equiv="refresh">`、`style="background:url(https://t.example/a)"`、`<img srcset="https://t.example/b 2x">`、`<img src="//t.example/c">`、`<img src="https://t.example/p.gif">`）。

- [ ] **Step 2: 写失败的测试**

```go
func TestParseCharsets(t *testing.T) {
	assert.Equal(t, "报销单已通过", mustParse(t, "gbk.eml").Subject)
	assert.Contains(t, mustParse(t, "gbk.eml").Text, "请查收发票")
	// big5.eml、iso2022jp.eml 同样断言标题与正文的已知内容
}
func TestParseAttachment(t *testing.T) { /* 1 个非 inline 附件，Filename=invoice.pdf，Part 非空 */ }
func TestSanitizeHostile(t *testing.T) {
	safe, blocked := mail.Sanitize(mustParse(t, "hostile.eml").HTML, nil, false)
	for _, bad := range []string{"<script", "onerror", "javascript:", "<form", "<iframe", "<base", "http-equiv", "url(", "srcset", "t.example"} {
		assert.NotContains(t, safe, bad)
	}
	assert.Equal(t, 4, blocked) // background、srcset、协议相对、普通 img
	safe, _ = mail.Sanitize(mustParse(t, "hostile.eml").HTML, nil, true)
	assert.Contains(t, safe, "https://t.example/p.gif")
	assert.Contains(t, safe, "https://t.example/c") // 协议相对地址加载时改写为 https:
	assert.NotContains(t, safe, "url(")             // CSS 背景图即使允许远程也不放行
}
func TestSanitizeInlineCID(t *testing.T) {
	// inline-image.eml → src 变成 data:image/png;base64,…，不含 cid:
	// cid-not-image.eml → src 被删除（非图片类型不允许内联）
}
func TestSanitizeComposeStripsRemote(t *testing.T) {
	// SanitizeCompose 去掉所有 http(s) 与 // 开头的资源，保留 <b><i><a href><ul><ol><li><blockquote><p><br>
}
```

- [ ] **Step 3: 运行，确认失败**：`go test ./core/mail/` → FAIL

- [ ] **Step 4: 实现 `Parse` / `ParseStructure`**：`go-message/mail.CreateReader` 遍历，`import _ "github.com/emersion/go-message/charset"` 注册编码；记录每段的 MIME 路径。

- [ ] **Step 5: 实现 `Sanitize` / `SanitizeCompose`**

先把 `cid:` 地址换成占位标记（bluemonday 不认识 `cid:` 会删掉），再用 bluemonday：`UGCPolicy()` 为基础，允许表格元素；`style` 只允许这些属性且值里不能含 `url(`、`expression(`：`color`、`background-color`、`font-family`、`font-size`、`font-weight`、`font-style`、`text-align`、`text-decoration`、`line-height`、`margin*`、`padding*`、`border*`、`width`、`height`、`display`、`vertical-align`。然后用 `golang.org/x/net/html` 遍历：去掉所有 `srcset`；`img` 的 `src` 若是 `//` 开头视为 `https:`；占位标记还原为对应附件的 `data:` 地址（只允许 `image/*`，否则删除 `src`）；`loadRemote == false` 时删除所有 `http(s)` 的 `src` 并计数。

- [ ] **Step 6: 运行，确认通过**：`go test ./core/mail/` → PASS

- [ ] **Step 7: Commit**：`git commit -m "feat(mail): parse MIME with charsets and enforce the HTML display contract"`

---

### Task 3: 生成邮件原文与回复规则

**Files:**
- Create: `core/mail/build.go`, `core/mail/reply.go`
- Test: `core/mail/build_test.go`, `core/mail/reply_test.go`

**Interfaces:**
- Consumes: `mail.Parse`、`mail.SanitizeCompose`（Task 2）
- Produces:
  - `type Outgoing struct { From, ReplyTo string; To, Cc, Bcc []string; Subject, HTML string; InReplyTo string; References []string; Attachments []Attachment }`
  - `mail.Build(o Outgoing, now time.Time) (raw []byte, messageID string, err error)`：HTML 经 `SanitizeCompose`；生成 `multipart/alternative`（HTML + 由 HTML 转出的纯文本）；有附件时外层 `multipart/mixed`；**`Bcc` 不写入头部**；Message-ID 为 `<随机 16 字节 hex@发件人域名>`
  - `mail.HTMLToText(html string) string`
  - `mail.ReplyRecipients(orig *Parsed, self string, all bool) (to, cc []string)`：优先 `Reply-To`；`all` 时把原 To / Cc 并入 Cc；去掉自己的地址（大小写不敏感）并去重
  - `mail.QuoteHTML(orig *Parsed) string`：「于 {日期}，{发件人} 写道：」加 `<blockquote>` 引用
  - `mail.ReplySubject(s string) string`、`mail.ForwardSubject(s string) string`：已有 `Re:` / `回复：` / `Fwd:` / `转发：` 前缀时不重复添加

- [ ] **Step 1: 写失败的测试**

```go
func TestBuildRoundTrip(t *testing.T) {
	// 中文标题、HTML 正文（含 <b>）、To 两人、Cc 一人、Bcc 一人、一个附件、InReplyTo、References
	// Parse 回来：Subject / To / Cc / InReplyTo / References / 附件一致；HTML 含 <b>；Text 非空且不含标签
	// 原文中不出现 Bcc 的地址；Parsed.MessageID == 返回的 messageID
}
func TestReplyRecipients(t *testing.T) {
	// 原邮件 From=a, To=[me, b], Cc=[c, ME@X]，self=me@x
	// all=false → to=[a]，cc=[]；all=true → to=[a]，cc=[b, c]（不含自己，不重复）
	// 原邮件有 Reply-To=r → to=[r]
}
func TestSubjects(t *testing.T) {
	assert.Equal(t, "Re: Q4", mail.ReplySubject("Re: Q4"))
	assert.Equal(t, "Re: 报销", mail.ReplySubject("报销"))
	assert.Equal(t, "Fwd: Q4", mail.ForwardSubject("Q4"))
}
```

- [ ] **Step 2: 运行，确认失败**：`go test ./core/mail/ -run 'Build|Reply|Subject'` → FAIL

- [ ] **Step 3: 实现**：`go-message/mail.CreateWriter`；标题用 `SetSubject`（RFC 2047）。

- [ ] **Step 4: 运行，确认通过** → PASS

- [ ] **Step 5: Commit**：`git commit -m "feat(mail): build HTML messages and reply/forward helpers"`

---

### Task 4: 界面事件总线

**Files:** Create `core/events/bus.go`；Test `core/events/bus_test.go`

**Interfaces:**
- Produces:
  - `type Event struct { Type string; AccountID, FolderID int64; ThreadIDs, MessageIDs []int64; Error string }`
  - 常量：`MessageReceived = "message.received"`、`MessagesChanged = "messages.changed"`、`SyncStarted`、`SyncFinished`、`SyncFailed`、`OutboxChanged = "outbox.changed"`
  - `events.New() *Bus`；`(*Bus).Publish(Event)`；`(*Bus).Subscribe() (<-chan Event, func())`

这个总线只给界面用，允许丢事件。插件 Hook 的可靠投递不走这里，而是在插件计划里按数据库游标实现（spec 5.3）。

- [ ] **Step 1: 写失败的测试**：`TestFanOut`；`TestSlowSubscriberDoesNotBlock`（不读的订阅者不阻塞 1000 次 `Publish`，另一个订阅者收到最后一条）；`TestUnsubscribeClosesChannel`。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**：每订阅者容量 64 的 channel，满了丢最旧的一条（`ponytail:` 注释：仅用于界面，可靠投递见插件计划）。
- [ ] **Step 4: 运行，确认通过** → PASS
- [ ] **Step 5: Commit**：`git commit -m "feat(events): lossy fan-out bus for UI notifications"`

---

### Task 5: Service 骨架、测试服务器与账户管理

**Files:**
- Create: `core/types.go`, `core/service.go`, `core/accounts.go`, `core/internal/testmail/imap.go`, `core/internal/testmail/smtp.go`, `core/helpers_test.go`
- Test: `core/accounts_test.go`

**Interfaces:**
- Consumes: `store.Open`（Task 1）、`events.New`（Task 4）
- Produces:
  - `type Options struct { DataDir string; AutoSync bool }`；`core.New(ctx, Options) (*Service, error)`；`(*Service).Close() error`；`(*Service).Subscribe() (<-chan events.Event, func())`
  - `type Security string`：`SecurityTLS`、`SecurityStartTLS`、`SecurityNone`
  - `type AccountInput struct { Email, Name, Alias, Signature, Username, Password string; IsDefault bool; SyncLimit int; IMAPHost string; IMAPPort int; IMAPSecurity Security; SMTPHost string; SMTPPort int; SMTPSecurity Security }`（`SyncLimit` 为 0 时取 2000；第一个账户自动成为默认发件账户）
  - `type Account`：同上去掉 `Password`，加 `ID int64`、`AuthType`、`Status string`
  - `(*Service).TestAccount(ctx, AccountInput) error`；`AddAccount(ctx, AccountInput) (Account, error)`；`ListAccounts(ctx) ([]Account, error)`；`RemoveAccount(ctx, id int64) error`
  - 本计划只实现密码 / 授权码登录；OAuth 在「账户接入」计划。IMAP 认证失败时把 `status` 置为 `reauth_required` 并发 `SyncFailed`。
  - 测试辅助：`testmail.StartIMAP(t) *testmail.IMAP`（字段 `Addr`、`User *imapmemserver.User`；方法 `Stop()`、`Restart()`、`DropNextFetch()`（下一次 FETCH 中途断开））；用户 `alice` / `secret`，预建 `INBOX`、`Sent`、`Trash`、`Archive`
  - `testmail.StartSMTP(t) (addr string, in *testmail.Inbox)`；`Inbox.Messages() []testmail.Received`；`Inbox.FailAfterData(n int)`（接下来 n 次在收完 DATA 后直接断开连接，不回 250）
  - `newTestService(t) *core.Service`：`keyring.MockInit()`，`DataDir` 用 `t.TempDir()`，`AutoSync: false`

- [ ] **Step 1: 写失败的测试**：`TestAddAccount`（钥匙串 `godwit` / `account:<id>` 存了密码）；`TestAddAccountWrongPassword`（返回错误，库和钥匙串都没有残留，错误信息不含密码）；`TestKeyringFailureRollsBack`（让钥匙串写入失败 → 账户行也不存在）；`TestPlaintextOnlyForLoopback`；`TestRemoveAccount`（库、钥匙串、`DataDir/cache/<id>` 都清掉）。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现测试服务器**：IMAP 用 `imapserver.New` + `imapmemserver`，`InsecureAuth: true`，监听 `127.0.0.1:0`；SMTP 用 `go-smtp` 写最小 `Backend`。
- [ ] **Step 4: 实现账户管理**：校验规则同 spec；`SecurityNone` 只允许 `localhost`、`127.0.0.1`、`::1`。`AddAccount` 顺序：`TestAccount` → 插入行 → 写钥匙串，失败则删除该行。
- [ ] **Step 5: 运行，确认通过** → PASS
- [ ] **Step 6: Commit**：`git commit -m "feat(core): service skeleton, accounts and test mail servers"`

---

### Task 6: IMAP 拉取同步与会话归并

**Files:**
- Create: `core/sync/fetch.go`, `core/sync/worker.go`, `core/mail/thread.go`
- Modify: `core/service.go`（`SyncNow`）
- Test: `core/sync_test.go`, `core/mail/thread_test.go`

**Interfaces:**
- Consumes: Task 1、2、4、5
- Produces:
  - `sync.Fetch(ctx, db *store.DB, c *imapclient.Client, accountID int64, bus *events.Bus) error`
  - `(*Service).SyncNow(ctx, accountID int64) error`：取得账户级互斥锁 → 连接 → 推送操作（Task 8 之前为空）→ `Fetch`
  - `(*Service).ListFolders(ctx, accountID int64) ([]Folder, error)`；`type Folder struct { ID, AccountID int64; Name, Role string; Unread int }`
  - `mail.ThreadKeys(p *Parsed) (ids []string, subjectKey string)`：`ids` = 自身 Message-ID + In-Reply-To + References；`subjectKey` = 去掉 Re/Fwd/回复/转发 前缀、空白折叠后的小写主题

- [ ] **Step 1: 写失败的测试**

```go
func TestSyncNewMessages(t *testing.T) {
	// INBOX 追加 3 封 → SyncNow：folders 含四种 role；messages 3 行，body_text、snippet 正确；
	// 带附件的那封 attachments 有 1 行且 cache_path 为空（未下载）；FTS 能搜到正文；收到 1 次 MessageReceived（3 个 ID）
	// 再次 SyncNow：不新增行、不再发 MessageReceived（覆盖 last_uid+1:* 在无新邮件时返回最大旧 UID 的情况）
}
func TestSyncRemoteFlagsAndExpunge(t *testing.T) { /* 远端标记 \Seen、删除并 expunge 一封 → 本地对应变化，FTS 条目也删除 */ }
func TestSyncIncompleteFetchDoesNotDelete(t *testing.T) {
	// 同步后 DropNextFetch() → SyncNow 返回错误；本地 3 封一封不少
}
func TestSyncUIDValidityChange(t *testing.T) { /* 在 Archive 上做：删除重建（新 UIDVALIDITY）+ 追加 2 封 → 只剩新的 2 行，旧附件缓存与 FTS 条目清掉 */ }
func TestThreading(t *testing.T) {
	// INBOX：A（Message-ID a）、B（In-Reply-To a）；Sent：C（References a b）；另一封 D 无引用头但主题 "Re: 同一主题" 与 A 相同且参与者重叠、时间在 30 天内
	// 同步两个文件夹后 A、B、C、D 的 thread_id 相同；主题相同但参与者不重叠的 E 是另一个会话
}
func TestSyncNowSerializesPerAccount(t *testing.T) {
	// 并发调用两次 SyncNow：都成功，没有重复行（-race 下运行）
}
```

- [ ] **Step 2: 运行，确认失败** → FAIL

- [ ] **Step 3: 实现 `sync.Fetch`**，对每个文件夹：
1. `LIST` 得到文件夹，`Role` 取 SPECIAL-USE，没有时按名字匹配（`INBOX`；`Sent`/`Sent Items`/`已发送`；`Trash`/`Deleted Items`/`已删除`；`Drafts`/`草稿`；`Junk`/`Spam`/`垃圾邮件`；`Archive`/`归档`）。服务器上已不存在的文件夹删除。
2. `SELECT`；UIDVALIDITY 与库里不同 → 删除该文件夹的邮件行（级联清附件、FTS 手动删）、清对应缓存文件，`last_uid` 归零，并把该文件夹上 `pending` 的操作标为 `conflict`。
3. `UID FETCH (last_uid+1):* (UID FLAGS RFC822.SIZE ENVELOPE BODYSTRUCTURE BODY.PEEK[HEADER.FIELDS (REFERENCES IN-REPLY-TO REPLY-TO)])`，**丢弃 `uid <= last_uid` 的结果**；首次同步只取最近 `sync_limit` 封。对每封用 `ParseStructure` 得到正文段，再 `UID FETCH uid (BODY.PEEK[段]...)` 拉正文文本 / HTML（解码后存库）。一个 `Transaction` 内插入 `messages`、`attachments`、`messages_fts`（`rowid` 用 `last_insert_rowid()`）。
4. 会话：按 `ThreadKeys` 在同账户内找已有 `message_id` 命中 → 用其 `thread_id`；否则按 `subjectKey` + 参与者有交集 + 30 天内找；都没有则新建 `threads` 行。更新 `threads.last_date`。
5. `UID FETCH 1:last_uid (UID FLAGS)`：**只有完整返回时**才更新标记、删除未返回的 UID（`ponytail:` CONDSTORE/QRESYNC 以后替换）；连接中断则整轮作废，返回错误。
6. 发 `MessageReceived` / `MessagesChanged`。

- [ ] **Step 4: 运行，确认通过**：`go test -race ./core/ -run 'TestSync|TestThreading'` → PASS

- [ ] **Step 5: Commit**：`git commit -m "feat(sync): headers-and-text fetch, lazy attachments, threading, safe expunge"`

---

### Task 7: 读取：会话、邮件、搜索、附件

**Files:** Create `core/threads.go`、`core/messages.go`（读取部分）；Test `core/read_test.go`

**Interfaces:**
- Produces:
  - `type View struct { FolderID int64; Unified string }`（`Unified` 为 `inbox` / `starred`，与 `FolderID` 二选一）
  - `type ThreadSummary struct { ID int64; Subject, Snippet string; Participants []string; Count, Unread int; LastDate time.Time; HasAttachments bool }`
  - `type ThreadPage struct { Items []ThreadSummary; NextCursor string }`
  - `(*Service).ListThreads(ctx, v View, cursor string, limit int) (ThreadPage, error)`：按 `last_date DESC, id DESC`；`limit` 上限 200
  - `type Thread struct { ID int64; Subject string; Messages []MessageSummary }`；`(*Service).GetThread(ctx, id int64) (Thread, error)`：按日期升序
  - `type MessageSummary struct { ID, ThreadID, FolderID int64; Subject, From, Snippet string; To, Cc []string; Date time.Time; Flags []string; HasAttachments bool }`
  - `type Message struct { MessageSummary; ReplyTo string; HTML, Text string; RemoteImagesBlocked int; Attachments []AttachmentInfo }`；`type AttachmentInfo struct { ID int64; Filename, ContentType string; Size int; Inline bool }`
  - `(*Service).GetMessage(ctx, id int64, loadRemoteImages bool) (Message, error)`：inline 图片需要时先下载（走 `GetAttachment` 的同一路径）再交给 `Sanitize`
  - `(*Service).Search(ctx, accountID int64, query string, limit int) ([]MessageSummary, error)`
  - `(*Service).GetAttachment(ctx, attachmentID int64) (path string, info AttachmentInfo, err error)`：未缓存时 `UID FETCH uid (BODY.PEEK[part])`，解码后 `WriteFileAtomic` 到 `DataDir/cache/<account>/<attachmentID>`，再写 `cache_path`；超过 `MaxMessageBytes` 返回 `ErrTooLarge`

- [ ] **Step 1: 写失败的测试**

```go
func TestListThreadsPaging(t *testing.T) { /* 5 个会话分页 2/2/1，顺序正确无重复；Count、Unread 正确；Unified=inbox 跨两个账户 */ }
func TestGetThreadOrder(t *testing.T) { /* 会话内按日期升序，含已发送文件夹里的自己的回复 */ }
func TestGetMessageSanitized(t *testing.T) { /* hostile.eml：符合 Task 2 的断言；RemoteImagesBlocked == 4 */ }
func TestSearchChineseBodyOnly(t *testing.T) {
	// 一封邮件：标题「通知」，发件人 finance@x，正文「请查收本月发票和报销单」
	// Search("发票") 命中（2 个字，只在正文）；Search("报销单") 命中；Search("不存在") 为空
}
func TestSearchQueryIsNotFTSSyntax(t *testing.T) { /* Search(`"unbalanced AND (`) 不报错 */ }
func TestGetAttachmentLazy(t *testing.T) {
	// 同步后 cache_path 为空 → GetAttachment 下载，文件内容与原附件一致，cache_path 写入；再次调用不再访问服务器（Stop 服务器后仍能返回）
}
```

- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**：搜索时查询少于 3 个字符（按 rune 计）用 `LIKE '%q%'` 查 `subject`、`from_addr`、`body_text`（转义 `%` `_`；`ponytail:` 全表扫描，量大时换成 bigram 索引）；否则包成 FTS5 短语后 `MATCH`。都限定在该账户、按日期倒序。
- [ ] **Step 4: 运行，确认通过** → PASS
- [ ] **Step 5: Commit**：`git commit -m "feat(core): threads, message reading, search and lazy attachments"`

---

### Task 8: 本地优先的修改与操作队列

**Files:** Create `core/sync/ops.go`；Modify `core/messages.go`、`core/sync/worker.go`；Test `core/ops_test.go`

**Interfaces:**
- Produces:
  - `(*Service).SetFlags(ctx, ids []int64, add, remove []string) error`
  - `(*Service).Move(ctx, ids []int64, folderID int64) error`（目标须属同一账户）
  - `(*Service).Delete(ctx, ids []int64) error`：移到 Role 为 `trash` 的文件夹；已在 Trash 的不做任何事
  - `(*Service).PendingOps(ctx, accountID int64) ([]Op, error)`；`type Op struct { ID int64; Kind, State string; Attempts int; LastError string }`
  - `sync.PushOps(ctx, db, c, accountID) error`

**规则**（对应 spec 第 4 节原则 6、7）：
- 每个操作记录创建时邮件所在的 `folder_id`、`uidvalidity`、`uid`；同一封邮件已有未完成操作时，新操作的 `depends_on` 指向它。
- 本地移动：只改 `messages.folder_id`，`uid` 置空、`uidvalidity` 置空；**本地 ID 不变**。
- 推送：按 id 顺序；`depends_on` 未完成的跳过；`SELECT` 目标文件夹并核对 `uidvalidity`，不一致标 `conflict`（发 `SyncFailed` 带说明）。依赖的移动操作完成后，用它得到的新位置补全本操作的 `folder_id/uidvalidity/uid`。
- `UID MOVE`：有 COPYUID 时写回新 `uid`、`uidvalidity`；没有时保持 `uid` 为空，下次拉取目标文件夹时，对 `uid` 为空且 `message_id` 相同的本地邮件**认领**新 UID，不新建行。服务器不支持 MOVE 时用 `UID COPY` + `STORE \Deleted` + `UID EXPUNGE`（需 UIDPLUS），都不支持则标 `failed`。
- 失败 `attempts+1`、记录 `last_error`；满 5 次标 `failed`，不再执行，发 `SyncFailed`。
- 远端拉取的标记与本地 `pending` 的 flags 操作冲突时，以本地待推送的结果为准（拉取时跳过有未完成 flags 操作的邮件）。

- [ ] **Step 1: 写失败的测试**

```go
func TestSetFlagsIsLocalFirst(t *testing.T) { /* 服务器停掉 → SetFlags 立即成功、GetMessage 反映、PendingOps 1 条 */ }
func TestMoveThenFlagOffline(t *testing.T) {
	// 离线：Move 到 Archive，再 SetFlags \Seen → Close → 同 DataDir 重开 → 服务器恢复 → SyncNow
	// 服务器上邮件在 Archive 且带 \Seen；本地 ID 与移动前相同；Archive 里只有 1 行
}
func TestMoveWithoutUIDPLUS(t *testing.T) { /* 关闭测试服务器的 UIDPLUS → 同上，结果相同（靠 Message-ID 认领） */ }
func TestStaleUIDNeverReplayed(t *testing.T) {
	// 离线 SetFlags（针对 Archive 里 UID 1）→ 服务器上重建 Archive 并放入另一封（同样 UID 1）→ SyncNow
	// 新邮件没有被打上 \Seen；该操作 State == "conflict"
}
func TestRemoteFlagsDoNotOverwritePending(t *testing.T) { /* 本地待推送 \Seen，远端同时去掉 \Seen → SyncNow 后本地与服务器都是 \Seen */ }
func TestFailedOpKeepsError(t *testing.T) { /* 目标文件夹在服务器上已删除 → attempts 递增、last_error 非空；满 5 次 State == "failed" 并发 SyncFailed */ }
func TestDeleteMovesToTrash(t *testing.T) { /* 进 Trash；Trash 里再删仍在 Trash */ }
```

- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**（每次修改在一个 `Transaction` 内同时改 `messages` 和插入 `ops`）
- [ ] **Step 4: 运行，确认通过**：`go test -race ./core/ -run 'Flags|Move|Stale|Remote|FailedOp|Delete'` → PASS
- [ ] **Step 5: Commit**：`git commit -m "feat(core): local-first ops with stable identity, dependencies and UIDVALIDITY guard"`

---

### Task 9: 草稿与发送

**Files:** Create `core/compose.go`, `core/outbox.go`, `core/send/smtp.go`；Modify `core/sync/worker.go`（处理发件箱）；Test `core/compose_test.go`

**Interfaces:**
- Consumes: `mail.Build`、`mail.ReplyRecipients`、`mail.QuoteHTML`、`mail.ReplySubject`、`mail.ForwardSubject`（Task 3）、`store.WriteFileAtomic`（Task 1）
- Produces:
  - `type Draft struct { ID, AccountID int64; Mode string; SourceMessageID int64; To, Cc, Bcc []string; ReplyTo, Subject, HTML string; Attachments []DraftAttachment; UpdatedAt time.Time }`；`type DraftAttachment struct { ID int64; Filename, ContentType string; Size int }`
  - `(*Service).NewDraft(ctx, accountID int64, mode string, sourceMessageID int64) (Draft, error)`：reply / reply_all 填好收件人、`ReplySubject`、`QuoteHTML`；forward 填 `ForwardSubject`、引用正文，并把原邮件的非 inline 附件复制进草稿（需要时先下载）；账户签名追加在正文末尾
  - `(*Service).SaveDraft(ctx, Draft) (Draft, error)`；`AddAttachment(ctx, draftID int64, srcPath string) (DraftAttachment, error)`（复制进 `DataDir/drafts/<id>/`，超过 `MaxMessageBytes` 返回 `ErrTooLarge`）；`RemoveAttachment(ctx, draftID, attachmentID int64) error`
  - `(*Service).Send(ctx, draftID int64) error`：校验收件人（To/Cc/Bcc 至少一个且地址合法）与总大小 → `Build` → `WriteFileAtomic` 原文到 `DataDir/outbox/<id>.eml` → 一个 `Transaction` 内插入 `outbox`（`queued`）并删除草稿及其附件记录 → 删除草稿附件目录
  - `type OutboxItem struct { ID int64; Subject string; State, LastError string; CreatedAt time.Time }`；`(*Service).ListOutbox(ctx, accountID int64) ([]OutboxItem, error)`
  - `(*Service).ResolveOutbox(ctx, id int64, action string) error`：`action` 为 `confirm_sent`（视为已发出，进入存入已发送）/ `resend`（回到 `queued`）/ `discard`（删除）
  - `send.Send(ctx, a Account, password string, from string, rcpts []string, raw []byte) error`：返回的错误能区分「提交前失败」（可安全重试）和「提交后结果未知」（`send.ErrUncertain`）

**发件箱状态机**（spec 第 4 节原则 8）：`queued → sending`（写库后才连接 SMTP）→ 成功 `sent` / 提交前失败回 `queued`（`attempts+1`，满 5 次 `failed`）/ `ErrUncertain` 或启动时发现停在 `sending` → `uncertain`。`uncertain` 时 worker 去 Sent 文件夹 `UID SEARCH HEADER Message-ID <id>`：找到则直接 `done`；找不到则保持 `uncertain` 等用户 `ResolveOutbox`，**绝不自动重发**。`sent → APPEND` 到 Sent（带 `\Seen`）→ `done`，删除原文文件；APPEND 失败只重试 APPEND。

- [ ] **Step 1: 写失败的测试**

```go
func TestReplyAllDraft(t *testing.T) { /* NewDraft(reply_all)：收件人符合 ReplyRecipients；主题 Re:；正文含 blockquote 和签名 */ }
func TestForwardCarriesAttachments(t *testing.T) { /* 转发带附件的邮件：草稿附件 1 个，文件在 DataDir/drafts/<id>/ 下，内容一致 */ }
func TestSendHTMLWithBcc(t *testing.T) {
	// HTML 正文 + Bcc 一人 → Send → SyncNow：SMTP 收到 1 次，RCPT 含 Bcc 地址，原文头部不含 Bcc；
	// 解析为 multipart/alternative；InReplyTo 正确；Sent 里有这封；outbox 为空；草稿附件目录已删除
}
func TestUncertainSendIsNotResent(t *testing.T) {
	// FailAfterData(1)（服务器收下了邮件但不回 250）→ Send → SyncNow：State == "uncertain"
	// 再 SyncNow：SMTP 总共只收到 1 次
	// 测试里往 Sent 放入同 Message-ID 的邮件 → SyncNow：State == "done"
}
func TestCrashDuringSendingBecomesUncertain(t *testing.T) { /* 直接把 outbox 行写成 sending → 重开 Service → State == "uncertain" */ }
func TestResolveOutboxResend(t *testing.T) { /* uncertain → ResolveOutbox(resend) → SyncNow → SMTP 再收到 1 次，State == "done" */ }
func TestSendValidates(t *testing.T) { /* 无收件人、地址非法、附件超限 → 返回错误，草稿保留，outbox 为空 */ }
```

- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行，确认通过** → PASS
- [ ] **Step 5: Commit**：`git commit -m "feat(core): HTML drafts, reply/forward and at-most-once outbox"`

---

### Task 10: 后台同步 worker

**Files:** Modify `core/sync/worker.go`, `core/service.go`；Test `core/worker_test.go`

**Interfaces:**
- Produces：`Options.AutoSync == true` 时 `New` 为每个账户启动 worker，`AddAccount` / `RemoveAccount` 启停对应 worker，`Close` 停止全部并等待退出。本地修改与 `Send` 后立即唤醒对应 worker。worker 与 `SyncNow` 共用 Task 6 的账户级互斥锁。启动时 `store.CleanOrphans` 清理 `cache/`、`drafts/`、`outbox/` 下没有对应记录的文件。

- [ ] **Step 1: 写失败的测试**：`TestWorkerPicksUpNewMail`（5 秒内收到 `MessageReceived`）；`TestWorkerPushesLocalChangesPromptly`；`TestWorkerReconnectsAfterServerRestart`（`Stop` → `SyncFailed` → `Restart` → 30 秒内收到新邮件）；`TestCloseStopsWorkers`（5 秒内返回，`-race` 下无数据竞争）；`TestStartupCleansOrphans`。
- [ ] **Step 2: 运行，确认失败**：`go test -race ./core/ -run 'Worker|CloseStops|Orphans'` → FAIL
- [ ] **Step 3: 实现**：循环 `SyncNow` + 处理发件箱 → 支持 IDLE 时对 INBOX IDLE 最多 25 分钟，否则等 5 分钟；收到 IDLE 通知或本地唤醒立即进入下一轮；失败发 `SyncFailed`，按 2、4、8… 秒退避，上限 5 分钟（包级变量，测试中调低）。
- [ ] **Step 4: 运行，确认通过**：`go test -race ./core/...` 全部 PASS；`CGO_ENABLED=0 go build ./...` 无输出
- [ ] **Step 5: Commit**：`git commit -m "feat(sync): background workers with IDLE, wake-ups, backoff and orphan cleanup"`
