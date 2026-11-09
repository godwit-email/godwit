# 各协议的邮件模型对照

调研于 2026-11-09，回答 [#585](https://github.com/godwit-email/godwit/issues/585)（地图 [#584](https://github.com/godwit-email/godwit/issues/584)）。只用协议规范和厂商官方文档，引用编号见文末。标「推断」的是从文档推出、文档没有直接写的结论；标「未核实」的是没在一手资料里找到原文的说法。

Godwit 现状按 `core/store/schema.go` 核对：`message_locations(message_id, folder_id, shown, uidvalidity INTEGER, uid INTEGER, entered_at, date)`，`UNIQUE(folder_id, uid)`；文件夹的拉取游标是 `folders.uidvalidity` + `folders.last_uid`。也就是说，**服务器位置**和**拉取游标**现在都是 IMAP 的整数形状。

## 一、总表

| | 一封邮件的标识 | 移动后标识变不变 | 容器模型 | 增量同步 | 推送（桌面客户端可用？） | 可改的状态 | 发信 |
|---|---|---|---|---|---|---|---|
| IMAP（现状） | 文件夹 + UIDVALIDITY + UID；Gmail 另有 `X-GM-MSGID` | 变（新文件夹新 UID） | 单文件夹；Gmail 多标签 | 每文件夹 UID/UIDVALIDITY | IDLE，可用 | 系统标记 + 关键字 | 另走 SMTP |
| POP3 | UIDL 字符串（1–70 字符，可选扩展）[1 §7] | 无移动 | 只有一个 maildrop [1] | 每次会话比对 UIDL 列表 | 无 [2] | 无，只能 DELE [1 §5] | 无，另走 SMTP [1 §2] |
| EWS | ItemId（不透明字符串，建议留 512 字节）+ ChangeKey（版本）[5][6] | **变**（移动是复制 + 删除）[5] | 单文件夹 | 每文件夹 SyncFolderItems + 不透明 SyncState [7][8] | streaming 通知，单连接最长 30 分钟，可用 [9] | IsRead、Flag、Categories（未核实全部字段） | CreateItem `SendAndSaveCopy` / SendItem [10] |
| Microsoft Graph | `id` 字符串；可选 ImmutableId [11][12] | 默认变；ImmutableId 在同一邮箱内不变 [11] | 单文件夹（`parentFolderId`）[12] | 每文件夹 `messages/delta`，不透明 deltaLink [13] | 只有 webhook / Event Hubs / Event Grid，**桌面不可用** [14] | isRead、flag、categories、importance、inferenceClassification [15] | `sendMail`（JSON 或 base64 MIME），202 无 id [16] |
| JMAP | Email `id`，服务器分配、不可变、账户内唯一 [3 §1.2][4 §4.1.1] | 不变 [4 §2] | **多 mailbox**（`mailboxIds`，至少一个）[4 §4.1.1] | 账户级 state + `Email/changes` [3 §5.1–5.2] | EventSource 可用；PushSubscription 需 https 回调 [3 §7] | 只有 `mailboxIds` 和 `keywords` [4 §4.6] | 先存 Email，再 `EmailSubmission/set` [4 §7] |
| Gmail REST API | message `id`（不可变）[17] | 不变（没有移动，只有改标签） | **多标签**，已读、星标也是标签 [18] | 账户级 `history.list` + historyId [19] | 只能经 Cloud Pub/Sub，**桌面不实用**（推断）[20] | 改 `labelIds`（SENT、DRAFT 不能手动改）[18] | `messages.send` / `drafts.send`，`raw` 是 base64url 的 RFC 2822 [21] |

## 二、逐个协议

### POP3（RFC 1939、RFC 2449）

- **标识。** 消息号只在一次会话内有效，下次会话重新编号 [1 §4]。跨会话的标识是 UIDL：服务器分配的 1–70 个字符，在一个 maildrop 内唯一、跨会话不变，规范只说服务器「should never reuse」[1 §7]。UIDL 是可选命令，用 CAPA 判断 [2 §6.8]。没有 UIDVALIDITY 这样的「全部作废」信号。
- **同步与推送。** 没有增量，只能每次连上后取 UIDL 列表和本地比对。规范不涉及推送 [2]。`LOGIN-DELAY` 告诉客户端两次登录之间至少隔多少秒 [2 §6.5]，限制轮询频率。`EXPIRE` 告诉客户端服务器保留邮件的最短天数，`EXPIRE 0` 表示不许留在服务器，`NEVER` 表示服务器不删 [2 §6.7]。
- **容器。** 只有一个 maildrop，没有文件夹，也没有标记 [1]。
- **修改。** 只有 DELE，而且只是标记，QUIT 进入 UPDATE 状态时才真正删除 [1 §5–6]；QUIT 之前断线，删除不生效。
- **发信。** 规范明说不管邮件如何进入传输系统，建议用 SMTP [1 §2]。

**放不进 Godwit 模型的地方：**

1. **服务器位置的形状。** 服务器位置只能是（收件箱，UIDL 字符串），`uid INTEGER` 存不下，`uidvalidity` 永远为空。
2. **期望位置大多没有服务器。** 已发送、废纸篓、归档、用户文件夹只能是本地文件夹（类似本地账户，ADR 0036），移入这些文件夹不对应任何服务器操作。要先定：把邮件移出收件箱时是否在服务器上 DELE（「下载后保留 N 天」这类策略），而不是套用 IMAP 的 MOVE。
3. **和 0034「位置不剩就删邮件」冲突。** 服务器因 `EXPIRE` 或别的客户端删了邮件，UIDL 消失。按 0034，收件箱位置被删、邮件一个位置不剩就会被删除；但 POP3 的语义是「下载到本地就归本地」，服务器上消失不该删本地邮件。POP3 下服务器位置消失只能清空服务器引用，不能删除位置和邮件。
4. **标记、标签只在本地。** 已读、星标、标签都无处推送；ADR 0035 的关键字同步不适用，开关应不可用。操作队列里不存在可以推送的标记操作。
5. **删除是会话事务。** DELE 要等到 QUIT 才生效，「结果不确定的操作先核对」要按下一次会话的 UIDL 列表核对。

### Exchange Web Services（EWS）

- **标识。** ItemId 的 `Id` 区分大小写，`ChangeKey` 标识一个版本 [6]。文档要求不要假定 ItemId 以后一直有效：移动实际上是复制一份、生成新 ID、再删掉原件；文件夹 ID 不变 [5]。`UpdateItem` 在默认的 AutoResolve 冲突处理下要求 ChangeKey，`SendItem` 也要求 ChangeKey，否则返回 `ErrorStaleObject` [6]。
- **同步。** 文件夹层级用 SyncFolderHierarchy，邮件用 SyncFolderItems，**每个文件夹单独同步**，各有一个 base64 的 SyncState [7][8]。变化类型是 Create、Update、Delete 和 ReadFlagChange [7]。
- **推送。** streaming、pull、push 三种；官方推荐 streaming，一个挂起的 GET 最长保持 30 分钟，到期重连 [9]。事件有 NewMail、Moved、Copied、Modified、Created、Deleted [9]。官方建议通知只用来知道「何时」变了，「变了什么」再走 SyncFolderItems，因为通知不带 ChangeKey，也不报已读变化 [7]。
- **可改状态。** IsRead、Flag、Categories（字段名按 EWS Message/Item 元素，未逐个核对原文）。没找到对应 IMAP `\Answered` 的布尔字段（未核实）。
- **发信。** 新邮件用 `CreateItem MessageDisposition="SendAndSaveCopy"`，由服务器存到 `SavedItemFolderId`（通常是已发送）；已存的草稿用 `SendItem` [10]。
- **生命周期。** 微软宣布从 2026-10-01 起在 Exchange Online 开始拦截 EWS 请求，本地部署的 Exchange Server 不受影响 [22]。截至本文日期，**EWS 只对本地部署的 Exchange 有意义**，Microsoft 365 账户应走 Graph。

**放不进 Godwit 模型的地方：**

1. **服务器位置的形状。** ItemId 是绑定在当前文件夹上的不透明字符串，语义上很接近 IMAP 的「文件夹 + UID」（移动后失效），所以 0005 的「待认领」思路照样成立；但 `uid INTEGER` 和 `UNIQUE(folder_id, uid)` 存不下，`uidvalidity` 没有对应物。
2. **游标形状。** 文件夹游标是不透明的 SyncState 字符串，不是 `uidvalidity + last_uid`。
3. **ChangeKey 不是位置。** 它是乐观并发的版本号。操作队列推送标记时要带上最近见到的 ChangeKey，冲突时重拉再试；现有队列只按 UIDVALIDITY 判断冲突（0005），没有「版本」这个概念。
4. **移动后的认领。** 现有认领依赖 COPYUID 或 Message-ID + 大小。EWS 的 MoveItem 是否在响应中返回新 ItemId 本次没核实；如果不返回，就只能退回 Message-ID + 大小。
5. **标签。** 0035 的「标签 ↔ IMAP 关键字」在 EWS 上要改成「标签 ↔ Categories」。Categories 是自由字符串，有大小写和显示名，不是小写 slug，0035 的「关键字就是 slug」规则不能照搬。
6. **发信会在服务器上留副本。** `SendAndSaveCopy` 由服务器写已发送，Godwit 不应再 APPEND，否则已发送里有两份。

### Microsoft Graph（收信）

- **标识。** `message.id` 默认在邮件换容器时改变；加上 `Prefer: IdType="ImmutableId"` 后，只要邮件留在同一邮箱里（包括移到别的文件夹）就不变，移到存档邮箱或导出再导入时才会变 [11][12]。这个头只对带上它的那次请求生效，每次请求都要带 [11]。文件夹 ID 本来就不变 [11]。`translateExchangeIds` 可以把已存的旧 ID 转成不可变 ID，一次最多 1000 个 [11]。
- **容器。** 每封邮件只有一个 `parentFolderId` [12]。有本地化无关的 well-known 名字（inbox、sentitems、deleteditems、junkemail、drafts、archive 等）[23]。
- **同步。** delta 查询**按文件夹**进行，要跟踪整个文件夹树就得逐个文件夹跟踪；返回 nextLink / deltaLink，里面的 token 对客户端不透明；删除以 `@removed` 返回 [13]。delta 也能带 ImmutableId 头，已有的 deltaLink 不用重新同步就能切过去 [11]。
- **推送。** 投递渠道只有 webhook、Azure Event Hubs 和 Azure Event Grid [14]，都要求服务端能被访问，桌面应用直接收不到（推断）。邮件订阅最长不到 7 天，带资源数据的不到 1 天；平均延迟不到 1 分钟、最长 3 分钟 [14]。
- **可改状态。** 任何时候都能改的是 `isRead`、`flag`（followupFlag）、`categories`、`importance`、`inferenceClassification`（focused / other）等；主题、正文、收件人只有草稿能改 [15]。message 资源上没有「已回复」字段 [12]。
- **发信。** `POST /me/sendMail` 收 JSON 或 base64 编码的 MIME，默认存到已发送，成功返回 `202 Accepted`，响应体为空，202 也不代表处理完毕 [16]。另一条路是先建草稿再 `send`；用 ImmutableId 建草稿时，发送后可以用同一个 ID 在已发送里找到这封 [11]。Godwit 在 #577 已经用 `/me/sendMail` 发送存好的 MIME（SMTP AUTH 被禁时）。

**放不进 Godwit 模型的地方：**

1. **身份和位置可以都来自服务器。** 用 ImmutableId 时，`id` 是账户内稳定的身份（像 `X-GM-MSGID`），位置只是「`parentFolderId` 是哪个」；服务器位置不需要每文件夹一个 UID，只需要「服务器确认它在这个文件夹」。现有表里没有「无 UID、只有确认在不在」的位置形状。不用 ImmutableId 就退回 EWS 那种「移动后 ID 变」的模型；没有理由不用。
2. **游标。** 每文件夹一个不透明 deltaLink，替代 `uidvalidity + last_uid`。
3. **移动在 delta 里是「源 removed + 目标新增」。** 用了 ImmutableId，两边是同一个 id，按 id 认领即可，不需要 Message-ID + 大小。0034 的「扫完所有文件夹才判定删除」照样需要：只看源文件夹的 `@removed` 分不清移走还是删了。
4. **没有桌面能用的推送。** 只能轮询 delta；「新邮件即时到达」在 Graph 后端上要降级。
5. **没有「已回复」。** 现有共享标记里的已回复只能留在本地（或者写扩展属性，未核实）。标签同样对应 `categories`，问题同 EWS 第 5 条。
6. **发信无回执 ID。** `sendMail` 返回 202、不给 id，「最多发一次」（ADR 0003、0006）的确认只能靠之后在已发送里找到；用「建草稿 + send」能拿到 id，更容易确认。服务器会自动存已发送，Godwit 不能再存一份。

### JMAP（RFC 8620、RFC 8621）

- **标识。** 所有记录 ID 由服务器分配、不可变 [3 §1.2]，在同一账户的同一类型内唯一 [3 §1.6.3]。Email 的 `id`、`blobId`、`threadId` 都不可变；换 mailbox 不改 id [4 §2、§4.1.1]。**例外：** 服务器合并会话时，必须删掉受影响的 Email 再重新插入，它们会得到新 id [4 §3]。
- **容器。** `mailboxIds` 是集合，一封 Email 任何时候至少属于一个 mailbox，服务器可以让它同时属于几个 [4 §1、§4.1.1]。一个 mailbox 最多一个 role，同一账户内两个 mailbox 不能有相同 role [4 §2]。是否允许多 mailbox 由服务器决定（Fastmail 式标签 vs. 传统文件夹）。
- **可改状态。** `Email/set` 能改的只有 `mailboxIds` 和 `keywords`，两者都能整体替换或逐项增删 [4 §4.6]。有定义的关键字是 `$seen`、`$flagged`、`$answered`、`$draft`，返回时一律小写；IMAP 的 `\Deleted` 不存在，带 `\Deleted` 的邮件不能经 JMAP 看到 [4 §4.1.1]。删除就是 destroy（硬删），进废纸篓是改 `mailboxIds`。
- **同步。** 每种数据类型一个账户级 state 字符串，数据一变它必须变 [3 §5.1]；用 `Foo/changes` 拿增量；服务器算不出来时返回 `cannotCalculateChanges`，客户端必须丢弃缓存重建，服务器应能从 30 天内给出的 state 算出增量 [3 §5.2]。`Email/queryChanges` 可增量维护某个查询的结果 [4 §4.5]。
- **推送。** StateChange 对象经两种通道送达：PushSubscription（服务器向 `https://` 回调 POST，需先验证）和 EventSource（客户端长连接，带 `ping`、`closeafter` 参数）[3 §7.1–7.3]。桌面客户端可以用 EventSource。
- **发信。** 要发的 Email 必须已存在于服务器上（不一定是草稿）；`EmailSubmission` 带 `identityId`、`emailId`、可选的 `envelope`（省略时由服务器按邮件头生成），`undoStatus` 是 pending / final / canceled，pending 时可撤销 [4 §7]。`onSuccessUpdateEmail` / `onSuccessDestroyEmail` 让服务器在提交成功后隐式执行一次 `Email/set`（如去掉 `$draft`、移到已发送）[4 §7.5]。Identity 决定能用哪些发件地址，不能发的地址返回 `forbiddenFrom` [4 §6]。
- **会话资源。** 从 `/.well-known/jmap` 取 Session [3 §2.2]，里面的 `apiUrl` 等是完整 URL [3 §2]。规范没限制它们与 Session 同主机（推断：可能指向另一台主机）。

**放不进 Godwit 模型的地方：**

1. **它其实最贴合 0034。** 一封 Email = 一封本地邮件，`mailboxIds` = 位置集合，`keywords` = 整封共享的标记，和 0034 对 Gmail 的设计同构。但**服务器位置没有 UID**：每个位置只有「服务器确认在这个 mailbox」这一位；`uid INTEGER`、`UNIQUE(folder_id, uid)`、`uidvalidity` 全部无对应物。
2. **游标是账户级的。** 一个 Email state + 一个 Mailbox state，不是每文件夹的 `uidvalidity + last_uid`。「UIDVALIDITY 重置只删对应位置」变成「`cannotCalculateChanges` 时整个账户重建」，要能在不丢本地标签、规则记录、Wit 引用的前提下按 id 重新认领。
3. **id 也会变。** 会话合并会让 Email 换 id [4 §3]。本地身份不能直接等于 JMAP id，需要一条「旧 id 消失、新 id 出现」时的认领路径（可用 `blobId` 判断内容相同；推断，规范没保证合并后 blobId 不变）。0034 的别名表可以复用。
4. **移动没有源和目标两步。** 一次 `Email/set` 原子地改 `mailboxIds`；0034 为 Gmail IMAP 设计的「先加目标再去源、结果不确定时逐个 SEARCH 核对」用不上，核对改成重读这封 Email 的 `mailboxIds`。这是简化，不是冲突。
5. **没有 `\Deleted`、删除即硬删。** Service 本来不提供硬删除（0005），彻底清除废纸篓才会用到 destroy。
6. **标签有两种落法。** ADR 0035 的「标签 ↔ 关键字」可以直接映射到 JMAP keywords；如果服务器允许多 mailbox，标签也可以是 mailbox。两者都合规，要选一种。
5. **发信要先把邮件上传到服务器。** 发件箱里的 MIME 要先变成服务器上的 Email（上传 blob + `Email/import` 或 `Email/set`），再提交；这两步之间失败，服务器上会留下一封草稿。`undoStatus: pending` 和延迟发送能力可以和 Godwit 的撤销发送对接，但撤销窗口的权威从本地移到了服务器。`apiUrl` 可能指向另一台主机，外发声明要包括它（地图「外发与凭据」一条）。

### Gmail REST API

- **标识。** message `id` 不可变，`threadId` 标识会话，`labelIds` 是标签集合，`historyId` 是最后修改它的历史记录 [17]。API 的 id 与 IMAP 的 `X-GM-MSGID` 是什么关系，官方文档没写（未核实），升级路径不能假定二者能互相换算。
- **容器。** 标签和邮件多对多，会话的标签是其中各封邮件标签的并集 [18]。INBOX、SPAM、TRASH、UNREAD、STARRED、IMPORTANT 和 CATEGORY_* 可以手动加减，SENT、DRAFT 由系统自动打、不能手动改 [18]。**已读、星标就是标签**（UNREAD、STARRED）[18]。归档 = 去掉 INBOX；API 里没有「所有邮件」这个文件夹。
- **同步。** 首次必须全量同步；之后用 `history.list` 从 `startHistoryId` 取增量，返回变了的邮件 ID 和变化类型；startHistoryId 太旧返回 404，只能全量同步；历史记录通常至少保留一周，但可能更短 [19]。
- **推送。** 只经 Cloud Pub/Sub：先建 topic，`watch` 至少每 7 天续一次，通知里只有邮箱地址和新的 historyId，可按标签过滤；订阅可以是 webhook 推送，也可以是客户端拉取 [20]。拉取订阅需要 Google Cloud 项目和 Pub/Sub 凭据，分发给终端用户的桌面应用不实用（推断）。
- **修改。** `messages.modify` / `batchModify` 增减标签；`trash` 进废纸篓；`delete` 立即永久删除；`insert` 类似 IMAP APPEND，`import` 走正常的投递扫描和分类 [17]。
- **发信。** `messages.send` 直接发，`drafts.send` 发草稿；邮件是 base64url 编码的 RFC 2822 MIME，放在 `raw` 里；要归入原会话，需要 `threadId`，且 `Subject`、`References`、`In-Reply-To` 要对得上 [17][21]。

**放不进 Godwit 模型的地方：**

1. **标记和位置在同一个命名空间。** Godwit 把 Gmail 标签当文件夹（位置），把已读、星标当共享标记（0034）。API 里两者都是 `labelIds` 的成员，后端要把 UNREAD、STARRED 拆出来变成标记，其余映射成位置。IMPORTANT、CATEGORY_* 归哪边要定（现在 IMAP 侧看不到 CATEGORY_*；推断）。
2. **服务器位置没有 UID。** 和 JMAP 一样，每个位置只有「是否带这个标签」一位。
3. **0034 里的 All Mail 难题消失。** API 没有「所有邮件」文件夹，归档后邮件仍可按 id 寻址，不需要 0034 那套「隐藏 All Mail 时等撤销窗口」。这又是简化，但同一个账户如果在 IMAP 和 API 两个后端之间切换，位置集合会不同（IMAP 有 `[Gmail]/All Mail` 位置，API 没有）。
4. **游标是账户级的 historyId**，过期（404）就整账户重建，同 JMAP 第 2 条。
5. **最小标签增删天然支持。** 0034 用 fork 的 `STORE ±X-GM-LABELS` 做的事，API 里是 `modify` 的 `addLabelIds` / `removeLabelIds`，没有 MOVE 能力差异。
6. **没有桌面能用的推送。** 只能轮询 `history.list`。
5. **用户标签与 Godwit 标签。** Gmail 用户标签已经被 Godwit 当作文件夹；0035 的「标签 ↔ 关键字」在 Gmail API 上没有对应物（API 没有 IMAP 关键字），只能关闭或改成映射到 Gmail 标签，后者会和「标签即文件夹」打架。

## 三、对接缝设计的结论

1. **「服务器位置」要换成协议自定义的不透明引用。** 现在是 `uidvalidity INTEGER + uid INTEGER`。五种协议里：POP3 是 UIDL 字符串，EWS 是 ItemId 字符串（+ ChangeKey 版本），Graph 默认 id 是字符串，JMAP、Gmail API、Graph ImmutableId 的位置只有「在 / 不在」一位，身份另有账户级 id。`UNIQUE(folder_id, uid)` 需要相应放宽。
2. **身份有三种来源**，接缝要都能表达：
   - 账户内稳定的服务器 id：JMAP Email id（会话合并时例外）、Gmail API message id、Graph ImmutableId，相当于今天的 `X-GM-MSGID` 路径；
   - 绑在位置上、移动就变的 id：IMAP UID、EWS ItemId、Graph 默认 id，走今天的 COPYUID / 待认领路径；
   - 只有一个位置、没有移动：POP3 UIDL。
3. **同步游标要能是每文件夹的不透明串（EWS SyncState、Graph deltaLink），也要能是账户级的（JMAP state、Gmail historyId）**，并且要有「游标失效 → 整个账户或文件夹按身份重新认领、不丢本地状态」的路径（JMAP `cannotCalculateChanges`、Gmail 404、IMAP UIDVALIDITY 变化是同一类事件）。
4. **多容器只有 JMAP（视服务器而定）和 Gmail**，0034 的「一封邮件多个位置」已经够用；单容器的 EWS、Graph、POP3 等同普通 IMAP。0034 的「位置不剩才删邮件」对 POP3 不成立，需要按后端区分「服务器位置消失」的含义。
5. **共享标记的交集很小。** 已读五种都有（POP3 只能本地）；星标在 Graph / EWS 是 followup flag（有三态）；已回复只有 JMAP 和 IMAP 有；自定义标记分别是 JMAP keywords、Graph / EWS categories、Gmail labels。界面和 Wit 需要按后端能力降级（地图「能力模型」一条）。0035 的开关要按后端改语义或禁用。
6. **推送：桌面可用的只有 IMAP IDLE、JMAP EventSource、EWS streaming。** Graph 和 Gmail API 的推送都要一个公网可达的服务端或云项目，后端接缝要把「推送」作为可选能力，退化为轮询。
5. **发信三种形状。** 提交 MIME 后服务器自动存已发送（Graph `sendMail`、EWS `SendAndSaveCopy`、Gmail `messages.send`）；先在服务器存 Email 再提交（JMAP、Graph 草稿 + send、EWS SendItem）；POP3 没有发信，仍要 SMTP 发信后端。前两种 Godwit 都不能再自己 APPEND 已发送。Graph `sendMail` 不回 id，「最多发一次」的确认要靠事后在已发送里找。
6. **选择第一种新协议时注意 EWS 已在 Exchange Online 停用**（2026-10-01 开始拦截）[22]，只剩本地部署的 Exchange 用户受益；Microsoft 365 用户的收信应走 Graph。

## 引用

1. RFC 1939, *Post Office Protocol - Version 3*. https://www.rfc-editor.org/rfc/rfc1939.html
2. RFC 2449, *POP3 Extension Mechanism*. https://www.rfc-editor.org/rfc/rfc2449.html
3. RFC 8620, *The JSON Meta Application Protocol (JMAP)*. https://www.rfc-editor.org/rfc/rfc8620.html
4. RFC 8621, *The JMAP for Mail*. https://www.rfc-editor.org/rfc/rfc8621.html
5. Microsoft Learn, *EWS identifiers in Exchange*. https://learn.microsoft.com/en-us/exchange/client-developer/exchange-web-services/ews-identifiers-in-exchange
6. Microsoft Learn, *ItemId*（EWS 参考）. https://learn.microsoft.com/en-us/exchange/client-developer/web-service-reference/itemid
7. Microsoft Learn, *Mailbox synchronization and EWS in Exchange*. https://learn.microsoft.com/en-us/exchange/client-developer/exchange-web-services/mailbox-synchronization-and-ews-in-exchange
8. Microsoft Learn, *SyncFolderItems*（EWS 参考）. https://learn.microsoft.com/en-us/exchange/client-developer/web-service-reference/syncfolderitems
9. Microsoft Learn, *Notification subscriptions, mailbox events, and EWS in Exchange*. https://learn.microsoft.com/en-us/exchange/client-developer/exchange-web-services/notification-subscriptions-mailbox-events-and-ews-in-exchange
10. Microsoft Learn, *Send email messages by using EWS in Exchange*. https://learn.microsoft.com/en-us/exchange/client-developer/exchange-web-services/how-to-send-email-messages-by-using-ews-in-exchange
11. Microsoft Learn, *Obtain immutable identifiers for Outlook resources*. https://learn.microsoft.com/en-us/graph/outlook-immutable-id
12. Microsoft Learn, *message resource type*（v1.0）. https://learn.microsoft.com/en-us/graph/api/resources/message?view=graph-rest-1.0
13. Microsoft Learn, *Get incremental changes to messages in a folder*. https://learn.microsoft.com/en-us/graph/delta-query-messages
14. Microsoft Learn, *Set up notifications for changes in resource data*. https://learn.microsoft.com/en-us/graph/change-notifications-overview
15. Microsoft Learn, *Update message*（v1.0）. https://learn.microsoft.com/en-us/graph/api/message-update?view=graph-rest-1.0
16. Microsoft Learn, *user: sendMail*（v1.0）. https://learn.microsoft.com/en-us/graph/api/user-sendmail?view=graph-rest-1.0
17. Google for Developers, *REST Resource: users.messages*. https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.messages
18. Google for Developers, *Manage labels*. https://developers.google.com/workspace/gmail/api/guides/labels
19. Google for Developers, *Synchronize clients with Gmail*. https://developers.google.com/workspace/gmail/api/guides/sync
20. Google for Developers, *Push notifications*. https://developers.google.com/workspace/gmail/api/guides/push
21. Google for Developers, *Send email*. https://developers.google.com/workspace/gmail/api/guides/sending
22. Microsoft Exchange Team Blog, *Retirement of Exchange Web Services in Exchange Online*. https://techcommunity.microsoft.com/blog/exchange/retirement-of-exchange-web-services-in-exchange-online/3924440
23. Microsoft Learn, *mailFolder resource type*（v1.0）. https://learn.microsoft.com/en-us/graph/api/resources/mailfolder?view=graph-rest-1.0
