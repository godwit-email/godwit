---
status: proposed
---

# 协议后端交出变更集，本地负责落库；第一种新协议是 JMAP

用户的邮箱不一定走 IMAP（#583）：本地部署的 Exchange 只开 EWS（#581），企业邮箱只开 POP3（#416），Stalwart、Fastmail、Cyrus 原生支持 JMAP（[Discussion #481](https://github.com/godwit-email/godwit/discussions/481)）。现在 `core/mailsync` 的每个函数都收 `*imapclient.Client`，一边讲 IMAP 一边写 SQLite，表里的服务器位置和游标也是 IMAP 的形状，第二种协议没有地方接进来。设计过程记在 #584 及其子 issue 里。

我们把**协议后端**（见 GLOSSARY）放在**变更集**这一层：一轮同步由收信后端交出协议无关的变更和一个不透明游标，怎么拉增量（UID 分页、`Email/changes`、`SyncFolderItems`、delta）全由后端自己决定；落库、认领、孤儿、合并，以及一轮同步内的顺序，都留在本地。协议后端放在本仓库的独立包里，不依赖 `core` 和 `store`。IMAP/SMTP 先迁到这个接缝后面，行为不变；第一种新协议是 JMAP。

## 接缝

- **一个账户一个收信后端、一个发信后端。** 收信后端提供：一轮同步、按服务器引用取内容（整封、某个部件、指定头字段，部件用后端给的不透明引用）、执行一条写回操作、可选的 Watch，以及添加账户时的检查。发信后端只负责投递，并声明服务器会不会自动存已发送副本；不会存的话，由收信后端执行「存到已发送」。发件箱、审批和「最多发一次」（[0003](0003-no-extension-can-send.md)、[0006](0006-send-at-most-once.md)）都留在 core。
- **写回操作返回四种结果之一**：完成（带新的服务器位置）、完成但位置未知、失败、不支持。「位置未知」就是现在的待认领，由本地按 Message-ID 加大小认领（[0005](0005-local-first-identity-vs-location.md)）。只有本轮出现过「位置未知」，才做第二次推送。
- **Watch 只发唤醒信号**，附上哪个文件夹或范围变了；随后马上跑一轮只拉那一处的同步。写库路径只有一条。
- **连接归后端**：拨号、重连、会话、协议记录都由后端负责，凭据从 core 给的回调里取；认证失败用统一的错误报回 core，由 core 刷新令牌或标「需要重新授权」。
- **接缝上的类型和协议无关**：标记用中性写法（系统标记用固定字段，自定义关键字用字符串），在边界上转成本地的 `\Seen` 拼写，公开 API 不变；MIME 结构用后端包自己的类型，不用 `imap.BodyStructure`；文件夹角色由后端识别。Gmail 的 IMAP 扩展（`X-GM-EXT-1`）搬进 IMAP 后端内部，在变更集里表现为「一封邮件有多个位置、身份在账户内稳定」。

## 数据形状

[0005](0005-local-first-identity-vs-location.md) 和 [0034](0034-message-and-its-locations.md) 里「服务器位置 = UIDVALIDITY + UID」的说法由本 ADR 修订：

- **服务器位置**改成一列不透明的 `server_ref`，约束为 `UNIQUE(folder_id, server_ref)`。IMAP 后端把 UIDVALIDITY 和 UID 编进这个字符串；UIDVALIDITY 一变，后端就发「文件夹重置」，本地清掉这个文件夹的服务器位置再重新认领。本地代码不再理解 UIDVALIDITY。
- **文件夹**加一列不透明的 `server_ref` 作唯一键：IMAP 用名字，其他协议用文件夹 id，所以改名时只改名字。
- **身份**：`messages.gmail_msgid` 改成通用的 `server_id`。后端声明账户的身份属于哪一类：账户内稳定（Gmail IMAP、JMAP、Gmail API、Graph ImmutableId）、只有位置（普通 IMAP、EWS）、只有收件箱（POP3）。身份稳定的账户照 0034 合并。JMAP 合并邮件后 id 会变，当作旧邮件消失、新邮件到达。
- **游标**是不透明字符串，账户级、文件夹级各有一个，由后端自己决定用哪个。每一页变更和它的游标在同一个事务里写入。`synced`、`filled_since` 是本地的同步进度，留在本地。
- **变更集**里，删除可以按增量写（「这个位置没了」），也可以给出一个文件夹的完整快照，由本地对比出差异。每封新出现的邮件都由后端标明是新到达还是补全；提醒、规则和 `message.received` 都以这个标记为准。
- **只有收件箱的账户**是 0034 的例外：服务器上的信没了，只清掉 `server_ref`，邮件仍然保留。

## 协议能力

