# Godwit 核心邮件引擎 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现 `core` 库里与邮件相关的部分：存储、MIME 解析与清洗、账户、IMAP 同步、本地优先的修改、草稿与发送、事件。完成后，不需要任何 GUI 就能通过 Go 测试完整地收发、搜索、整理邮件。

**Architecture:** 所有能力挂在 `core.Service` 上，这是唯一对外的入口（后续 GUI、JSON-RPC、MCP 都只调用它）。数据存 mygo sqlite（Go API），原始 .eml 存磁盘；每个账户一个同步 worker，本地修改先写库和操作队列，再由 worker 推到服务器。

**Tech Stack:** Go 1.27.1；`github.com/egoist/mygo/plugins/sqlite`（mygo v0.3.0）；`github.com/emersion/go-imap/v2` v2.0.0-beta.8；`github.com/emersion/go-smtp` v0.25.0；`github.com/emersion/go-message` v0.18.2；`github.com/microcosm-cc/bluemonday` v1.0.27；`github.com/zalando/go-keyring` v0.2.8；`golang.org/x/net/html`。

**Spec:** `docs/superpowers/specs/2026-10-08-godwit-design.md`

### 计划拆分

spec 包含多个相互独立的子系统，按依赖顺序拆成五份计划，每份完成后都能单独测试：

1. **核心邮件引擎**（本文件）
2. AI 与 Skill：模型服务（OpenAI / Anthropic 兼容）、Skill 加载与 UI 绑定、`RunSkill`、`Ask`
3. 插件运行时：先做 QuickJS-on-wazero 与 goja 的对比验证，再做 manifest、权限、配置声明、Hook、actions/cards/tools
4. GUI：mygo 托盘应用、邮件阅读的沙箱 iframe、设置页
5. Headless：`--headless` 下的 JSON-RPC 与 MCP

## Global Constraints

- Go 版本由 `mise.toml` 固定为 1.27.1；module 路径 `github.com/godwitmail/godwit`。
- 不使用 cgo：`CGO_ENABLED=0 go build ./...` 和 `CGO_ENABLED=0 go test ./...` 必须通过。`go test -race` 本身需要 cgo，只在本机开发时使用，不算违反。
- 存储只用 mygo sqlite 插件的 Go API（`sqlite.Open` / `Execute` / `Query` / `Transaction`），**不调用** `mygo.Use(sqlite.Plugin)`。
- 迁移用 `PRAGMA user_version` 加一个按顺序执行的 SQL 列表。
- 对外不暴露 IMAP UID，只用内部 `int64` ID。
- `Delete` 只是移到 Trash 角色的文件夹，Service 不提供硬删除。
- HTML 在 core 层清洗，所有出口拿到的都是安全内容。
- 密码只存系统钥匙串（go-keyring），不进 SQLite、不进日志、不进错误信息。
- 测试里 SQLite 原生库由 mygo 自动下载到 CLI 缓存；离线或 CI 环境设置 `MYGO_SQLITE_LIBRARY`。
- 与 spec 的有意差异：spec 写了 CONDSTORE 增量同步，v1 先每次全量拉取已同步范围内的 FLAGS，CONDSTORE 作为后续优化（代码里用 `ponytail:` 注释标出）。

## Review Focus

1. **中文搜索**：用户搜「发票」「报销单」时必须能命中正文和标题。FTS5 用 `trigram` 分词；少于 3 个字符的查询退回对标题和发件人做 `LIKE`。→ Task 7。
2. **非 UTF-8 编码**：GBK、GB2312、Big5、ISO-2022-JP 的标题和正文要正确解码，不能出现乱码。需要导入 `go-message/charset`。→ Task 2。
3. **UIDVALIDITY 变化**：服务器重建文件夹后，本地不能出现重复邮件，也不能残留失效的邮件。→ Task 6。
4. **其他客户端的修改**：在手机上标记已读或删除邮件后，下次同步要反映到本地。→ Task 6。
5. **离线修改后重启**：断网时做的标记、移动，在应用重启后仍会推送到服务器，失败的操作保留错误信息而不是丢弃。→ Task 8。

---

## File Structure

```
go.mod
core/
  service.go            Service、Options、New、Close、SyncNow；把各部分组装起来
  types.go              对外的数据类型：Account、Folder、MessageSummary、Message、Draft、Event 等
  accounts.go           账户的增删查、校验、TestAccount
  messages.go           ListMessages、GetMessage、Search、GetAttachment、SetFlags、Move、Delete
  compose.go            SaveDraft、SendDraft
  events/bus.go         事件总线
  store/store.go        打开数据库、迁移
  store/schema.go       迁移 SQL 列表
  store/query.go        对 sqlite.Rows 的取值辅助函数
  mail/parse.go         MIME 解析
  mail/sanitize.go      HTML 清洗、远程图片拦截、cid 内联图片替换
  mail/build.go         由草稿生成 RFC 5322 原文
  sync/worker.go        每账户的同步 worker：连接、拉取、推送操作、IDLE
  sync/fetch.go         文件夹与邮件的拉取逻辑
  sync/ops.go           操作队列的推送
  send/smtp.go          SMTP 发送
  internal/testmail/    测试用的内存 IMAP 服务器与捕获型 SMTP 服务器
```

