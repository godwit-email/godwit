---
status: proposed
---

# 邮件引擎在独立的后端进程里，界面是它的客户端

今天 `core.Service` 由 GUI 创建并拥有（`cmd/godwit/gui.go:255`），关窗后靠 Keep in background 把整个应用——含界面与 WebView——留在后台继续收信；UI 崩溃、WebView 异常、升级重启都会打断收信。另一个拥有者 `godwit --headless` 没有桌面通知，且与 GUI 互斥（`ownerlock`）。#652 要的是「一个长期在线的后端」成为产品形态。

进程边界就此定死：**后端进程是数据目录唯一的拥有者**——账户与凭据（系统 keychain）、同步 worker、规则与标签、通知与托盘都归它；**界面进程不再创建 `core.Service`**，它是本机 API 的客户端。两者不只是同一个安装包，而且是**同一个可执行文件**（macOS `Godwit.app/Contents/MacOS/Godwit`、Windows `Godwit.exe`、Linux `godwit`），后端以 `--headless` 参数运行。

## 接缝

- 界面用本机 API 新增的 **`ui` 角色**：`full` 的全部方法，加上只给界面的方法（全部钉在 `rpcbind/testdata/ui_methods.txt`）。其中一部分是今天在 `hidden` 里、只有界面用得着的几条——窗口收窄开关（`SetInboxOnly`/`InboxOnly`）、阅读器的两步取图（`GetMessageLazyImages` 与流式的 `InlineImagesStream`/`FetchAttachmentsStream`）、头像（`SenderAvatar`/`ModelServiceAvatar`/`PluginReadme`）、带进度回调的三条（同样以 `*Stream` 形式）。`InlineImages`、`FetchAttachments` 与带回调的进程内形式，以及 `Close`、`Redact`、`AuditExternal`、`Proxy`、`FollowConversation` 继续不上 wire（后者客户端有 `SubscribeConversation`）。另一部分是这次新加的：界面状态上报（`SetUIForeground`、`SetUIReady`、`SetComposeOpen`）、生命周期（`StopUIBackend`、`ReplaceUIBackend`、`TrayShown`）、放不进一条响应的结果改成数据目录里的 0600 文件交接（`RawMessageFile`、`ExportAccountsFile`），以及图片缓存、最近日志、Store 版本检查等界面专用的几条。事件走 `Subscribe` 这条流——它已经在 wire 上；读得慢的界面丢最旧的变化通知，不丢点击与托盘请求。
- **本机 API 从此常开**，因为界面就要用它；「智能 › 接入 AI 应用」开关的含义收窄成「外部客户端（`full`/`mcp`）能不能连」。信任边界不变：同一个系统用户（[0002](0002-single-owner-local-api.md)）。这个开关只管外部角色；同一用户下的程序也能出示 `ui-token`（同用户信任边界，[0002](0002-single-owner-local-api.md)），所以 `ui` 角色的发信、转发规则、授权与放宽通过 MCP 发送同样记审计（来源 `ui`），只是不按外部来源拦截。
- **附件与图片不过 wire**：同机，界面直接读数据目录里的文件路径。
- 界面把「我在前台」报给后端（一条 `ui` 角色方法）；后端据此抑制通知、并把后台轮次收窄到收件箱——今天这个判断在 `app.App.foreground` 里。

## 生命周期

