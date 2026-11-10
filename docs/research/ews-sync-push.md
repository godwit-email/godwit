# EWS 同步、标记与推送的语义

研究于 2026-11-10，为 #636（地图 #634）。对象是本地部署的 Exchange 2016 / 2019 / SE。
[O] = 微软一手资料（Microsoft Learn 的 EWS 参考与概念文、Open Specifications 的 MS-OX* 规范）；
[S] = 二手（Thunderbird ews-rs / ews_xpcom、EWS Managed API 源码、KDE Akonadi EWS 资源）。
**未核实** 标出的条目需要在真实 Exchange 2019 上测一遍。

主要来源：
- Mailbox synchronization and EWS：https://learn.microsoft.com/en-us/exchange/client-developer/exchange-web-services/mailbox-synchronization-and-ews-in-exchange
- SyncFolderItems operation：https://learn.microsoft.com/en-us/exchange/client-developer/web-service-reference/syncfolderitems-operation
- MS-OXWSSYNC v20250520（PDF）：https://officeprotocoldocs-f5hpbjgea6b8gneq.b02.azurefd.net/files/MS-OXWSSYNC/%5bMS-OXWSSYNC%5d.pdf
- Notification subscriptions：https://learn.microsoft.com/en-us/exchange/client-developer/exchange-web-services/notification-subscriptions-mailbox-events-and-ews-in-exchange
- 订阅亲和：https://learn.microsoft.com/en-us/exchange/client-developer/exchange-web-services/how-to-maintain-affinity-between-group-of-subscriptions-and-mailbox-server
- 通知错误：https://learn.microsoft.com/en-us/exchange/client-developer/exchange-web-services/handling-notification-related-errors-in-ews-in-exchange
- Throttling：https://learn.microsoft.com/en-us/exchange/client-developer/exchange-web-services/ews-throttling-in-exchange
- EWS identifiers：https://learn.microsoft.com/en-us/exchange/client-developer/exchange-web-services/ews-identifiers-in-exchange
- MS-OXOMSG v20250520（PDF）：https://officeprotocoldocs-f5hpbjgea6b8gneq.b02.azurefd.net/files/MS-OXOMSG/%5bMS-OXOMSG%5d.pdf
- MS-OXWSNTIF（PDF）：https://officeprotocoldocs-f5hpbjgea6b8gneq.b02.azurefd.net/files/MS-OXWSNTIF/%5bMS-OXWSNTIF%5d.pdf
- Thunderbird：https://github.com/thunderbird/ews-rs （commit 726235b），`rust/ews_xpcom` 于 https://github.com/mozilla/releases-comm-central

---

## 1. SyncFolderHierarchy / SyncFolderItems

### SyncState
- 不透明的 base64 字符串，每次成功请求后更新；不带 SyncState 即全量（"all items in their current state are returned as if the items have never been synchronized"）。客户端 MUST 保存响应里的 SyncState 供下次用。[O MS-OXWSSYNC §3.1.1, §3.1.4.2.3.8]
- 文件夹层级一个 SyncState（`SyncFolderHierarchy`），**每个文件夹各一个** SyncState（`SyncFolderItems`："you have to sync each folder in the mailbox independently"）。[O Mailbox sync] → 对接缝：EWS 用**文件夹级游标**（`FolderState.Cursor` = 该文件夹的 SyncState），账户级游标可放 `SyncFolderHierarchy` 的 SyncState。
- SyncState 状态存在客户端，不依赖某台邮箱服务器，所以"can survive a mailbox failover"。[O Mailbox sync]
- **大小**：文档没给上限或典型值。Learn 示例都截短了。**未核实**；需在大文件夹上实测（ICS 状态，可能随文件夹内容增长到 KB 级以上）。`messages`/游标列若有长度约束要留余量。
- **失效时间**：文档没有任何 SyncState 过期时间的说法。**未核实**。
- **失效时的错误**：`ResponseClass="Error"`、`ResponseCode` = `ErrorInvalidSyncStateData`，MessageText "Synchronization state data is corrupt or otherwise invalid."，响应里 `SyncState` 为空、`IncludesLastItemInRange` 为 true。[O SyncFolderItems operation 错误示例] ews-rs 的 `ResponseCode` 枚举里也有 `ErrorInvalidSyncStateData`。[S ews-rs `types/common/response.rs`] → 对接缝：这个错误就该映射成 `FolderReset`（丢掉游标、重认领）。其他可能的错误码（如文件夹已删 `ErrorSyncFolderNotFound`）文档只在 ResponseCode 全表里，**未在真实服务器上看过**。