---

### Task 1: 项目骨架与存储层

**Files:**
- Create: `go.mod`, `core/store/store.go`, `core/store/schema.go`, `core/store/query.go`
- Test: `core/store/store_test.go`

**Interfaces:**
- Produces:
  - `store.Open(ctx context.Context, path string) (*store.DB, error)`：打开数据库并执行未执行的迁移。
  - `type DB struct{ *sqlite.DB }`：直接嵌入 mygo 的 `*sqlite.DB`，后续任务直接用它的 `Execute` / `Query` / `Transaction`。
  - `store.Migrations []string`
  - `store.Int(v any) int64`、`store.Str(v any) string`：把 `Rows.Values` 里的单元格转成 Go 值（nil 视为零值）。

- [ ] **Step 1: 初始化 module**

`go mod init github.com/godwitmail/godwit`，`go get github.com/egoist/mygo@v0.3.0`。

- [ ] **Step 2: 写失败的测试 `TestOpenMigrates`**

```go
func TestOpenMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "godwit.db")
	db, err := store.Open(ctx, path)
	require.NoError(t, err)
	rows, _ := db.Query(ctx, "PRAGMA user_version")
	assert.Equal(t, int64(len(store.Migrations)), store.Int(rows.Values[0][0]))
	db.Close()

	db, err = store.Open(ctx, path) // 再次打开不重复执行迁移
	require.NoError(t, err)
	_, err = db.Execute(ctx, "INSERT INTO messages_fts(rowid, subject, from_addr, body) VALUES (1, '报销单', 'a@b.c', '发票')")
	require.NoError(t, err)
	rows, _ = db.Query(ctx, "SELECT rowid FROM messages_fts WHERE messages_fts MATCH '报销单'")
	assert.Len(t, rows.Values, 1)
}
```

测试断言库统一用 `github.com/stretchr/testify`。

- [ ] **Step 3: 运行，确认失败**

Run: `go test ./core/store/ -run TestOpenMigrates`
Expected: FAIL（`store.Open` 未定义）

- [ ] **Step 4: 实现 `store.Open` 和迁移 1**

`Open` 先确保父目录存在，再 `sqlite.Open`，读 `user_version`，把剩余迁移和 `PRAGMA user_version = N` 放进同一个 `Transaction`。连接打开后执行 `PRAGMA journal_mode = WAL`。

迁移 1 建这些表（列名就是后续任务使用的名字）：

| 表 | 列 |
|---|---|
| `accounts` | `id INTEGER PK`, `email TEXT NOT NULL UNIQUE`, `name`（发件人名称）, `alias`（侧栏显示名称）, `signature TEXT`, `is_default INTEGER DEFAULT 0`, `sync_limit INTEGER DEFAULT 2000`, `auth_type TEXT DEFAULT 'password'`, `status TEXT DEFAULT 'ok'`（`ok` / `reauth_required` / `error`）, `username`, `imap_host`, `imap_port INTEGER`, `imap_security`, `smtp_host`, `smtp_port INTEGER`, `smtp_security`, `created_at INTEGER` |
| `folders` | `id INTEGER PK`, `account_id`（外键，级联删除）, `name TEXT`, `role TEXT NOT NULL DEFAULT ''`, `uidvalidity INTEGER`, `last_uid INTEGER DEFAULT 0`；`UNIQUE(account_id, name)` |
| `messages` | `id INTEGER PK`, `folder_id`（外键，级联删除）, `uid INTEGER`（可空）, `message_id TEXT`, `subject`, `from_addr`, `to_addrs`, `date INTEGER`, `flags TEXT NOT NULL DEFAULT ''`（空格分隔）, `has_attachments INTEGER`, `snippet`, `raw_path TEXT`；`UNIQUE(folder_id, uid)`；索引 `(folder_id, date DESC, id DESC)` |
| `messages_fts` | `fts5(subject, from_addr, body, tokenize='trigram')` |
| `drafts` | `id INTEGER PK`, `account_id`, `to_addrs`, `cc_addrs`, `subject`, `body_text`, `in_reply_to`（引用的内部 message ID，可空）, `attachments TEXT`（JSON：文件路径数组）, `updated_at INTEGER` |
| `ops` | `id INTEGER PK`, `account_id`, `kind TEXT`（`flags` / `move` / `send`）, `payload TEXT`（JSON）, `attempts INTEGER DEFAULT 0`, `last_error TEXT`, `created_at INTEGER` |

