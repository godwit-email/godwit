# 假 EWS 服务器的做法（#638）

调研于 2026-11-10，服务于地图 #634 与决定票 #647「测试：契约测试用什么假服务器」。标注：[源] = 读过源码或官方文档原文；[推] = 推断或凭经验、未逐条核对，落地前要验证。

## 结论

**自己写一个进程内的 Go 假 EWS 服务器，放在 `core/internal/testmail/ews.go`，和 POP3 的做法一样；不用现成的开源假服务器，也不做录制回放。** 认证默认用 Basic，另外在同一个假服务器里实现一个最小的服务器端 NTLMv2 校验（约 150 行），只给一两条认证测试用。估计 1200–1800 行（不含测试）。

理由：

- 现成的东西没有一个能直接用在 Go 里，而最接近的（Thunderbird 的 `EwsServer.sys.mjs`）也没有订阅、Autodiscover、FindItem 和 NTLM，正好缺我们契约要的几块。
- 录制回放需要一台能录的 Exchange；唯一的真实环境是用户的生产 Exchange 2019，录下来的是真信，不能进仓库。回放也做不了契约要的故障注入（Reset、RejectAuth、Watch 中途投信）。
- 仓库的先例：协议库自带内存服务器时放在 fork 里（JMAP 的 `jmapmemserver`），否则自写放 testmail（POP3，PR #632 的 `core/internal/testmail/pop3.go`，323 行）。Go 的 EWS 库（#637、#646）都没有服务器端，所以走 POP3 那条路。若 #646 最后决定自写或 fork 一个 EWS 客户端库，假服务器仍放 testmail：它要实现的是契约的 `backendtest.Server` 钩子，和 Godwit 的测试强绑定。

## 1. 现成方案对照

| 方案 | 语言 / 许可 | 覆盖 | 状态 | 能不能用 |
|---|---|---|---|---|
| Thunderbird `mailnews/test/fakeserver/EwsServer.sys.mjs` | JS（Gecko 内部模块）/ MPL-2.0 | SyncFolderHierarchy、GetFolder、SyncFolderItems、CreateItem、CreateFolder、MoveItem、CopyItem、MoveFolder、CopyFolder、UpdateItem、GetItem（含 `IncludeMimeContent`）、DeleteItem、MarkAsJunk、DeleteFolder、EmptyFolder、MarkAllItemsAsRead、UpdateFolder；Basic 和 Bearer 认证；故障钩子 `busyResponses`（ErrorServerBusy）、`maxSyncItems`（强制分页）、`version` | 活跃，1960 行 | 不能直接跑（依赖 `httpd.sys.mjs`、`DOMParser`、`MockServer.sys.mjs`）。**可以照抄形状**：它的响应模板是「删到最少仍能被真客户端接受」的 XML，是写 Go 版最好的参照。没有 Subscribe / GetStreamingEvents / GetEvents、FindItem、Autodiscover、NTLM。 [源] |
| evolution-ews `tests/`（libuhttpmock `UhmServer`） | C / LGPL | 录制回放：`--write-traces` 对真服务器录，默认离线回放 trace 文件 | trace 只有 `Exchange2007_SP1`、`Exchange2010_SP2` 下的文件夹增删移和日历时区；最后一次改 trace 是 2014-01 | 不能用：版本太老、不是邮件同步、需要真服务器重录。证明了录制回放的路子维护不下去。 [源] |
| exchangelib `tests/` | Python / BSD | 绝大多数测试连真服务器（`settings.yml`，CI 用加密的 `settings.yml.ghenc` 连作者自己的测试服务器，没有就 `SkipTest`）；Autodiscover、transport（含 NTLM 的 `WWW-Authenticate` 识别）用 `requests_mock` 打桩 | 活跃 | 不能用：靠真服务器。`requests_mock` 打桩只测客户端解析，测不了状态。 [源] |
| DavMail | Java / GPL | 测试连真 Exchange | — | 不能用。 [推：未逐个看测试文件] |
| npm `exchange-test-server`（xmail-client） | Node / MIT | 自称实现 EWS API（express + sqlite + libxmljs） | 最后发布 0.1.5，2015-07-15 | 不能用：十年没动，依赖原生模块。 [源：npm registry] |
| Go EWS 库（mhewedy/ews 及其 fork afansv、Abovo-Media，vihapr/go-ews 等） | Go | 只有客户端 | — | 没有服务器端，也没有可复用的测试服务器。详细评估在 #637。 [源：pkg.go.dev 列表；未逐个读源码] |
| WireMock 之类的通用 SOAP 桩 | Java | 按请求体匹配回固定响应 | — | 无状态，做不了 SyncState 递进和故障注入；还要拉 JVM 进 CI。 [推] |

