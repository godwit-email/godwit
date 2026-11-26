# 网页按入口划分信任级别

每个 HTML 入口（`compose.html`、`wit.html`、`reader.html` 等）有自己的信任级别和 CSP，全部不允许远程请求。所有可信页面都能看到 mygo 绑定的服务，所以权限在 Go 端按调用者判定：撰写窗口的方法只作用于该窗口绑定的草稿；嵌入页面先向 Go 登记角色（`wit`、`reader`）和上下文，每个方法先核对角色，不符就拒绝。

## Consequences

- **邮件 HTML 永远不进可信页面的文档**：阅读区正文只放进 `sandbox="allow-same-origin allow-scripts"` 的 iframe；`allow-scripts` 是 WebKit 让可信父页面安装的事件监听器生效所需，邮件脚本仍由 Go 清洗和正文首位 CSP 的 `default-src 'none'` 禁止。iframe 永远不能调用 Go。暗色配色也由可信父页面读取正文计算样式并添加临时颜色覆盖，不向邮件注入脚本，不修改存储原文。远程图片在交给页面的正文里改写成 `godwit-img://m<邮件 ID>/<token>`，由 Go 注册的 `godwit-img` 协议代为提供（`webui.Reader.ImageHandler`）：只有页面正在显示、且用户放行了图片的那封邮件的 token 才会被解析成原地址，经 core 的客户端下载（只连公网地址，不带 Cookie 和 Referer）并缓存，另有像素预算；其余一律 404。页面本身不调用任何取图方法，iframe 的 CSP 只放行 `data:` 和 `godwit-img:`；WebView2（Windows）不请求页面里写的自定义协议，MyGo 在回环地址 `http://<host>.godwit-img.localhost` 上提供它，所以 Windows 上图片地址用这个形式，CSP 也只多放行 `http://*.godwit-img.localhost`。这个协议只提供图片，不承载页面，所以 mygo 把来自自定义协议的页面视为可信这一点不适用。`mail.Sanitize` 继续在 Go 端执行，iframe 是第二道防线。
- **阅读器复用 WebView，不复用邮件授权**：新正文在旧正文后面排版，报告尺寸并经过绘制后才显示。旧正文随即解绑；最多保留一个不绑定邮件的备用 WebView。下次使用同一 WebView 会获得新的登记 ID，即使再次打开的是同一封邮件。旧登记的回调和图片地址不能作用于新邮件。只有用户点「加载」才给当前邮件单独放行图片；信任发件人或重试不产生这个例外。没有手动放行时，图片代理按核心当前的策略取图：即使旧正文还在屏幕上，移入垃圾箱或撤销信任后也不再自动取图。
- 撰写窗口里的正文和粘贴内容只经过反序列化变成 Plate 的数据结构，由 React 渲染，不使用 `dangerouslySetInnerHTML`。
- **模型输出按不可信内容渲染**：链接只允许 http、https、mailto，点击一律经 Go 确认后用系统浏览器打开；不显示图片；不允许原始 HTML。
- 页面内导航一律拦截，`window.open` 拒绝。
