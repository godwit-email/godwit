# 发件人头像：BIMI、favicon、Gravatar 统一实施计划

> **For agentic workers:** 使用 superpowers:executing-plans 在当前对话逐项实施；用户明确选择并行执行时才使用 superpowers:subagent-driven-development。任务通过复选框跟踪。

**Goal:** 在现有发件人头像位置，优先显示可信 BIMI Logo，再尝试网站 favicon、Gravatar，最终保留字母头像。

**Architecture:** core 根据当前邮件的认证信息、发件人地址和图片加载策略决定来源；头像下载复用公网 HTTP 客户端与现有图片缓存机制。app 异步获取头像，使用现有 MyGo SVG／位图渲染，保持头像尺寸和阅读器布局。

**Tech Stack:** Go、现有 SQLite store、net/mail、crypto/sha256、golang.org/x/net/html、publicsuffix、MyGo；ICO 解码单独使用 github.com/fyne-io/image/ico，不引入 Fyne UI 或头像 SDK。

**Spec:** 本文“设计与已确定的行为”；设计和实施步骤保存在同一个计划中。

**状态:** 已合并 Claude Fable 5.1 审阅中经核实的修改；2026-10-13 完成任务 1 的兼容性前置检查，可信服务商名单为空，BIMI 真实验证待做，解析器及任务 2 延后，继续任务 3～6 的 favicon／Gravatar／字母头像流程。来源顺序和自有域名 Gravatar 兜底保持已确定行为。设计、实施步骤、服务商证据均维护在本计划内；BIMI 服务商信任证据须在任务 1 验证，不能以解析测试替代。

## Global Constraints

- 顺序：可信 BIMI → favicon → Gravatar → 字母；命中即停止，不同时请求所有来源。
- 已知公共邮箱地址跳过 favicon；其他域名也允许 Gravatar 兜底。
- favicon 和 Gravatar 只表示头像，不显示认证标识；本期 BIMI 也不新增认证徽章。
- 任意邮件头、DNS 记录、品牌证书，都不能单独证明当前邮件来自该品牌。
- 网络头像遵循现有 RemoteImages 与垃圾邮件策略；垃圾邮件不自动请求，用户对当前邮件点击“加载图片”后可以请求。新邮件在政策允许时预取头像，纯文本邮件同样覆盖。
- 网络请求不在绘制回调执行；切换邮件、删除账号、关闭服务时取消或丢弃旧结果。
- 保留正文图片原有格式、安全限制和接口行为，不因头像支持 SVG／ICO 而允许正文加载它们。
- 不新增商业 API、云代理、品牌映射库、联系人系统、个人资料卡或手工上传头像。

## Review Focus

1. 同一发件域名的一封邮件 BIMI 验证通过、另一封失败：只能共享图像，不能共享认证结论。
2. 发件人注入 Authentication-Results／BIMI-*、重复头和误导性 authserv-id：未经确认来源的结果不能启用 BIMI。
3. 网站首页／图标重定向到内网，或 SVG 包含外链、循环引用、过多路径：拒绝请求或渲染，继续兜底。
4. 图片政策从 auto 改为 ask，或请求完成前切换线程：缓存和迟到结果不能绕过当前政策或显示到别的邮件。
5. 公共邮箱、个人自有域名、IDN 和多级公共后缀：按地址域名准确分流，不把 Gmail、QQ 等服务商 Logo 当个人头像。

---

## 设计与已确定的行为

### 1. 来源顺序

| 地址／结果 | 下一步 |
| --- | --- |
| 任意地址，有可信且可显示的 BIMI 图标 | 显示 BIMI，停止 |
| BIMI 缺失、不可信、认证失败、图标损坏或不支持 | 进入普通头像兜底，不授予认证含义 |
| 已知公共邮箱域名 | 跳过 favicon，尝试该完整邮箱地址的 Gravatar |
| 其他域名 | 尝试 favicon；没有可用图标时尝试完整邮箱地址的 Gravatar |
| Gravatar 无匹配、网络失败或所有来源被政策禁止 | 显示原有字母头像 |

公共邮箱识别由 core 从 core/auth.ListProviders 提取 Domains 并传入 internal/avatar；internal/avatar 不反向依赖 core/auth。排除表补齐 proton.me、protonmail.com、pm.me、fastmail.com、hey.com、gmx.com、gmx.de、gmx.net、web.de、mail.com、tutanota.com、tuta.io、yandex.com、yandex.ru、mail.ru。这是一份“公共邮箱域名排除表”，不是品牌头像映射表；精确匹配，不根据发件人名称、MX 托管服务或字符串包含关系判断。该分类只决定是否跳过 favicon，不声称识别出了所有个人用户。

