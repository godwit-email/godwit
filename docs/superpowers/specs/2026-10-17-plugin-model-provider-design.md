# 插件模型 API：HTTP 模型与插件直接推理

日期：2026-10-17 起草，2026-10-18 按实现改写。状态：已在 godwit-email/godwit#109 实现；取代 #108 的草案（其中请求校验、会话标识、Zen Free 专用逻辑和设置迁移不再采用）。

关联：StartLux 本地判断模型 godwit-email/godwit#90（实现 §8 的 `modelAssets`）、godwit-email/plugin-startlux。实施计划：docs/superpowers/plans/2026-10-18-plugin-models.md。

## 1. 用户体验与范围

用户安装、授权并启用声明 `models` 能力的插件后，它的模型出现在默认模型、小任务模型和判断模型的选择器里，按插件分组显示为「插件 / 模型」。不需要手建服务、选择协议或填 Key（凭据由插件自己的设置管理）。启用插件不改用户的默认模型，也不下载任何模型文件。

两种执行方式：

- **HTTP 模型**：由 Godwit 已有的 SDK 推理（compat_oai 基于 openai-go、anthropic-sdk-go、Godwit 自己的 Responses 插件；判断用 `ai.NewJudge`）。插件只调整 SDK 编码好的请求（URL、请求头、正文），和 aio-proxy 插件用自定义 `fetch` 包装标准 AI SDK provider 是同一个思路。Godwit 和插件都不写协议代码。
- **插件判断模型**：插件自己回答判断题（如 StartLux 本地模型）。

脚本与原生是插件运行时，和这两种执行方式互相独立。原生插件的直接流式生成不在范围内：ABI v1 没有流和取消回调。手动添加的模型服务照常工作。

## 2. 模型描述

```ts
export type Model =
  | {
      id: string
      name: string
      transport: "http"
      protocol: "openai-completions" | "openai-responses" | "anthropic-message" | "typesafe-systemone" | "openai-decisions"
      baseURL: string
    }
  | { id: string; name: string; transport: "plugin"; operation: "judge"; assets?: Record<string, string> }
```

Go 侧是 `plugins.ProviderModel`，由 `plugins.ValidateModels` 校验：

- 前三种协议生成，后两种判断；能力由 protocol（或 `operation: "judge"`）推导，不另行声明。
- `id` 在插件内唯一、非空、不超过 200 字节、不含换行，允许 `org/model` 形式；`name` 非空；一份列表最多 200 个模型。任何一项不合法，整份列表作废。
- `baseURL` 与同协议的手建服务含义相同，路径由 SDK 拼接（如 `openai-completions` → `/chat/completions`）。必须是不带凭据、查询和片段的 https 地址；原生插件可用 http（本机服务）。
- `assets` 是 registry 资产组名 → 变体 ID，不是下载地址，只有原生插件可声明。URL、大小、哈希、内存要求由宿主从受信任的 release 读取。
- `transport` 表示谁执行请求，不表示数据是否联网。
- 列出模型不代表已下载或已加载。

## 3. JS API

加入 `sdk/godwit.d.ts`：

```ts
export interface ModelRequest { modelId: string; method: string; url: string; headers: Record<string, string>; body: string }
export interface JudgeRequest { modelId: string; input: string; questions: Question[] }

export interface ModelProvider {
  listModels(): MaybePromise<{ models: Model[] }>
  prepareRequest?(req: ModelRequest): MaybePromise<{ url: string; headers: Record<string, string>; body: string }>
  judge?(req: JudgeRequest): MaybePromise<{ answers: Answer[] }>
}
// Ctx.models: { register(provider: ModelProvider): void }
```

- `register` 只能在初始化时调用一次，manifest 必须声明 `"capabilities": ["models"]`；声明了却没注册、重复注册、未声明就注册都是加载错误。
- 省略 `prepareRequest` 表示请求原样发送。`body` 是 SDK 编码的 JSON 文本，插件可以按需改写。
- `judge` 只用于 `transport: "plugin"` 的模型。`Question` / `Answer` 是 Godwit 已有的判断契约（与 `ctx.llm.decide` 相同），不是 TypeSafe 或 OpenAI 的线格式。