### 分页：MaxChangesReturned 与 IncludesLastItemInRange
- `MaxChangesReturned` 必填，取值 1–512（含）；一次最多返回 512 条变更，余下的要再调。[O MaxChangesReturned 参考；MS-OXWSSYNC §3.1.4.2.4.1 `maxInclusive 512`]
- `IncludesLastItemInRange` = 本次响应是否已含最后一条变更；false 就拿新 SyncState 继续调。[O MS-OXWSSYNC §3.1.4.2.3.6] Thunderbird 循环到它为 true 为止，页大小用 256（`EXCHANGE_MAX_PAGE_SIZE: u16 = 256`）。[S ews_xpcom `sync_messages_for_folder.rs`, `protocol_shared/src/lib.rs`]
- 每页带一个新 SyncState → 正好对上 `Page{Changes, Folders[f].Cursor}` 同一事务写入。

### 变更类型与顺序
- 变更为 `Create` / `Update` / `Delete` / `ReadFlagChange`（EWS 里叫 ReadFlagChange，Managed API 叫 ReadFlagChange/ReadStateChange）。只给变更，不给内容。[O Mailbox sync; MS-OXWSSYNC §3.1.4.2.3.2]
- Exchange 2010 SP2 起"items and folders are returned in order from newest to oldest"。[O Mailbox sync] → 首次同步天然先拿最新的。
- `ReadFlagChange` 只带 ItemId 和 IsRead：直接映射 `FlagsChanged{Seen}`，不必 GetItem。[O Mailbox sync best practices]
- `Update` 不说改了什么；要 GetItem 拿 `IsRead`、`Flag`、`Categories` 等再比较 → 对接缝是 `FlagsChanged`（完整 Flags）。
- `SyncFolderItems` 和 `FindItem` 一样拿不到 Body、Attachments；SyncFolderItems 只能请求"summary properties"。[O SyncFolderItems operation remarks；S EWS Managed API `SyncFolderItemsRequest.cs`: "SyncFolderItems can only handle summary properties"] 微软建议 `BaseShape=IdOnly`，再分批 `GetItem`（建议每批 10 个）。[O Mailbox sync best practices] Thunderbird 就这么做，并注释 SyncFolderItems 会"silently fail to return some metadata fields"。[S]
- 不支持对 Office 365 Group 邮箱和 Public Folders 用 SyncFolderItems。[O]（本项目范围外。）

### SyncScope、Ignore
- `SyncScope`：`NormalItems`（只普通项）或 `NormalAndAssociatedItems`（也含 FAI，即文件夹关联的隐藏项）；可选，Exchange 2007 RTM 不支持。[O SyncScope；MS-OXWSSYNC <7>] 邮件同步用 `NormalItems`（默认）。
- `Ignore`：按 ItemId 跳过若干项，`ArrayOfBaseItemIdsType`，文档没写数量上限。[O Ignore] 用途是"reply all 风暴"之类；对 Godwit 意义不大。