### 2. BIMI 的范围与信任

第一期作为邮件客户端消费收件服务商的验证结果，不在桌面端实现完整 SMTP／SPF／DKIM／DMARC 或 VMC／CMC 验证器，也不只凭 DNS 的 l= 下载并标记为 BIMI。

优先读取可信服务器提供的 BIMI-Indicator，验证 From 域名与认证结果的关系。本期默认要求可信 Authentication-Results 提供匹配的 policy.indicator-hash；缺失或不匹配时不使用该内嵌图标。没有可用内嵌图标时，优先使用同一可信验证结果的 policy.indicator-uri；BIMI-Location 只有在任务 1 证明服务商清除伪造头并绑定当前邮件验证结果时才可使用，否则忽略。重复、冲突、不明来源或无法绑定的结果均不能启用 BIMI。

所引草案 §5.4 的 hash 要求和禁止 BIMI-Location 条款处于 ARC 场景，§5.3／§5.5 同时允许基于服务商信任的头部消费；本期不实现 ARC。上述默认 hash 校验是本期采取的保守规则，不把 ARC 条款泛化为所有服务商的协议要求。

可信服务商名单以实际收件账号的服务器为依据，不以发件域名或账号的可编辑 Provider 字段单独决定。每项在本计划记录 IMAP 主机、允许的 authserv-id、服务商清除外部伪造头的证据、图标绑定方式以及可复现样本。还须核实 IMAP APPEND、跨账号迁入、POP 代收等路径：IMAP 主机、顶部 Received、文件夹角色和 authserv-id 字符串匹配均不能单独证明消息经过可信 SMTP 入口。规则不能排除伪造来源时不启用该服务商；本机 APPEND 或已知导入邮件直接跳过 BIMI，邮件移动不改变来源信任。没有满足条件的服务商时保持名单为空，正常执行 favicon／Gravatar。

任务 1 是 BIMI 的兼容性前置检查。如果目标服务商没有可证明可信的头部链路，就记录该账号不能启用 BIMI。若所有目标服务商均不满足条件，延后任务 2 的迁移、同步与补头改动，任务 5 直接使用普通头像路径，不增加占位实现。支持这些账号所需的额外验证能力超出本期范围。

每封邮件的认证结论独立保存和检查。图标字节可按内容或 URL 共享缓存，不能按发件域名缓存一个 bimi=pass 供其他邮件使用。

仅对已启用可信规则的服务商，新同步抓取必要头；旧邮件第一次查看时仅补取这些头，使用 BODY.PEEK[HEADER.FIELDS ...]、Partial 和已有账号锁／UIDVALIDITY 校验，不下载完整原文，不更改已读标记，不全量重同步。补头非阻塞尝试账号锁，锁忙、离线或补头超过 2 秒时进入普通头像兜底，不把暂时失败保存为“无头”；下次查看可重试。抓取上限为 128 KiB 加 1 字节溢出探测，请求与响应段匹配使用同一 Partial 规格。截断／不完整时不能启用 BIMI。

### 3. favicon

域名按 net/mail 解析地址后规范化：域名转小写、IDNA 转 ASCII；用 publicsuffix.EffectiveTLDPlusOne 找组织域名。子域与组织域相同则只尝试一次，不把 co.uk 等公共后缀当网站。

依次尝试原始发件域名、组织域名。每个网站使用 HTTPS：先 /apple-touch-icon.png，再首页中声明的 apple-touch-icon／apple-touch-icon-precomposed／icon，最后 /favicon.ico。首页只读前 256 KiB；用现有 x/net/html tokenizer 解析 link，不执行脚本。相对地址以最终首页 URL 为基准；只接受 HTTPS 图标地址，最多尝试 3 个声明地址，单个域名总期限 5 秒，两个网站合计最多 10 秒。

favicon 不从邮件正文、签名、追踪链接或 display name 猜品牌；公共邮箱不发起网站请求。允许网站图标来自公开 CDN，但每次跳转和实际拨号都必须执行公网地址检查。

每站最终选中的安全图像按网站键缓存 24 小时；FindFavicon 先检查该条目，再进入候选发现，不能因为只缓存了图标 URL 而在读信时重新抓首页。该条目和网站未命中使用同一独立键空间及容量限制。