- [ ] **Step 5: 运行，确认通过**

Run: `go test ./core/store/` → PASS；再跑 `CGO_ENABLED=0 go build ./...` → 无输出。

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum core/store
git commit -m "feat(store): open sqlite with user_version migrations and trigram FTS"
```

---

### Task 2: MIME 解析与 HTML 清洗

**Files:**
- Create: `core/mail/parse.go`, `core/mail/sanitize.go`
- Test: `core/mail/parse_test.go`, `core/mail/sanitize_test.go`, `core/mail/testdata/*.eml`

**Interfaces:**
- Produces:
  - `mail.Parse(raw []byte) (*mail.Parsed, error)`
  - `type Parsed struct { MessageID, Subject, From string; To, Cc []string; Date time.Time; InReplyTo string; References []string; Text, HTML string; Attachments []Attachment }`
  - `type Attachment struct { Filename, ContentType, ContentID string; Inline bool; Data []byte }`
  - `mail.Sanitize(html string, inline map[string]Attachment, loadRemote bool) (safe string, blockedRemote bool)`：`inline` 以 Content-ID（不含尖括号）为键。
  - `mail.Snippet(p *Parsed) string`：纯文本前 200 个字符，空白折叠。

- [ ] **Step 1: 准备测试样本**

`testdata/` 下放：`alternative.eml`（text + html）、`attachment.eml`（含 PDF 附件）、`gbk.eml`（标题是 `=?GBK?B?...?=`，正文 `Content-Type: text/plain; charset=gbk`）、`inline-image.eml`（HTML 里 `<img src="cid:logo">`）、`hostile.eml`（HTML 里有 `<script>`、`onerror`、`javascript:` 链接、`<img src="https://tracker.example/p.gif">`）。

- [ ] **Step 2: 写失败的测试**

```go
func TestParseGBK(t *testing.T) {
	p := mustParse(t, "gbk.eml")
	assert.Equal(t, "报销单已通过", p.Subject)
	assert.Contains(t, p.Text, "请查收发票")
}
func TestParseAttachment(t *testing.T) {
	p := mustParse(t, "attachment.eml")
	require.Len(t, p.Attachments, 1)
	assert.Equal(t, "invoice.pdf", p.Attachments[0].Filename)
	assert.False(t, p.Attachments[0].Inline)
}
func TestSanitizeHostile(t *testing.T) {
	p := mustParse(t, "hostile.eml")
	safe, blocked := mail.Sanitize(p.HTML, nil, false)
	assert.True(t, blocked)
	for _, bad := range []string{"<script", "onerror", "javascript:", "tracker.example"} {
		assert.NotContains(t, safe, bad)
	}
	safe, blocked = mail.Sanitize(p.HTML, nil, true)
	assert.False(t, blocked)
	assert.Contains(t, safe, "https://tracker.example/p.gif")
}
func TestSanitizeInlineCID(t *testing.T) {
	p := mustParse(t, "inline-image.eml")
	safe, _ := mail.Sanitize(p.HTML, inlineMap(p), false)
	assert.Contains(t, safe, `src="data:image/png;base64,`)
	assert.NotContains(t, safe, "cid:")
}
```

- [ ] **Step 3: 运行，确认失败**

Run: `go test ./core/mail/` → FAIL（函数未定义）

- [ ] **Step 4: 实现 `Parse`**

用 `go-message/mail.CreateReader` 遍历各部分，并 `import _ "github.com/emersion/go-message/charset"` 注册非 UTF-8 编码。`text/plain` 和 `text/html` 各取第一个非附件部分；其他部分（或 `Content-Disposition: attachment`）进 `Attachments`。

- [ ] **Step 5: 实现 `Sanitize`**

先用 bluemonday：`UGCPolicy()` 为基础，额外允许表格相关元素和属性、`style` 属性中的这些 CSS 属性：`color`、`background-color`、`font-family`、`font-size`、`font-weight`、`font-style`、`text-align`、`text-decoration`、`line-height`、`margin*`、`padding*`、`border*`、`width`、`height`、`display`、`vertical-align`。然后用 `golang.org/x/net/html` 遍历结果：`img` 的 `src` 若是 `cid:` 则替换为对应附件的 `data:` URI（找不到就删除 `src`）；若是 `http(s):` 且 `loadRemote == false`，删除 `src` 并把返回值 `blockedRemote` 置为 true。

- [ ] **Step 6: 运行，确认通过**

Run: `go test ./core/mail/` → PASS

- [ ] **Step 7: Commit**

```bash
git add core/mail
git commit -m "feat(mail): parse MIME with charsets and sanitize HTML"
```

---

### Task 3: 由草稿生成邮件原文

**Files:**
- Create: `core/mail/build.go`
- Test: `core/mail/build_test.go`

**Interfaces:**
- Consumes: `mail.Parse`（Task 2）
- Produces:
  - `type Outgoing struct { From string; To, Cc []string; Subject, Text string; InReplyTo string; References []string; Attachments []Attachment }`
  - `mail.Build(o Outgoing, now time.Time) (raw []byte, messageID string, err error)`：Message-ID 格式为 `<随机 16 字节 hex@发件人域名>`。

- [ ] **Step 1: 写失败的测试 `TestBuildRoundTrip`**

用一个包含中文标题、两个收件人、一个附件、`InReplyTo` 和 `References` 的 `Outgoing` 调用 `Build`，再 `Parse` 回来，断言：Subject、To、InReplyTo、References、附件文件名和内容与输入一致；`Parsed.MessageID` 等于 `Build` 返回的 `messageID`；`Date` 等于 `now`（秒级）。

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./core/mail/ -run TestBuildRoundTrip` → FAIL

- [ ] **Step 3: 实现 `Build`**

用 `go-message/mail.CreateWriter`；有附件时为 `multipart/mixed`，否则单个 `text/plain; charset=utf-8`。标题用 `mail.Header.SetSubject`（自动按 RFC 2047 编码）。

- [ ] **Step 4: 运行，确认通过**

Run: `go test ./core/mail/` → PASS

- [ ] **Step 5: Commit**

```bash
git add core/mail/build.go core/mail/build_test.go
git commit -m "feat(mail): build RFC 5322 messages from drafts"
```

---

### Task 4: 事件总线

**Files:**
- Create: `core/events/bus.go`
- Test: `core/events/bus_test.go`

**Interfaces:**
- Produces:
  - `type Event struct { Type string; AccountID, FolderID int64; MessageIDs []int64; Error string }`
  - 事件类型常量：`events.MessageReceived = "message.received"`、`events.MessagesChanged = "messages.changed"`、`events.SyncStarted = "sync.started"`、`events.SyncFinished = "sync.finished"`、`events.SyncFailed = "sync.failed"`
  - `events.New() *Bus`；`(*Bus).Publish(e Event)`；`(*Bus).Subscribe() (<-chan Event, func())`，第二个返回值用于取消订阅。

- [ ] **Step 1: 写失败的测试**

`TestFanOut`：两个订阅者都收到同一事件。`TestSlowSubscriberDoesNotBlock`：一个订阅者不读，`Publish` 1000 次仍在 1 秒内返回，另一个订阅者能收到最后一条。`TestUnsubscribeClosesChannel`：取消后 channel 被关闭。

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./core/events/` → FAIL

- [ ] **Step 3: 实现**

每个订阅者一个容量 64 的带缓冲 channel；满了就丢弃最旧的一条再放入新的（`ponytail:` 注释说明：慢订阅者会丢事件，需要可靠投递时改为每订阅者独立队列）。

- [ ] **Step 4: 运行，确认通过**

Run: `go test ./core/events/` → PASS

- [ ] **Step 5: Commit**

```bash
git add core/events
git commit -m "feat(events): add fan-out event bus"
```

---

### Task 5: Service 骨架、测试服务器与账户管理

**Files:**
- Create: `core/types.go`, `core/service.go`, `core/accounts.go`, `core/internal/testmail/imap.go`, `core/internal/testmail/smtp.go`
- Test: `core/accounts_test.go`

**Interfaces:**
- Consumes: `store.Open`（Task 1）、`events.New`（Task 4）
- Produces:
  - `type Options struct { DataDir string; AutoSync bool }`
  - `core.New(ctx context.Context, opts Options) (*Service, error)`；`(*Service).Close() error`；`(*Service).Subscribe() (<-chan events.Event, func())`
  - `type Security string`，取值 `SecurityTLS = "tls"`、`SecurityStartTLS = "starttls"`、`SecurityNone = "none"`
  - `type AccountInput struct { Email, Name, Alias, Signature, Username, Password string; IsDefault bool; SyncLimit int; IMAPHost string; IMAPPort int; IMAPSecurity Security; SMTPHost string; SMTPPort int; SMTPSecurity Security }`（`SyncLimit` 为 0 时取 2000；第一个账户自动成为默认发件账户）
  - `type Account struct`：同上字段，去掉 `Password`，加 `ID int64`、`AuthType string`、`Status string`
  - 本计划只实现密码 / 授权码登录（`auth_type = 'password'`）。Outlook 所需的 OAuth（XOAUTH2）和 `Reauthorize` 放在后续的账户接入计划里；`status` 列现在就建好，IMAP 认证失败时置为 `reauth_required` 并发 `SyncFailed`。
  - `(*Service).TestAccount(ctx, in AccountInput) error`
  - `(*Service).AddAccount(ctx, in AccountInput) (Account, error)`：先校验并 `TestAccount`，通过后写库、把密码存进钥匙串（service 名 `godwit`，user 为 `account:<id>`）
  - `(*Service).ListAccounts(ctx) ([]Account, error)`；`(*Service).RemoveAccount(ctx, id int64) error`：同时删除钥匙串条目和该账户的 .eml 目录
  - 测试辅助：`testmail.StartIMAP(t) *testmail.IMAP`，`IMAP{Addr string; User *imapmemserver.User}`，方法 `Stop()` 和 `Restart()`（在同一地址重新监听，数据保留）；用户名 `alice`、密码 `secret`，预建 `INBOX`、`Sent`、`Trash`、`Archive`；`testmail.StartSMTP(t) (addr string, got *testmail.Inbox)`，`Inbox` 有 `Messages() []testmail.Received`，`Received{From string; To []string; Data []byte}`，同样只接受 `alice` / `secret`。
  - 测试辅助：`newTestService(t) *core.Service`（放在 `core/helpers_test.go`），调用 `keyring.MockInit()`，`DataDir` 用 `t.TempDir()`，`AutoSync: false`。

- [ ] **Step 1: 写失败的测试**

```go
func TestAddAccount(t *testing.T) {
	s := newTestService(t)
	imap := testmail.StartIMAP(t)
	smtpAddr, _ := testmail.StartSMTP(t)
	acct, err := s.AddAccount(ctx, localInput(imap.Addr, smtpAddr, "secret"))
	require.NoError(t, err)
	pw, _ := keyring.Get("godwit", fmt.Sprintf("account:%d", acct.ID))
	assert.Equal(t, "secret", pw)
	list, _ := s.ListAccounts(ctx)
	assert.Len(t, list, 1)
}
func TestAddAccountWrongPassword(t *testing.T) {
	// 密码错误：返回错误，库里没有账户，钥匙串里没有条目，错误信息里不含密码
}
func TestPlaintextOnlyForLoopback(t *testing.T) {
	// IMAPHost "mail.example.com" + SecurityNone → 返回校验错误，且不发起任何网络连接
}
func TestRemoveAccount(t *testing.T) {
	// 删除后：ListAccounts 为空，钥匙串条目不存在，DataDir/raw/<id> 目录不存在
}
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./core/ -run Account` → FAIL

- [ ] **Step 3: 实现测试服务器**

IMAP 用 `imapserver.New(&imapserver.Options{NewSession: ..., InsecureAuth: true})` 加 `imapmemserver`，在 `127.0.0.1:0` 上监听，`t.Cleanup` 关闭。SMTP 用 `go-smtp` 写一个最小的 `Backend`，把收到的邮件存进 `Inbox`，`AllowInsecureAuth = true`。

- [ ] **Step 4: 实现 Service 骨架与账户管理**

`New` 打开 `DataDir/godwit.db`，创建事件总线。校验规则：Email、Username、两个 Host 非空；端口在 1–65535；`SecurityNone` 只允许 `localhost`、`127.0.0.1`、`::1`。`TestAccount` 依次登录 IMAP（`imapclient.DialTLS` / `DialStartTLS` / `DialInsecure` 按 Security 选择）和 SMTP（`smtp.DialTLS` / `smtp.Dial` + `StartTLS`），各自 30 秒超时。返回的错误要包一层，不带密码。

- [ ] **Step 5: 运行，确认通过**

Run: `go test ./core/ -run Account` → PASS

- [ ] **Step 6: Commit**

```bash
git add core
git commit -m "feat(core): add service skeleton, account management and test mail servers"
```

---

### Task 6: IMAP 拉取同步

**Files:**
- Create: `core/sync/fetch.go`, `core/sync/worker.go`
- Modify: `core/service.go`（加 `SyncNow`）
- Test: `core/sync_test.go`

**Interfaces:**
- Consumes: `store.DB`、`mail.Parse`、`mail.Snippet`、`events.Bus`、`Account`
- Produces:
  - `sync.Fetch(ctx context.Context, db *store.DB, c *imapclient.Client, accountID int64, rawDir string, bus *events.Bus) error`：同步所有文件夹
  - `(*Service).SyncNow(ctx context.Context, accountID int64) error`：建立连接，先推送操作队列（Task 8 之前为空操作），再执行 `sync.Fetch`
  - `(*Service).ListFolders(ctx, accountID int64) ([]Folder, error)`，`type Folder struct { ID, AccountID int64; Name, Role string; Unread int }`

- [ ] **Step 1: 写失败的测试**

```go
func TestSyncNewMessages(t *testing.T) {
	// memserver 的 INBOX 追加 3 封邮件 → SyncNow
	// 断言：ListFolders 包含 INBOX（Role "inbox"）、Sent（"sent"）、Trash（"trash"）、Archive（"archive"）；
	//       messages 表里 INBOX 有 3 行，subject、from_addr、snippet 正确；
	//       .eml 文件存在；messages_fts 能搜到第一封的正文；收到一次 MessageReceived 事件，MessageIDs 长度 3
	// 再次 SyncNow：不新增行，不再发 MessageReceived
}
func TestSyncRemoteFlagsAndExpunge(t *testing.T) {
	// 同步后，在 memserver 一侧把第 1 封标记 \Seen、删除并 expunge 第 2 封 → SyncNow
	// 断言：第 1 封 flags 含 \Seen；第 2 封的行、.eml 文件、FTS 条目都不存在；发了 MessagesChanged
}
func TestSyncUIDValidityChange(t *testing.T) {
	// Archive 里有 3 封并已同步 → 在服务器上删除并重建 Archive（新 UIDVALIDITY），追加 2 封 → SyncNow
	// 断言：Archive 只剩新的 2 行，旧行、旧 .eml 文件、旧 FTS 条目都不存在（INBOX 不能删除，所以用 Archive 测）
}
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./core/ -run TestSync` → FAIL

- [ ] **Step 3: 实现 `sync.Fetch`**

对每个文件夹：
1. `LIST` 得到所有文件夹，`Role` 优先取 SPECIAL-USE 属性（`\Sent` 等），没有时按名字匹配（`INBOX`→inbox；`Sent`/`Sent Items`/`已发送`→sent；`Trash`/`Deleted Items`/`已删除`→trash；`Drafts`/`草稿`→drafts；`Junk`/`Spam`/`垃圾邮件`→junk；`Archive`/`归档`→archive）。服务器上已不存在的文件夹从本地删除。
2. `SELECT` 后若 UIDVALIDITY 与库里不同：删除该文件夹的所有邮件行、FTS 条目、.eml 文件，`last_uid` 归零。
3. `UID FETCH (last_uid+1):* (UID FLAGS BODY.PEEK[])`，每封：保存 `.eml` 到 `rawDir/<accountID>/<messageRowID>.eml`，`Parse`，在一个 `Transaction` 里插入 `messages` 和 `messages_fts`。首次同步只拉最近 `sync_limit` 封（默认 2000）（`ponytail:` 注释：更早的邮件按需加载以后再做）。
4. `UID FETCH 1:last_uid (UID FLAGS)`：更新变化的 flags；本地有、服务器没返回的 UID 视为已 expunge 并删除（`ponytail:` 注释：CONDSTORE/QRESYNC 以后替换全量 FLAGS）。
5. 有新邮件发 `MessageReceived`，有 flags 变化或删除发 `MessagesChanged`。

- [ ] **Step 4: 运行，确认通过**

Run: `go test ./core/ -run TestSync` → PASS

- [ ] **Step 5: Commit**

```bash
git add core
git commit -m "feat(sync): fetch folders, messages and remote flag changes over IMAP"
```

---

### Task 7: 读取路径：列表、详情、搜索、附件

**Files:**
- Create: `core/messages.go`（读取部分）
- Test: `core/messages_read_test.go`

**Interfaces:**
- Consumes: Task 2 的 `Parse`、`Sanitize`；Task 6 同步得到的数据
- Produces:
  - `type MessageSummary struct { ID, FolderID int64; Subject, From, Snippet string; To []string; Date time.Time; Flags []string; HasAttachments bool }`
  - `type Message struct { MessageSummary; Cc []string; Text, HTML string; RemoteImagesBlocked bool; Attachments []AttachmentInfo }`，`type AttachmentInfo struct { Index int; Filename, ContentType string; Size int }`（只列非 inline 附件）
  - `type Page struct { Items []MessageSummary; NextCursor string }`
  - `(*Service).ListMessages(ctx, folderID int64, cursor string, limit int) (Page, error)`：按 `date DESC, id DESC` 排序；cursor 是不透明字符串，空串表示第一页；`limit` 超过 200 按 200 处理
  - `(*Service).GetMessage(ctx, id int64, loadRemoteImages bool) (Message, error)`
  - `(*Service).Search(ctx, accountID int64, query string, limit int) ([]MessageSummary, error)`
  - `(*Service).GetAttachment(ctx, messageID int64, index int) (mail.Attachment, error)`

- [ ] **Step 1: 写失败的测试**

```go
func TestListMessagesPaging(t *testing.T) {
	// 同步 5 封不同日期的邮件；limit 2 依次翻页 → 2、2、1 条，NextCursor 最后为空，顺序按日期倒序且无重复
}
func TestGetMessageSanitized(t *testing.T) {
	// 同步 hostile.eml → GetMessage(id, false)：HTML 不含 <script>，RemoteImagesBlocked 为 true
}
func TestSearchChinese(t *testing.T) {
	// 同步正文含「请查收本月报销单」的邮件
	// Search("报销单") 命中；Search("报销") 命中（2 个字符，退回 LIKE，标题含「报销」）；Search("不存在的词") 为空
}
func TestSearchQueryIsNotFTSSyntax(t *testing.T) {
	// Search(`"unbalanced AND (`) 不返回错误
}
func TestGetAttachment(t *testing.T) {
	// 同步 attachment.eml → GetAttachment(id, 0) 的 Filename 为 invoice.pdf，Data 与样本一致
}
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./core/ -run 'TestListMessages|TestGetMessage|TestSearch|TestGetAttachment'` → FAIL

- [ ] **Step 3: 实现**

搜索：查询少于 3 个字符（按 rune 计）时，对 `subject` 和 `from_addr` 做 `LIKE '%q%'`（转义 `%`、`_`）；否则把查询包成 FTS5 短语（`"` 转义为 `""`）后 `MATCH`。两种都限定在该账户的文件夹内，按日期倒序。

- [ ] **Step 4: 运行，确认通过**

Run: 同 Step 2 → PASS

- [ ] **Step 5: Commit**

```bash
git add core
git commit -m "feat(core): list, read, search messages and fetch attachments"
```

---

### Task 8: 本地优先的修改与操作队列

**Files:**
- Create: `core/sync/ops.go`
- Modify: `core/messages.go`（写入部分）、`core/sync/worker.go`（`SyncNow` 先推送操作）
- Test: `core/messages_write_test.go`

**Interfaces:**
- Produces:
  - `(*Service).SetFlags(ctx, ids []int64, add, remove []string) error`
  - `(*Service).Move(ctx, ids []int64, folderID int64) error`：目标文件夹必须属于同一账户
  - `(*Service).Delete(ctx, ids []int64) error`：等价于 `Move` 到该账户 Role 为 `trash` 的文件夹；已在 Trash 中的邮件不做任何事（不硬删除）
  - `(*Service).PendingOps(ctx, accountID int64) ([]Op, error)`，`type Op struct { ID int64; Kind string; Attempts int; LastError string }`
  - `sync.PushOps(ctx, db *store.DB, c *imapclient.Client, accountID int64) error`

- [ ] **Step 1: 写失败的测试**

```go
func TestSetFlagsIsLocalFirst(t *testing.T) {
	// 同步后关掉 IMAP 服务器 → SetFlags([id], ["\\Seen"], nil) 立即成功，GetMessage 的 Flags 含 \Seen，PendingOps 有 1 条
}
func TestOpsSurviveRestart(t *testing.T) {
	// 离线 SetFlags + Move → Close → 用同一个 DataDir 重新 New → 服务器恢复 → SyncNow
	// 断言：memserver 上该邮件已在目标文件夹且带 \Seen；PendingOps 为空；本地只有 1 行（无重复）
}
func TestFailedOpKeepsError(t *testing.T) {
	// 移动到一个已在服务器上被删除的文件夹 → SyncNow 返回 nil（单个操作失败不让同步失败）
	// PendingOps 中该操作 Attempts 为 1，LastError 非空；尝试满 5 次后标记为失败、不再重试，并发 SyncFailed 事件
}
func TestDeleteMovesToTrash(t *testing.T) {
	// Delete → 邮件出现在 Trash；对 Trash 里的邮件再 Delete → 邮件仍在 Trash
}
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./core/ -run 'TestSetFlags|TestOps|TestFailedOp|TestDelete'` → FAIL

- [ ] **Step 3: 实现**

每次修改在一个 `Transaction` 里同时改 `messages` 行并插入 `ops` 行。`flags` 的 payload：`{uid, folder, add, remove}`；`move` 的 payload：`{uid, from, to}`，本地先把行的 `folder_id` 改成目标文件夹、`uid` 置空。`PushOps` 按 id 顺序执行：`move` 用 `UID MOVE`，若响应带 COPYUID 就把新 UID 写回该行；没有 COPYUID 就删除本地行，交给下次拉取重新获取。成功的操作删除；失败的 `attempts+1` 并记录 `last_error`，满 5 次不再执行。

- [ ] **Step 4: 运行，确认通过**

Run: 同 Step 2 → PASS

- [ ] **Step 5: Commit**

```bash
git add core
git commit -m "feat(core): local-first flag/move/delete with persistent op queue"
```

---

### Task 9: 草稿与发送

**Files:**
- Create: `core/compose.go`, `core/send/smtp.go`
- Modify: `core/sync/ops.go`（处理 `send` 操作）
- Test: `core/compose_test.go`

**Interfaces:**
- Consumes: `mail.Build`（Task 3）、操作队列（Task 8）
- Produces:
  - `type Draft struct { ID, AccountID int64; To, Cc []string; Subject, Text string; InReplyTo int64; Attachments []string; UpdatedAt time.Time }`（`InReplyTo` 是内部 message ID，0 表示没有；`Attachments` 是本地文件路径）
  - `(*Service).SaveDraft(ctx, d Draft) (Draft, error)`：`ID == 0` 时新建
  - `(*Service).SendDraft(ctx, draftID int64) error`：生成原文 → 写入 `ops`（kind `send`，payload 含原文所在路径）→ 删除草稿，在同一个 `Transaction` 内完成
  - `send.Send(ctx context.Context, a Account, password string, from string, to []string, raw []byte) error`

- [ ] **Step 1: 写失败的测试**

```go
func TestSendDraft(t *testing.T) {
	// SaveDraft（中文标题，两个收件人，回复某封已同步邮件）→ SendDraft → SyncNow
	// 断言：SMTP Inbox 收到 1 封，收件人正确，解析后 InReplyTo 等于原邮件的 Message-ID；
	//       memserver 的 Sent 里有这封；草稿已删除；PendingOps 为空
}
func TestSendIsNotDuplicatedOnRetry(t *testing.T) {
	// SMTP 成功、APPEND 到 Sent 失败（Sent 在服务器上被删）→ 再次 SyncNow
	// 断言：SMTP 只收到 1 封
}
func TestSendDraftValidates(t *testing.T) {
	// 没有收件人，或收件人地址不合法 → 返回错误，草稿保留，不产生操作
}
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./core/ -run 'TestSend'` → FAIL

- [ ] **Step 3: 实现**

`send` 操作分两步，各自完成后立即更新 payload：先 SMTP 发送并在 payload 里记 `sent: true`，再 `APPEND` 到 Role 为 `sent` 的文件夹（带 `\Seen`）。重试时跳过已完成的步骤，所以 SMTP 不会重复发送。原文存在 `DataDir/outbox/<opID>.eml`，操作完成后删除。

- [ ] **Step 4: 运行，确认通过**

Run: `go test ./core/ -run 'TestSend'` → PASS

- [ ] **Step 5: Commit**

```bash
git add core
git commit -m "feat(core): drafts and SMTP send through the op queue"
```

---

### Task 10: 后台同步 worker

**Files:**
- Modify: `core/sync/worker.go`, `core/service.go`
- Test: `core/worker_test.go`

**Interfaces:**
- Produces:
  - `Options.AutoSync == true` 时，`New` 为每个已有账户启动 worker，`AddAccount` 为新账户启动，`RemoveAccount` 停止对应 worker，`Close` 停止全部并等待退出。
  - 本地修改（Task 8、9）发生后通知对应 worker 立即推送，不必等下一轮。

- [ ] **Step 1: 写失败的测试**

```go
func TestWorkerPicksUpNewMail(t *testing.T) {
	// AutoSync 为 true 的 Service + 账户 → 在 memserver 的 INBOX 追加一封
	// 5 秒内收到 MessageReceived
}
func TestWorkerPushesLocalChangesPromptly(t *testing.T) {
	// SetFlags 后 5 秒内 memserver 上该邮件带 \Seen
}
func TestWorkerReconnectsAfterServerRestart(t *testing.T) {
	// imap.Stop() → 收到 SyncFailed → imap.Restart() → 追加邮件 → 30 秒内收到 MessageReceived
}
func TestCloseStopsWorkers(t *testing.T) {
	// Close 在 5 秒内返回，且之后不再有 goroutine 访问数据库（用 -race 跑）
}
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test -race ./core/ -run 'TestWorker|TestCloseStops'` → FAIL

- [ ] **Step 3: 实现**

worker 循环：`SyncNow` → 若服务器支持 IDLE，对 INBOX 进入 IDLE，最多 25 分钟；否则等待 5 分钟。IDLE 收到变更通知、或本地修改的通知到来时，立即退出等待进入下一轮。连接失败时发 `SyncFailed`，按 2、4、8……秒退避，上限 5 分钟。测试里通过一个未导出的包级变量把退避上限调低。

- [ ] **Step 4: 运行，确认通过**

Run: `go test -race ./core/...` → 全部 PASS；`CGO_ENABLED=0 go build ./...` → 无输出

- [ ] **Step 5: Commit**

```bash
git add core
git commit -m "feat(sync): background workers with IDLE, nudges and reconnect backoff"
```