### 按时间限定（NumberOfDays / MaximumCount）
- MS-OXWSSYNC 的 `SyncFolderItemsType`（schema version="Exchange2016"）有三个可选元素：`NumberOfDays`（"the time window (expressed in a number of days) of the items to include"）、`MaximumCount`（"maximum number of items (from the latest received time)"）、`MinimumCount`。[O MS-OXWSSYNC §3.1.4.2.3.8] Learn 的 SyncFolderItems 参考页**没有**列这三个元素。
- EWS Managed API：`NumberOfDays` "was introduced with Exchange 2013"，0 表示不限。[S OfficeDev/ews-managed-api `Core/Requests/SyncFolderItemsRequest.cs`] `MaximumCount`/`MinimumCount` Managed API 未暴露。
- **未核实**：2016/2019 本地版是否真按这些过滤；同一个 SyncState 下改 `NumberOfDays` 会怎样；邮件滑出窗口时是否发 `Delete`。在验证之前，不能把它当 `Window.Since` / `Window.Newest` 的实现依据。
- 稳妥做法（文档支持）：**`FindItem` + `IndexedPageItemView` + `SortOrder` 按 `item:DateTimeReceived` 降序**做窗口和「加载更早」。`IndexedPageItemView` 有 `MaxEntriesReturned`、`Offset`、`BasePoint`（Beginning/End）；响应 `RootFolder` 带 `IndexedPagingOffset`、`TotalItemsInView`、`IncludesLastItemInRange`。[O Perform paged searches] 默认每次最多 1000 项（EWSFindCountLimit；带限制条件的搜索为 250）。[O Throttling]
  - 偏移分页在新邮件插到前面时会错位；文档建议多取一个"anchor"项比对。[O Perform paged searches] 对「加载更早」可改用 `Restriction` `IsLessThan DateTimeReceived <已有最老时间>` 免掉偏移漂移（**设计推断**，未测）。
- 文档里的「过滤同步」模式：先把 SyncFolderItems `IdOnly` 跑到最新、丢弃结果，再用 FindItem 排序分页预取关心的范围，之后用 SyncState 跟踪增量，只看范围内的变化。[O Mailbox sync "Filtered synchronization"] 代价：首次仍要把整个文件夹的 IdOnly 变更翻一遍（每页 ≤512）。

### 对接缝的含义
- 首次同步：`SyncFolderItems` 全量翻页（IdOnly），按窗口只对窗口内的 GetItem 取内容；窗口外的项要么跳过（`Backfill` 时再取），要么记下 id。`Synced` 在最后一页（`IncludesLastItemInRange=true`）置真。
- 「加载更早」（`SyncRequest.Older`）：`FindItem` 按 `DateTimeReceived` 降序、`IsLessThan` 已有最老时间，产出 `MessageAppeared{Backfill:true}`。
- `FolderSnapshot`：EWS 没有便宜的"列出文件夹所有 id+flags"之外的办法，只能 FindItem 分页（IdOnly + IsRead + Flag）；增量路径不需要它。

## 2. ItemId、ChangeKey、移动与复制

- ItemId 区分大小写；存库建议留 512 字节。[O EWS identifiers]
- **移动会换 id**："If an item is moved in the store, the ID can change … An item is actually copied, and a new ID is generated, and then the original item is deleted." 文件夹 id 不变（"folder IDs are immutable"）。[O EWS identifiers] Graph 文档也说普通 id 只在移动时变。[O Graph immutable id]
- `ChangeKey`：项每次改动都换新的；`UpdateItem` 带上它可防并发覆盖（不匹配即失败）。不带 ChangeKey 也行（`ErrorInvalidChangeKey` 只在给了无效值时出现）。[O EWS identifiers] Thunderbird 改标记时 `change_key: None`。[S `change_flag_status.rs`] → 建议标记写回不带 ChangeKey（服务器以最后写为准），避免每次先取。
- `MoveItem` / `CopyItem` 的 `ReturnNewItemIds`：true 时响应带新 ItemId；Exchange 2010 SP1 引入。[O ReturnNewItemIds] Thunderbird 在 > Exchange2010 时设它。[S `copy_move_item.rs`] → 对接缝：Move/Copy 返回 `Done` + 新 `MessageRef`。
- `ConvertId` 可在 `EwsLegacyId`、`EwsId`、`EntryId`、`HexEntryId`、`StoreId`、`OwaId` 之间转换；没有 immutable 格式。[O EWS identifiers 表 3]
- **不可变 id**：`Prefer: IdType="ImmutableId"` 是 Microsoft Graph 的功能（移动到同邮箱其他文件夹 id 不变；移到存档邮箱或导出再导入会变）。[O Graph outlook-immutable-id] **未找到** EWS 或本地 Exchange 2016/2019 支持它的一手资料；按"没有"处理。→ 和 ADR 0037 里 EWS 归为「只有位置」的身份一致。账户内可作弱身份的只有 `InternetMessageId`（不保证唯一）。
- 同步里：移动在源文件夹表现为 `Delete`，在目标文件夹为 `Create`（新 id）。[推断自"copied … then deleted"；**未在真实服务器上核对**事件形态]