支持现有 PNG／JPEG／GIF／WebP、ICO 和静态 SVG。ICO 解码后以 PNG 缓存；BIMI SVG 要求 Tiny PS，favicon SVG 不要求 tiny-ps 声明，但只能使用同一受限静态元素集。格式不支持、HTML 冒充图片或安全校验失败都视为该候选不可用。

favicon 位图要求原始宽、高均至少 32px；任一边小于 32px 即丢弃，不通过放大补足，不以 HTML 的 sizes 声明代替实际尺寸。ICO 以实际选中并解码的图像尺寸判断。SVG 为矢量图，不适用位图最小尺寸。尺寸不合格继续尝试后续候选／网站，全部不可用后再尝试 Gravatar；写入和读取头像缓存均遵守该规则。

### 4. Gravatar

邮箱去首尾空白、整体转小写，再计算 SHA-256；不去除 +tag，不折叠 Gmail 的点，不将别名合并为其他地址。

固定请求：`https://gravatar.com/avatar/<sha256>?s=64&d=404&r=g`。只获取图像，不使用 profile API 或 SDK，也不传显示名／原始邮箱。404 表示无头像并回退到字母，持久化未命中 24 小时；429、5xx、超时采用现有 5 分钟失败缓存，避免逐帧重试。Gravatar 的哈希仍可能关联到邮箱，不能把哈希请求称为匿名。

### 5. 下载、缓存与政策

复用 imagefetch.NewClient 的公网拨号与传输限制，头像使用 imagefetch.NewAvatarClient(base) 派生严格客户端：复制已有客户端，复用 Transport，并叠加 HTTPS 和跳转次数检查，保留原 CheckRedirect 的限制。下载入口也检查初始 URL。imagecache 持有该头像客户端，favicon 首页使用同样规则；不新增 core.Options 客户端配置，沿用 ImageClient 的测试注入。无 Cookie／Referer、通用 UA、超时和公网地址检查保持有效。读取上限在流式下载时生效，不能先下载完再判断。

头像单文件最多 256 KiB、位图最多 1024×1024；SVG 最多 32 KiB，XML 嵌套最多 32 层、元素最多 4096、路径命令总数最多 16384。拒绝 DTD、外部引用、脚本、动画、嵌入位图和循环 use；不在 UI 绘制时解析。合法但渲染器不支持的文件直接兜底。

头像复用 imagecache 的单飞、并发上限、原子写入、LRU 和关闭逻辑，使用独立键空间，纳入现有 256 MiB 总缓存容量与清除按钮。普通图片缓存键不变；正文入口继续拒绝 SVG／ICO，即使头像缓存中存在对应 URL。头像成功记录 24 小时刷新；404、图标格式被拒绝或网站发现完成且无可用图标等确定未命中持久化 24 小时，网站未命中按规范化 host 记录，避免重复抓首页。任何候选出现超时、429、5xx 等瞬时错误时不记录该网站的长期未命中，仅用现有 5 分钟内存失败缓存。刷新失败时普通头像可显示上次安全图像，但 BIMI 仍须当前邮件认证通过。

持久化落点为现有缓存目录：普通文件名仍为 `<key>-<format>-<w>x<h>`，头像文件名为 `<key>-<format>-<w>x<h>-<writtenUnix>`，未命中为零字节 `<key>-miss-0x0-<writtenUnix>`。`imagecache.AvatarKey(source, identity)` 生成 `avatar_<source>_<32hex>`，identity 为 URL 或规范化网站 host；保留 source 以便重启校验 favicon 下限。未命中记录最多 1000 项，过期或超过上限时删除。parseName 区分普通、头像与未命中，只有头像格式集合包含 SVG，不扩大 imagefetch.Formats；重启恢复写入时间和未命中，mtime 仅作 LRU。清除按钮覆盖全部条目。

RemoteImages=auto 时允许自动网络头像；trusted 只允许已信任发件人；ask 时直到用户对当前邮件点“加载图片”才请求。新邮件到达时在 ImagesAllowed 通过后预取头像；从正文预取的无 HTML 提前返回和正文数量／字节上限路径中独立出来，不能遗漏纯文本邮件，也不能因为正文下载失败而跳过头像。用户点“加载图片”后同时重试头像。内嵌 BIMI 图像不产生外部请求，可在通过信任和格式校验后本地显示；远程来源在禁用状态下连 DNS 发现也不执行。缓存命中不能绕过远程来源的显示政策。

