# macOS 签名与通知权限

发布流程和 CI 的手动 `signed_macos` 构建使用此 Action 准备签名证书、公证凭据及
通信通知 provisioning profile。构建配置见根目录的
[`mygo.config.ts`](../../../mygo.config.ts)。

普通本地构建使用 ad-hoc 签名，不启用发件人头像通知。签名构建需要同时提供
`MACOS_PROVISIONING_PROFILE` 和 `MACOS_ENTITLEMENTS`；缺少匹配的 profile 却声明
受限权限，可能导致应用被 macOS 终止。只提供其中一项时，构建配置会报错。
运行时没有通信通知权限时回退普通通知。

## 签名和发布配置

1. 在 Apple Developer 的 Certificates, Identifiers & Profiles 中，为 macOS
   App ID `email.godwit.app` 启用 **Communication Notifications**。
2. 创建或重新生成该 App ID 的 **Developer ID provisioning profile**，确认其中
   授予 `com.apple.developer.usernotifications.communication`，并匹配发布证书。
3. 按 profile 的值准备包含应用、团队标识及通信权限的完整 entitlements 文件。
   [`assets/notifications.entitlements.plist`](../../../assets/notifications.entitlements.plist)
   已配置 Godwit 的应用、团队和通信权限，并保留加载原生插件需要的
   library-validation 设置（见 [ADR 0017](../../../docs/adr/0017-macos-library-validation.md)）。
4. 本地签名构建时，设置 `MACOS_PROVISIONING_PROFILE` 为 profile 路径、
   `MACOS_ENTITLEMENTS` 为完整 entitlements 文件路径，并设置匹配的
   `MACOS_SIGNING_IDENTITY`。MyGo 在签名前将 profile 嵌入
   `Godwit.app/Contents/embedded.provisionprofile`。需要公证时，另将
   `NOTARY_KEYCHAIN` 指向保存了 `notary` 公证凭据的 keychain。
5. 用 `go tool mygo build` 生成签名应用和安装产物；配置公证凭据后同时进行公证。
   开发时清除 profile 和 entitlements 环境变量：`mygo dev` 默认使用 ad-hoc 签名，
   且开发 bundle identifier 为 `email.godwit.app.dev`，不匹配正式应用的 profile。

参考：[Apple 通信通知实现](https://developer.apple.com/documentation/usernotifications/implementing-communication-notifications)、
[macOS 支持的 capabilities](https://developer.apple.com/help/account/reference/supported-capabilities-macos/)、
[TN3125：provisioning profile 与受限权限](https://developer.apple.com/documentation/technotes/tn3125-inside-code-signing-provisioning-profiles)。

## CI 配置与手动验证入口

`MACOS_PROVISIONING_PROFILE` 仓库 secret 保存描述文件的 base64 内容，调用 Action 时
通过 `MACOS_PROFILE_BASE64` 环境变量传入。其余凭据通过 `MACOS_CERTIFICATE`、
`MACOS_CERTIFICATE_PASSWORD`、`APPLE_API_KEY`、`APPLE_API_KEY_ID` 和
`APPLE_API_ISSUER` 传入；证书和 API 私钥同样使用 base64 内容。

Action 在构建前校验 profile 的平台、有效期、应用/团队标识、通信权限及实际
签名证书。缺失或不匹配时构建失败，避免发布时静默关闭头像功能。校验工具为
[`scripts/macos_profile.swift`](../../../scripts/macos_profile.swift)，在 Action 中编译
执行；校验测试在 macOS CI 上运行。普通 PR 构建不接触签名私钥。

在 [CI 工作流](../../workflows/ci.yml)手动勾选 `signed_macos`，会构建、公证 arm64
和 amd64 的 DMG 并保存为 Actions artifact；这个验证入口不上传更新服务器，也不
发布 GitHub Release。[发布工作流](../../workflows/release.yml)复用此 Action。

## 验证步骤与待验收项

- 检查 DMG 的 `stapler validate`、包内应用的严格签名验证和 Gatekeeper 检查，
  确认嵌入 profile 的应用、团队、权限和签名证书匹配。
- 正常安装并启动签名构建，检查原生插件加载。通知验收前退出同一应用标识的旧版
  或其它测试副本；旧副本的注册信息可能导致系统仍按普通通知显示。
- 检查 PNG、SVG 发件人头像和 Godwit 小图标、无效图片时的普通通知回退、关闭
  预览、多封邮件分别通知，以及点击通知后定位到对应邮件。

当前 MyGo v0.6.0 的头像转换实现尚未重新进行系统通知视觉验收。此前的视觉和
系统点击回调验收使用的是旧实现的原生测试应用，不能作为当前实现的验收结果。
尚未实测正式应用真实新邮件的点击定位，也未在 Windows、Linux 桌面或 Intel Mac
上做视觉验收。
