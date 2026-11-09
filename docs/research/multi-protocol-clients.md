# 多协议客户端怎么切协议后端：Himalaya、aerc、Thunderbird

调研于 2026-11-09，回答 #586（地图 #584）。只读一手源码和项目自己的 spec，下文每条结论都带文件路径。重点看接口形状和踩过的坑，不看移植。

| 项目 | 版本 | 许可 |
|---|---|---|
| [pimalaya/himalaya](https://github.com/pimalaya/himalaya) | `42134fd`（2026-11-08） | MIT OR Apache-2.0 |
| pimalaya `io-imap` / `io-jmap` / `io-msgraph` / `io-gmail` | `f2d2687` / `13868b4` / `2061693` / `8855f88` | 均为 MIT OR Apache-2.0 |
| [~rjarry/aerc](https://git.sr.ht/~rjarry/aerc) | `5d77848`（2026-10-22，0.22.0 之后） | MIT |
| [comm-central](https://hg.mozilla.org/comm-central) `rust/ews_xpcom`、`rust/protocol_shared` | `f703fa543f35` | MPL-2.0 |
| [thunderbird/ews-rs](https://github.com/thunderbird/ews-rs)（ews_xpcom 依赖的 `ews` crate） | `726235b` | MPL-2.0 |

**许可结论**：Himalaya 一系和 aerc 的代码与 Godwit 的 Apache-2.0 兼容，必要时可以带版权声明借用。Thunderbird 和 ews-rs 是 MPL-2.0，按文件计的弱 copyleft：整文件引入要保留 MPL 并公开该文件的改动，所以只借思路，不抄代码。另外 Himalaya 一系是 Rust，aerc 是 Go，但 aerc 的 notmuch 后端走 cgo（见下），不符合 ADR 0001。

下文 Himalaya 路径相对 `pimalaya/himalaya`，aerc 路径相对 aerc 仓库根，Thunderbird 路径相对 comm-central 根。

## 1. 一句话对比

| | 后端怎么选 | 共享接口形状 | 能力怎么表达 | 同步状态 |
|---|---|---|---|---|
| Himalaya | 账户配了哪些块，按固定顺序挑第一个存储后端，发信单独解析 | 一个 `enum BackendClient`，每个共享方法 `match` 分派到各协议适配器的同名方法；没有 trait | 静态能力表：每个后端 × 每个动词一行，四级 `Full / Partial / Server / None`，连接前就能拒绝 | CLI 本身无状态；离线同步交给另一套（neverest + pimdir 存储） |
| aerc | 源 URL 的 scheme（`imap://`、`jmap://`、`maildir://`、`notmuch://`）查注册表 | 每个后端一个 goroutine，吃 `WorkerMessage` 消息，按类型 `switch`，回 `Done / Error / Unsupported` | 运行时：不支持的消息回 `Unsupported`，UI 回滚乐观更新；另有很薄的 `Capabilities{Sort, Thread, Extensions}` | 各后端自己管（IMAP 用 UIDVALIDITY 编进 ID，JMAP 有本地 cache） |
| Thunderbird | 账户类型决定 XPCOM contract ID（EWS 或 Graph 客户端） | `IExchangeClient` IDL：EWS 和 Graph 共用一套异步方法 + 回调 listener | 没有能力表；服务器太旧时在操作内部降级，并通过 `useLegacyFallback` 告诉调用方 | 每个文件夹一个不透明 `syncStateToken`，由调用方保存、每页回调更新 |

## 2. Himalaya

### 2.1 后端选择：`src/backend.rs`

`Backend` 只是 `--backend` 全局参数的枚举（`Auto`、`Imap`、`Jmap`、`Gmail`、`Msgraph`、`Maildir`、`M2dir`、`Mbox`、`Pimdir`、`Smtp`、`Sieve`），外加 `allows_*()` 判断某个分支能不能跑，以及按 cargo feature 编进来的 `COMPILED` 列表。它不是接口，只是选择器。

真正的分派在 `src/shared/client.rs`：`EmailClient` 持有一个 `BackendClient` 枚举，`list_mailboxes`、`move_messages` 等方法对每个协议写一个 `match` 分支，调用 `src/<proto>/backend.rs` 里的同名固有方法。spec 明说「CLI 自己拥有共享类型，CLI 和 io-* crate 之间没有聚合库」（`cairn/spec/backends.md` 开头）。也就是说，Himalaya 的接缝在应用层，协议库只提供协议原语。

### 2.2 能力表：`src/capability.rs`

这是最值得借鉴的部分。

- **能力名**：`Capability` 枚举，名字是点分字符串，例如 `mailbox.create`、`message.move`、`flags.junk`、`envelope.search.body`、`submit.copy`、`vacation`。搜索的每个子句和排序键也单独算一个能力（`Search(SearchOption)`）。命名照搬 pimdir STORAGE Annex B，只去掉领域前缀。
- **四级支持**（`enum Support`）：
  - `Full`：结果和其他声明者完全一样；
  - `Partial(&str)`：能做，但附文字说明哪里不同；
  - `Server(&str)`：要看服务器连上后宣告了什么，比如 IMAP `message.remove` 要有 UIDPLUS，JMAP `submit` 要 session 里有 `urn:ietf:params:jmap:submission`；
  - `None(&str)`：不支持，附原因和替代路径。
- **两层判定**（文件头注释）：静态表编进二进制，`Resolution::require()` 在**连接、取凭据、TLS 之前**就拒绝做不到的动词；`Server` 级的留给适配器连上后再拒绝，统一报 `capability-missing`。搜索子句不支持时单独报 `search-unsupported`。
- **规则**：「后端只有在给出和其他声明者相同结果时才声明某能力；结果不同的留作协议专属命令」（文件头）。所以 Gmail 的 `flags.answered`、`flags.draft` 是 `None`，而不是假装成功（`cairn/spec/backends.md`「Gmail and Graph declare no answered or draft flag」）。
- **解析**（`Resolution::of`）：只看配置里有哪些块，不连接。存储后端按 `STORAGE_ORDER` 选第一个，本地优先（Maildir → … → JMAP → Gmail → Graph → IMAP）。发信：存储后端自己能发（JMAP、Gmail、Graph、pimdir）就用它，否则用 SMTP。账户级功能（目前只有 `vacation`）单独找第一个支持它的后端。注释说明了原因：本地存储从服务商同步的账户，自动回复还在服务商那边。
- **ID 一致性**：所有交换 ID 的动词都解析到同一个存储后端，「所以一个返回的 id 在其他动词里有效」（`Resolution` 的文档注释）。`Envelope.id` 是不透明字符串：IMAP UID、JMAP email id、Maildir 文件名（`src/email/envelope.rs`）。
- **共享输出不带后端私有字段**（`cairn/spec/backends.md`「Shared outputs carry no backend details」）。后端私有的东西走 `himalaya <proto> …` 子命令。

### 2.3 Gmail 标签和 JMAP 多 mailbox 怎么统一

做法不是在模型里加「标签」概念，而是**把标签当 mailbox，把差异写进能力表的 `Partial` 说明**：

- Gmail：标签即 mailbox（`cairn/spec/backends.md`「Network backends」）。`copy` 就是加目标标签，`move` 是加目标标签同时去掉源标签，每封一次 `messages.modify`（`src/gmail/backend.rs:255-274`）。删 mailbox 只删标签，邮件留在别的标签或 All Mail 里，记为 `Partial`。`flags.junk` 实际是 `SPAM` 标签，会把邮件挪进垃圾箱，也是 `Partial`。
- JMAP：`copy` 是往 `mailboxIds` 里加一个 id，所以两边是**同一封信**，共享标志、删一处两处都没（`COPY_SHARED` 常量；`cairn/spec/backends.md`「A copy on JMAP and Gmail is one message in two mailboxes」）。跨账户的 `Email/copy` 留作 `jmap email copy` 专属命令。删 mailbox 时，同时在别的 mailbox 里的邮件会留下（RFC 8621 §2.5），记为 `Partial`。
- 角色：共享 `Mailbox` 带可选 role（inbox、all、archive、drafts、junk、sent、trash…）。JMAP 读 `role`，Gmail 读固定系统标签 id，Graph 用一次 `$batch` 把 well-known 名解析成文件夹 id，IMAP 只标 `INBOX`（io-imap 还发不出 `LIST RETURN (SPECIAL-USE)`）。其余用 `mailbox.alias.<role>` 配置覆盖（「Mailbox role」）。

对 Godwit 的含义：「一封信在多个位置」不需要换数据模型，但 copy、删除文件夹、垃圾箱这些语义在不同协议下确实不同，必须在能力上写明，不能都叫同一个名字（AGENTS.md「名字是承诺」）。

### 2.4 其他记在 spec 里的坑

均出自 `cairn/spec/backends.md`：

- IMAP 没有 MOVE 时用 `UID COPY` + `\Deleted` + `UID EXPUNGE`（要求 UIDPLUS），**绝不发裸 `EXPUNGE`**，以免误删别的 `\Deleted` 邮件。
- copy/move 的计数以 `COPYUID` 为准（Mailo 会发 UIDVALIDITY 为 0 的 `COPYUID`，也照收）；拿不到就用 `UID SEARCH UID` 反查。只有 tagged `OK` 不算成功。
- `UID STORE` 回显少于请求数（有的服务器对没变化的邮件不回显）时，用 `UID SEARCH` 补数。
- `APPEND` 成功但没有 `APPENDUID`、按 Message-ID 也搜不到时，仍算成功，结果里的 id 为可选。
- Graph 不能 `APPEND`（MIME 创建出来永远是草稿），所以 `message.add` 是 `None`，保存到「已发送」之类在**连接前**就拒绝。
- JMAP 发信：先作为 `$draft` 存进草稿箱，再用 `onSuccessUpdateEmail` 挪到已发送并去掉 `$draft`。
- 网络后端：每次读写各有 1 分钟预算，并设 socket 读超时，防止服务器静默挂起。
- JMAP 下载可能在另一个主机（Fastmail 的 user-content 主机），要对下载主机单独建认证连接（`cairn/spec/provider-quirks.md`）。**这对 Godwit 的外发声明有影响**：JMAP 后端连接的主机不止 session URL 一个。

### 2.5 io-* crate 的结构和测试

四个 crate 结构一致（以 io-imap README 为准）：

1. **无 I/O 的协程层**：`no_std` 状态机，`resume(arg) -> Yielded(WantsRead | WantsWrite(bytes) | …) | Complete(result)`（`io-imap/src/coroutine.rs`、`io-jmap/src/coroutine.rs`）。协议逻辑全在这一层，不碰 socket。
2. **轻客户端**：阻塞式，包装调用方自己打开的流。
3. **完整客户端**：加上 TCP、TLS（rustls 或 native-tls）。

目录按 RFC 分（`io-imap/src/rfc3501`、`rfc4315`、`rfc6851`…；`io-jmap/src/rfc8620`、`rfc8621`），Gmail、Graph 按 REST 资源分（`io-msgraph/src/v1/rest/users/messages/…`）。

测试分四层：

| 层 | 例子 | 是否默认运行 |
|---|---|---|
| 单元测试 | io-imap `src/` 内 255 个 `#[test]` | 是 |
| 离线协程测试：给协程喂预制的 HTTP/IMAP 响应字节，断言它写出的请求和返回值 | `io-jmap/tests/rfc8621.rs`、`io-gmail/tests/coroutines.rs`、`io-msgraph/tests/coroutines.rs`（49 个） | 是 |
| 本地 Stalwart 容器 | `io-imap/tests/stalwart.sh`、`io-jmap/tests/stalwart.sh` 起服务器并通过 JMAP 管理接口建账户 | `#[ignore]` |
| 真实服务商 | `io-imap/tests/{fastmail,gmail,outlook}.rs` 等，凭据走环境变量 | `#[ignore]` |

另有 `io-imap/fuzz/fuzz_targets/{reads,response,stream_batch}.rs`。Himalaya 自己的 `tests/` 只测 CLI 层（`capability.rs`、`account_capability.rs`、`envelope_search.rs`…）。

协程层的好处是协议逻辑可以离线、逐字节测。Godwit 用 Go，对应做法是：协议包只依赖 `io.ReadWriter` / `http.RoundTripper`，测试用进程内假服务器或录好的响应。不必照搬协程。

### 2.6 服务商实测报告（`cairn/spec/testing/`）

方法见 `provider-test-plan.md`：每个服务商跑一遍共享命令和协议专属命令，**只在本次新建的假 mailbox 里操作**。结果写成 `imap-smtp-<provider>.md`，带版本、日期、服务器能力和逐条 P1、P2… 发现。发现的 bug 当场修，库层的修进 io-imap 或 io-gmail（`testing/README.md`「Status」）。服务商行为另外沉淀进 `provider-quirks.md`。

国内三家（2026-10-18，himalaya v2.2.1 `4df380b`）：

**QQ 邮箱**（`imap-smtp-qq.md`）
- 用授权码，不能用 QQ 密码。
- 文本搜索（`SEARCH SUBJECT/FROM`）返回 0 条；按标志搜索可用。报告推测与 Coremail 同类。
- `APPEND` 会**自动加 `\Seen`**，即使没请求任何标志。
- 默认文件夹用英文名（`Sent Messages`、`Deleted Messages`、`Junk`），另有 `其他文件夹/QQ邮件订阅`；分隔符 `/`；宣告 `UIDPLUS MOVE`。

**163 邮箱**（Coremail，`imap-smtp-163.md` 和 `provider-quirks.md`）
- 宣告了 `SASL-IR` 却不支持：内联初始响应会得到 tagged `BAD`，需要 `imap.sasl-ir = false`。
- 登录后必须发 RFC 2971 `ID`，并且要带字段；发 `ID NIL` 会被接受，但下一条命令报 `Unsafe Login`。
- `AUTHENTICATE PLAIN` 返回 `Not support mechanism`（只宣告 `AUTH=XOAUTH2`），授权码要走 `LOGIN` 命令。
- 文本搜索返回 0 条，标志搜索正常。
- 不能 `DELETE` 非空文件夹（`NO DELETE can't delete mailbox with letter in it`）。
- 中文文件夹名：草稿箱、已发送、已删除、垃圾邮件、病毒文件夹、广告邮件；modified UTF-7 正常。

**139 邮箱**（中国移动，`imap-smtp-139.md`）
- `imap.139.com:993` 用 rustls 握手失败（`HandshakeFailure`），报告判断它只接受 rustls 不实现的旧套件（如 AES-CBC）。native-tls 可用；SMTP 用 TLS 1.3 正常。**Godwit 用 Go `crypto/tls`，默认套件里仍有 ECDHE-CBC，但不含 RSA 密钥交换（Go 1.22 起），所以需要实测，不能当成已解决。**
- `STATUS` 计数为空，`mailbox list --counts` 两列都空。
- 文本搜索**忽略条件，返回全部邮件**（QQ 和 163 是返回 0 条），比返回空更危险：客户端会把整个文件夹当成搜索结果。
- 偶发 `NO COPY errno:1407, user maillist null`，重试后邮件已从源文件夹移走。
- 偶发 `FETCH` 不返回正文，重试成功。
- UID 不连续（10150、10152…），合法但要注意。
- `APPEND` 保留请求的标志，这点和 QQ 不同。

Godwit 已经发 `ID`（`core/conn/conn.go:208`），163 的配置也已在 `core/auth/providers.go`。上面其余几条（搜索不可信、APPEND 自动已读、139 的 TLS 和 STATUS）是否已经处理，没有逐一核对。按 AGENTS.md，IMAP 怪异行为应修在 go-imap fork，并配一个 Godwit 侧的复现测试。**把这几份报告逐条转成 fork 里的假服务器测试，正好回答地图里「服务商实测报告怎么转成回归测试」。**

## 3. aerc（已核实存在，结构与描述一致）

### 3.1 接口

- `worker/types/worker.go`：

  ```go
  type Backend interface {
      Run()
      Capabilities() *models.Capabilities
      PathSeparator() string
  }
  ```

  `Worker` 持有 `Backend` 和两个 channel：`actions`（UI → 后端）和 `messages`（后端 → UI）。按消息 id 关联回调；收到 `Done` 或 `Cancelled` 后清掉回调。
- `worker/types/messages.go`：约 50 种消息类型。请求有 `Connect`、`ListDirectories`、`OpenDirectory`、`FetchDirectoryContents`、`FetchDirectoryThreaded`、`SearchDirectory`、`FetchMessageHeaders`、`FetchFullMessages`、`FetchMessageBodyPart`、`FlagMessages`、`AnsweredMessages`、`ForwardedMessages`、`CopyMessages`、`MoveMessages`、`DeleteMessages`、`AppendMessage`、`ModifyLabels`、`CheckMail`、`StartSendingMessage` 等；响应有 `Done`、`Error`、`Unsupported`、`ConnError`、`Cancelled`、`DirectoryInfo`、`MessageInfo`、`LabelList` 等。
- 选择：`worker/worker.go` 解析源 URL，取 scheme 中 `+` 前的部分，查 `worker/handlers/register.go` 的注册表；各后端在 `init()` 里 `RegisterWorkerFactory`。`worker/worker_enabled.go` 默认编进 imap、jmap、maildir、mbox；notmuch 要 `//go:build notmuch`（`worker/handler_notmuch.go`），它的 `lib/notmuch/*.go` 用 cgo 绑 libnotmuch。
- 后端主循环（如 `worker/jmap/worker.go` 的 `handleMessage` 和 `Run`）就是一个大 `switch msg.(type)`，没处理的类型返回 `types.ErrUnsupported`，再转成 `Unsupported` 消息。`ErrNoop` 表示操作没产生效果，不回 `Done`。
- 中间件：`types.WorkerInteractor` 接口加 `Unwrap()`，可以层层包装；例如 `worker/middleware/foldermapper.go` 做文件夹改名映射。

### 3.2 各后端边界

- **IMAP**（`worker/imap/`，用 emersion/go-imap v1.2.1）：UID 编成字符串 `"<文件夹>:<UIDVALIDITY>:<UID 补零到 12 位>"`（`worker/imap/imap.go:19-28`），所以共享层的 `models.UID` 是不透明字符串，UIDVALIDITY 变了旧 id 自动失效。Gmail 扩展单独放在 `xgmext.go`（用 `X-GM-RAW` 搜索、标签）。还有 `seqmap.go` 维护序号到 UID 的映射，用来处理 `EXPUNGE`。
- **JMAP**（`worker/jmap/`，用 `git.sr.ht/~rockorager/go-jmap`）：move、copy、delete 都是对 `mailboxIds/<id>` 打补丁的一次 `Email/set`（`worker/jmap/set.go:86-142`）。打开 `use-labels` 后，`archive` 角色的 mailbox 换成虚拟的「All mail」文件夹，并启用 `ModifyLabels`（`worker/jmap/directories.go:92-110`；`doc/aerc-jmap.5.scd`「use-labels」）。也就是说，aerc 把「多 mailbox」做成**可选的标签视图**，默认仍按文件夹呈现。有本地 cache（`worker/jmap/cache/`）。
- **发信不在 worker 接口里**：`lib/send/sender.go` 按发信 URL 的协议选 SMTP、sendmail 或 JMAP；只有 JMAP 回到 worker 发 `StartSendingMessage`（`lib/send/jmap.go`）。收、发两条线是分开的。
- **Maildir / notmuch**：notmuch 是 maildir 加上查询库，虚拟文件夹由 `query-map` 定义（`worker/notmuch/worker.go`）。

### 3.3 坑

- **乐观更新 + 回滚**：`lib/msgstore.go:665-690` 先在本地标记删除；收到 `Error` 或 `Unsupported` 就 `revertDeleted`。说明能力只在运行时才知道，UI 必须能撤销。Himalaya 的静态表就是为了避免这种情况。
- **`Capabilities` 太薄**：只有 `Sort`、`Thread` 和 IMAP 扩展字符串列表（`models/models.go:104-112`），UI 对其他差异一无所知，只能试了再说。
- **Exchange 的 IMAP 怪异行为**：HEAD 提交 `5d77848`「imap: replace flags on unilateral FETCH updates」——Office 365 在 `UID STORE` 之后发的 `FETCH` 不带 UID（违反 RFC 3501 §6.4.8），go-imap 当成单边更新；aerc 原来把标志 OR 进去，结果标志「只能加不能减」。这个坑 Godwit 的 go-imap v2 fork 也要确认。
- 测试覆盖面小：worker 下只有 11 个 `_test.go`（imap 的 configure、search、seqmap，notmuch 的 message、search，foldermapper，thread 等），没有假服务器。

## 4. Thunderbird `ews_xpcom`

### 4.1 分层（`mailnews/protocols/exchange/docs/architecture.md`）

- C++ 侧负责 Thunderbird 自己的概念：文件夹、incoming server、本地存储、撤销。Rust 侧只负责「和服务器说话」，两边通过 XPCOM 交互。文档明确说协议客户端**不知道邮件在本地怎么存**。
- 接缝是 `mailnews/protocols/exchange/src/IExchangeClient.idl`，**EWS（`rust/ews_xpcom`）和 Graph（`rust/graph_xpcom`）共用这一套接口**，按 contract ID 选实现（例如 `@mozilla.org/messenger/ews-client;1`）。方法有：`checkConnectivity`、`syncFolderHierarchy(listener, syncStateToken)`、`createFolder`、`syncMessagesForFolder(listener, folderId, syncStateToken)`、`getMessage`、`changeReadStatus`、`changeFlagStatus`、`changeReadStatusAll`、`createMessage(folderId, isDraft, isRead, stream)`、`deleteMessages`、`deleteFolder`、`emptyFolder`、`updateFolder`、`moveItems`、`copyItems`、`moveFolders`… 全部异步，结果通过回调接口（`IExchangeMessageSyncListener` 等）返回。ID 一律是不透明字符串。
- `rust/ews_xpcom/src/client.rs`：`XpComEwsClient` 由 `ServerVersionHandler`、`OperationQueue`（5 个 runner）和 `OperationSender` 组成；每个 EWS 操作一个子模块（`src/client/*.rs`）。

### 4.2 同步

- `src/client/sync_folder_hierarchy.rs`：用 `SyncFolderHierarchy` 加不透明 `SyncState` 增量同步文件夹树。首次同步（token 为空）时先用一次 `GetFolder` 批量取所有 distinguished folder，建立「远端 id → 知名文件夹」映射（`get_well_known_folder_map`）。
- `src/client/sync_messages_for_folder.rs`：循环调用 `SyncFolderItems`（`BaseShape::IdOnly`，每页 `EXCHANGE_MAX_PAGE_SIZE`），直到 `includes_last_item_in_range`。每页之后通过 `listener.on_sync_state_token_changed` 把新 token 交给调用方保存，所以中断后从最后一页续上。变更类型有 `Create`、`Update`、`Delete`、`ReadFlagChange`。
- **两步取数**：同步只拿 id，再用 `GetItem`（每批 10 个）取字段。注释写明，`SyncFolderItems` 会**静默地不返回**某些元数据字段，也不报错，只能分两步（同文件 :51-54）。
- 字段按服务器版本取：Exchange 2013 及以上才请求 `item:Preview`、`item:Flag`。
- `ChangeKey` 被忽略，总是取最新版本。

### 4.3 认证

- `rust/protocol_shared/src/authentication/authentication_provider.rs` 的 `auth_header_value()`：
  - Basic（`passwordCleartext`）和 **NTLM 都交给 Necko**（Gecko 的 HTTP 栈），通过维护 Necko 的 HTTP 认证缓存实现（`auth_cache_manager.rs`）。
  - OAuth2：每次请求都向 `msgIOAuth2Module` 要 Bearer token；用户可以覆盖组织的 OAuth 参数（`IOAuth2CustomDetails`）。
  - 其他方法直接报错。
- EWS 端点来自 server 的 `ews_url` 设置（`base_http_url`）。
- 401 处理（`rust/protocol_shared/src/operation_sender.rs:610-720`）：从 `WWW-Authenticate` 解析 `realm`，和保存的不同就存下来重试；否则弹密码框重试或放弃。
- **并发下的认证和限流**：认证失败、限流时，多个 runner 抢同一个 `Line` 令牌，只有一个去处理（弹框或等待），其余等它释放后直接重试（architecture.md「Error handling」）。

对 Godwit：Go 里没有 Necko，NTLM 要自己实现（纯 Go 的 NTLMSSP 库可选，具体选型留给 EWS ticket）。Kerberos/Negotiate 连 Thunderbird 也没做。

### 4.4 其他坑

- **限流不走 HTTP 状态码**：EWS 用 500 加 SOAP Fault 或 `ErrorServerBusy`，里面带 `BackOffMilliseconds`，要解析后按要求等待重试（`rust/ews_xpcom/src/response_parser.rs`）。
- **服务器版本**：从每个响应的 `ServerVersionInfo` 头更新，请求时带 `RequestServerVersion`（`src/server_version.rs`）。不同版本操作不同：`MarkAsJunk` 2013 才有，旧服务器退化成普通 move（`src/client/mark_as_junk.rs`）；`ReturnNewItemIds` 2010 SP1 才有（`copy_move_item.rs`）；`EmptyFolder`、`changeReadStatusAll` 不支持时退化并回 `useLegacyFallback`。
- **move 会换 ItemId**：copy/move 返回新 id，或返回「需要重新同步」（`copy_move_item.rs` 的 `CopyMoveSuccess`）。期望位置模型（ADR 0005、0034）要能接受「移动后 id 变了」。
- 删除每批最多 1000 个，这个上限文档里没写，是从错误信息里看到的（`delete_messages.rs:24-26`）。删除时遇到「服务器上已经没了」当成功处理，避免本地留下僵尸邮件。
- **发信**：`CreateItem` + `MessageDisposition::SendOnly`，MIME 用 base64 放进 `MimeContent`；Bcc 不在 MIME 里，要单独作为 `BccRecipients` 传。已发送副本由 Thunderbird 自己的发信模块保存，不让 EWS 存（`src/client/send_message.rs`）。
- **测试**：`mailnews/test/fakeserver/EwsServer.sys.mjs` 是一个 JS 写的假 EWS 服务器，响应模板取自 O365 真实响应的精简版；同目录还有 `GraphServer.sys.mjs`。`mailnews/protocols/exchange/test/unit/` 有约 30 个 xpcshell 测试（`test_syncMessagesForFolder.js`、`test_httpAuthCache.js`、`test_server_version.js`、`test_item_operations_exchange_2010.js`…）。

## 5. 对 #584 各待定项的输入

只列事实推出的选项，不替地图下决定。

- **接缝层级**：三家都把「协议客户端」和「本地存储 / 应用概念」分开。Thunderbird 分得最彻底（客户端不知道本地存储）；Himalaya 的适配器在应用层，协议库只做原语；aerc 的 worker 直接产出 UI 模型。地图要求的「独立包、不依赖 `core` 和 `store`」与 Thunderbird 的边界一致。
- **能力模型**：Himalaya 的静态表（四级 + 文字说明 + 连接前拒绝）是唯一成形的设计。aerc 的反面教训是能力只在运行时才知道，UI 得回滚。`Partial` 和 `None` 带的说明文字可以直接给界面和 Wit 用。注意 Himalaya 的「同名必须同结果」规则：JMAP/Gmail 的 copy 是同一封信，和 IMAP 的 copy 不同。
- **数据模型**：三家的共享 ID 都是**不透明字符串**。aerc 把 UIDVALIDITY 编进 IMAP id；Thunderbird 用每文件夹的不透明 sync token；Himalaya 要求 ID 只在同一个存储后端内有效。EWS 的 move 会换 id，JMAP/Gmail 一封信可以有多个位置。
- **发信**：三家都把「能自己发信的存储后端」（JMAP、Gmail、Graph、EWS）和「需要 SMTP 的」（IMAP、本地）区分开。Bcc 要从 MIME 里剥掉，单独作为信封或字段传。已发送副本由谁保存，各协议不同（JMAP 用 `onSuccessUpdateEmail`，EWS 让客户端自己存，Gmail 的 `submit.copy` 在 Himalaya 表里是 `Full`）。这些都只在协议后端的「最后投递」一步，不影响 ADR 0003/0006 的链路。
- **外发**：JMAP 下载主机可能和 API 主机不同（Fastmail），EWS 的 Autodiscover 和端点设置、OAuth 的 token 端点也都是额外的主机，后端声明连接主机时都要算进去。
- **测试**：可以照 io-* 的四层（单元测试 / 喂录制字节的离线测试 / 本地服务器容器 / `#[ignore]` 的真实服务商），加上 Thunderbird 的假 EWS 服务器思路；服务商实测报告照 Himalaya 的格式，每条 P 编号对应一个回归测试。
- **第一种新协议**：没有新事实偏向某一种。工作量的线索：Himalaya 的 Graph 表里 `None` 最多（不能 APPEND、没有 answered/draft/junk 标志、`$search` 和 `$filter` 不能同时用）；EWS 还要额外处理 NTLM、服务器版本差异和 move 换 id。

## 未核对

- Himalaya 的 `src/<proto>/backend.rs` 只抽读了 Gmail 和 JMAP 的 copy/move/delete；IMAP 的实现细节以 spec 为准。
- neverest / pimdir 的同步引擎和存储格式没有读，只引用了 Himalaya spec 里对它的描述。
- Thunderbird 的 `graph_xpcom` 和 C++ 侧（`ExchangeFolder.cpp` 等）没有读。
- 139 邮箱在 Go `crypto/tls` 下能否握手没有实测。