设置页现有远程图片说明补充：包含品牌／个人头像；可能联系发件域名网站和 Gravatar。仅已在到达时预取且仍命中缓存的请求不随读信发出；旧邮件、预取未完成、缓存未命中或过期、手动加载时仍可能在阅读期间请求，不能承诺所有头像都隐藏阅读时间。无自建网络代理时对方仍能看到本机出口 IP；无 Cookie／Referer 不会隐藏 IP。

### 6. UI 与范围

先覆盖 app/reader.go 现有发件人头像，不新增邮件列表列、账号头像或联系人界面。保持 32 DIP 圆形占位、现有名称标签和字母回退；SVG 用 ui.Image 保留原色，不用会统一染色的 ui.Icon。

先显示字母，后台加载；不增加阻塞阅读的 spinner 或网络错误提示。来源缺失是正常结果。失败仍可打开邮件；头像结果按 message ID 和 reader.gen 绑定，切换线程丢弃旧结果。复用现有加载图片动作和设置，不新增每个来源的开关。

### 7. 预计投入

有可信服务商时，统一方案粗估 8～15 人日；全部服务商无可信证据时跳过同步／补头工作，但不将本期宣称为已支持 BIMI。原分项估算为：BIMI 服务商信任核对 1～2 天，格式／缓存／favicon 3～5 天，同步与来源解析 2～4 天，UI／兼容性验证 2～4 天。不同任务有交叉，该区间不是分项上限相加。需要自行验证 ARC／品牌证书或支持不提供可信头部的服务商时，超出本期估算。

## 实施任务

### Task 1: 确定 BIMI 的可信收件链路与解析输入

**当前结论（2026-10-13）：** 没有单独授权的测试账号凭据或真实样本，未能确认任何可信服务商。兼容性前置检查已记录；下列真实测试及解析器实施步骤保持待做，不能将它们标记为通过。不创建恒 false 的占位解析器或假成功样本；恢复 BIMI 工作须先满足“服务商证据记录”的启用门槛。

**Files（有可信服务商后）：** 创建 `internal/avatar/bimi.go`、`internal/avatar/bimi_test.go`、`internal/avatar/testdata/`；更新本计划的服务商证据记录，必要时复用 `core/auth/providers.go` 的账号服务器信息，不新增证据文档。

**Interfaces（有可信服务商后）：** 产出 `avatar.BIMIInput{From, IMAPHost, Header string}`、`avatar.BIMIResult{Data []byte; URL string}` 和 `avatar.TrustedBIMI(in BIMIInput) (BIMIResult, bool)`。Header 保存规则消费的头部顺序和重复项；core 排除已知本机 APPEND／导入来源后才调用解析器。证据在本计划记录，无证据时函数结果必须是 false。

- [ ] 在用户授权的测试账号上获取真实成功样本，并通过受控外部发件验证伪造 Authentication-Results／BIMI-* 不会被视为可信；只保存脱敏头，保留验证需要的顺序、重复项和域名关系。同时检查 IMAP APPEND／迁入／代收保留伪造头的风险及来源可区分性；不能证明可排除的路径使该服务商保持禁用。不可达或没有账号时不得伪造“已实测”结论。
- [ ] 写 `TestTrustedBIMI`：可信样本 → 图标／URL；外部伪造、authserv-id 相同但来源不符、重复冲突、域名不符、indicator hash 缺失／不符且无其他可信图标来源、未知服务器 → false；匹配的可信 indicator-uri 可用，未绑定的 BIMI-Location 不可用。证据不足的服务器同样 false。
- [ ] 运行 `go test ./internal/avatar -run TestTrustedBIMI -count=1`，确认新行为未实现时失败。
- [ ] 实现 `TrustedBIMI`，只启用有证据支持的规则；不按头部字符串包含关系搜索 pass，不把未经验证的 ARC 头当证明。
- [ ] 重跑测试，在本计划记录服务商、证据来源、真实样本、伪造／导入检查及结论；未支持账号保持普通头像路径。
- [x] 检查是否存在至少一个可启用的目标服务商：当前无可证实的可信服务商，延后任务 2 与未使用的生产 BIMI 解析器；任务 5 不调用补头／BIMI 分支，继续任务 3～6。
- [ ] 仅提交本任务文件，提交尾部带 `Co-authored-by: Codex <noreply@openai.com>`。

### Task 2: 保留新邮件认证头并按需补取旧邮件

**前置条件:** 任务 1 至少确认一个可信目标服务商，否则整体延后；同步与补头仅对启用服务商执行。2026-10-13 判定前置条件未满足，本任务整体延后，不增加头像头字段、数据库迁移或同步／补头代码。