## 3. 标记

| Godwit Flags | EWS | 说明 |
|---|---|---|
| Seen | `message:IsRead`（读写） | 同步里有专门的 `ReadFlagChange`。[O] |
| Flagged | `item:Flag` / `FlagStatus`（`NotFlagged`/`Flagged`/`Complete`） | `Flag` 元素 **Exchange 2013 起**才有。[O Flag 参考] Thunderbird 写回时同时设 `item:Flag` 和扩展属性 `PidTagFlagStatus`（tag 0x1090 = 4240），注释说只设一个"status does not apply on the server side"，取消旗标写 0（文档未定义的值）。[S `change_flag_status.rs`] `Complete` 怎么映射到 Godwit 的布尔 Flagged 要定。 |
| Answered | 扩展属性 `PidTagLastVerbExecuted`（PR_LAST_VERB_EXECUTED，0x1081，PtypInteger32） | MS-OXOMSG §2.2.1.14 值：`NOTEIVERB_REPLYTOSENDER` 102、`REPLYTOALL` 103、`FORWARD` 104。配套 `PidTagLastVerbExecutionTime`（0x1082）。[O MS-OXOMSG] 注意：Learn 的 "PidTagLastVerbExecuted Canonical Property" 页贴的表其实是 `PidTagIconIndex`（0x1080）的值（0x105 Replied、0x106 Forwarded），别照它写。[O MS-OXOMSG §2.2.1.10 对照] 读：102/103 → Answered；104 是转发（IMAP 的 `$Forwarded`）。写：EWS 没有专门字段，要 `SetItemField` 扩展属性（Outlook 也会同时改 IconIndex；是否必须一起写 **未核实**）。 |
| Draft | `item:IsDraft` | true = 未发送。[O IsDraft] 由 MAPI `mfUnsent` 决定，首次保存后不能改（**未在 EWS 文档里找到"只读"的明文**，按只读处理）。→ `FeatureDraft` 只读。 |
| Keywords | `item:Categories`（`ArrayOfStringsType`，任意字符串） | 文档没写数量/长度上限。[O Categories] 是 Outlook 的「类别」，用户可见，带主类别列表的颜色；能当自定义关键字，但会出现在 Outlook 里。 |

- 写回都用 `UpdateItem`（`SetItemField` / `DeleteItemField`），可一次批多个 `ItemChange`。[S ews-rs `update_item.rs`]
- 同步 `Update` 后需要 GetItem 这些字段才能出完整 `FlagsChanged`；`Flag` 只在 ≥2013 时请求（Thunderbird 也这样判断）。[S]

## 4. 推送

三种订阅都由 `Subscribe` 创建，事件类型一样：`NewMail`、`Created`、`Deleted`、`Modified`、`Moved`、`Copied`、`FreeBusyChanged`，另有服务器发的 `Status`（心跳）。[O Notification subscriptions]

### Streaming（推荐）
- `GetStreamingEvents` 是挂起的请求；`ConnectionTimeout` 取值 **1–30 分钟**（Exchange 2010 SP1 引入）。到时连接关闭，客户端再发一次。[O ConnectionTimeout；MS-OXWSNTIF §3.1.4.2.4.1 `StreamingSubscriptionConnectionTimeoutType` 1–30；Notification subscriptions "maximum of 30 minutes"]
- 服务器每次最多从队列取 50 个事件编成一个通知；队列空时发 `StatusEvent`；返回的事件从队列删除；订阅的超时计时器重置。[O GetStreamingEvents operation remarks]
- 一个 GetStreamingEvents 最多带 200 个 SubscriptionId。[O 订阅亲和]
- 并发上限是 `HangingConnectionLimit`（web.config），默认 **Exchange 2016/2019 为 10**，2013 为 3；不走 EWSMaxConcurrency。[O Throttling] 同一用户的 Outlook for Mac 等也占这个预算。
- 连接之间订阅能活多久：文档只说订阅可以超时；`ErrorSubscriptionNotFound` = 订阅过期、EWS 进程重启或 id 无效。**未找到**断开后订阅保留时长的一手数字。[O 通知错误]
- KDE Akonadi：streaming 时 `ConnectionTimeout` 设 30 分钟，60 秒收不到任何响应就重发请求；服务器版本不支持时退到 pull，每 10 秒 `GetEvents`。[S kdepim-runtime `ewssubscriptionmanager.cpp`]

