# 发件人头像通知

目标是由系统显示发件人大头像，并保留 Godwit 的小应用图标。通知统一使用
MyGo API；macOS 使用通信通知，Windows、Linux 继续使用已有通知。

## 当前行为

- macOS 12+、应用签名具有通信通知权限、每封邮件开启通知预览时，尝试头像通知。
- 复用现有发件人头像服务及图片权限，获取头像最多等待 750ms。
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

Godwit 已固定到合入提交 `dc7d9667bb34` 的 Go pseudo-version
`v0.5.6-0.20261016073705-dc7d9667bb34`，无需等待正式发版，也不使用本地 replace。
页面侧 `mygo-runtime` 对齐到该提交对应的已发布版本 0.5.5。
SVG 栅格化修复见 [#421](https://github.com/egoist/mygo/pull/421)。合入前通过
`go.mod` 的远程 replace 固定到 fork 提交 `768644e06d3f`，CI 可独立下载；
合入后切回上游版本。
头像通过 `NotificationOptions.Sender` 传给 MyGo；原生通知发送、donation、回退和
生命周期均由 MyGo 处理。Godwit 只保留取头像前的权限检查。正式版本发布后可再升级。

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
   多封邮件分别通知。签名、原生显示与公证验收结果见本任务的交付记录。

参考：[Apple 通信通知实现](https://developer.apple.com/documentation/usernotifications/implementing-communication-notifications)、
[macOS 支持的 capabilities](https://developer.apple.com/help/account/reference/supported-capabilities-macos/)、
[TN3125：provisioning profile 与受限权限](https://developer.apple.com/documentation/technotes/tn3125-inside-code-signing-provisioning-profiles)。

## CI

`MACOS_PROVISIONING_PROFILE` 仓库 secret 保存描述文件的 base64 内容。发布流程与
CI 的手动 `signed_macos` 构建复用 `.github/actions/macos-sign`，在构建前校验
profile 的平台、有效期、应用/团队标识、通信权限及实际签名证书。缺失或不匹配
时构建失败，避免发布时静默关闭头像功能。普通 PR 构建不接触签名私钥。

在 CI 工作流手动勾选 `signed_macos`，会构建、公证 arm64 和 amd64 的 DMG 并保存
为 Actions artifact；这个验证入口不上传更新服务器，也不发布 GitHub Release。