**Files:** 修改 `core/mail/parse.go`、`core/mailsync/fetch.go`、`core/mailsync/messages.go`、`core/store/schema.go`，必要时修改 `core/service.go` 复用账号锁通道；创建 `core/avatar_headers.go`、`core/avatar_headers_test.go`；扩展 `core/mail/parse_test.go`、`core/sync_test.go`、`core/store/store_test.go`。

**Interfaces:** `mail.Parsed` 增加 `AvatarHeader string`；新迁移增加 `messages.avatar_headers TEXT`（NULL=尚未获取，空字符串=获取完成且无相关头）。`(*Service).avatarHeaders(ctx context.Context, messageID int64) (avatar.BIMIInput, error)` 为后续解析器提供当前邮件及真实账号服务器。

- [ ] 写测试：重复头及顺序保留；新同步保存必要头；旧数据库无损迁移；旧邮件补取一次；UIDVALIDITY 改变不取错邮件；BODY.PEEK 不改已读；用一个断言覆盖已有邮件移动／本地认领时元数据保留；无服务器 UID 或已知本机导入时跳过 BIMI；账号锁忙立即返回、补头超时不阻塞普通头像、Partial 匹配、超限不能通过。
- [ ] 运行 `go test ./core/mail ./core/mailsync ./core/store ./core -run 'AvatarHeader|AvatarMigration|AvatarSync' -count=1`，确认失败。
- [ ] 按任务 1 规则列出并抓取必要头；仅存在 bimi 认证项或 BIMI 图标头时保存相关内容，否则写空字符串。Received 仅保存规则实际需要的条目；保留被消费头的重复项／顺序，不转换成单值 map。使用 128 KiB 加 1 字节溢出探测，完整性检查先于裁剪；不扩大既有回复／线程头的存储。
- [ ] `avatarHeaders` 使用既有 begin／bind、账号锁通道的非阻塞获取、服务器 UID 和 UIDVALIDITY 检查，锁忙跳过、不改变现有同步的锁等待行为；旧邮件补头最多 2 秒。BODY.PEEK HEADER.FIELDS 使用 Partial，请求与 FindBodySection 使用同一段规格。成功后只更新该邮件元数据；暂时失败保持 NULL，不完整数据不得通过 BIMI。
- [ ] 重跑相关测试；单独检查既有同步／移动／认领测试。
- [ ] 仅提交本任务文件，保留共同作者尾部。

### Task 3: 头像专用格式校验与共享缓存

**Files:** 创建 `internal/imagefetch/avatar.go`、`internal/imagefetch/avatar_test.go`；修改 `internal/imagefetch/imagefetch.go`、`internal/imagecache/imagecache.go`、`internal/imagecache/imagecache_test.go`、`go.mod`、`go.sum`；扩展 `internal/imagefetch/imagefetch_test.go`。

**Keys:** `imagecache.AvatarKey(source, identity string) string` 返回 `avatar_<source>_<32hex>`；GetAvatar 从 URL 生成键，PutAvatar／LookupAvatar／AvatarMiss／RememberAvatarMiss 使用完整键，网站 identity 使用规范化 host。

**Interfaces:** `imagefetch.NewAvatarClient(base *http.Client) *http.Client`；`imagefetch.DownloadAvatar(ctx context.Context, client *http.Client, src string) ([]byte, error)`（流式 256 KiB 上限，Accept 包含 SVG 与现有位图／ICO，初始 URL 仅 HTTPS）；`imagefetch.CheckAvatarImage(data []byte, source string) (out []byte, format string, width, height int, err error)`；`(*imagecache.Cache).GetAvatar(ctx context.Context, src, source string) (Image, error)`；`(*imagecache.Cache).PutAvatar(ctx context.Context, key string, data []byte, source string) (Image, error)`；`(*imagecache.Cache).LookupAvatar(ctx context.Context, key string) (Image, bool, error)`（只读未过期的头像条目，不发网络）；`(*imagecache.Cache).AvatarMiss(key string) bool`、`(*imagecache.Cache).RememberAvatarMiss(key string) error`（供网站最终未命中查询／记录）。source 仅 bimi／favicon／gravatar，key 使用 avatar／source 独立键空间；PutAvatar 用于已验证的内嵌 BIMI 或已选中的网站图像，仍执行对应格式校验，不能接受任意正文数据。格式校验放在 imagefetch，保持依赖方向 avatar → imagecache → imagefetch。

