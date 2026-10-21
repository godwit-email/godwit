# Agent 框架用 Genkit Go，并隔离在 `ai` 模块后面

Wit 的 agent 循环、流式输出、工具调用和审批中断都基于 Genkit Go（锁定 v1.13.1）：在三个候选里它的二进制最小（22 MB），而且自带经过验证的工具中断 / 恢复。`ai` 模块对外只暴露自己的类型（流式事件、工具定义、审批），不把 Genkit 的类型泄漏到 Service API，以后要换框架只动这一层。

## Consequences

- 审批用工具中断实现：未获批准时调用 `ToolContext.Interrupt`，不产生副作用；用户批准后由宿主执行实际操作，再用 `Respond` + `WithToolResponses` 把结果交还。**不用 `Restart`**，否则工具会再执行一次。
- Genkit 要求恢复时一次处理完所有被中断的调用，所以用户批准一张卡片时宿主立即执行并记下结果，等所有卡片处理完再一次性恢复 agent。
- Genkit 的 Anthropic 插件只保留工具参数格式根级的 `type`、`properties`、`required`，所以插件和 MCP 的工具注册时先把 `$defs` 展开成内联定义，并一律 `WithStrictSchema(false)`。

## Considered Options

- **Fantasy**：自带的 agent 循环会重建工具参数格式，Anthropic 模块无条件引入 Bedrock / Vertex，二进制 50 MB。
- **Eino ADK**（2026-10-10 复评）：checkpoint 是无文档、无版本的 gob 结构，会冻结系统提示；恢复时会再次调用工具函数；Anthropic key 为空时会把环境变量 `ANTHROPIC_API_KEY` 发往配置的地址；依赖的 sonic 不支持 Go 1.27。只有 Genkit 的 `Respond` 路径出问题、且 Eino 提供有版本的 checkpoint 时才重新评估。
- **自己基于官方 SDK 写循环**：估算 2–4 人周，协议差异需要长期维护。
