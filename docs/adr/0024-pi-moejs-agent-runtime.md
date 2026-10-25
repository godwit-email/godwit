---
status: accepted
---

# Agent 运行时换成 pi + moejs，取代 Genkit 和 goja

Wit 和其他 AI 功能改由 **pi** 运行：`pi-ai` 负责各家模型协议，`pi-agent-core` 负责 agent 循环。pi 是 TypeScript 写的，运行在 **moejs** 里。moejs 是纯 Go 的 JS 运行时，满足 [0001](0001-pure-go-no-cgo.md)。Genkit、openai-go、anthropic-sdk-go 和 goja 都已删除（goja 在 #238，其余在 #236），脚本插件也在 moejs 上运行。这份 ADR 取代 [0008](0008-goja-script-plugins.md) 和 [0007](0007-genkit-agent-framework.md)。

决定的依据是维护面：各家协议的差异（推理参数、兼容服务的怪癖、订阅服务）由 pi-ai 跟进，Godwit 不再在 Genkit 插件之外自己补协议。2026-10-24 的原型验证了 pi-ai 的 OpenAI 和 Anthropic provider（官方 JS SDK）能在 moejs 里完成流式输出和工具循环。

## 边界

- **Go 继续负责**：存储（[0021](0021-conversations-in-sqlite-rows.md)）、审批与至多一次（[0004](0004-permission-gateway-taint.md)、[0006](0006-send-at-most-once.md)）、邮件工具、OAuth、日志脱敏，以及**所有 HTTP**。pi-ai 的每个 provider 都接受注入的 `fetch`，bundle 里的 `fetch` 交给 Go，用 Godwit 的模型客户端发出：不跟随重定向，插件模型照旧走插件客户端（[0020](0020-plugin-models-host-sdk.md)）。
- **`ai` 模块对外的类型不变**（迁移期间让一次运行走 pi 的 `ai.Request.Pi` 已随 Genkit 删除）。
- **宿主只补标准 Web API 的形状**（`fetch`、`Response`、`Headers`、`ReadableStream`、`AbortController`、`URL` 等），不提供 Node 的 API，所以以后换 JS 引擎只需要重写宿主层。
- **插件和 agent 不共用运行时**：每次运行各用一个 moejs 运行时，脚本插件也各用自己的，插件运行时默认不暴露这些全局对象。

## Consequences

- 迁移期间，设置里「实验室」分区的「新 AI 引擎（pi）」开关（`Settings.PiEngine`）让用户先试用 pi，还没迁到 pi 的功能自动走 Genkit。#236 删除 Genkit 时一并去掉了这个开关和「实验室」分区：所有 AI 功能都走 pi，Genkit 时代中断的运行（存成 pi 消息或原样的 Genkit 消息）也在 pi 上恢复。
- bundle 由 `mise run ai:bundle` 用 bun 打包、压缩后提交进仓库（和 `mcpserver/views` 一样），`go build` 不依赖 bun。CI 重新打包并检查结果与提交的一致。
- moejs 固定依赖上游版本（目前是 `v0.1.0-alpha.9`）。它还是 alpha，只有一个维护者，所以每次升级 moejs 或 pi 都必须先通过 `core/ai/pi` 的冒烟测试：在 moejs 里运行真实的 provider 代码，对接模拟服务。出问题时再 fork。
- 对未知的 OpenAI 兼容服务，pi-ai 默认会把推理模型的系统提示改用 `developer` 角色发送，还会附带 `store: false`。很多兼容服务不接受这两样，Genkit 路径也都不发，所以 bundle 把这两个兼容项固定关掉。pi-ai 把不认识的 Chat Completions `finish_reason`（例如 xAI 的 `end_turn`）都当作失败，bundle 先按 Genkit 的读法把它们换成 pi-ai 认识的。
- pi-ai 的 Anthropic provider 和 Genkit 的插件一样只保留工具参数格式根级的 `type`、`properties`、`required`，所以插件和 MCP 工具的 `$defs` 仍然先展开成内联定义（`ai.NormalizeSchema`）。
- 每次运行新建一个运行时，加载已编译的 bundle。它不跨运行保留任何状态，取消时直接终止整个事件循环。
- 对话一律存成 pi 消息（#234）。已有的 Genkit 格式对话在启动时转换（照 [0021](0021-conversations-in-sqlite-rows.md) 的做法：`user_version` 提升前先备份数据库，转换不了的对话保留原样，在 pi 上照旧能读、能继续，下次启动再试）。这一步不可回退，旧版本打不开转换后的库。
- 脚本插件一律在 moejs 上运行：esbuild 把插件编译成一个 ES 模块（严格模式，不再降级），每个插件一个运行时，不与 agent 的 bundle 共用。内存上限按宏任务计（`plugins.MemoryLimit`），调用期限和以前一样靠 `Interrupt` 抢占。

## Considered Options

- **侧载 pi CLI（RPC 模式）**：需要随应用分发一个内嵌 Bun 的独立程序，压缩后 31–45 MB；而且 RPC 里的确认是进程内的 Promise，跨重启的审批恢复仍然要 Godwit 自己做。
- **只用 pi-agent-core，模型仍由 Genkit 调用**：只换掉了循环，provider 的维护面没有变小，反而多了一层跨语言调用。
- **Go 原生框架**（Eino ADK、Google ADK Go、tRPC-Agent-Go）：没有一个能直接取代 Godwit 现有的审批和存储，迁过去也省不掉这部分业务代码，而 provider 层仍要逐个适配。
- **在 goja 里运行 pi**：goja 对 ES2017+ 的支持不完整，脚本插件现在要靠 esbuild 降级才能跑。moejs 通过了它所运行的全部 test262 用例，按它自己的基准，调用耗时约为 Sobek 的一半。