- [ ] 写测试：合法 Tiny PS、静态 favicon SVG、PNG／DIB 两种 ICO；尺寸／字节／XML／路径上限；use 循环和外链拒绝；HTML 冒充图像拒绝；初始 HTTP、HTTPS→HTTP 跳转和重定向内网拒绝；普通 `Get`／`Dims` 在导入 ICO 解码包后仍拒绝 SVG／ICO；跨来源键隔离；24 小时刷新／未命中、重启后 TTL／SVG 恢复和 5 分钟瞬时失败缓存；单飞、清除竞态和 Close 取消。
- [ ] `TestFaviconImageMinSize`：16×16、31×32、32×31 位图／ICO 拒绝，32×32 接受；以真实解码尺寸为准，不放大后再判断；安全 SVG 不受该位图下限影响。缓存中的不合格 favicon 同样不可用；该最小尺寸规则仅适用于 favicon 来源。
- [ ] 运行 `go test ./internal/avatar ./internal/imagecache ./internal/imagefetch -run 'Avatar|BIMIImage|FaviconImage' -count=1`，确认失败。
- [ ] 实现 NewAvatarClient 及流式 DownloadAvatar；imagecache 从既有 client 派生头像 client，favicon 首页复用同一严格构造方法。测试用 TLS httptest 及已有 ImageClient 注入，不增加第二个 core.Options 字段。
- [ ] 用标准库 XML token 读取和白名单限制静态 SVG；使用现有位图解码；ICO 使用独立解码包并在解码前检查目录项偏移／长度／尺寸与资源上限，转为 PNG。不要手写完整 ICO／DIB 解码器，也不要直接调用 MustParseSVG 处理网络内容。
- [ ] CheckAvatarImage 在 source=favicon 时校验原始位图宽、高均至少 32px，ICO 在转 PNG 前检查实际解码尺寸；读取已有 favicon 缓存时也检查该下限，不返回过小图像。
- [ ] 复用现有缓存工作流添加头像和未命中入口，有限格式由头像入口校验；限制在读取和解码前生效。按设计的四段头像文件名与 miss 条目持久化写入时间／未命中；parseName 单独识别头像 SVG，不修改正文 Formats；清除、重启和淘汰覆盖头像／未命中，未命中最多 1000 项。LRU 访问时间仍只用于淘汰；普通缓存行为不增加 TTL。
- [ ] 重跑测试，包括整个原有 imagecache／imagefetch 测试，确认总缓存大小和清除行为包含头像。
- [ ] 仅提交本任务文件及必要依赖，保留共同作者尾部。

### Task 4: favicon 发现、公共邮箱分流与 Gravatar

**Files:** 创建 `internal/avatar/favicon.go`、`internal/avatar/gravatar.go`、`internal/avatar/domains.go` 及对应 `_test.go`；公共邮箱 Domains 由任务 5 的 core 传入，不导入 core/auth，不改账号自动发现行为。

**Interfaces:** `avatar.PersonalDomain(domain string, providerDomains []string) bool`；`avatar.FaviconSites(domain string) []string`；`avatar.FindFavicon(ctx context.Context, client *http.Client, cache *imagecache.Cache, host string) (imagecache.Image, bool)`；`avatar.GravatarURL(email string) string`。FindFavicon 返回一站的安全图像；原始域名与组织域的调用顺序由 core 编排，格式校验使用任务 3 的 imagefetch.CheckAvatarImage。

- [ ] 写测试：公共邮箱精确匹配；自有域名仍可兜底；`mail.example.co.uk` → 原始域名／`example.co.uk`；IDN；同域去重；固定候选顺序；HTML 属性顺序与相对地址；首页重定向后的基准地址；恶意 URL／HTTPS 降级／内网／超过候选数停止；首页读取最多 256 KiB；网站成功结果／确定未命中在重启后不再抓首页，瞬时错误不写长期未命中；所有图标失败返回无图像。
- [ ] `TestGravatarURL` 对大小写／前后空白产生同一 SHA-256；保留点和 +tag；断言固定 `s=64&d=404&r=g`，没有原始邮箱、名称、profile 请求。
- [ ] 运行 `go test ./internal/avatar -run 'PersonalDomain|Favicon|Gravatar' -count=1`，确认失败。
- [ ] 实现域名函数、有限 HTML tokenizer 发现和顺序下载；先用 LookupAvatar／AvatarMiss 查询网站键，选中图像后用 PutAvatar 保存网站结果。首页使用严格 HTTPS 客户端、HTML Accept、同一通用 UA／无 Referer、流式读取前 256 KiB 后关闭，仅解析已完整读到的 link。首页网络失败不影响最后尝试 /favicon.ico。5 秒每站、原始域名＋组织域合计 10 秒；所有候选确实不可用才记录网站未命中，取消／瞬时失败不记录。Gravatar 使用标准库 SHA-256，无 SDK／密钥。
- [ ] 重跑测试，确保第一页发现无结果后仍尝试 /favicon.ico、子域失败后尝试组织域。
- [ ] 验证过小 favicon 不终止候选链：16px 候选后有 32px 候选时选后者，所有候选过小时返回无图像，由 core 继续 Gravatar 兜底。
- [ ] 仅提交本任务文件，保留共同作者尾部。

