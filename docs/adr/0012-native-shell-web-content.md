# 主窗口原生，撰写、Wit 页和阅读正文用网页

主窗口（顶栏、图标栏、侧栏、邮件列表、设置）用 mygo 原生 UI。撰写窗口（每封草稿一个整窗网页）、Wit 页和阅读区的邮件正文（用 mygo `ui.WebView` 嵌入主窗口）用网页：自研编辑器在中文输入、粘贴、撤销、无障碍上很难追上成熟编辑器，原生渲染也会把多栏、表格排版的 HTML 邮件压平。

网页只是视图，**Go 端是内容的权威**：保存、版本、冲突、审批、对话存储都留在 Go。前端栈是 React 19 + Vite 多入口 + shadcn/ui（Base UI 版本）+ Plate；shadcn 组件由脚本调用 shadcn CLI 生成，**生成的文件不手改**，外观只靠主题变量，行为差异在自己的目录里包一层，升级时只需重新生成。

## Consequences

- 网页里的 Wit 用 `useChat` + 自定义 `WitTransport`，agent 循环留在 Go，不写 AI SDK provider。
- 逐词修订还是整体重写由 Go 端判定，部分接受时网页只回传每处的决定，由 Go 生成结果 HTML，不在前端另写一份差异算法。
- 原生界面的图标从 `web/node_modules` 嵌入，所以任何 Go 编译和测试之前都要先 `bun install`。
- 网页和原生界面都用系统字体，不内嵌 Geist。