**录制回放 vs 自写**：录制只适合「固定请求→固定响应」。我们的契约（`backend/backendtest/backendtest.go`）每条测试都要：新账户、投信、同步、写回、再同步看到结果，外加 `Reset`、`RejectAuth`、Watch 期间投信。这些要求服务器有状态，回放做不到。真实 Exchange 的响应可以另外拿几份做**解析器的 golden 测试**（见 §5），但不作为契约的服务器。

## 2. 契约要求 → 假服务器要实现的操作

契约的钩子（`backendtest.Server`）：`Receiver`、`Deliver`、`Reset`、`RejectAuth`；发信钩子（`backendtest.SendServer`）：`Accepted`、`Refuse`、`LoseVerdict`、`RejectAuth`。契约测试：Describe、FirstSync、CursorResumes、FolderReset、Check、Fetch、Headers、Find、SetFlags、Move、Copy、Append、Failed、Unsupported、Watch、AuthFailed；发信 Send、Refused、Uncertain、SendAuthFailed。[源]

| 契约 / 后端动作 | EWS 操作 | 假服务器最少要做的 |
|---|---|---|
| 发现（#640 决定是否要） | Autodiscover POX：`POST /autodiscover/autodiscover.xml`，请求 `AcceptableResponseSchema` 为 `http://schemas.microsoft.com/exchange/autodiscover/outlook/responseschema/2006a` | 回一个 `Account/Protocol`（`Type` 为 `EXCH`/`EXPR`）带 `EwsUrl` 指向自己。SOAP Autodiscover（`GetUserSettings`）只在 #640 选它时再加。必须是 HTTPS（`httptest.NewTLSServer`，和 POP3 一样把证书池交给客户端）。[推：POX 形状凭经验，#635 会核对] |
| Check / 首次登录 | 任一 EWS 请求（例如 `GetFolder` 取 `msgfolderroot` 或 `inbox`） | 401 + `WWW-Authenticate` 区分认证失败；SOAP Header 里回 `t:ServerVersionInfo`（Exchange 2019 为 `MajorVersion="15" MinorVersion="2"`），给 #648 的最低版本判断用。 |
| 文件夹列表、账户游标 | `SyncFolderHierarchy`（`SyncState` 可空） | 回 `m:Changes` 里的 `t:Create`/`t:Update`/`t:Delete` 和 `t:Folder`（`FolderId`、`ParentFolderId`、`DisplayName`、`FolderClass="IPF.Note"`）；`SyncState` 用「版本号:偏移」这样的不透明字符串。角色靠 `DistinguishedFolderId`（inbox、sentitems、drafts、deleteditems、junkemail、archive…）在 `GetFolder` 里解析成 Id。 |
| FirstSync、CursorResumes | `SyncFolderItems`（`ItemShape`、`SyncFolderId`、`SyncState`、`MaxChangesReturned` ≤ 512） | 按偏移切片，`IncludesLastItemInRange=false` 表示还有；`t:Create`/`t:Update`/`t:Delete`/`t:ReadFlagChange`。每个 `t:Message` 至少带 `ItemId Id ChangeKey`、`IsRead`、`DateTimeReceived`、`Size`、`InternetMessageId`（具体取哪些属性由 #641 定）。 [源：MS 文档] |
| FolderReset | `SyncFolderItems` 回 `ResponseClass="Error"`、`ResponseCode` = `ErrorInvalidSyncStateData`、空 `SyncState` | `Reset(folder)` 让该文件夹的版本号加一，旧 SyncState 一律无效；同时换掉该文件夹所有 ItemId，让旧位置作废（贴近真实：真 Exchange 不会因 SyncState 失效换 Id，但契约要求旧位置作废，这一点要在 #641 里对一下语义）。[源：错误形状；推：Id 是否该换] |
| Fetch、Headers | `GetItem`，`ItemShape` 里 `IncludeMimeContent=true`；头字段用 `AdditionalProperties` 里 `t:FieldURI FieldURI="item:InternetMessageHeaders"` 或直接从 MIME 截 | `t:MimeContent CharacterSet="UTF-8"` 内容是整封原文的 base64；不存在回 `ErrorItemNotFound`。EWS 不给 MIME 结构，`Structure == nil` 与 #634 的「取内容」一致。 |
| Find（按 Message-ID） | `FindItem`，`Restriction` 为 `t:IsEqualTo` + `t:FieldURI FieldURI="message:InternetMessageId"` + `t:FieldURIOrConstant/t:Constant` | 只认这一种 Restriction 形状，其他回错误，免得假服务器悄悄放过客户端写错的查询。 |
| SetFlags | `UpdateItem`，`ConflictResolution="AlwaysOverwrite"`，`t:SetItemField` 改 `message:IsRead`、`item:Flag`（`FlagStatus`）、`item:Categories`（自定义关键字）；已回复/草稿走扩展属性（PR_LAST_VERB_EXECUTED 0x1081 等，#636 定） | 只实现 SetItemField / DeleteItemField 两种，每次改动让 `ChangeKey` 变，并在下次 SyncFolderItems 里产生 `t:Update` 或 `t:ReadFlagChange`。 |
| Move、Copy、Failed | `MoveItem` / `CopyItem`（`ToFolderId`、`ItemIds`） | 回新 `ItemId`（Exchange 2010 SP1 起默认回新 Id，`ReturnNewItemIds`）[推]；目标不存在回 `ErrorFolderNotFound`（契约 Failed）。 |
| Append | `CreateItem MessageDisposition="SaveOnly"` + `SavedItemFolderId` + `t:Message/t:MimeContent`，可带 `t:IsRead` 或 `ExtendedProperty` PR_MESSAGE_FLAGS (0x0E07) 让它不是草稿 | 存进文件夹，回新 Id。 |
| Purge / Delete | `DeleteItem DeleteType="HardDelete"` / `"MoveToDeletedItems"` | 硬删直接删；`MoveToDeletedItems` 移到 deleteditems。 |
| 发信（#644 若选 EWS） | `CreateItem MessageDisposition="SendAndSaveCopy"`（或 `SendOnly`）+ `SavedItemFolderId` 指 sentitems | 解析 MIME 的收件人记入 `Accepted`；`Refuse(rcpt)` 回 `ErrorInvalidRecipients` 之类；`LoseVerdict` 用 `http.Hijacker` 读完请求后直接关连接；成功时往 sentitems 放副本（这正是「服务器自动存已发送」的能力）。 |
| Watch | 二选一，看 #642：`Subscribe`（`StreamingSubscriptionRequest`，2010 SP1+）+ `GetStreamingEvents`；或 `Subscribe`（`PullSubscriptionRequest`）+ `GetEvents` | 见 §3。 |
| 限流（#634 未定项） | 任何操作回 `ErrorServerBusy`，`MessageXml` 里带 `t:Value Name="BackOffMilliseconds"` | 一个 `BusyNext(n)` 钩子，照抄 Thunderbird 的 `busyResponses`。 |