### Task 5: core 串联来源与图片加载政策

**Files:** 创建 `core/avatars.go`、`core/avatars_test.go`；修改 `core/images.go`，必要时修改 `core/service.go`、`core/settings.go`；扩展 `core/images_test.go`。

**Interfaces:** `core.Avatar{Data []byte; Format, Source string; Width, Height int}`；`(*Service).SenderAvatar(ctx context.Context, messageID int64, allowRemote bool) (Avatar, error)`，空 Data 表示正常无头像。allowRemote 是用户对当前邮件点击“加载图片”的许可；自动获取始终检查 ImagesAllowed，垃圾邮件仅在此许可为 true 时可以请求；存储／取消错误才向调用方返回 error，来源缺失或下载失败继续兜底。

- [ ] 写表驱动测试：BIMI 命中不请求其他来源；BIMI 不可信转 favicon；公共邮箱不请求网站；favicon 命中不请求 Gravatar；自有域名双失败后请求 Gravatar；404 → 空；同域认证成功／失败邮件不能共享通过状态。
- [ ] 覆盖 auto／trusted／ask／垃圾邮件／当前邮件许可、缓存命中后的政策检查、设置切换、内嵌 BIMI 无网络、本地无效地址和服务 Close／账号删除取消；新邮件 HTML／纯文本均预取，正文失败／上限不跳过头像，ask／垃圾邮件不自动预取，预取后读信缓存命中不重发网站发现或图像请求；账号锁忙时普通头像不等待。每项用请求计数断言，不能只检查最后显示结果。
- [ ] 运行 `go test ./core -run 'SenderAvatar|AvatarPolicy|AvatarCancellation' -count=1`，确认失败。
- [ ] 实现固定顺序编排，复用 ImagesAllowed 与当前邮件许可；core 提取 provider Domains 传入 PersonalDomain。有限缓存去重图像，认证决策每封单独检查；服务商规则为空则直接兜底，不创建无用的同步占位。头像获取不写入 MessageSummary、不新增 Local API 或插件公开接口。
- [ ] 将头像纳入新邮件事件的现有预取工作，独立于正文无 HTML／错误／数量及字节上限的提前返回；用同一政策判定，取消时终止后续工作。预取未完成或缓存过期时阅读器仍可后台加载，隐私说明明确这一限制。
- [ ] 重跑本任务及原有图片政策测试；验证服务关闭后不会留下头像下载工作。
- [ ] 仅提交本任务文件，保留共同作者尾部。

### Task 6: 阅读器显示、文案与最终验证

**Files:** 修改 `app/reader.go`、`internal/kit/display.go`、`app/settings.go`、`internal/i18n/zh.go`、`internal/i18n/en.go`；扩展 `internal/kit/kit_test.go`、`app/reader_test.go`、`app/reader_body_test.go`、`app/settings_test.go`。

**Interfaces:** 保留 `kit.Avatar(c, name)`；新增 `kit.AvatarImage(c *ui.Context, name string, image ui.ImageSource) ui.Element`（nil → 原字母头像）。reader 的加载结果按 message ID／gen 保存，解析成安全 UI 图像后再绘制；SVG 用 ui.ParseSVG 返回错误时回退，用 ui.Image 保留品牌色。