### Pull
- `PullSubscriptionRequest` 的 `Timeout` 取值 **1–1440 分钟**：多久不 `GetEvents` 服务器就删订阅；每次成功 GetEvents 重置。[O Timeout；Notification subscriptions 表 5]
- `Watermark` 是邮箱事件表里的书签；Subscribe 可带 watermark 从那里续上。事件表只保留 **30 天**，超出或来自别的数据库 → `ErrorInvalidWatermark`。[O Watermark；PullSubscriptionRequest]
- 但微软建议丢订阅后**不要**带旧 watermark 重订（"causes a linear scan for events, which is costly"），而是新订阅 + 比较文件夹属性 `PR_LOCAL_COMMIT_TIME_MAX`（0x670a0040）和 `PR_DELETED_COUNT_TOTAL`（0x670b0003）找出间隙里的变化。[O 通知错误 "Recovering from lost subscriptions"] 对 Godwit：直接对受影响文件夹跑一轮 `SyncFolderItems` 更简单，SyncState 已经保证不漏。
- `ErrorMissedNotificationEvents`：漏了事件，处理同上。[O]

### Push
- 服务器回调客户端的 HTTP 监听地址，要求客户端可被寻址（"the client is IP addressable"）；微软自己说 push "have fallen out of favor since the advent of streaming notifications"。[O Notification subscriptions] 桌面客户端不适用。

### 推送只做唤醒
- 微软推荐的模式就是"notifications determine *when*，SyncFolderItems determines *what*"；通知不带 ChangeKey、不报已读变化，`NewMail`/`Modified` 都应触发 SyncFolderItems。[O Mailbox sync best practices] 一次用户操作会产生多条通知，建议延迟几秒再同步。[O] → 完全对上 `Watch(wake func(folders))` 的"只唤醒"语义：事件里有 `ParentFolderId`，可直接映射成 `wake([]FolderRef)`。
- `SubscribeToAllFolders` 只占一个订阅名额。[O Throttling]

## 5. 多台 CAS 与亲和

- 普通请求的亲和由服务器处理；**通知例外**，客户端要自己维护亲和，否则请求被路由到没有该订阅的邮箱服务器，返回 `ErrorSubscriptionNotFound`。Exchange 2013 起订阅存在**邮箱服务器**上（2010 在 CAS 上）。[O 订阅亲和]
- 机制（cookie）：
  - `X-AnchorMailbox`：首个订阅请求带，值为锚邮箱的 SMTP 地址；
  - `X-PreferServerAffinity: true`：与 X-AnchorMailbox 一起带；
  - `X-BackEndOverrideCookie`：首个订阅响应里 Set-Cookie 回来，之后的 Subscribe / GetStreamingEvents / GetEvents 都带上。
  - 服务器用 X-PreferServerAffinity + X-BackEndOverrideCookie 路由，两者有效时忽略 X-AnchorMailbox；没 cookie 时按 X-AnchorMailbox 路由。
  - 后续订阅响应**不再**返回该 cookie，客户端要自己缓存。
  - 同时会返回 `exchangecookie`，2013 起被忽略。[O 订阅亲和]
- 单用户场景（Godwit）：X-AnchorMailbox = 用户自己的地址，同一 HTTP client 持 cookie jar 即可；不需要 impersonation。
- 文档写"applies to on-premises or Exchange Online"。负载均衡器是否尊重这个 cookie 取决于部署（**未核实**用户的 MAIL02–05 前面是什么 LB）。
- 订阅所在邮箱服务器不可用时，订阅和排队的通知都丢，要重订；纯同步（只存 SyncState）则不受故障切换影响。[O Mailbox sync]

## 6. 限流