## 4. HTTP 模型的调用语义

每个插件模型有自己的 `http.Client`（每个运行实例、模型和设置快照一个；并发首次解析共用同一个）。它的 RoundTripper：

1. 确认插件实例仍在运行、仍是同一实例、设置快照未变，否则返回 `ErrModelUnavailable`。
2. 脚本插件：先标记 holds_mail（请求含邮件内容），然后把 SDK 编码好的请求 `{modelId, method, url, headers, body}` 交给 `prepareRequest`。原生插件只有在 describe 的 `methods` 列出 `models.prepare` 时才调用它。交给插件的请求上限 8 MiB。
3. 用返回的 url / headers / body 重建请求并发送，响应原样交还 SDK，所以流式输出、工具调用、审批和历史与手建服务一样工作。SDK 的每次重试都会再走一遍 prepare。

Godwit 不校验返回请求的正文、tools 或 model 字段，不加占位工具，也不生成会话标识；这些属于插件和上游之间的事。

出站规则：

- 脚本插件的请求经 `plugins.Client` 发出：自己解析 DNS、不连私有地址、不走代理，只能是 https 且主机在 `permissions.net` 内，不跟随重定向。每个请求按实际结果审计为 `plugin:<name>:models`（allow / deny / error 及原因，只记 host:port），和 `net.fetch` 一致。
- 原生插件拥有 Godwit 的权限，请求走普通模型客户端，和手建服务一样。

生命周期与设置：

- 插件停用、被替换或保存设置后，它的模型在引擎里的实例被清掉，进行中的生成以 `CodeStale` 结束；之前取得的客户端和 judge 从此拒绝调用。
- 脚本插件的 `listModels` / `prepareRequest` / `judge` 都用该模型被列出时的设置快照作为 `ctx.config`，新设置不会配旧模型。
- 引擎用负数 ServiceID（引用的哈希）区分插件模型，不会与已存服务冲突。Version 是插件代码哈希与设置版本的哈希，技能和快捷回复的缓存随插件代码或设置一起失效。

## 5. 插件判断模型

```text
HTTP + judge 协议    → ai.NewJudge → prepare → HTTP
plugin + judge       → 准入排队 → judge（JS）/ models.judge（原生）→ 校验答案
```

- 宿主先用 `ai.ValidateQuestions` 校验问题，拆成每次一题调用，按原顺序合并答案，每题用 `ai.ValidateAnswers` 校验。
- 每个插件同时只有一个判断调用。排队中的调用方可以取消，不计失败；已发出的调用继续占着位置直到插件返回（JS 有 VM 时限，原生有 ABI 超时），调用方可以先停止等待。
- 原生插件声明了 `assets` 的模型，调用前先经 `modelAssets.Verify` 校验文件，再 `models.load {modelId, assets: 目录}`。每个原生进程只做一次：进程有编号，加载记录只对加载它的那个进程有效；进程重启、assets 改变都会重新校验和加载，并释放旧的文件租约。插件停用时释放全部租约。

## 6. 原生方法

沿用 C ABI v1 和 JSON 信封，只新增 method：

| method | 请求 | result |
| --- | --- | --- |
| describe | {} | 现有字段，加可选 `methods` 字符串数组 |
| models.list | {} | `{models: Model[]}` |
| models.prepare | ModelRequest | `{url, headers, body}`；只在 `methods` 列出时调用 |
| models.load | `{modelId, assets: {资产组名: 已验证目录的绝对路径}}` | {} |
| models.judge | JudgeRequest | `{answers: Answer[]}` |

`configure` 不变。`models.list` 不依赖模型文件已下载，宿主不会为列出模型而下载。

## 7. 引用、设置与目录

- 插件模型的引用是 `plugin/<插件名>/<模型 id>`，和 `Settings.Translator` 用同一个 `plugin/` 前缀。`DefaultModel`、`SmallModel`、`JudgeService` 都接受这种引用，没有 slug 表，也不迁移设置。
- 插件没运行或不再提供该模型时返回 `ErrModelUnavailable`，不退回默认模型（小模型也不退回）；选择器里显示为「插件未运行」。
- 模型服务不会再分到 slug `plugin`。旧库里已经叫 `plugin` 的服务在启动时改名，设置、runs、conversations 里的引用随之改写，旧 slug 记为别名。确定没有对应插件模型时（插件不存在，或它的列表里没有这个模型），`plugin/…` 按服务引用解析，所以 skill 文件里的旧引用仍然有效；插件列表出错时不改走别的服务。
- 模型列表按运行实例和设置快照缓存（原生插件用进程已确认接收的设置），设置变化后重新列。一个插件列表失败只影响它自己，`ListPluginModels` 把错误和其他插件的结果一起返回。

