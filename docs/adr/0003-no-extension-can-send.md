# 任何扩展都不能发信

Godwit 不向插件、MCP 和 Wit 提供发信能力：插件的 `mail` 权限只有 `read` 和 `write`，没有 `send`；MCP 没有 `Send`，也不能删除、移动、改设置或处理审批；Wit 只能创建或修改草稿。发送永远由用户在撰写窗口亲手点击（或按 ⌘Enter）触发。这样即使模型被邮件里的 prompt 注入操控，最坏也只能留下一封草稿。

MCP 部分已由 [0023](0023-mcp-send-setting.md) 修订：`godwit mcp` 可以请求发送，默认每次由用户在 Godwit 里确认，用户也可以改成不允许或直接发送。插件和 Wit 不变。

另一个例外是本机 API 的 `full` 角色（见 [0002](0002-single-owner-local-api.md)）：它代表用户自己的程序，可以发送，所有这类发送都记审计。

`message.beforeSend` Hook 可以返回 `warn`（用户可以「仍然发送」）或 `block`（只能修改草稿或停用该插件），但不能替用户发送。
