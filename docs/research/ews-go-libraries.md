# Go 的 EWS、SOAP、NTLM、Kerberos 库

调研于 2026-11-10，回答 [#637](https://github.com/godwit-email/godwit/issues/637)（地图 [#634](https://github.com/godwit-email/godwit/issues/634)）。数据取自各仓库本身（GitHub API：最近提交、open issues/PR 数、LICENSE、源码），pkg.go.dev 与之同源。标 **[未核实]** 的是推断或没在真实服务器上试过的。

需要覆盖的 EWS 操作（下称「必需集」）：SyncFolderHierarchy、SyncFolderItems、GetItem（含 MimeContent）、MoveItem、CopyItem、UpdateItem、DeleteItem、CreateItem（MessageDisposition=SendAndSaveCopy）、Subscribe + GetStreamingEvents。

## 结论先说

| 候选 | 用途 | 最近提交 | open issues+PR | 许可证 | 必需集覆盖 | 判定 |
|---|---|---|---|---|---|---|
| [mhewedy/ews](https://github.com/mhewedy/ews) | EWS 客户端 | 2020-08（push 2022-12） | 5 | MIT（文本照抄，版权行写的是 Pietro Gagliardi） | 只有 CreateItem；其余是日历/人员/头像 | 不用：停更，几乎不覆盖 |
| [hoshii-ai/ews](https://github.com/hoshii-ai/ews)（mhewedy fork） | EWS 客户端 | 2026-11-05 | 0 | 同上 MIT | SyncFolderItems、GetItem+MIME、MoveItem、UpdateItem、CreateItem SendAndSaveCopy（`SendMIME`）、Subscribe、GetStreamingEvents、GetAttachment、FindFolder；**缺** SyncFolderHierarchy、CopyItem、DeleteItem | 只作参考实现，不依赖、不 fork |
| [vihapr/go-ews](https://github.com/vihapr/go-ews)（mhewedy fork） | EWS 客户端 | 2026-07-29 | 0 | 同上 MIT | GetItem、UpdateItem、DeleteItem、CreateItem（批量）；无同步、无订阅 | 不用 |
| [gunsluo/goews](https://github.com/gunsluo/goews) | EWS 客户端 + 生成的全量类型 | 2024-09-29 | 0 | **无 LICENSE** | `schema/` 有全部操作的类型（从 learn.microsoft.com 文档生成，1000+ 文件）；封装只有 SendEmail/FindItem/GetItem/CreateItem | 不能用（无许可证）；NTLM 走已归档的 go-ntlm |
| [beorereleverion/go-ews](https://github.com/beorereleverion/go-ews) | EWS 客户端 | 2023-11 | — | MIT | CreateItem、FindItem、GetFolder、FindPeople | 不用 |
| [hooklift/gowsdl](https://github.com/hooklift/gowsdl) | WSDL→Go 生成 | 2024-08-01 | 97 | MPL-2.0 | — | 不用：停滞，EWS 的 types.xsd 用不上 |
| [xuri/xgen](https://github.com/xuri/xgen) / [droyo/go-xml](https://github.com/droyo/go-xml) xsdgen | XSD→Go 生成 | 2026-10 / 2021-03 | 35 / 52 | BSD-3 / MIT | — | 不用（理由见下） |
| [Azure/go-ntlmssp](https://github.com/Azure/go-ntlmssp) | NTLM over HTTP | 2026-08-24（功能），2026-11 有依赖更新 | 7 | MIT | NTLMv2 ✔，CBT/EPA ✘，MIC ✘ | **推荐**，是 `http.RoundTripper` |
| [bodgit/ntlmssp](https://github.com/bodgit/ntlmssp) | NTLM | 2024-05 | 9 | BSD-3 | NTLMv2 ✔，CBT ✔，签名/加密 ✔ | 备选：EPA 必须时取它的核心 |
| [vadimi/go-http-ntlm](https://github.com/vadimi/go-http-ntlm) + vadimi/go-ntlm | NTLM over HTTP | 2024-11 / 2021-05 | 2 | MIT / 无声明（fork 自已归档的 ThomsonReutersEikon/go-ntlm） | 无 CBT | 不用 |
| [launchdarkly/go-ntlmssp](https://github.com/launchdarkly/go-ntlmssp) | Azure 的 fork | 2026-09 | 1 | MIT | 同 Azure 旧版 | 不用（为 ld-relay 改的） |
| [jcmturner/gokrb5](https://github.com/jcmturner/gokrb5) v8 | Kerberos/SPNEGO | 2023-05-06，最新 tag v8.4.4（2023-02） | 113 issues，32 PR | Apache-2.0 | SPNEGO HTTP 客户端 ✔，只读 FILE ccache | 第一版不做 Kerberos；以后做再看 |
| [oiweiwei/go-msrpc](https://github.com/oiweiwei/go-msrpc) `ssp/` + `oiweiwei/gokrb5.fork/v9` | NTLM/Kerberos/SPNEGO（GSSAPI 风格） | 2026-10-31 | 1 | MIT | NTLM 带 CBT、Kerberos、SPNEGO | 以后要 Kerberos 或 CBT 时的备选 |
| [thunderbird/ews-rs](https://github.com/thunderbird/ews-rs) | Rust EWS 类型（参考） | 2026-10-27 | issue 关闭，走 Bugzilla | MPL-2.0 | SyncFolderHierarchy、SyncFolderItems、GetItem、Move/Copy/Delete/Update/CreateItem 等；**无** Subscribe/GetStreamingEvents、GetAttachment | 借鉴组织方式 |

**推荐**：

1. **EWS：在仓库里自己写一个薄的 SOAP 层**（比如 `backend/ews/ewsxml` 之类），用 `encoding/xml` 手写必需集的请求/响应类型，按 ews-rs 的方式组织（一操作一文件、请求类型关联响应类型、统一的 `ResponseClass` 处理）。不依赖也不 fork 任何现有 Go EWS 库：活的只有 hoshii-ai/ews，单人维护、0 star、缺三个必需操作，还带着日历/人员/头像这些 Godwit 不要的东西；fork 过来的维护量不比自己写小。hoshii-ai/ews 的 `GetStreamingEvents`（`xml.Decoder` 逐信封读长连接）和 `SyncFolderItems` 可以当参考（MIT，照抄要保留版权声明）。
2. **不用代码生成**：gowsdl 停滞且有 97 个 open issue；EWS 的 types.xsd 大量用抽象类型、`xs:choice`、substitution group，生成器出来的代码要么不可用要么全是指针字段（gunsluo/goews 就是这样，且无许可证不能用）。必需集只有十来个操作，手写可控。**[未核实]**：没有实际拿 Exchange 2019 的 `Services.wsdl`/`types.xsd` 跑生成器。
3. **NTLM：`github.com/Azure/go-ntlmssp`（v0.1.x）**，直接塞进 `backend/jmap` 那种传输链：`authTransport{creds, ntlmssp.Negotiator{RoundTripper: transport()}}`，`authTransport` 已经 `SetBasicAuth`，`Negotiator` 正是从 Basic 头里取用户名密码。缺口是 CBT（EPA）和 MIC；若真实服务器要求 EPA，再在 `godwit-email/go-ntlmssp` fork 里补（上游已有 PR #35 CBT、#127 MIC 可作底稿），与 go-imap/go-jmap 的 fork 策略一致。
4. **Kerberos：第一版不做**。gokrb5 停更两年多；不入域的 Mac 没有可用票据，gokrb5 也读不到 macOS 的系统票据缓存。以后要做时在 gokrb5 v8 与 oiweiwei 的 v9 fork 间再选。

## EWS 客户端

### mhewedy/ews 及其 fork

- 主仓：最后一次代码提交 2020-07-31（"fix #5"），README 改动 2020-08-06；5 个 open issue/PR，无人回应。操作只有 CreateItem（邮件、日历）、GetUserPhoto、GetUserAvailability、GetRoomLists、FindPeople、GetPersona；`ewsutil` 是发信、建日程的便捷函数。
- 传输：`ews.go` 每次请求新建 `http.Client`，`Config{NTLM, SkipTLS}` 两个开关，NTLM 用 `Azure/go-ntlmssp`；不能注入 transport/代理/根证书。SOAP 头固定 `RequestServerVersion Exchange2013_SP1`。XML 用 `encoding/xml` + 字面前缀的标签（`xml:"m:GetItem"`）。
- LICENSE 是 MIT 文本，但版权行写的是 "Copyright (c) 2016 Pietro Gagliardi"（从别处拷来的），GitHub 识别为 NOASSERTION。按 MIT 使用问题不大，但署名混乱。
- 已知问题 #13：对 IIS 走 HTTP/2 时收到 `HTTP_1_1_REQUIRED`（NTLM 不能跑在 HTTP/2 上，见下文）。
- Fork（按最近 push 排）：
  - **hoshii-ai/ews**（2026-11-05）：100 次提交，2023 年后 23 次，提交人只有 mhewedy 和 `intx4`，0 star，0 issue。加了 `ContextClient`（带 `context`）、`Config.HTTPClient`/`Config.Transport`（可注入 RoundTripper）、`Config.ServerVersion`、`WithAnchorMailbox`；操作：`SyncFolderItems`、`GetItem`（`NewGetItemMimeShape`、`MimeContent.Decode`）、`MoveItem`、`UpdateItem`、`SendItem`、`SendMIME`（CreateItem + MIME + SendAndSaveCopy）、`Subscribe`/`SubscribeToAllFolders`/`Unsubscribe`、`GetStreamingEvents`（`EventStream.Next` 逐个读）、`GetAttachment`、`FindFolder`、`FindItem`、`ConvertId`。源码里**没有** SyncFolderHierarchy、CopyItem、DeleteItem。`go.mod` 里 go-ntlmssp 还钉在 2022 年的伪版本。有 `testdata` 录制的响应（ServerVersionInfo 15.20，即 Exchange Online），**[未核实]** 是否在本地部署的 Exchange 2019 上跑过。
  - vihapr/go-ews（2026-07）：加了 GetItem/UpdateItem/DeleteItem/CreateItem 的批量版本和时区，无同步、订阅。
  - ZPCA96/ews、gunsluo/goews 等其余 fork 基本停在 2023–2024，改动面小。

### gunsluo/goews

不是 fork，独立写的 v3。`schema/` 目录有 1000+ 个文件，每个 EWS 元素一个 Go 类型，注释里带 learn.microsoft.com 链接，看起来是从文档页生成的；所有字段都是指针，靠 `SetForMarshal` 给元素名加 `m:`/`t:` 前缀。封装的操作只有 SendEmail、QueryMessage、CreateItem、FindItem、GetItem。NTLM 用自带的 `ntlmssp/transport.go`，底层是 `vadimi/go-ntlm`。**仓库没有 LICENSE**，默认保留所有权利，不能拿来用，也不能照抄它生成的类型。

### 其他

GitHub 搜 `ews language:go`：beorereleverion/go-ews（MIT，2023-11，CreateItem/FindItem/GetFolder/FindPeople）、daostas/ews（2026-02，无许可证）、若干小工具（发信、日历提醒、OAuth 代理）。没有找到覆盖同步和通知的成熟库。

## SOAP / XML：手写还是生成

- **手写（`encoding/xml`）**：EWS 请求要求 `m:`（messages）和 `t:`（types）两个命名空间。`encoding/xml` 对命名空间前缀支持弱，业界通行做法是 mhewedy 系的字面前缀标签（`xml:"t:ItemId"`），序列化可行；反序列化时 `encoding/xml` 按本地名匹配，忽略前缀即可。必需集约十个操作，加上共用的 ItemId/FolderId/ItemShape/ResponseMessage，规模可控。
- **gowsdl**：最后提交 2024-08-01，97 个 open issue，含多命名空间解析问题（#239、#240 已关但说明有坑）。MPL-2.0 只影响生成器本身。
- **xsd 生成器**（xuri/xgen 活跃，droyo/go-xml 2021 后停）：能生成类型但不生成 SOAP 绑定；EWS types.xsd 的抽象基类（`BaseItemIdType`、`BaseFolderIdType`、`ItemType` 的各种子类）需要多态反序列化，Go 结构体表达不了，生成后还得手改。**[未核实]**：未实际试跑。
- 结论：手写。生成的价值在全量覆盖，而 Godwit 只做邮件、只要十来个操作。

## NTLM

### Azure/go-ntlmssp（推荐）

- 维护：微软的 Quim Muntal 在 2025-11 做了一轮重构（`NewAuthenticateMessage`、Basic 改为 opt-in `AllowBasicAuth`、自定义 workstation/domain、跨域账户），2026-07 加 options 式 negotiate，2026-08 支持 Key Exchange（导出 session key）。tag v0.1.0、v0.1.1。MIT。
- 协议：README 写明只实现 NTLMv2、只 Unicode；`NewAuthenticateMessage` 遇到服务器要 `NTLMSSP_NEGOTIATE_LM_KEY` 直接报错（拒绝 v1）。
- **CBT/EPA：不支持**。`avids.go` 定义了 `avIDMsvChannelBindings` 常量，但全仓没有用到；open PR #35「NTLMv2 with MAC and EPA support」（2022 起未合）。**MIC：不支持**，`authenicateMessage.MIC` 字段存在但不写；open PR #127「Add MIC Support」（2026-11-08，来自 masterzen/winrm 那边）。
- 接口：`ntlmssp.Negotiator{RoundTripper}` 实现 `http.RoundTripper`，从请求的 Basic 头取用户名密码（`DOMAIN\user` 拆域），先匿名发一次，401 后按 `WWW-Authenticate` 走 NTLM 或 Negotiate（Negotiate 里装的仍是 NTLM token，不是 Kerberos），请求体会缓冲重放。与 `backend/jmap/client.go` 的 `authTransport` → `transport()` 链正好拼上，代理、`conn.TLSRoots` 都在内层 `http.Transport` 上，不受影响。
- 要注意：
  - **必须关 HTTP/2**。`transport()` 克隆自 `http.DefaultTransport`，`ForceAttemptHTTP2` 为真；NTLM 是连接级认证，IIS 对 HTTP/2 上的 NTLM 回 `HTTP_1_1_REQUIRED`（mhewedy/ews #13 就是这个）。EWS 用的 transport 要设 `TLSNextProto` 为空 map 或 `ForceAttemptHTTP2=false`。
  - NTLM 握手的三步要落在同一条 TCP 连接上：依赖 keep-alive 与连接复用，不能 `DisableKeepAlives`。并发请求时每条新连接都要重新握手（open issue「Error 401 with concurrent requests」，2023）。
  - GetStreamingEvents 是长连接，会占住一条已认证连接，其他请求会另开连接再握手。

### bodgit/ntlmssp（EPA 必须时的备选）

- BSD-3，最后有效提交 2024-05（之后只有 dependabot），9 个 open issue。被 masterzen/winrm 用。
- 支持 NTLMv1/v2、签名与加密、**Channel Bindings**（`bindings.go`，`tls-server-end-point`，按 RFC 5929 取服务器证书哈希）。README 自带一张对比表，明确标 Azure 版「Channel Bindings: No」。
- HTTP 封装 `ntlmssp/http.Client` 是包一层 `*http.Client` 的 `Do`，不是 RoundTripper，且要求 `Transport` 是 `*http.Transport`（类型断言）。CBT 取自 `resp.TLS.PeerCertificates[0]`。要用的话取它的 `ntlmssp.Client` 核心，自己写个 RoundTripper。

### 其他

- vadimi/go-http-ntlm：MIT，RoundTripper，底层 vadimi/go-ntlm（fork 自已归档的 ThomsonReutersEikon/go-ntlm，2021 后不动），无 CBT。
- launchdarkly/go-ntlmssp：Azure 的 fork，为 ld-relay 小改，没理由用。
- oiweiwei/go-msrpc 的 `ssp/ntlm`：MIT，2026-10 仍活跃，NTLM 支持 channel bindings（`ssp/ntlm/config.go`、`v2.go`），但接口是 GSSAPI 风格，为 DCE/RPC 设计，接 HTTP 要自己写 RoundTripper。

### 服务器那边要不要 EPA

用户的服务器是 15.2.1544.x，即 Exchange 2019 CU14；CU14 起安装时默认开启 Extended Protection。**[未核实]**：按微软的 Extended Protection 文档，前端 `Default Web Site/EWS` 设为 `Accept`（带 CBT 就校验，不带也放行），`Required` 只在后端站点；用户现在通过 DavMail（Java HttpClient 的 NTLM，不带 CBT）能用，也旁证前端不强制。所以第一版不带 CBT 大概率能连，但要在真实服务器上确认；若被要求 CBT，按上面 fork 方案补。

## Kerberos / SPNEGO

- jcmturner/gokrb5 v8：Apache-2.0，最后提交 2023-05-06，最新 tag v8.4.4（2023-02-25），113 个 open issue、32 个 open PR，基本无人处理。提供 `spnego.Client`（包 `*http.Client` 的 `Do`）和 `spnego.SetSPNEGOHeader(cl, req, spn)`，后者可以嵌进 RoundTripper。SPNEGO 只提供 Kerberos 机制，不会退回 NTLM。
- 系统票据：`credentials.LoadCCache(path)` 只解析 FILE 格式的 ccache（版本 1–4）。macOS 的 Heimdal 默认用 API/KCM 类型的缓存，**[未核实]** 但据此 gokrb5 读不到 `kinit` 或系统登录拿到的票据；要用系统票据得 cgo 调 GSS.framework，与纯 Go 构建相悖。
- 不入域的 Mac：没有票据，只能用用户名密码现场向 KDC 要票（`client.NewWithPassword`），这需要客户端能直连 KDC（88 端口）并知道 realm/KDC（krb5.conf 或 DNS SRV）。公司外网通常连不到 KDC，而 NTLM 只需要 HTTPS 能到 EWS。
- oiweiwei/go-msrpc 依赖 `oiweiwei/gokrb5.fork/v9`（活跃 fork）。以后要做 Kerberos 时，这是比原版更可能有人维护的选择。
- 结论：第一版只做 NTLM（以及服务器若开了 Basic 时的 Basic）；Kerberos 等有真实需求再开票。

## Thunderbird ews-rs 可借鉴的组织方式

MPL-2.0，Rust crate `ews`，问题走 Bugzilla。`ews/src/types/` 下：

- **一操作一文件**：`sync_folder_hierarchy.rs`、`sync_folder_items.rs`、`get_item.rs`、`move_item.rs`、`copy_item.rs`、`delete_item.rs`、`update_item.rs`、`create_item.rs`、`get_folder.rs`、`find_item.rs`、`mark_all_read.rs`、`mark_as_junk.rs` 以及文件夹的增删改移。每个文件有请求结构体、对应响应和用录制 XML 做的序列化/反序列化测试。
- **请求绑定响应**：`trait Operation { type Response; const NAME }`，`#[operation_response(ItemResponseMessage)]` 宏生成响应类型。Go 里可写成泛型 `func Call[Req Operation[Resp], Resp any](ctx, c, req) (Resp, error)` 或各操作一个函数。
- **统一的响应分类**：`ResponseMessages<T>{ Vec<ResponseClass<T>> }`，`ResponseClass` 是 `Success(T) | Warning(T) | Error(ResponseError)`，按 `ResponseClass` 属性分派；`ResponseCode` 是完整枚举。EWS 批量操作逐项成败，这一层必须有。
- **共享数据抽出**：MoveItem/CopyItem 共用 `CopyMoveItemData`，`BaseItemId`/`BaseFolderId` 是枚举（ItemId / DistinguishedFolderId 等），`RealItem` 枚举具体条目类型，`MimeContent` 单独类型。
- **只写用到的字段**：`Message` 只有 Thunderbird 需要的属性，未知元素忽略。
- 传输、认证（NTLM、OAuth）、重试、限流都不在 crate 里，在 Thunderbird 的 `ews_xpcom`；类型层与传输层分开，Godwit 也该这样。
- ews-rs 没有 Subscribe/GetStreamingEvents（**[未核实]** Thunderbird 大概靠轮询 SyncFolderItems），也没有 GetAttachment；这两块参考 hoshii-ai/ews。