- [ ] 写测试：初始字母占位；成功后保持名称标签／32 DIP／布局；解析失败仍字母；旧线程迟到结果丢弃；点击加载图片触发头像重试；禁用网络与垃圾邮件不自动加载、垃圾邮件手动许可可加载；设置中文／英文说明覆盖网站与 Gravatar。
- [ ] 新增 `TestRemoteImagesAvatarSetting` 检查中英文说明；运行 `go test ./internal/kit ./app -run 'Avatar|ReaderRemoteImages|RemoteImagesAvatarSetting' -count=1`，确认新行为未实现时失败；既有 `TestReaderRemoteImages` 保留通过。
- [ ] 实现异步加载和旧结果隔离，复用 app 既有 query／取消机制；不在 View 回调调用 DNS、HTTP 或 SVG 解析，不影响打开邮件与回复操作。
- [ ] 设置说明中文：`远程图片设置也适用于发件人头像。允许自动加载时，新邮件会提前获取头像；旧邮件、缓存未命中或过期、手动加载时，仍可能在读信时访问发件人网站和 Gravatar，对方可看到本机 IP 并可能推测阅读时间。Gravatar 使用邮箱哈希查询。` 英文提供等义说明，保留现有无 Cookie／Referer 描述，不作全局隐藏阅读时间的承诺。
- [ ] 运行 `go test ./internal/avatar ./internal/imagefetch ./internal/imagecache ./internal/kit ./core/... ./app ./contract -count=1` 与 `go test -race ./internal/avatar ./internal/imagecache ./core -run 'Avatar|ImageCache' -count=1`；有失败先排查，不扩大成无关重构。
- [ ] macOS 手工查看可信 BIMI、网站 PNG／ICO／SVG、公共邮箱 Gravatar、全部失败、离线、远程图片禁用、快速切换线程。检查品牌原色、圆形裁切和无布局跳动；使用经授权测试账号，不把真实邮箱提交到随机演示服务。
- [ ] 仅提交本任务文件，保留共同作者尾部；报告实测支持的服务商及剩余限制。

## 服务商证据记录

### 2026-10-13 兼容性前置检查

**可启用的可信服务商名单：空。** 这是证据不足的支持决定，不是所有服务商均不安全或不支持 BIMI 的结论。本期不宣称已支持 BIMI。

| 证据项 | 当前记录与结论 |
| --- | --- |
| 经授权的目标账号／IMAP 主机／authserv-id | 没有单独授权的测试凭据或真实样本，未确定可实测目标；不从用户私有邮箱数据库、系统钥匙串或凭据中查找 |
| 真实成功样本与图标绑定 | 未获得；没有脱敏样本路径，未验证 From、indicator-hash 或 indicator-uri 的绑定 |
| 受控外部伪造 Authentication-Results／BIMI-* | 未执行；未发送测试邮件或修改外部账号，不推断服务商会清除伪造头 |
| IMAP APPEND／跨账号迁入／POP 代收 | 未实测；尚不能证明来源可区分或这些路径不会保留可伪造头 |
| 公开资料的证明范围 | 本计划所列协议草案和 Apple 指导提供实现背景；本次未重新核实页面，不将泛泛服务商说明视为本项目实测证据 |
| 支持决定 | 无可证实的可信收件链路，所有服务商保持未启用；正常使用 favicon／Gravatar／字母兜底 |

恢复门槛：获得用户单独授权的测试账号与受控发件路径后，逐项记录实际 IMAP 主机、允许的 authserv-id、真实成功脱敏头（保留顺序与重复项）、外部伪造头处理、图标绑定，以及 APPEND／迁入／代收的可复现检查。不能排除的来源路径使该服务商继续禁用；IMAP 主机、顶部 Received、文件夹角色或 authserv-id 字符串匹配均不能替代来源证明。只有至少一个服务商满足全部门槛，才恢复任务 1 的解析器／测试和任务 2，再审查任务 5 的 BIMI 接入。

本次采纳的实施变化：延后未使用的 `BIMIInput`／`BIMIResult`／`TrustedBIMI`、生产解析器与测试样本，以及任务 2 整体；任务 5 按普通头像来源与政策实施，不引入补头或 BIMI 分支。任务 3 的图像格式校验不授予任何邮件认证含义。后续任务中的 BIMI 专属集成断言和手工可信 BIMI 验证同步延后，favicon／Gravatar 的验证仍须完成。

## 资料

- [BIMI 客户端与服务器实施指导（2026 年 5 月工作草案）](https://datatracker.ietf.org/doc/html/draft-brotman-ietf-bimi-guidance-15)：信任收件服务器结果、图标绑定和隐私。
- [Apple 对收件服务商的 BIMI 要求](https://developer.apple.com/support/bimi)：服务商验证后提供客户端所需头部。
- [BIMI SVG Tiny PS 要求](https://bimigroup.org/creating-bimi-svg-logo-files/)：静态 SVG 限制。
- [Gravatar 图像请求](https://docs.gravatar.com/sdk/images/)与[邮箱哈希](https://docs.gravatar.com/rest/hash/)：SHA-256、d=404、尺寸和评级。
- [ICO 解码包](https://github.com/fyne-io/image)：仅使用 ico 包，依赖许可证和实际解码边界在任务 3 核对。