- 界面启动时后端不在，就拉起它并等 `godwit.sock`。
- **关窗先隐藏，隐藏满 5 分钟后界面进程退出**，后端与托盘留下继续收信（见下文「关窗的取舍」）。5 分钟内重开就是把隐藏的窗口拿出来；之后重开是一次界面冷启动。计时到点时撰写窗口还开着，就等最后一个关掉后界面才退出。时长只有 `cmd/godwit/platform.go` 的 `hiddenExitAfter` 一处。
- **界面没退出就不见了**（崩溃、被杀），它拉起的后端照常留下：「关窗后继续、退出即停止」与「一直继续」要的就是界面出事不打断收信。用户从托盘图标发现它，用托盘的「退出」或「退出并停止收信」停掉；托盘图标关了的，重新打开 Godwit 会连上同一个后端，再 ⌘Q（「一直继续」下 ⌘Q 不停后端，先在设置里打开托盘图标）。**开发会话例外**：`mygo dev` 以 `MYGO_ENV=development` 运行界面，它拉起的后端继承这个变量（`core.Options.StopWithUI`），最后一个界面连接一断——退出、被 `mygo dev` 杀掉、热重载换下、隐藏满 5 分钟退出——就停，不管「后台收信」怎么选；否则它会在 `mygo dev` 结束后一直占着 `Godwit-dev` 与本机 API socket，之后任何一次开发构建（版本号相同）的界面都会连到它的旧代码上。要在开发时看后端常驻的行为，用 `mise run build` 的构建。
- 后端要界面显示什么（托盘的打开、写邮件，点通知）时：有 `ui` 连接就发事件（`OpenRequested`、`ComposeRequested`、`NotificationClicked`）；没有就以 `--data-dir <目录> --open window|compose|<通知 id>` 拉起界面，已在运行的界面经单实例锁收到同样的参数。
- 应用图标上的未读数由界面设置，随界面进程退出而消失（macOS 的 Dock 角标、Windows 的任务栏标记都属于界面进程）；界面不在时只有后端托盘上的未读数（开了「在菜单栏显示未读数」时）。
- **退出默认连后端一起退出**；设置里的「后台收信」三态取代 `KeepInBackground`：关窗后停止 / 关窗后继续、退出即停止（默认）/ 一直继续。选「一直继续」时由系统的登录项拉起（macOS 的 `SMAppService.mainAppService`、Windows 非商店包的注册表 `Run` 或 MSIX 的 `uap5:StartupTask`），托盘菜单里另设「退出并停止收信」。
- 托盘图标与菜单归后端：打开窗口、立即同步、退出并停止收信（及现有的写邮件、最近登录验证码）。
- **单实例锁只给界面**：`mygo.App.RequestSingleInstanceLock()` 按 `bundleIdentifier + 应用名` 建锁，只由界面调用（二次启动把参数转交给已在运行的界面并聚焦窗口）；后端进程不调它，只靠数据目录上的 `ownerlock` 保证单一拥有者，两者互不抢锁。
- **通知由后端发**，身份就是 Godwit（例外：界面在时，MCP 发送请求的通知由界面发，点它直接打开确认卡片）：
  - macOS 上后端就是 `Godwit.app/Contents/MacOS/Godwit --headless`，`NSBundle.mainBundle` 仍是 `Godwit.app`（`email.godwit.app`），通知授权弹窗与系统设置条目只出现一次，签名构建下共享嵌入描述文件与 `INSendMessageIntent` 通信通知权限（可带发件人头像）。原先以为点通知一定由系统唤起界面，实际后端跑着 AppKit 循环并挂了 `UNUserNotificationCenterDelegate` 时，点击回调既可能落到后端也可能落到界面（界面未运行时直接回调后端，系统不会另起一个不带 `--headless` 的进程）——改为**后端和界面都处理 `OnNotificationClick` 并按通知 `id` 去重**：后端收到时，若有 `ui` 连接就发事件让界面聚焦定位，没有就拉起界面进程打开目标项。
  - Windows 上原先以为要靠 MSIX 包内第二个进程借主应用的 AUMID 发 WinRT Toast；核对 `mygo` 源码后确认它实际走 `Shell_NotifyIconW` 托盘气泡（Windows 10/11 渲染为横幅），直接挂在后端自己的托盘图标上，点击产生 `ninBalloonUserClick` 回到后端进程——根本没有跨可执行文件的 AUMID 问题，后端收到点击后同样通知或拉起界面。
