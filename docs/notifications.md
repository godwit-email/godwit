# 发件人头像通知

目标是由系统显示发件人大头像，并保留 Godwit 的小应用图标。通知统一使用
MyGo API；macOS 使用通信通知，Windows、Linux 继续使用已有通知。

## 当前行为

- macOS 12+、应用签名具有通信通知权限、每封邮件开启通知预览时，尝试头像通知。
- 复用现有发件人头像服务及图片权限，获取头像最多等待 750ms。
- 头像原样传给 MyGo；MyGo 在 macOS 上将 SVG 等格式转成 PNG 后再提交给系统。
- 没有权限、头像不可用或通信通知准备失败时，回退普通通知。
- 保留主线每封邮件单独通知的行为；关闭预览时，不提供发件人资料。
- 复用原来的通知 ID、点击处理和通知清理。

**普通本地构建不启用头像通知；发布和手动签名验证构建启用。** 只有显式提供 `MACOS_PROVISIONING_PROFILE`
和 `MACOS_ENTITLEMENTS` 时，构建才会嵌入 profile 并声明受限权限，避免缺少匹配
profile 的构建被 macOS 终止。运行时先检查自身签名的权限，再读取头像；MyGo
也会在向 SiriKit 提交邮件内容前检查权限。
只提供 profile 或 entitlements 其中一项时，配置会在构建前报错。

## MyGo 上游进度

- [#408：签名前嵌入 provisioning profile](https://github.com/egoist/mygo/pull/408)，已合入。
- [#409：macOS 发件人头像通知](https://github.com/egoist/mygo/pull/409)，已合入。
- [#421：提交前将头像转成位图](https://github.com/egoist/mygo/pull/421)，已合入。

Godwit 使用包含以上修复的正式版本 MyGo v0.6.0，页面侧 `mygo-runtime` 同为 0.6.0。
头像通过 `NotificationOptions.Sender` 传给 MyGo；SVG 转换、原生通知发送、donation、
回退和生命周期均由 MyGo 处理。Godwit 只负责取头像前的权限检查。

## 签名和发布配置

1. 在 Apple Developer 的 Certificates, Identifiers & Profiles 中，为 macOS
   App ID `email.godwit.app` 启用 **Communication Notifications**。
2. 创建或重新生成该 App ID 的 **Developer ID provisioning profile**，确认其中
   授予 `com.apple.developer.usernotifications.communication`，并匹配发布证书。
3. 按 profile 的值准备包含应用、团队标识及通信权限的完整 entitlements 文件；
   `assets/notifications.entitlements.plist` 已配置 Godwit 的应用、团队和通信权限，
   并保留主线加载原生插件需要的 library-validation 设置。
   设置 `MACOS_PROVISIONING_PROFILE` 为 profile 路径、`MACOS_ENTITLEMENTS` 为完整
   entitlements 文件路径，并设置匹配的 `MACOS_SIGNING_IDENTITY`。MyGo 在签名前将
   profile 嵌入 `Godwit.app/Contents/embedded.provisionprofile`。
4. 用 `go tool mygo build` 生成签名应用、DMG、更新归档及公证产物。开发时清除上述
   profile 和 entitlements 环境变量：`mygo dev` 默认使用 ad-hoc 签名，且开发 bundle
   identifier 为 `email.godwit.app.dev`，不匹配正式应用的 profile。
5. 用这个签名构建实测启动、来信通知的头像和小图标、点击定位、关闭预览和
   多封邮件分别通知。验收前退出同一应用标识的旧版或其它测试副本，并通过
   正常安装/启动登记新 bundle；旧副本的注册信息可能导致系统仍按普通通知显示。
   实际验收结果见下方交付记录。

参考：[Apple 通信通知实现](https://developer.apple.com/documentation/usernotifications/implementing-communication-notifications)、
[macOS 支持的 capabilities](https://developer.apple.com/help/account/reference/supported-capabilities-macos/)、
[TN3125：provisioning profile 与受限权限](https://developer.apple.com/documentation/technotes/tn3125-inside-code-signing-provisioning-profiles)。

## CI

`MACOS_PROVISIONING_PROFILE` 仓库 secret 保存描述文件的 base64 内容。发布流程与
CI 的手动 `signed_macos` 构建复用 `.github/actions/macos-sign`，在构建前校验
profile 的平台、有效期、应用/团队标识、通信权限及实际签名证书。缺失或不匹配
时构建失败，避免发布时静默关闭头像功能。普通 PR 构建不接触签名私钥。
校验工具为 `scripts/macos_profile.swift`，使用 Foundation 和 CryptoKit，
在签名 Action 中编译执行；校验测试仅在 macOS CI 上运行，无需 Python。

在 CI 工作流手动勾选 `signed_macos`，会构建、公证 arm64 和 amd64 的 DMG 并保存
为 Actions artifact；这个验证入口不上传更新服务器，也不发布 GitHub Release。

## 2026-10-16 交付验收

以下视觉验收和安装包使用当时的 MyGo fork 转换实现。之后改为 Godwit 本地转换，
现已改由 MyGo v0.6.0 转换；尚未对这个实现重新进行系统通知视觉验收。

- 签名原生测试应用实测 PNG、SVG 均显示发件人大头像和 Godwit 小角标。
- 无效图片、关闭预览均显示普通 Godwit 通知；点击 SVG 测试卡片收到对应通知 ID。
- [手动签名构建](https://github.com/godwit-email/godwit/actions/runs/38887761310)
  已生成 arm64、amd64 公证 DMG，未发布版本或上传更新服务器。
- 下载后两份 DMG 均通过 `stapler validate`；两份包内应用均通过严格签名验证及
  Gatekeeper 检查，嵌入 profile 的应用、团队、权限和签名证书匹配。
- arm64 公证产物已安装并正常启动，原生插件进程正常；验收用的同标识测试副本已退出。

通知视觉和系统点击回调使用虚构内容的原生测试应用验收，未发送真实测试邮件。
正式应用每封邮件的通知数据及定位逻辑通过现有应用测试验证；尚未实测真实新邮件的
点击定位，也未在 Windows、Linux 桌面或 Intel Mac 上做视觉验收。
