# Godwit 里依赖 IMAP 的地方

调研于 2026-11-09，对象是本仓库 `8cff8bfe`（release 1.32.0）。回答 [#587](https://github.com/godwit-email/godwit/issues/587)，属于地图 [#584 协议后端接缝](https://github.com/godwit-email/godwit/issues/584)。

方法：逐个读完 `core/mailsync` 的全部非测试文件；在 `core`、`app` 里 grep `go-imap`、`imapclient`、`uidvalidity`、`X-GM-EXT-1`、`\Seen`，读每处命中；对照 ADR 0005、0034、0035、0036。行号都是这个提交上的。下文「轮次」指一轮同步（`syncAccount` 里的一次），「单封」指用户一次操作只碰一封或几封的读写，「连接」指拨号、登录、能力探测这类连接管理。

## 结论速览

1. **IMAP 进到了三层**：协议调用（`imapclient`，集中在 `core/mailsync` 加 `core` 里 7 处直接调用）、**存储模型**（`folders.uidvalidity/last_uid`、`message_locations.uidvalidity/uid`、`messages.flags`、`messages.gmail_msgid`、`accounts.imap_*`），还有**对外的值**（`Flags []string` 用 IMAP 系统标记的拼写 `\Seen`、`\Flagged`，`AccountInput.IMAPHost/Port/Security`）。ADR 0005 说 Service 不暴露 UID，这一条成立；但标记的拼写和服务器设置是对外暴露的。
2. **连接是裸的 `*imapclient.Client`**：`mailsync` 每个导出函数都收 `*imapclient.Client`，`core` 拿到连接后自己 `Select`、`Fetch`、`Append`。没有中间接口，换协议的第一步就是在这里切一刀。
3. **服务器位置等于 (folder, UIDVALIDITY, UID)**：去重、认领、操作推送、附件下载、退订头补取，全都靠「SELECT 后比对 UIDVALIDITY，再按 UID 取」。`thread.go`、`messages.go` 里甚至用 `(folder_id, uid)` 当插入时的行定位键。
4. **Gmail 是 IMAP 里的另一个方言**：`X-GM-EXT-1` 分支散在 `Fetch`、`syncFolders`、`orphan`、`pushOp`、`dialAccount`、`check.go`、`gmailAccount`，加上 fork 的 `STORE X-GM-LABELS`。换成 Gmail REST 时这些全部换掉；换成别的协议时，「一封邮件多个位置」这个模型（ADR 0034）可以留着复用。
5. **测试替身只有一个**：`core/internal/testmail`，基于 go-imap 的 `imapmemserver`，加上 Gmail 模式和断线、拒绝的注入。`core` 有 23 个测试文件、`mailsync` 有 3 个测试文件直接用它（`coretest.Env.AddAccount` 也默认起它）。没有协议无关的假后端。

## 一、`dialAccount` 的 7 处调用

`dialAccount`（`core/service.go:667`）负责：读账户、跳过本地归档（`ErrLocalArchive`，:673）、取凭据、`conn.DialIMAPAbortable`（:684），令牌被拒时刷新重试一次，认证失败时标 `reauth_required`，成功后写 `accounts.imap_gmail = Caps().Has("X-GM-EXT-1")`（:701），清掉 `reauth_required` 并唤醒 worker。全是**连接管理**，第四个参数 `h` 只给 IDLE 用。

| # | 调用处 | 拿连接做什么 | IMAP 能力 | 类别 |
|---|---|---|---|---|
| 1 | `core/service.go:725` `syncAccount` | `QueuePurge`、`processOutbox`、`PushOps`、`Fetch`、`PushNewOps` | 全部（见下文） | 轮次 |
| 2 | `core/worker.go:248` `syncRound` | 第二条连接：`UnilateralDataHandler`（Expunge/Mailbox/Fetch 都触发唤醒），`SELECT INBOX`（:263）后 `mailsync.Idle`（:274）；没有 `IDLE` 能力就立刻释放，改成轮询 | IDLE、NOOP、未经请求的 EXISTS/EXPUNGE/FETCH | 连接 / 推送通知 |
| 3 | `core/older.go:43` `LoadOlder` | `mailsync.FetchOlder` | SELECT、UIDVALIDITY、按序号 FETCH UID、UID FETCH | 单次分页（用户点「加载更早」） |
| 4 | `core/unsubscribe.go:199` `fetchListHeaders` | 旧邮件没缓存 List-* 头时补取 | SELECT（只读）+ 比对 UIDVALIDITY（:204），UID FETCH `BODY.PEEK[HEADER.FIELDS (LIST-…)]`（:207-209） | 单封 |
| 5 | `core/messages.go:1139` `gmailAccount` | 老账户 `imap_gmail` 为空、又判断不出来时，登录一次只为看能力 | CAPABILITY `X-GM-EXT-1`（:1144） | 连接 / 能力探测 |
| 6 | `core/attachments.go:234` `attachmentFetcher.fetchOnce`（经 `dialDownload` :406-407） | 批量取附件，一条连接复用，换文件夹时重新 SELECT（:242） | SELECT（只读）+ UIDVALIDITY、UID FETCH `BODY.PEEK[part]` 流式读 | 单封（批） |
| 7 | `core/attachments.go:393` `download`（经 `dialDownload`） | `GetRawMessage` 取整封、单个附件 | 同上，part 为空时取整封（`fetchPart` :417、`fetchSection` :437-449） | 单封 |

注：源码里直接写 `s.dialAccount(` 的是 6 处，`attachments.go:407` 是包装函数 `dialDownload`，它有两个调用方（第 6、7 行），所以按调用路径算是 7 处。另有一处**不经 `dialAccount`** 的 IMAP 拨号：`core/check.go:118` 添加账户时的连接检查，直接调 `conn.DialIMAPAbortable`，读 `X-GM-EXT-1`（:120）并 `mailsync.CountFolders`（:122，LIST）。

## 二、逐文件：用到的 IMAP 能力

### `core/mailsync`（非测试文件全部列出）

| 文件 | 用到的 IMAP 能力 | 类别 |
|---|---|---|
| `fetch.go` | 整个同步轮次的入口 `Fetch`（:100）。`SELECT (EXAMINE)` 后比对 UIDVALIDITY，不一致就 `resetFolder`（:250-251, :271-289）；`last_uid` 游标 + `UID SEARCH UID n:*` 分页取新邮件（:405-428）；首轮按序号取顶部 UID（:377）或按序号范围取最新 N 封（:389-400）；`UID SEARCH SINCE`（:621，按 INTERNALDATE 只比日期，:58-59）补全范围；FETCH `UID FLAGS RFC822.SIZE INTERNALDATE BODYSTRUCTURE BODY.PEEK[HEADER.FIELDS (…)]`（:361-364, :494-497）；按 MIME 路径 `BODY.PEEK[1.2]<0.N>` 部分取正文（:714-750）；BODYSTRUCTURE 坏掉时取整封，用 `imapserver.ExtractBodyStructure` 重建（:537-552）；`X-GM-EXT-1` 分支：`mergeOld`、`keepInAllMail`（`UID SEARCH X-GM-MSGID`，:179）、`reshow`、轮末统一删孤儿（:118-153, :298-304）；`imap.Error`（NO）只跳过该文件夹（:129-133）；`flagString` 去掉 `\Recent`（:820-829） | 轮次 |
| `folders.go` | `LIST "" *`（:105, :119, :268）；SPECIAL-USE 属性映射角色（:24-30）；`\Noselect`/`\NonExistent`（:99-101）；层级分隔符 `delim`（:92-97）；按名字猜角色（:32-47, :83-89）；Gmail 下删文件夹时保留有 `gmail_msgid` 或待推送操作的邮件（:155-157）；`SentFolder` 首轮前直接 LIST 找已发送（:259-279） | 轮次；`SentFolder`、`CountFolders` 也被发件箱和连接检查用 |
| `gmail.go` | `fetchImport`：Gmail 下先只取 `UID FLAGS X-GM-MSGID`，已存的邮件只加位置（`gmailLocate`），其余再整封取（:19-42）；`mergeOld` 给老行补 `X-GM-MSGID` 后合并（:48-89） | 轮次 |
| `gmail_ops.go` | Gmail 的移动：`SELECT` + `UID SEARCH X-GM-MSGID` 定位源和目标（`gmailLocation` :195-218），fork 的 `UID STORE ±X-GM-LABELS`（:151, :157），系统标签 `\Inbox \Sent \Draft \Trash \Spam`（:178-192），写后再查一次作为后置条件（:162-174） | 单封操作（在轮次的推送阶段执行） |
| `locations.go` | 只有 SQL，但语义是 IMAP 的：`uid IS NOT NULL` 表示「服务器位置已确认」（`serverIn`、`unlocated` :12-16），`locate`/`unlocate` 写 `(uidvalidity, uid)`（:60-76） | 存储模型 |
| `merge.go` | 只有 SQL；按 `gmail_msgid` 合并重复行（:114-）、`uid IS NOT NULL` 迁移位置（:49-51） | 存储模型（Gmail） |
| `messages.go` | `importMessage` 用 `(folder_id, uid)` 回查刚插入的行（:68-89）；`claim`：没有 COPYUID 的 MOVE 到达后按 Message-ID + RFC822.SIZE 认领（:189-231）；`unclaimable`（:239-270）；`syncFlags`：每轮 `UID FETCH 1:last_uid FLAGS` 全量刷新，UID 不在了就删位置（:279-327，:278 记着「换成 CONDSTORE/QRESYNC」）；Trash/Junk 里消失视为 Gmail 永久删除（:323-325） | 轮次 |
| `ops.go` | 推送操作队列：`SELECT`（读写）+ UIDVALIDITY 校验（:206-213）；`UID STORE ±FLAGS.SILENT`（:375-392）；`UID MOVE` 取 COPYUID（:399-405），没有 MOVE 时 `UID COPY` → `STORE \Deleted` → `UID EXPUNGE` 逐条等 OK（:407-417），两者都没有就失败（:189-192）；断线重发前按 UID 查是否已移走（:244-275）；`InFolder`：`UID SEARCH HEADER Message-ID LARGER SMALLER` 再取头核对（:333-364）；Gmail 下 STORE 后 FETCH 确认还在（:224-235）；`awaiting_claim` 状态（:438） | 单封操作（轮次的推送阶段） |
| `older.go` | `FetchOlder`：SELECT + UIDVALIDITY（:42-48），按序号窗口倒着 FETCH UID 找缺的（:63-81），再 `fetchImport` | 单次分页 |
| `purge.go` | 只在有 `UIDPLUS` 时排清除（:72）；`STORE +FLAGS \Deleted` → `UID EXPUNGE`（:223, :230），中途反复核对；取消时 `-FLAGS \Deleted`（:250-252）；清除条件里比对 `l.uidvalidity = f.uidvalidity`（:58-60, :133-136） | 单封操作（轮次的推送阶段） |
| `security.go` | 给缓存里缺 Authentication-Results 的邮件补取头：按 `(folder, uidvalidity, uid)` 选 50 封（:16-17），`UID FETCH BODY.PEEK[HEADER.FIELDS]`（:30） | 轮次 |
| `thread.go` | 不调 IMAP；`threadTail` 用 `(folder_id, uid)` 定位刚插入的邮件（:151-153） | 存储模型 |
| `local.go` | 不依赖 IMAP：本地归档的邮件只有位置，没有服务器位置（ADR 0036） | — |
| `worker.go` | `Idle`：先 `NOOP` 收积压的更新，再 `IDLE`，等未经请求的更新、唤醒、超时或断线（:16-40） | 连接 / 推送通知 |

### `core` 里直接用 IMAP 的其他文件

| 文件 | 用到的 IMAP 能力 | 类别 |
|---|---|---|
| `service.go` | `dialAccount`（:667-722，见上）；`syncAccount` 定死顺序：清除排队 → 发件箱 → 推操作 → `Fetch` → 关键字转标签 → 再推一次（:724-773）；`pushOps` 的签名带 `*imapclient.Client`（:822-823） | 连接 + 轮次编排 |
| `worker.go` | 第二条 IDLE 连接（:248-275），见上表第 2 行 | 连接 |
| `older.go` | 见上表第 3 行 | 单次分页 |
| `unsubscribe.go` | 见上表第 4 行 | 单封 |
| `messages.go` | `serverLocation` 联表条件 `l.uid IS NOT NULL`（:160）；`systemFlags` 规范化 IMAP 系统标记（:722-724）；`SetFlags` 把 IMAP 标记写进 `messages.flags` 并排 `flags` 操作（:774-814）；`archiveTargets`/`gmailAccount`：Gmail 的归档 = 去 INBOX 标签，判断方法依次是 `imap_gmail` 列、provider + 主机 + 993/TLS、有没有 `[Gmail]` 文件夹、最后登录看能力（:1090-1145） | 单封 + 能力判断 |
| `attachments.go` | 见上表第 6、7 行；`attRow` 带 `uidValidity`、`uid imap.UID`（:51-71），`GetRawMessage` 查 `l.uidvalidity, l.uid`（:366-382） | 单封 |
| `outbox.go` | `processOutbox` 收一个 `*imapclient.Client`（:280）；找已发送用 `mailsync.SentFolder`（:306）；发送结果不确定或归档前用 `mailsync.InFolder` 按 Message-ID 查重（:385, :518）；`APPEND` 到已发送并带 `\Seen`（:526-536）；SMTP/Graph 发信本身不碰 IMAP | 轮次中的单封操作 |
| `keywordlabels.go` | IMAP 关键字和标签互转（ADR 0035）：从 `messages.flags` 解析关键字，跳过 `\` 和 `$` 开头的标记（:32-47）；`pushLabelKeyword` 借 `SetFlags` 把 slug 当关键字推上去（:126-170）；`importKeywordLabels` 每轮调一次（`service.go:755`） | 轮次 + 单封 |
| `check.go` | 添加账户时的 IMAP 检查（:117-130），不经 `dialAccount` | 连接 |
| `accounts.go` | `AccountInput`/`ServerSettings` 的 `IMAPHost/IMAPPort/IMAPSecurity`（:85, :358, :586-592, :682-685）；创建时写 `imap_gmail`（:214）；`LabelKeywords` 开关（:636-637） | 账户配置 |
| `rules.go`、`savedview.go`、`labels.go`、`contacts.go`、`threads.go`、`wit.go` | 用 `instr(flags, '\Seen')`、`` `\Flagged` `` 判断已读、星标（如 `rules.go:138`、`savedview.go:278`、`service.go:864, :892`、`wit.go:908-912`） | 存储模型 |
| `core/mail/parse.go` | `ParseStructure` 的入参是 `imap.BodyStructure`（:390-405），MIME 结构用的是 go-imap 的类型 | 内容解析 |
| `core/store/schema.go` | `accounts.imap_host/port/security`（:14）；`folders.uidvalidity, last_uid`（:22-23）以及后来加的 `synced`（:184）、`filled_since`（:241）、`delim`（:325）；`messages.flags` 存 IMAP 标记字符串（:42）；`message_locations(uidvalidity, uid)`，`UNIQUE(folder_id, uid)`（:638-647）；`messages.gmail_msgid`（:675-678）；`accounts.imap_gmail`（:693-695）；`accounts.label_keywords`（:698）；`ops.state` 有 `awaiting_claim` | 存储模型 |
| `core/conn/conn.go` | `DialIMAP`/`DialIMAPAbortable`（:153-172）：隐式 TLS 遇到明文问候时退回 STARTTLS（:174-177）；GBK/Big5 的 `WordDecoder`（:187）；登录后发 `ID`（NetEase 要求，:204-211）；`LOGIN` 或 `AUTHENTICATE XOAUTH2`，没有 `AUTH=XOAUTH2` 就算认证失败（:227-240）。SMTP 拨号也在这里（:259-）。`Endpoint`、`Security` 两种协议共用 | 连接 |
| `core/conn/xoauth2.go` | SASL XOAUTH2，IMAP 和 SMTP 共用 | 连接 |
| `core/conn/trace.go` | 按 `*imapclient.Client` 存脱敏的协议记录，解析失败时写进错误报告（:61-72）；脱敏规则认 IMAP 的 tag、序号集、系统标记（:171-211） | 连接 / 诊断 |
| `core/conn/classify.go` | 把 IMAP/SMTP 拨号错误归类（:40）；不调 go-imap，规则通用 | 连接 |
| `core/auth/providers.go` | 每个服务商一对 `IMAP, SMTP Server`（:43），默认 993/TLS（:61）；Outlook 的 OAuth scope 是 `IMAP.AccessAsUser.All`（:53） | 账户发现 |
| `core/auth/detect.go` | 自动发现只找 IMAP：域名表、MX、mailservers 数据库、`_imaps._tcp` SRV（:88）、最后猜 `imap.<domain>`（:98） | 账户发现 |
| `core/send/*` | 不依赖 IMAP（SMTP 和 Graph 发信） | — |

### 测试替身

| 位置 | 内容 |
|---|---|
| `core/internal/testmail/imap.go` | `StartIMAP`（:486）：go-imap 的 `imapmemserver`，开在 127.0.0.1，alice/secret。可选项：`WithXOAuth2`、`RequireID`、`WithoutMove`、`WithoutUIDPlus`、`WithoutCOPYUID`、`WithoutIdle`（:35-62）；故障注入：`DropAfterNextAppend`、`DropOnNextUIDExpunge`、`DropFetchAfter`、`BlockLogin`、`SetPassword`、`Stop/Restart`（:106-572）；在连接层改写响应，补上 imapserver 缺的 ID（:146-275） |
| `core/internal/testmail/gmail.go` | `Gmail()` 模式（:43）：`X-GM-EXT-1`，一封邮件多个文件夹各有 UID，`X-GM-MSGID` 搜索（:111），`STORE X-GM-LABELS`（:57），注入 `RejectNextGmailSearch/Select`、`DropAfterGmailLabelStore` |
| `core/internal/testmail/smtp.go`、`dkim.go` | SMTP 假服务器；`SaveToSent(im)` 让发出的信出现在 IMAP 的已发送里 |
| `core/coretest/coretest.go` | `Env.AddAccount` 给每个账户起一对 testmail IMAP + SMTP（:215-238）；`Env.IMAP(id)`、`Deliver` 往文件夹里塞信（:278, :295） |
| 使用方 | `core` 下 23 个 `_test.go` 直接引用 `testmail`（最多的是 `gmail_recovery_test.go`、`gmail_test.go`、`gmail_merge_test.go`、`ops_test.go`）；`core/mailsync` 下 `fetch_test.go`、`folders_test.go`、`security_test.go` 直接构造 go-imap 类型。按 AGENTS.md，IMAP 怪异行为的回归测试（如 `TestToParsedReducedHeaderEcho`，`fetch_test.go:49`）必须留在 Godwit 侧 |

## 三、清单：如果换协议，要改什么

按「换掉 IMAP 时这块要不要动、动成什么样」分组。每组先说依赖的是什么，再列位置。

### A. 连接、认证与能力探测（连接管理）

依赖：一条长连接、`LOGIN`/`XOAUTH2`、`CAPABILITY`、`ID`、STARTTLS 回退、协议记录。

- [ ] `core/conn/conn.go:153-240` 拨号和登录，要有每种协议自己的版本；`Endpoint`/`Security`/`ErrAuthFailed` 可以共用
- [ ] `core/conn/trace.go:61-72, :171-211` 协议记录按 `*imapclient.Client` 存，脱敏规则是 IMAP 语法
- [ ] `core/service.go:667-722` `dialAccount`：取凭据、令牌重试、`reauth_required` 这部分和协议无关，可以留下；拨号（:684）和写 `imap_gmail`（:701）要换成后端自己的能力记录
- [ ] `core/check.go:117-130` 添加账户的连接检查，读 `X-GM-EXT-1` 和 LIST 文件夹数
- [ ] `core/messages.go:1109-1145` `gmailAccount` 判断「这个账户的归档是不是去标签」，最后一步要登录 IMAP 才能判断
- [ ] `core/auth/providers.go:43, :53, :61`、`core/auth/detect.go:88-99` 发现和服务商表只有 IMAP/SMTP 两槽；JMAP 的 `/.well-known/jmap`、EWS Autodiscover 没有位置放
- [ ] `core/accounts.go:85, :214, :358, :586-592, :682-685` 以及 `app/onboarding.go`、`app/settings_accounts.go`、`app/sync_status.go`：`IMAPHost/Port/Security` 是公开字段和设置界面
- [ ] `core/store/schema.go:14, :693-695` `accounts.imap_*`、`accounts.imap_gmail`

### B. 服务器位置与同步游标（UID / UIDVALIDITY / last_uid）

依赖：每个文件夹一个 UIDVALIDITY 代号和单调递增的 UID；`UID SEARCH n:*` 找新信；SELECT 后比对代号，不一致就作废整个文件夹。ADR 0005、0034 把它写成「服务器位置 = UIDVALIDITY + UID」。

- [ ] `core/store/schema.go:22-23, :184, :241` `folders.uidvalidity/last_uid/synced/filled_since`：游标是 IMAP 形状的；JMAP 是账户级 state 字符串，EWS 是每个文件夹的 SyncState，POP3 只有 UIDL
- [ ] `core/store/schema.go:638-647` `message_locations(uidvalidity, uid)` 和 `UNIQUE(folder_id, uid)`：服务器位置要换成不透明的 ID（JMAP Email id、EWS ItemId+ChangeKey）
- [ ] `core/mailsync/locations.go:12-16, :60-76` `uid IS NOT NULL` 表示「服务器已确认」，语义可以留，列要改
- [ ] `core/mailsync/fetch.go:245-289` `syncFolder`/`resetFolder`：SELECT → 比对 UIDVALIDITY → 取新信 → 补全 → 补头 → 同步标记，这个顺序本身就是 IMAP 的轮次
- [ ] `core/mailsync/fetch.go:372-430, :561-655` `fetchNew`、`backfill`、`backfillRange`：靠 UID 分页、序号取最新 N 封、`SINCE` 按日期补全
- [ ] `core/mailsync/older.go:27-92` `FetchOlder` 靠序号窗口
- [ ] `core/mailsync/messages.go:279-327` `syncFlags`：每轮全量 `FETCH 1:last_uid FLAGS`，并靠「UID 不见了」发现删除和移出；有变更通知的协议（JMAP `Email/changes`、EWS `SyncFolderItems`）可以直接给出增量
- [ ] `core/mailsync/messages.go:68-89`、`core/mailsync/thread.go:151-153` 插入时用 `(folder_id, uid)` 回查刚插入的行，换协议时要改成别的键
- [ ] `core/mailsync/security.go:15-70` 补 Authentication-Results 按 `(folder, uidvalidity, uid)` 取头
- [ ] `core/messages.go:160` `serverLocation`；`core/attachments.go:51-71, :366-382, :425-435`；`core/unsubscribe.go:187-209`：单封读取都是「SELECT + 校验 UIDVALIDITY + UID FETCH」
- [ ] `core/mailsync/purge.go:58-60, :133-136` 清除条件比对 `l.uidvalidity = f.uidvalidity`

### C. 文件夹模型（LIST / SPECIAL-USE）

依赖：文件夹是一棵树，名字带分隔符，角色来自 SPECIAL-USE 或名字猜测；一封邮件（非 Gmail）只在一个文件夹里。

- [ ] `core/mailsync/folders.go:24-101` 角色识别（SPECIAL-USE + 多语言名字表）、`\Noselect`、分隔符
- [ ] `core/mailsync/folders.go:118-187` `syncFolders` 用 LIST 结果全量对齐 `folders` 表；文件夹消失时删位置和孤儿邮件
- [ ] `core/mailsync/folders.go:259-279` `SentFolder` 首轮前直接 LIST
- [ ] `core/store/schema.go:325` `folders.delim`
- [ ] 角色解析 `RoleFolder`/`AltRole`（`folders.go:233-254`）只读数据库，可以留；POP3（只有收件箱）和 JMAP（一封邮件在多个 mailbox）要分别对上「一个位置」和「多个位置」（ADR 0034 已经给 Gmail 做了多位置）

### D. 内容获取（BODYSTRUCTURE / 部分取 / 懒加载附件）

依赖：先取 BODYSTRUCTURE 和选定的头，正文按 MIME 路径只取前 N 字节，附件按路径懒取；不读 ENVELOPE。

- [ ] `core/mailsync/fetch.go:361-364` 同步取的头字段列表
- [ ] `core/mailsync/fetch.go:494-497, :537-552, :667-802` `importItems`、`fillStructures`、`textParts`/`fetchTexts`/`toParsed`：按 part 路径部分取
- [ ] `core/mail/parse.go:390-405` `ParseStructure` 吃的是 `imap.BodyStructure`：要么换成本仓库自己的结构类型，要么让每个后端都转成 go-imap 的类型
- [ ] `core/attachments.go:417-470` 附件按 IMAP part 编号（`attachments.part`）流式取；JMAP 用 blobId，EWS 用 AttachmentId，都不是 MIME 路径
- [ ] `core/unsubscribe.go:207-209` 按头字段补取
- [ ] POP3 只能 `TOP`/`RETR` 整封，「懒加载附件」这个前提不成立

### E. 标记与关键字（IMAP flags 当成数据模型）

依赖：`messages.flags` 存空格分隔的 IMAP 标记字符串；系统标记 `\Seen \Flagged \Answered \Draft \Deleted`；关键字就是标签 slug（ADR 0035）。

- [ ] `core/store/schema.go:42` `messages.flags`；`core/mailsync/fetch.go:820-829` `flagString`
- [ ] `core/messages.go:722-724, :774-814` `systemFlags`、`SetFlags`、`flagWrites`：公开 API 收的就是 IMAP 拼写
- [ ] `MessageSummary.Flags`（`core/messages.go:33`）对外暴露 `\Seen`；`app/reader.go:133, :259`、`app/mail_list.go:65, :196`、`app/contacts.go:407` 直接比对 `` `\Seen` ``
- [ ] SQL 里的 `instr(flags, '\Seen')`：`core/service.go:864, :892`、`core/savedview.go:278`、`core/labels.go:273, :302, :332`、`core/contacts.go:113`、`core/messages.go:399-401, :1033`；`core/rules.go:138`；`core/wit.go:908-912`
- [ ] `core/mailsync/ops.go:375-392` `storeFlags`
- [ ] `core/keywordlabels.go:32-56, :126-170` 关键字和标签互转；JMAP 有 keywords 可以直接对上，EWS 用 Categories，POP3 没有
- 结论：把 `\Seen` 这类拼写当成本地的标准名字留下、由后端负责翻译，改动最小；改公开 API 的代价大得多

### F. 写回服务器（操作队列的推送）

依赖：`MOVE` 或 `COPY`+`\Deleted`+`UID EXPUNGE`；`UIDPLUS` 的 COPYUID 给出目标 UID，拿不到时进 `awaiting_claim`，下轮按 Message-ID + 大小认领；清除必须有 `UIDPLUS`。

- [ ] `core/mailsync/ops.go:130-292` `pushOp`：SELECT 校验、断线后查是否已移走、MOVE/COPY 回退
- [ ] `core/mailsync/ops.go:399-418` `move`，`:436-462` `moved` 按 COPYUID 更新位置
- [ ] `core/mailsync/messages.go:189-270` `claim`/`unclaimable`：「待认领」整个机制是因为 IMAP MOVE 可能不返回新 UID；JMAP、EWS 的移动返回的 ID 是确定的，这套机制用不上
- [ ] `core/mailsync/purge.go:67-111, :172-252` 清除：`UIDPLUS` 门槛、`\Deleted` → `UID EXPUNGE`、多次核对
- [ ] `core/mailsync/ops.go:333-364` `InFolder` 用 `SEARCH HEADER Message-ID`
- [ ] `ops.state` 里的 `awaiting_claim`（schema）；`core/mailsync/purge.go:39-40` `opReady`
- 操作队列本身（ADR 0005 的「本地先改、后台推」）和协议无关，可以留下，只换「怎么执行一条操作」

### G. Gmail 扩展（X-GM-EXT-1 / X-GM-MSGID / X-GM-LABELS）

依赖：一封邮件在多个文件夹各有 UID，`X-GM-MSGID` 是身份；移动可以改成增删标签。ADR 0034。

- [ ] `core/mailsync/fetch.go:118-153, :160-217, :298-304, :489-505`
- [ ] `core/mailsync/gmail.go`（全文件）、`core/mailsync/gmail_ops.go`（全文件）
- [ ] `core/mailsync/folders.go:155-157`、`core/mailsync/messages.go:323-325`、`core/mailsync/ops.go:143-166, :224-235, :250-253, :288-290`
- [ ] `core/mailsync/merge.go:114-` 按 `gmail_msgid` 合并
- [ ] `core/messages.go:1089-1145` 归档语义、`core/service.go:701`、`core/check.go:120`、`core/accounts.go:214`
- [ ] `core/store/schema.go:675-678, :693-695`
- 换成 Gmail REST API 时这一组整体换掉，但「一封邮件 + 多个位置」的表结构可以直接复用（标签 = 位置）；JMAP 的 `mailboxIds` 也是同一个形状

### H. 新邮件通知（IDLE）

依赖：第二条长连接 SELECT INBOX 后 IDLE，靠未经请求的 EXISTS/EXPUNGE/FETCH 唤醒；没有 IDLE 就轮询。

- [ ] `core/worker.go:236-284` `syncRound`
- [ ] `core/mailsync/worker.go:16-40` `Idle`
- [ ] 只盯 INBOX；JMAP 是 EventSource/推送，EWS 是 streaming/pull 订阅，Graph 是 webhook（桌面端收不到，只能轮询 delta），POP3 只能轮询

### I. 已发送归档（APPEND）

依赖：SMTP 发出后，客户端自己 `APPEND` 到已发送，并用 `SEARCH HEADER Message-ID` 防重复。

- [ ] `core/outbox.go:280-312` `processOutbox` 收 `*imapclient.Client`，`:385`、`:508-545` `archiveItem` 的 InFolder + APPEND
- [ ] 发件箱状态里的 `archive_uncertain`/`send_uncertain`（`core/outbox.go:545-` 状态机注释）就是为 APPEND 和 IMAP 查重设计的；JMAP `EmailSubmission` 和 EWS `SendItem` 会自己存一份到已发送，Graph 发信也一样，这一步要能由后端声明「不需要」
- 发信本身（`core/send/smtp.go`、`core/send/graph.go`）已经和 IMAP 分开了

### J. 测试替身

- [ ] `core/internal/testmail/imap.go`、`gmail.go` 是唯一的收信假服务器，绑死 go-imap `imapmemserver`；每种新后端都要有进程内的假服务器（地图里「测试」一项）
- [ ] `core/coretest/coretest.go:215-238, :278-318` `Env.AddAccount`/`IMAP`/`Deliver` 默认起 IMAP；要让 `coretest` 能选后端，现有 23 + 3 个直接用 testmail 的测试文件才能留着当 IMAP 后端的回归测试
- [ ] 断线、拒绝、缺能力这类注入（`WithoutMove`、`DropOnNextUIDExpunge` 等）测的都是 IMAP 特有的不确定性；新后端要有自己的一套

### K. 不用动、或基本不用动的

- `core/mailsync/local.go`、本地归档账户（ADR 0036）：没有服务器位置，`dialAccount` 直接返回 `ErrLocalArchive`（`core/service.go:673`）
- `core/mailsync/thread.go`（除 :151-153 的定位键）、`core/mailsync/merge.go` 的合并逻辑、`tidyThreads`、事件发布、`logArrivals`
- `core/send/*`、`core/conn/classify.go`、`core/conn/xoauth2.go`（SMTP 也在用）
- 操作队列的表结构和状态机（只换执行器），`RoleFolder` 解析

## 四、给接缝设计的观察

- **切口在 `*imapclient.Client` 上。** `mailsync` 的导出函数（`Fetch`、`FetchOlder`、`PushOps`、`PushNewOps`、`QueuePurge`、`InFolder`、`SentFolder`、`CountFolders`、`Idle`）加上 `core` 里 5 段自己写 IMAP 命令的代码（附件、退订头、APPEND、IDLE 选 INBOX、能力探测），就是协议后端要覆盖的全部调用面。
- **同步轮次的顺序写死在 `syncAccount`**（`core/service.go:724-773`）：清除排队 → 发件箱 → 推操作 → 拉取 → 关键字 → 再推。前后两次推送是因为 IMAP 的「待认领」；没有认领的协议用不上第二次推送。
- **`mailsync` 一边讲 IMAP，一边直接写 SQLite**：`Fetch` 里协议调用和 `db.Transaction` 交错在一起。地图要求后端「不依赖 `core` 和 `store`」，所以要把「服务器说了什么」和「本地怎么落库」拆开，落库部分（位置、认领、孤儿、合并）大多可以留在 `mailsync`，变成协议无关的部分。
- **能力分支已经存在**：`MOVE`、`UIDPLUS`、COPYUID、`IDLE`、`X-GM-EXT-1` 各有分支和降级，可以直接作为能力模型（地图里「能力模型」一项）的第一批条目。

## 来源

- 源码：上文所引各文件，提交 `8cff8bfe`
- ADR：`docs/adr/0005-local-first-identity-vs-location.md`、`0034-message-and-its-locations.md`、`0035-labels-as-imap-keywords.md`、`0036-local-archive-account.md`
- `GLOSSARY.md`「期望位置 / 服务器位置」
- `AGENTS.md`「IMAP server quirks」