**协议能力**（`Feature`，见 GLOSSARY）分两层声明。随协议写死的是一张静态表，放在后端包里。要看服务器的那部分（UIDPLUS、MOVE、IDLE、`X-GM-EXT-1`）在连上服务器后写进账户，记录时机和 0034 记 `X-GM-EXT-1` 相同。分三级：支持、支持但有说明、不支持（附原因）。

命名规则：会改变用户数据结果的差异，就拆成不同的名字，例如「复制」（产生独立副本）和「加到文件夹」（同一封邮件多一个位置）；只是精度或匹配方式不同，就同名并附一句说明。界面把做不到的操作置灰并写明原因，整块功能都不存在时才隐藏。Wit 的工具描述不随账户变化，调用时返回「不支持：原因」，这样审批和「信任的只读工具」都不会失效（[0004](0004-permission-gateway-taint.md)）。没有推送的后端由自己声明轮询间隔：HTTP 增量接口 1 分钟，POP3 和没有 IDLE 的 IMAP 5 分钟。

## 账户与服务商

服务商表从「IMAP、SMTP 两个槽」改成一组后端条目，每条是「协议 + 端点 + 登录方式」，同一个服务商可以有多条。账户也改成收信后端、发信后端两个通用槽，Outlook 的 Graph 发信（#577）成为发信槽里的一种协议。本机 API 里账户的 `IMAPHost/Port/Security`、`SMTP*` 直接换成通用字段，这是破坏性改动。

## JMAP

- **为什么先做 JMAP**：它对接缝的检验最强（账户级游标、一封邮件在多个 mailbox、推送、服务器自动存已发送、下载主机和 API 主机不同）；用户出了问题可以随时退回 IMAP。EWS 和 POP3 等接缝验证过之后，各自另开工作。
- **实现与维护**：由维护者实现并维护，#481 的贡献者帮忙实测和评审。JMAP 先作为实验性功能上线，要在设置里打开才能添加 JMAP 账户。如果以后停止维护，就退回实验性：停止添加新账户，并提示已有账户改用 IMAP。
- **主机与凭据**：信任 session 给出的主机；HTTP 重定向只跟随同一主机的，添加账户时的 `/.well-known/jmap` 除外。添加账户时显示 session 主机。第一版的认证方式是 Basic 和 Bearer。同步流量和 IMAP 一样算账户配置，不走外发审批。
- **第一版范围**：发信走 SMTP；已有的 IMAP 账户不能原地切换成 JMAP。
- **客户端库**：用 `godwit-email/go-jmap`，它是 `git.sr.ht/~rockorager/go-jmap` 的 fork。规矩和 go-imap fork 一样，内存 JMAP 服务器也放在这个 fork 里。

## Considered Options

- **接缝放在 IMAP 动词这一层**（ListFolders、FetchByUID、StoreFlags），让其他协议去模拟 IMAP：改动最小，但 JMAP、Gmail 的账户级游标和 EWS 的 SyncState 都得硬塞成 UID 的形状。Himalaya、aerc、Thunderbird 都没有走这条路。
- **后端直接落库**：每种协议最自由，但违反「不依赖 store」，落库、认领、合并的逻辑也要每个后端各写一份。
- **服务器位置保留整数列，另加一列文本**：迁移最少，但本地每一处都要同时处理两种形状。
- **照搬 Himalaya 的四级能力**（`Full / Partial / Server / None`）：命令行用户读说明就够了，但 Godwit 的本地存储、界面和 Wit 都要按结构化的信息分支，`Partial` 里的说明文字没法拿来分支。
- **只在运行时报「不支持」**（aerc 的做法）：界面只能先让用户点下去，失败了再回滚。
- **JMAP 后端做成原生插件**（[0015](0015-native-plugins-trusted-subprocess.md)）：协议后端调用很频繁、要按页提交变更、要保持推送长连接，全部搬到子进程的 JSON ABI 上代价太大。

## Consequences

- `mailsync` 要先把「服务器说了什么」和「本地怎么落库」拆开。落库这一部分（位置、认领、孤儿、合并）变成协议无关的代码，留在 `mailsync`。
- 迁移时把现有数据按同样的规则编码进新列：`server_ref` 由 UIDVALIDITY 和 UID 编成，`server_id` 取 `gmail_msgid`，文件夹的 `server_ref` 取名字，IMAP 的文件夹游标由 `uidvalidity/last_uid` 编成。用户看到的行为不变。
- 测试：一套后端契约测试，同时跑 IMAP（testmail）和 JMAP（内存服务器）。现有的 coretest 测试一行不改，作为回归网。
- 按 AGENTS.md「IMAP server quirks」，服务器的怪异行为仍然修在 go-imap 或 go-jmap 的 fork 里，不放进协议后端。
