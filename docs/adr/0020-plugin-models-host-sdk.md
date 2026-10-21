# 插件模型由宿主的 SDK 推理，插件只改写请求

声明 `models` 能力的插件可以提供两种模型。**HTTP 模型**由 Godwit 已有的 SDK 推理（compat_oai、anthropic-sdk-go、自己的 Responses 插件，判断用 `ai.NewJudge`），插件只能通过 `prepareRequest` 改写 SDK 编码好的请求（URL、请求头、正文），响应原样交还 SDK。这样流式输出、工具调用、审批和历史都和手建服务一样工作，Godwit 和插件都不用写协议代码；供应商专用逻辑（如 Zen Free）全部留在插件里，Godwit 不校验插件返回的正文、tools 或 model 字段。**插件判断模型**（`transport: "plugin"`）由插件自己回答判断题，比如本地运行的模型。

插件模型的引用是 `plugin/<插件名>/<模型 id>`，和 `Settings.Translator` 用同一个前缀，`DefaultModel`、`SmallModel`、`JudgeService` 都接受，不需要 slug 表，也不迁移设置。插件没运行或不再提供该模型时返回 `ErrModelUnavailable`，**不回退到默认模型**，免得邮件内容悄悄发给用户没选的服务。

## Consequences

- 脚本插件的请求经 `plugins.Client` 发出：只能是 https、主机必须在 `permissions.net` 内、不连私有地址、不跟随重定向，每个请求都记审计；改写请求前先给插件标上 `holds_mail`。原生插件拥有 Godwit 的权限，请求走普通模型客户端。
- 插件停用、被替换或保存设置后，它的模型实例被清掉，进行中的生成以 `CodeStale` 结束；`listModels`、`prepareRequest` 和 `judge` 都使用该模型被列出时的设置快照。
- 脚本插件不能在自己的调用里使用自己的模型（VM 一次只跑一个调用，会互相等到超时），这种情况立即报错。
- 原生插件声明的模型文件（`assets`）通过 `core/modelseam.go` 的 `modelAssets` 接缝管理：URL、大小、哈希由宿主从受信任的 release 读取，后台调用不会暗中开始下载。