不实现：文件夹的建/改名/删（除非能力表 #643 声明支持）、`GetAttachment`（等「取内容」定了再加）、日历联系人、`ExpandDL`、`ResolveNames`。

### 最小 XML 形状

请求只按**本地名 + 命名空间**解析（`encoding/xml` 结构体带 `xml:"http://schemas.microsoft.com/exchange/services/2006/messages SyncFolderItems"`），不认前缀。按 Body 下第一个元素的本地名分派，未知操作回 SOAP Fault，并让测试失败（Thunderbird 是直接 throw）。

响应统一外壳：

```xml
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Header><h:ServerVersionInfo xmlns:h="http://schemas.microsoft.com/exchange/services/2006/types"
      MajorVersion="15" MinorVersion="2" MajorBuildNumber="1544" MinorBuildNumber="11" Version="V2017_07_11"/><!-- Version 值 [推] --></s:Header>
  <s:Body>
    <m:XxxResponse xmlns:m="http://schemas.microsoft.com/exchange/services/2006/messages"
                   xmlns:t="http://schemas.microsoft.com/exchange/services/2006/types">
      <m:ResponseMessages>
        <m:XxxResponseMessage ResponseClass="Success"><m:ResponseCode>NoError</m:ResponseCode>…</m:XxxResponseMessage>
      </m:ResponseMessages>
    </m:XxxResponse>
  </s:Body>
</s:Envelope>
```

批量操作（`GetItem`、`MoveItem` 等带多个 `ItemId`）每个 Id 一条 `ResponseMessage`，可以部分成功部分失败——客户端必须逐条看 `ResponseClass`，假服务器要能造出混合结果。

**前缀要故意变化**：MS 文档自己的例子里，`SyncFolderItemsResponse` 用默认命名空间而子元素用 `m:` 前缀，`GetStreamingEvents` 的成功例子甚至没有 `Envelope`。假服务器可以在一个开关下随机换前缀（`m:`/默认命名空间/`s:` vs `soap:`），保证客户端只按命名空间解析。[源：MS 文档例子]

