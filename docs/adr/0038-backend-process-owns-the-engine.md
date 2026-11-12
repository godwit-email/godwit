---
status: proposed
---

# 邮件引擎在独立的后端进程里，界面是它的客户端

今天 `core.Service` 由 GUI 创建并拥有（`cmd/godwit/gui.go:255`），关窗后靠 Keep in background 把整个应用——含界面与 WebView——留在后台继续收信；UI 崩溃、WebView 异常、升级重启都会打断收信。另一个拥有者 `godwit --headless` 没有桌面通知，且与 GUI 互斥（`ownerlock`）。#652 要的是「一个长期在线的后端」成为产品形态。

进程边界就此定死：**后端进程是数据目录唯一的拥有者**——账户与凭据（系统 keychain）、同步 worker、规则与标签、通知与托盘都归它；**界面进程不再创建 `core.Service`**，它是本机 API 的客户端。两者同属一个安装包、一个 bundle。

## 接缝

- 界面用本机 API 新增的 **`ui` 角色**：`full` 的全部方法，加上今天在 `hidden` 里、只有界面用得着的几条——窗口收窄开关（`SetInboxOnly`/`InboxOnly`）、阅读器的两步取图（`GetMessageLazyImages`/`InlineImages`/`FetchAttachments`）、头像（`SenderAvatar`/`ModelServiceAvatar`/`PluginReadme`）、带进度回调的三条。`Close`、`Redact`、`AuditExternal`、`Proxy`、`FollowConversation` 继续不上 wire（后者客户端有 `SubscribeConversation`）。事件走 `Subscribe` 这条流——它已经在 wire 上，带背压。
- **本机 API 从此常开**，因为界面就要用它；「智能 › 接入 AI 应用」开关的含义收窄成「外部客户端（`full`/`mcp`）能不能连」。信任边界不变：同一个系统用户（[0002](0002-single-owner-local-api.md)）。
- **附件与图片不过 wire**：同机，界面直接读数据目录里的文件路径。
- 界面把「我在前台」报给后端（一条 `ui` 角色方法）；后端据此抑制通知、并把后台轮次收窄到收件箱——今天这个判断在 `app.App.foreground` 里。

## 生命周期

- 界面启动时后端不在，就拉起它并等 `godwit.sock`。
- **关窗即释放界面进程**，后端与托盘留下继续收信；重开窗口是一次界面冷启动（耗时先量出来）。
- **退出默认连后端一起退出**；设置里的「后台收信」三态取代 `KeepInBackground`：关窗后停止 / 关窗后继续、退出即停止（默认）/ 一直继续。选「一直继续」时由系统的登录项拉起后端（macOS 的 `SMAppService`、Windows 的 MSIX `startupTask`），托盘菜单里另设「退出并停止收信」。
- 托盘图标与菜单归后端：打开窗口、立即同步、退出并停止收信。
- **通知由后端发**，身份就是 Godwit：macOS 上后端必须是 `Godwit.app` 内的可执行文件，main bundle 才是 Godwit.app，于是通知的授权弹窗与设置条目只出现一次，点通知由系统唤起界面——正好是「点通知打开 app」。Windows 上后端在同一个 MSIX 包里，用主 app 的 AUMID 发通知（同一 package 允许），自己的 `startupTask` 做登录启动。
- **界面不在时的确认**：MCP 发送留在待确认里（`mcp_sends` 那套已经绑 revision、有 TTL、有上限），并发一条通知；点通知唤起界面并跳到那张卡片。Wit 的运行与插件的待批动作不排队——没有界面就不该开始。
- **升级**：界面连上先对版本，不一致就让后端干净退出（走既有的关闭路径，发件箱与「最多发一次」的语义不变）再重新拉起；不做跨版本兼容。

## 平台

macOS 与 Windows 做全套（托盘、原生通知、登录启动），Linux 只保证后端进程能常驻并发通知，登录启动留给用户自己写 systemd user unit。

## Considered Options

- **交接式**（界面仍内嵌引擎，退出时把数据目录锁交给后端进程）：界面代码几乎不动，但每次开关窗口都要重开数据库、重连所有账户（IDLE、认证、限流），而且这层让出协议在做完真客户端之后要删掉。
- **独立 helper `.app`**（自己的 bundle id）：职责更清楚、能单独更新，但用户会看到第二次通知授权弹窗和第二个设置条目，发件人头像那种通信通知也用不上。
- **远端后端**（跨机器）：要网络传输、认证与新的信任边界，与 0002 冲突。本次不做，只在「连接怎么建立」一处留缝，不预造抽象。
- **界面只读共享 SQLite**：违反单一写入者，而界面本来就要写（标记、草稿、设置、操作队列）。
- **只把收信挪出去、界面仍独占引擎**：等于把 `core.Service` 劈成两半，每条读取路径都要判一次「这个数据在哪边」。

## Consequences

- `app/` 里约 233 处对 `core.Service` 的调用改走客户端。[`contract.Remote`](../../contract/api.go) 已经是一个「实现 `core.Service` 子集、走 socket 的类型化客户端」，`contract_test.go` 用它把每个场景跑两遍——界面客户端照它的骨架生成。
- `hidden` 里那几条从「不上 wire」改成「只给 `ui` 角色」；`rpcbind/testdata/full_methods.txt`、`hidden` 的注释与 `TestMCPAllowlistContents` 一起更新（AGENTS.md：一个名字就是承诺）。
- 通知的展示逻辑（合并 1 秒、跳过被拦截的发件人、前台不提醒）从 `app/notify.go` 搬到后端；`cmd/godwit/notify_*.go` 的平台细节留在原地。
- 后端传与界面相同的 `core.Options`（`AutoSync`、`LabelMail`、`SecurityMail` 都开，和今天的 `gui.go:255` 一样）；跑不跑由用户设置决定（labeler 每批都读 `Settings.AutoLabel`），不新增「常驻时跑什么」的开关——「界面不在」不该改变用户已经做过的选择。
- 事件总线是 lossy 的（满了丢最旧的）：界面重连后重新全量拉一次状态，不靠事件补齐——它启动时本来就是全量拉。
- 测试：`app/` 的测试改在 socket 上跑（`contract` 已有先例）；`coretest` 加「后端 + 界面」两级；`--headless` 的含义变成「后端进程本身」，不再是「另一个没有通知的拥有者」。
- 先量数字再动结构（#652 的第 2 步）：一轮同步的耗时、下行字节、分配，IDLE → 通知的延迟，常驻态的内存与唤醒次数，界面冷启动耗时，都要能在本机重复跑出来。
- 要记住的已知弱点：macOS 上 `go-keyring` 走 `/usr/bin/security`，keychain 项的 ACL 信任的是 `/usr/bin/security` 而不是 Godwit 的签名——同一用户下的任何程序都能无提示读到凭据。后端不是新增的风险面（它本来就在同一用户下跑），但这条要写进评审。
