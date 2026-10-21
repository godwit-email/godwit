# 网页按入口划分信任级别

每个 HTML 入口（`compose.html`、`wit.html`、`reader.html` 等）有自己的信任级别和 CSP，全部不允许远程请求。所有可信页面都能看到 mygo 绑定的服务，所以权限在 Go 端按调用者判定：撰写窗口的方法只作用于该窗口绑定的草稿；嵌入页面先向 Go 登记角色（`wit`、`reader`）和上下文，每个方法先核对角色，不符就拒绝。

## Consequences

- **邮件 HTML 永远不进可信页面的文档**：阅读区正文只放进 `sandbox="allow-same-origin"`、不允许脚本的 iframe，iframe 永远不能调用 Go。远程图片由页面逐张请求 Go 代为下载并返回 `data:` URL，不注册自定义协议（mygo 把来自自定义协议的页面视为可信）。`mail.Sanitize` 继续在 Go 端执行，iframe 是第二道防线。
- 撰写窗口里的正文和粘贴内容只经过反序列化变成 Plate 的数据结构，由 React 渲染，不使用 `dangerouslySetInnerHTML`。
- **模型输出按不可信内容渲染**：链接只允许 http、https、mailto，点击一律经 Go 确认后用系统浏览器打开；不显示图片；不允许原始 HTML。
- 页面内导航一律拦截，`window.open` 拒绝。