## 3. 推送

- **Streaming**（`GetStreamingEvents`，`ConnectionTimeout` 1–30 分钟）：服务器在一条 HTTP 响应里分块持续写出多个 SOAP 文档，每批事件一个；没有事件时定期写带 `StatusEvent` 的心跳；超时写 `ConnectionStatus=Closed` 后结束。假服务器用 `http.Flusher`：`Deliver` 时往订阅队列推 `NewMailEvent`/`CreatedEvent`，写一个文档并 `Flush`。约 100–150 行。故障钩子：`DropStreams()`（关掉所有流，对应 JMAP 的 `DropEventSources`）、`ExpireSubscription()`（回 `ErrorSubscriptionNotFound` / `ErrorInvalidSubscription`）。[源：GetStreamingEvents 文档的单批形状；推：「一条响应里多个文档」是凭 EWS Managed API 的行为，#636 / #642 要核对 Exchange 2019 的实际分块]
- **Pull**（`GetEvents` + `Watermark`）：普通请求响应，最容易写（约 60 行），只是后端要自己轮询。
- 契约 Watch 只要求「开始时醒一次、投信后再醒一次」，两种都能满足；假服务器只实现 #642 选中的那种。

## 4. NTLM

- **Azure/go-ntlmssp 只有客户端**：导出的只有 `Negotiator`（`http.RoundTripper`）、`GetDomain` 等；`challengeMessage` 只有 `UnmarshalBinary`，没有生成 Type 2、校验 Type 3 的服务器端代码。它自己的单元测试也只是在 `httptest` 里回 `WWW-Authenticate: NTLM` 然后放行，真正的握手放在需要 Windows + IIS 的 e2e 测试里。仓库活跃（2026-11-09 有推送）。[源]
- 有服务器端的 Go 实现：`github.com/m7913d/go-ntlm`（ThomsonReutersEikon/go-ntlm 一系的 fork，有 server session，最后推送 2024-05，许可证未标 SPDX）、`jsthtlf/go-ntlm`、一些 go-smb2 fork 的 `internal/ntlm`（internal，导不进来）。都不值得为测试引入新依赖。[源：GitHub API；未读完代码]
- **自己写校验很小**：服务器回 Type 2（8 字节随机 challenge、`NEGOTIATE_UNICODE | NTLM | TARGET_INFO`、带 domain 的 AV pairs）；收到 Type 3 后算 `NTOWFv2 = HMAC_MD5(MD4(UTF16LE(password)), UTF16LE(UPPER(user) + domain))`，`NTProofStr = HMAC_MD5(NTOWFv2, serverChallenge || blob)`，与 `NtChallengeResponse` 前 16 字节比较（MS-NLMP 3.3.2）。MD4 用 `golang.org/x/crypto/md4`（`x/crypto` 已在 go.mod 里，现为 indirect）。约 120–180 行加测试。[源：go-ntlmssp 的 `nlmp.go` 用的是同一套算法；推：行数]
- **NTLM 认的是 TCP 连接**：Type 1→2→3 必须在同一条 keep-alive 连接上，之后这条连接上的请求都免认证。假服务器用 `httptest.NewUnstartedServer` + `Config.ConnContext` 给每条连接挂状态。这正好能测出两类真 bug：客户端在握手中途换了连接（连接池、`Close: true`），以及跑在 HTTP/2 上（IIS 对 NTLM 会降回 HTTP/1.1；`httptest` 的 TLS 服务器默认不开 HTTP/2，要显式保持关闭）。[推：IIS 的 HTTP/2 行为凭经验，#635 核对]
- **建议**：假服务器 `WithAuth(Basic|NTLM)`，默认 Basic，契约全量跑 Basic；另外跑一遍只含 Check、FirstSync、AuthFailed 的 `RunOnly` 用 NTLM，再加一条「`DOMAIN\user` 与 `user@domain` 两种用户名格式都落到同一个 user/domain」的单测（#639 决定格式）。Kerberos / Negotiate 无法有意义地模拟，若 #639 要支持，只能靠手动对真服务器验证。

## 5. 真实响应怎么用

契约用自写假服务器，但它的 XML 是我们照文档写的，和真 Exchange 2019 会有出入（多余元素、前缀、空元素写法）。补一层便宜的保险：

- 在客户端（或 fork 的库）里放几份 golden 响应做解析测试：MS 文档上的例子（公开）、Thunderbird 模板（MPL-2.0，可引用形状，不整段复制进 MIT 仓库为宜）。
- 维护者有测试 Exchange 时手动录几份 `SyncFolderItems`、`GetItem`、`GetStreamingEvents` 的响应，脱敏后加进去。**不要**用 #581 用户的生产服务器录。

## 6. 规模估计

| 部件 | 估计行数（Go，不含测试） | 参照 |
|---|---|---|
| 存储模型（文件夹、条目、ChangeKey、变更日志、SyncState 编码） | 250–350 | jmapmemserver `changes.go` 100 行、`email.go` 489 行 |
| SOAP 外壳、分派、XML 结构体、错误响应 | 250–350 | — |
| 九个操作（SyncFolderHierarchy、GetFolder、SyncFolderItems、GetItem、FindItem、UpdateItem、MoveItem/CopyItem、DeleteItem、CreateItem 含发信） | 400–600 | Thunderbird 17 个操作共 1960 行 JS（含大量注释和模板） |
| 订阅（streaming 或 pull 其一） | 60–150 | jmapmemserver `eventsource.go` 97 行 |
| Autodiscover POX | 40–60 | — |
| 认证（Basic + NTLM 校验 + 每连接状态） | 150–220 | — |
| 故障钩子（Reset、RejectAuth、BusyNext、DropStreams、ExpireSubscription、Refuse、LoseVerdict、DropAfterNextCreate 之类） | 80–120 | jmapmemserver `faults.go` 75 行 |
| **合计** | **约 1200–1800** | POP3 假服务器 323 行；IMAP 包装 619 行（主体是 go-imap 的 imapmemserver）；jmapmemserver 约 1070 行 |

再加 `ews_test.go`（跑 `backendtest.Run`、`RunSender` 和 NTLM 的 `RunOnly`，按 `pop3_test.go` / `jmap_test.go` 的样子）约 150–250 行。可以分两步交付：先 Basic + 同步 + 取内容（契约用 `RunOnly` 只跑读的部分），再加写回、发信、推送、NTLM。

## 7. 依赖其他票的地方

- #640：要不要 Autodiscover、POX 还是 SOAP。
- #641：SyncFolderItems 取哪些属性、`Reset` 时旧 ItemId 是否作废（影响假服务器 Reset 的写法）。
- #642：streaming 还是 pull——假服务器只做选中的。
- #644：发信若走 SMTP，CreateItem 的 SendAndSaveCopy 部分不用做，发信契约继续用 testmail 的 SMTP。
- #639：NTLM 模式要不要、用户名格式。
- #646：若 fork 客户端库，XML 结构体可以和库共享（服务器端复用库的类型定义能省 200 行左右），但假服务器本身仍放 testmail。

## 来源

- Thunderbird 假 EWS 服务器：https://searchfox.org/comm-central/source/mailnews/test/fakeserver/EwsServer.sys.mjs （原文 https://hg.mozilla.org/comm-central/raw-file/tip/mailnews/test/fakeserver/EwsServer.sys.mjs ），引入 bug https://bugzilla.mozilla.org/show_bug.cgi?id=1890503 ，分页 https://bugzilla.mozilla.org/show_bug.cgi?id=1975706 ，发信假服务器 https://bugzilla.mozilla.org/show_bug.cgi?id=1896171
- evolution-ews 测试与 trace：https://gitlab.gnome.org/GNOME/evolution-ews/-/tree/master/tests ，`tests/ews-test-common.c`
- exchangelib 测试：https://github.com/ecederstrand/exchangelib/blob/master/tests/common.py ，`tests/test_autodiscover.py`、`tests/test_transport.py`
- npm exchange-test-server：https://registry.npmjs.org/exchange-test-server
- Azure/go-ntlmssp：https://github.com/Azure/go-ntlmssp （`challenge_message.go`、`negotiator.go`、`nlmp.go`、`negotiator_test.go`、`E2E_README.md`）
- m7913d/go-ntlm：https://github.com/m7913d/go-ntlm
- MS-NLMP（NTLMv2 计算）：https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-nlmp/
- SyncFolderItems：https://learn.microsoft.com/en-us/exchange/client-developer/web-service-reference/syncfolderitems-operation
- GetStreamingEvents：https://learn.microsoft.com/en-us/exchange/client-developer/web-service-reference/getstreamingevents-operation
- 仓库内：`backend/backendtest/backendtest.go`、`backend/backendtest/sender.go`、`core/internal/testmail/{imap,jmap,smtp}.go`、PR #632 的 `core/internal/testmail/pop3.go`、`github.com/godwit-email/go-jmap@v0.5.4-godwit.2/jmapmemserver`