## 8. 模型文件接缝（#90）

`core/modelseam.go` 定义宿主与托管模型文件之间的接缝：

```go
type modelAssets interface {
	Status(ctx context.Context, plugin, release string, refs map[string]string) (ModelStatus, error)
	Prepare(ctx context.Context, plugin, release string, refs map[string]string,
		progress func(ModelStatus)) (dirs map[string]string, releaseLease func(), err error)
	Verify(ctx context.Context, plugin, release string, refs map[string]string) (dirs map[string]string, releaseLease func(), err error)
	Remove(ctx context.Context, plugin, release string, refs map[string]string) error
}
```

- `plugin` 是 manifest.name。`release` 是已安装 release 的资产摘要（`plugins.AssetsDigest`），#90 把它存进插件记录后接入；在那之前调用处传 `""`，并标了 `shortcut:`。`refs` 是模型的 `assets`。
- Status 只读元数据和磁盘，不下载、不全量哈希。Prepare 只在用户明确要求时下载并校验。Verify 只校验不下载，每个新进程使用一次。Remove 只删除没有租约、也没有别的模型引用的文件。
- 本 PR 的默认实现是 `noModelAssets`：Status 返回 `state=error, code=update_required`，Prepare / Verify 返回同码的 `*ModelError`。#90 提供真正的实现，并负责 `ListModels` / `PrepareModel` / `RemoveModelFiles`、下载进度和下载 UI。

状态码（`ModelError.Code`，宿主诊断，不是插件故障）：

| code | 含义 |
| --- | --- |
| model_not_ready | 所需文件缺失；后台调用不会暗中开始下载 |
| model_unavailable | 模型暂不能用（如仍有租约，不能删除） |
| insufficient_memory | 不满足当前平台的内存要求 |
| unsupported_platform | 当前平台没有所需资产 |
| update_required | 宿主尚不支持托管资产，提示更新 |
| verification_failed | 哈希不符，不得加载 |

## 9. 错误与失败计数

- 判断：上面的状态码，以及原生插件 `models.judge` 返回的其他错误信封，都映射为 `ai.ErrJudgeUnavailable`。`models.load` 返回的任何错误信封（包括 `invalid_request`）也是，因为它与具体邮件无关。崩溃、超时、答错映射为 `ai.ErrJudgePlugin`。两者都会停掉本轮分类，邮件保持待处理。
- 只有 `models.judge` 的 `invalid_request` 表示这条输入模型处理不了，与 HTTP 400 同类：分类器保留这封邮件现有的标签，继续下一封。
- 失败计数沿用连续 `maxFailures` 次停用的规则。脚本插件抛错、原生插件崩溃或超时、答案不合法（列表、URL、答案校验不过）计一次失败。原生插件正常返回的错误信封算一次应答，会把计数清零。调用方取消、插件已关闭不计。
- 脚本插件不能在自己的调用里（action、tool、hook 中的 `ctx.llm.complete` / `decide`）使用自己的模型：它的 VM 一次只跑一个调用，会互相等到超时。这种情况立即报错，提示换一个默认模型或判断模型。
- JS 插件抛出的错误没有错误码。

## 10. 不在范围内

- 校验插件返回请求的正文、tools、model；占位工具；宿主生成的会话标识。
- 任何供应商专用逻辑（如 Zen Free），这些属于插件。
- 持久化的模型目录、定时刷新。
- 在 openai-go/v3 的 `Decisions` API 上实现 Decisions 判断。
- 真实的 `modelAssets`、`release` 摘要、下载与准备 UI（#90）。
- `prepareRequest` 自身的错误信息还没有传到界面，用户看到的是引擎的通用提示「could not reach the model service」。