- **界面不在时的确认**：MCP 发送留在待确认里（`mcp_sends` 那套已经绑 revision、有 TTL、有上限），并发一条通知；点通知唤起界面并跳到那张卡片。Wit 的运行与插件的待批动作不排队——没有界面就不该开始。
- **升级**：界面连上先对版本，不一致就让它拉起的后端干净退出（走既有的关闭路径，发件箱与「最多发一次」的语义不变）再重新拉起，「一直继续」下也是；用户自己用 `--headless` 起的后端不替换，界面告诉用户先停掉它。不做跨版本兼容。

## 平台与真机核实（#719）

macOS 与 Windows 做全套（托盘、原生通知、登录启动），Linux 只保证后端进程能常驻并发通知，登录启动留给用户自己写 systemd user unit。

- **隐藏 Dock 与任务栏**：macOS 上后端在 `mygo.App.Run()` 前设 `mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)`（`NSApplicationActivationPolicyAccessory`），保留完整 AppKit 主循环（通知中心、托盘、系统翻译、隐藏窗口 `PrintPDF`），但不进 Dock 和应用切换器，界面保持默认的 `Regular` 照常出现；无图形会话（SSH、Linux 无显示服务、进程内测试）时跳过 `mygo.App.Run()` 直接提供本机 API。Windows 上后端不建可见窗口（`mygo` 只有 0×0 隐藏消息窗口，也不启动 WebView2），天然无任务栏按钮，只有通知区域的托盘图标。
- **登录启动与系统设置联动**：
  - macOS：不用单独的 LaunchAgent plist，直接经 `mygo.App.SetOpenAtLogin` 注册 `SMAppService.mainAppService`（无需额外描述文件；用户在「系统设置 › 通用 › 登录项」关掉后状态非 `smEnabled`，`OpenAtLogin()` 即返回 `false`）。系统在登录时拉起应用会带 `keyAELaunchedAsLogInItem`，`mygo.App.WasOpenedAtLogin()` 为 `true`：此时读设置，开了 `LaunchAtLogin` 就进界面（确保唯一后端运行），只选了后台收信「一直继续」就直接以无窗口后端运行。
  - Windows：非商店安装（`packaged() == false`）由 `mygo.App.SetOpenAtLogin` 写 `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`（带 `--mygo-opened-at-login`，并读取任务管理器写入的 `StartupApproved\Run` 禁用位）；Microsoft Store 的 MSIX 包（`packaged() == true`）在 `msix.yml` 生成的 `AppxManifest.xml` 里对同一个 `Godwit.exe` 声明 `uap5:StartupTask`（`Windows.FullTrustApplication`），由 WinRT `StartupTask` 启停并读取 `DisabledByUser` 状态。
- **已核实与待签包真机验收的边界**：
  - 已在本机 macOS arm64 构建产物 `dist/darwin-arm64/Godwit.app`（ad-hoc 签名）、`mygo@v0.8.3` 源码与 `msix.yml` 上核实：`Info.plist`（`email.godwit.app`、`CFBundleExecutable=Godwit`、`NSUserActivityTypes=[INSendMessageIntent]`）；ad-hoc 下仅带 `disable-library-validation` 时 `SecTaskCopyValueForEntitlement` 返回 `false` 自动回退普通通知；`Godwit.app/Contents/MacOS/Godwit --headless` 正常监听 `godwit.sock` 响应 `call`、不出现在 `lsappinfo`/Dock、收 `SIGINT` 零退出码清理 socket 与 token。
  - 依赖正式签名环境或 Windows 实机、归入发版前人工核对清单（#737）的项：Developer ID 签名加公证并嵌入 `embedded.provisionprofile` 后的发件人头像通知与 `SMAppService` 首次批准体验，以及 Windows MSIX 实机上的 `uap5:StartupTask` 启停与卸载无残留。

## 关窗的取舍（#738）

[`docs/process-benchmark.md`](../process-benchmark.md) 拆分前后同机实测（中位数）：关窗即退出界面后，后台进程树的 RSS 从约 440–456 MiB 降到 132–157 MiB（界面与 WebView 的子进程约 300 MiB 被释放），30 秒内的上下文切换降九成以上；代价是重开窗口从约 30 ms 的「显示隐藏窗口」变成 330–500 ms 的界面冷启动。