- 本地版的默认值随版本不同、管理员可改、**客户端无法查询**，文档刻意不列默认值。[O Throttling "this article does not provide the default setting values"]
- 文档里给出的数字：
  - `EWSMaxConcurrency`：2010 为 10，2013 与 Exchange Online 为 27（每用户并发请求数，streaming 不计）。[O Throttling] 2016/2019 的默认值文档未单列；ews-rs 测试夹具里见到 "policy part 'MaxConcurrency', policy value '27'"。[S ews-rs `soap.rs`]
  - `EWSMaxSubscriptions`：Exchange 2013 本地 5000，Online 20。[O 订阅亲和、通知错误]
  - `HangingConnectionLimit`：2016/2019/Online 10，2013 为 3。[O Throttling]
  - `EWSFindCountLimit`：1000；带 restriction 或 AQS 的搜索 250。[O Throttling]
  - `ConcurrentSyncCalls`（**Exchange 2016/2019 新增**）：每用户并发 SyncFolderHierarchy/SyncFolderItems 数。[O Throttling 表 1] ews-rs 夹具里的真实报错："policy part 'ConcurrentSyncCalls', policy value '5' … Suggested backoff time 5000 ms"（来源服务器未注明，可能是 Online）。[S ews-rs `soap.rs`] → 同一账户的多文件夹同步不要并发超过 ~5，最好串行。
  - 批请求执行上限一分钟，超了会超时。[O Throttling]
- 撞限的错误：
  - `ErrorExceededConnectionCount`（EWSMaxConcurrency / 流连接数）、`ErrorExceededSubscriptionCount`、`ErrorExceededFindCountLimit`、`ErrorServerBusy`（带 `BackOffMilliseconds`）。[O Throttling 表 3]
  - HTTP 层：503 = 请求在 IIS 排队；500 + `ErrorServerBusy`（可能带 `BackOffMilliseconds`）；200 + `ErrorInternalServerError`，内层可能是 `ErrorServerBusy`。[O Throttling 表 4]
  - `ErrorServerBusy` 可能以 SOAP Fault 形式出现（`faultcode a:ErrorServerBusy`，`detail` 里 `<t:Value Name="BackOffMilliseconds">`），也可能在 `ResponseMessage` 里（`MessageXml/Value Name="BackOffMilliseconds"`）。两种都要解析。[S ews-rs `soap.rs` 测试]
  - 微软："If you do get throttled, stop sending requests … wait for the back off time to expire"。[O Mailbox sync]

## 7. 对三张下游票的要点

- **增量同步：SyncState 与接缝游标**：文件夹级游标 = SyncFolderItems 的 SyncState，账户级 = SyncFolderHierarchy 的 SyncState；每页 ≤512 条带新 SyncState，可逐页落库；`ErrorInvalidSyncStateData` → `FolderReset`；窗口与「加载更早」用 FindItem 按 DateTimeReceived，`NumberOfDays` 先实测再说。
- **推送**：streaming + `wake` 唤醒；连接 ≤30 分钟循环续；带 X-AnchorMailbox / X-PreferServerAffinity 并保存 X-BackEndOverrideCookie；`ErrorSubscriptionNotFound` / `ErrorInvalidSubscription` → 重订 + 对订阅文件夹补一轮同步；不用 push。
- **协议能力表与身份**：身份「只有位置」（无 immutable id）；Move/Copy 用 `ReturnNewItemIds` 得 `Done`；Seen/Flagged 读写（Flagged 需 ≥2013），Answered 靠 0x1081 扩展属性，Draft 只读，Keywords 可用 Categories 但用户可见。

## 未核实 / 待实测清单

1. SyncState 实际大小与有无过期。
2. `NumberOfDays` / `MaximumCount` 在 2016/2019 的实际效果，及窗口变化时的 Delete 行为。
3. 移动在 SyncFolderItems 里的事件形态（源 Delete + 目标 Create）。
4. 写 `PidTagLastVerbExecuted` 是否需同时写 `PidTagIconIndex` 才在 Outlook 显示"已回复"。
5. `IsDraft` 能否通过 UpdateItem 修改。
6. streaming 订阅在连接断开后能保留多久；用户环境的负载均衡器是否尊重 X-BackEndOverrideCookie。
7. 2016/2019 默认 throttling 值（`Get-ThrottlingPolicy` 可查，但客户端不能）。

另：MS-OXWSMSHR 是文件夹共享协议，和本票无关，未读。