结论：**关窗先隐藏，隐藏满 5 分钟后界面进程退出**。刚关就重开的常见情形保持瞬开；长时间放在后台才换回那约 300 MiB。隐藏期间界面照样报「不在前台」，后端照常发通知；后台收信为「关窗后停止」与 ⌘Q 不变。

## Considered Options

- **交接式**（界面仍内嵌引擎，退出时把数据目录锁交给后端进程）：界面代码几乎不动，但每次开关窗口都要重开数据库、重连所有账户（IDLE、认证、限流），而且这层让出协议在做完真客户端之后要删掉。
- **包内第二个可执行文件**（`Contents/MacOS/godwit-backend` 或第二个 `.exe`）：与同一个二进制加 `--headless` 相比，每个架构白白多打一份约 53 MB 的可执行文件，还要额外维护嵌套签名、权限与第二个启动入口，而通知归属与 Windows 托盘气泡并不需要拆成两个文件。
- **独立 helper `.app`**（自己的 bundle id）：职责更清楚、能单独更新，但用户会看到第二次通知授权弹窗和第二个设置条目，发件人头像那种通信通知也用不上。
- **远端后端**（跨机器）：要网络传输、认证与新的信任边界，与 0002 冲突。本次不做，只在「连接怎么建立」一处留缝，不预造抽象。
- **界面只读共享 SQLite**：违反单一写入者，而界面本来就要写（标记、草稿、设置、操作队列）。
- **只把收信挪出去、界面仍独占引擎**：等于把 `core.Service` 劈成两半，每条读取路径都要判一次「这个数据在哪边」。

## Consequences

- `app/` 里约 233 处对 `core.Service` 的调用改走客户端。[`contract.Remote`](../../contract/api.go) 已经是一个「实现 `core.Service` 子集、走 socket 的类型化客户端」，`contract_test.go` 用它把每个场景跑两遍——界面客户端照它的骨架生成。
- `hidden` 里那几条从「不上 wire」改成「只给 `ui` 角色」；`rpcbind/testdata/full_methods.txt`、`hidden` 的注释与 `TestMCPAllowlistContents` 一起更新（AGENTS.md：一个名字就是承诺）。
- 通知的展示逻辑（合并 1 秒、跳过被拦截的发件人、前台不提醒）从 `app/notify.go` 搬到后端；`cmd/godwit/notify_*.go` 的平台细节留在原地。
- 后端传与界面相同的 `core.Options`（`AutoSync`、`LabelMail`、`SecurityMail` 都开，和今天的 `gui.go:255` 一样）；跑不跑由用户设置决定（labeler 每批都读 `Settings.AutoLabel`），不新增「常驻时跑什么」的开关——「界面不在」不该改变用户已经做过的选择。
- 事件总线是 lossy 的（满了丢最旧的，但不丢点击、托盘的打开/写邮件/退出这类请求）：界面与后端的连接断了不重连，界面提示后退出，下次启动时全量拉一次状态，不靠事件补齐。单条结果或流事件超过发送队列一半时只让这一次调用失败（`message_too_large`），不断开连接。
- 测试：`app/` 的测试改在 socket 上跑（`contract` 已有先例）；`coretest` 加「后端 + 界面」两级；`--headless` 的含义变成「后端进程本身」，不再是「另一个没有通知的拥有者」。
- 先量数字再动结构（#652 的第 2 步）：一轮同步的耗时、下行字节、分配，IDLE → 通知的延迟，常驻态的内存与唤醒次数，界面冷启动耗时，都要能在本机重复跑出来。
- 要记住的已知弱点：macOS 上 `go-keyring` 走 `/usr/bin/security`，keychain 项的 ACL 信任的是 `/usr/bin/security` 而不是 Godwit 的签名——同一用户下的任何程序都能无提示读到凭据。后端不是新增的风险面（它本来就在同一用户下跑），但这条要写进评审。
