# 插件模型 API：HTTP 模型与插件直接推理

日期：2026-10-17。状态：待讨论的设计草案，未实施。

关联：Zen Free 参考实现 https://github.com/magpie-community/plugins/tree/main/packages/zen-free ；StartLux 计划 https://github.com/godwit-email/godwit/pull/90 （本次读取的 head：f011274f9cfc38be1b7597494e93c7a31e0106bc，当前只包含计划）。

## 1. 用户体验与范围

用户安装并授权启用插件后，其 provider 和模型目录自动进入模型选择器；不需要手建服务、选择协议或为 Zen Free 填 Key。未下载的本地模型也列出，并显示宿主管理的大小、内存要求和下载状态；列出不意味着已经能调用。生成模型可以设为默认或小任务模型；判断模型出现在判断模型选择器。启用插件本身不改用户的默认模型、不自动下载所有模型权重。

统一的是模型发现、身份、选择、配置、生命周期与错误处理。HTTP 模型由 Godwit 推理；直接执行的模型由插件推理。脚本与原生是插件运行时，和这两种执行方式互相独立。

本版支持五种 HTTP 协议，以及插件直接执行 judge。原生直接流式生成不在本版范围：ABI v1 没有流和取消回调，不能假装它已经支持。现有手动添加的 HTTP 服务继续工作。

以下接口名称和执行方式是本草案的建议；用户已指定三个生成协议的命名，并要求把判断模型也纳入统一模型列表。

## 2. 模型描述

```ts
export type GenerateProtocol =
  | "openai-completions"
  | "openai-responses"
  | "anthropic-message"

export type JudgeProtocol =
  | "typesafe-systemone"
  | "openai-decisions"

export type ModelProtocol = GenerateProtocol | JudgeProtocol

export interface ModelIdentity {
  /** 插件内唯一、稳定；更改显示名称不改变它。允许 org/model 形式。 */
  id: string
  name: string
}

export interface HTTPModel extends ModelIdentity {
  transport: "http"
  protocol: ModelProtocol
  baseURL: string
}

export interface PluginJudgeModel extends ModelIdentity {
  transport: "plugin"
  operation: "judge"
  /** registry 中的资产名 → 变体 ID；不是下载 URL。 */
  assets?: Record<string, string>
}

export type ProviderModel = HTTPModel | PluginJudgeModel

export interface ModelsResult {
  models: ProviderModel[]
}
```

`transport` 是谁执行请求，不是部署位置：JS 插件也可以在 judge 回调里调用远程服务；原生插件也可以只提供 HTTP 模型。不能从 transport 推断“数据不会联网”。

HTTP 模型的能力由 protocol 推导，前三种为生成，后两种为判断，不再重复声明 capabilities。直接执行的模型由 operation 推导能力。本地 StartLux 不填 protocol 或 baseURL；不能把它标成实际上不提供的 HTTP 协议。

HTTP 的 baseURL 是去掉结尾斜杠的 API 基址，拼接规则统一如下：

| protocol | POST 路径 | 用途 |
| --- | --- | --- |
| openai-completions | /chat/completions | 生成 |
| openai-responses | /responses | 生成 |
| anthropic-message | /messages | 生成 |
| typesafe-systemone | /systemone | 判断 |
| openai-decisions | /decisions | 判断 |

例如 `https://opencode.ai/inference/v1` 与 `/messages` 拼成 `https://opencode.ai/inference/v1/messages`。适配现有 SDK 时须保证最终地址符合该规则，不能由 SDK 再多加一个 `/v1`。

首版一个插件一个 provider，其身份来自 manifest.name，显示名称复用 registry 的标题（侧载时回退插件名）。不再给 register 增加重复的 provider ID。模型 id 在同一 provider 内唯一；同名重复、未知协议、不合法 URL、超限 JSON 都是契约错误，整份新列表不发布。

模型描述不保存凭据，也不要求插件返回剩余额度、价格、上下文窗口或推理档位；这些不是首版接入所必需的字段。

`listModels` 是模型目录，不是“已经下载 / 已经装入内存”的列表。`assets: { model: "4b-q4" }` 表示该模型依赖当前受信任 registry release 的 model 资产组中 4b-q4 变体。GGUF、decision_config 等属于同一变体的文件，由 registry 描述；URL、大小、SHA-256、minMemoryGB 由宿主从该 release 读取，不相信动态列表自行提供的下载信息。多个组可由同一映射关联，固定引擎资产沿用 release 的固定资产定义。

插件只声明稳定的模型 id/name、执行方式与资产引用；大小、平台、内存等展示信息由宿主从 registry 补齐，不维护第二份资产元数据。保留插件模型目录，不直接将 registry 变体展开成模型：一个模型可能依赖多个资产组，资产变体也不一定代表可调用模型。交叉校验检查引用是否合法，不要求两份完整目录逐项一致。

首版本地托管资产只给原生插件使用，沿用 #90 的信任边界；JS 运行时没有读取 GGUF / 加载 libllama 的接口，声明 assets 会被拒绝。JS 的 HTTP 模型和不依赖托管资产的 judge 回调仍可使用本 API。侧载原生插件没有签名 registry 资产目录时，不能启动宿主托管的下载。

## 3. 完整的 JS API

下面的类型加入现有 `sdk/godwit.d.ts`。Text、MaybePromise、Question、Answer 沿用已有定义；Question / Answer 是 Godwit 已有的标准判断契约，不是 TypeSafe 的原生线格式。

```ts
export type JSONValue =
  | null | boolean | number | string
  | JSONValue[]
  | { [key: string]: JSONValue }

export interface ModelHTTPRequest {
  url: string
  headers: Record<string, string>
  /** 已按该模型的 HTTP 协议编码；实际发送方法固定为 POST。 */
  body: Record<string, JSONValue>
}

export interface PrepareModelRequest {
  model: HTTPModel
  /** 不包含业务数据的随机会话标识；工具续轮、审批恢复和重启后保持稳定。 */
  sessionId: string
  /** Unix 毫秒；和 sessionId 一起由宿主持久化。 */
  sessionCreatedAt: number
  /** 每次实际 HTTP 尝试唯一，重试会改变。 */
  requestId: string
  request: ModelHTTPRequest
}

export interface JudgeModelRequest {
  modelId: string
  input: string
  questions: Question[]
}

export interface JudgeModelResult {
  answers: Answer[]
}

export interface ModelProvider {
  listModels(): MaybePromise<ModelsResult>

  /** 可省略；省略就原样发送宿主编码的请求。 */
  prepareRequest?(
    request: PrepareModelRequest,
  ): MaybePromise<ModelHTTPRequest>

  /** 列表含 transport=plugin、operation=judge 的模型时必须实现。 */
  judge?(request: JudgeModelRequest): MaybePromise<JudgeModelResult>
}

// 加入已有 Ctx，而不是替换它。
// models: { register(provider: ModelProvider): void }
```

`register` 仅允许初始化时调用一次，并且 manifest 必须声明 `capabilities: ["models"]`。声明能力却未注册、重复注册、注册未声明的能力均为加载错误。初始化不联网；初始化后宿主才调用 listModels。

可选回调的省略表示明确的不支持，抛错表示本次调用失败：prepareRequest 缺省是原样请求；judge 缺省而列表返回直接执行判断模型是契约错误。

调用参数是当前调用的 JSON 副本。回调返回 JSON 数据，不传 goja 对象、函数、流、指针或 AbortSignal。调用取消沿用当前 VM CallScope 的 context。

## 4. HTTP 调用的完整语义

1. 宿主解析模型引用，确认插件当前启用、授权有效、配置版本未变化；按所选模型的 protocol 编码请求。
2. 宿主在适配前记录真正提供给本轮的工具集合和对应 Gate。生成请求设置 stream=true；判断请求沿用现有非流式编码。
3. 每一次实际 HTTP 尝试（包括 SDK 重试、工具续轮和审批恢复）调用 prepareRequest。每次传入新编码或未适配的副本，避免重试时累加占位工具、请求头或正文修改。一次性任务也生成独立的 session；不由插件用首条提示猜测会话。
4. 插件返回完整的 url、headers、body。宿主校验协议规定的 model 不变、生成请求仍为流式、必要协议字段及真实工具定义仍存在、数据不超限。插件允许补充占位工具，但不能替换原有工具名、参数 schema 或赋予执行权限。
5. 宿主用最新的插件配置代数、授权和启用状态复核后发送。HTTP 请求、响应解析、重试、取消由宿主负责。JS 的推理请求用模型专用网络管线，不通过会缓冲全响应的 ctx.net.fetch。
6. 返回的工具调用仍只允许命中宿主原始工具集合；未知工具和占位工具不能执行，明确报错并保留上一轮状态。首版不做模糊工具名匹配，也不把被截断的流当成功。

所有首版 HTTP 协议的 JSON 输入、适配结果限制为 1 MiB；模型列表也限制为 1 MiB。不得为传给插件而静默截断正文。超过时是输入大小错误，尚未调用插件的不计插件失败；插件输出超限属于契约错误。流式生成响应沿用现有引擎边界，不受这个回调 JSON 上限限制。

User-Agent 可以由 prepareRequest 设置。宿主负责 Content-Length、Host 等传输字段，过滤 hop-by-hop 头、拒绝不合法头和 URL；禁止推理请求自动重定向。

脚本结果必须再次满足 HTTPS、manifest.net 域名、DNS/IP 等现有出站检查，不能复用默认模型客户端绕过沙箱。开发测试的 HTTP / loopback 开口仍仅限测试。原生插件无沙箱，不能声称它的自行联网受 net 权限限制；宿主发送的模型请求仍验证安全 URL，并禁止重定向。

## 5. 直接执行判断模型

```text
同一份模型列表
  HTTP + judge protocol → 宿主编码 → prepareRequest → HTTP → 宿主转换 Answer
  plugin + judge        → 宿主准入排队 → judge → 宿主验证 Answer
```

插件 judge 接收 Godwit 的标准 Question / Answer，不接受 OpenAI 或 TypeSafe 的 HTTP body。宿主先验证问题，再拆成每次 1 题调用 judge，按原顺序合并；一次调用的 questions 仍为数组，与现有判断工具契约一致。

首版每插件至多一个 judge 调用在执行，JS 和原生均由宿主排队；多个本地模型也共享这个槽位。逻辑 Ask 是配置版本的原子单位，在开始准入时才绑定版本；尚未准入的 Ask 不预先固定旧配置。取消排队直接出队，不计失败。原生已发出的调用无法在 ABI v1 下取消：调用方可停止等待，槽位和资产租约必须等实际返回或进程被杀才释放。JS 沿用 VM 的 30 秒调用时限；原生每次派发后的 60 秒看门狗沿用 #90，排队时间不消耗该看门狗。

有托管资产的模型在调用前必须经过宿主准备：下载、校验后调用原生 models.load。models.load 接收模型 ID 和已验证的资产绝对路径，加载权重并做最小推理；成功才标为 ready，不在 listModels 或 configure 中装权重。它承接 #90 原拟 probe 的模型装载与验证职责，模型 API 不再额外要求 probe。下载有独立的进度、取消与网络预算；排队、下载、宿主哈希校验均不占 ABI 调用的 60 秒看门狗，真正派发 models.load 后才开始最多 60 秒的冷启动预算，单题推理另有 60 秒预算。该预算采用 #90 提供的实测依据：首次 dlopen 约 1.5 秒，Metal 4B 装载约 0.3–0.6 秒，CPU 2B 约 1.7 秒；本设计未独立复测。

已经下载的模型可在进程空闲退出后自动重新校验并 load，无需再次询问下载；尚未下载的模型被后台分类等调用时返回 model_not_ready，不暗中开始 GB 级下载。用户显式“下载并使用 / 准备模型”才发起下载。取消中的原生 load 仍须等实际返回或终止进程才能释放准入和资产租约。

结果必须严格匹配问题的 name、类型、数量以及概率范围。坏答案、未知 modelId、未知方法、ABI 崩溃等按各自输入或插件契约边界处理，不能用“拒答”掩盖输入过长。任何失败不提交半批结果，分类器保持原标签。

model_not_ready / model_unavailable 在 Ask 边界均映射为可由 errors.Is(err, ai.ErrJudgeUnavailable) 识别的错误，具体模型错误码继续保留。分类器停止本轮，失败 Ask 涉及的邮件不写入或删除标签，不回滚此前已成功处理的邮件，也不计插件故障；具体错误类型见 §9。

## 6. JS 使用示例：Zen Free

manifest（版本等基础字段略）：

```json
{
  "name": "zen-free",
  "main": "index.ts",
  "capabilities": ["models"],
  "permissions": {
    "net": ["opencode.ai", "models.opencode.ai", "raw.githubusercontent.com"]
  },
  "config": []
}
```

```ts
import type { Ctx } from "@godwit/sdk"
import { discoverFreeModels, adaptZenRequest } from "./zen"

export default function (ctx: Ctx) {
  ctx.models.register({
    async listModels() {
      // 通过 ctx.net.fetch 获取上游列表及协议元数据；这两个辅助函数是插件代码。
      const models = await discoverFreeModels(ctx)
      return {
        models: models.map(model => ({
          id: model.id,
          name: model.name,
          transport: "http" as const,
          protocol: model.protocol,
          baseURL: "https://opencode.ai/inference/v1",
        })),
      }
    },

    prepareRequest(call) {
      // 固定 public 凭据；按 protocol 设置头和工具声明。
      // 利用宿主 sessionId、sessionCreatedAt、requestId 生成 Zen 所需标识。
      return adaptZenRequest(call)
    },
  })
}
```

发现逻辑筛选真正免费的、未退役且协议受支持的模型，不能把非免费模型硬塞进列表。五种 HTTP 协议在宿主层都可用，但 Zen 是否接受某个免费判断模型的请求要单独验证，不能据其名字保证可用。

若 JS 插件需要自行实现 judge，注册同一对象的 judge 回调，并在 listModels 返回 `{ id, name, transport: "plugin", operation: "judge" }` 即可。它可用 ctx.config、ctx.storage、ctx.net 做适配；这不代表 goja 能加载 libllama 或运行 Node / WebAssembly。本地 libllama 走下面的原生实现。

## 7. 原生 API 与使用示例

沿用 C ABI v1 的 godwit_plugin_init、call、free_buffer 和 JSON envelope。新增的只是 method 字符串；现有 describe / configure 继续使用。其他能力已有的 probe 方法不受影响，本模型能力以 models.load 完成对应模型的加载与验证。

| method | 请求 | result |
| --- | --- | --- |
| describe | {} | 现有 name/version/abi/capabilities；增加可选 methods 字符串数组 |
| configure | 现有 {config: {...}}；基础 API PR 和 #90 均不扩展 | {} |
| models.list | {} | ModelsResult |
| models.prepare | PrepareModelRequest | ModelHTTPRequest |
| models.load | ModelLoadRequest；原生、有托管资产的模型必须支持 | {}；已加载并通过最小推理验证 |
| models.judge | JudgeModelRequest | JudgeModelResult |

新增 models 能力必须在 describe.methods 中列出支持的方法，宿主在加载时检查 models.list 必须存在、其余方法是否可用。没有声明 models.prepare 就采用原样 HTTP 请求，不能把该方法的运行失败误当成“可省略”；列表包含 plugin judge 时必须声明 models.judge，包含 assets 时还必须声明 models.load。旧 translate 插件不要求有 methods，保持兼容。methods 的新增必须写入 SDK 文档，并做严格字段解析的兼容测试。

```ts
// 原生 JSON 契约；不是 JS 的文件系统 API。
interface ModelLoadRequest {
  modelId: string
  /** 固定引擎及该模型所引用变体的已验证目录，键为资产名。 */
  assets: Record<string, string>
}
```

以下是 call 的 JSON 请求体，不包括 method 和外层信封；字段名固定为小驼峰。configure 始终保持现有格式，基础 API PR 和 #90 均不改动：

```json
{"config": {}}
```

固定引擎目录与所选模型目录统一由 models.load.assets 交付。发现目录不依赖引擎或模型文件已下载，插件必须能完成 describe / configure / models.list；宿主不得为发现目录触发下载。

models.load 的请求与上述 ModelLoadRequest 一致，包含固定资产和所选模型引用的全部变体：

```json
{"modelId": "decision-4b-q4", "assets": {"engine": "/absolute/verified/engine", "model": "/absolute/verified/4b-q4"}}
```

models.load.assets 映射的键为 registry 资产名，值为已验证目录的绝对路径；目录内文件名严格使用 AssetFile.name，不重命名。models.list 的 assets 值是变体 ID，与这里的目录路径不可混用。不同资产组的名称不得冲突。

宿主从 models.list 中已验证的资产引用解析路径，不接受插件通过 load 参数要求任意下载或任意本机路径。插件仍须检查所需组与文件及模型 ID。models.load 在每次新进程和实际切换驻留模型时运行；对同一已加载模型的重复准备由宿主合并。首版每插件一个驻留模型，加载 B 前排空 A 的逻辑 Ask；无需新增 unload ABI，成功替换或进程退出即释放旧模型。

每次真正加载前均重新计算所需资产的 SHA-256，包括每个新进程代、空闲退出后的懒重启，以及同一进程切换到另一模型；同一次准备取得的验证结果与租约用于 models.load，跨进程代不可复用验证结论。校验覆盖固定引擎及所选模型的全部文件，在全局插件锁之外执行、持有资产租约、支持取消；不以文件大小 / mtime 缓存替代本次哈希。驻留模型复用不每题重新校验。macOS 上资产文件带 com.apple.quarantine 时视为 verification_failed，提示重新下载；宿主不自行移除标记。校验失败与取消均不计插件故障。

成功与错误信封沿用：

```json
{"ok": true, "result": {"models": []}}
```

```json
{"ok": false, "error": {"code": "rate_limited", "message": "服务暂时限流", "retryable": true}}
```

原生 Zen Free 只需声明 methods 为 `["models.list", "models.prepare"]`，返回和 JS 相同的数据。由原生代码自己获取目录、读取 configure 的凭据；宿主发送推理 HTTP。它同样不需要开放端口、不通过 ABI 传 SSE。

原生 StartLux 的 manifest 示意：

```json
{
  "name": "startlux",
  "version": "0.1.0",
  "native": { "abi": 1, "idle": 120 },
  "capabilities": ["models"],
  "config": []
}
```

该 manifest 中 idle 是 #90 的拟议字段，不是当前本地分支已支持的字段。原生不声明 permissions。4B / 2B 的选择从插件 config.select 移到统一模型选择器，各自模型描述关联不同的 registry 变体；不保留第二套相互覆盖的“当前模型”配置。#90 原放在 select.options 的 minMemoryGB 随之移入受信任 registry 的 AssetVariant；文件大小和哈希继续使用 AssetFile。

AssetVariant.minMemoryGB 支持所有平台共用的数值，或按平台声明的映射，例如：

```json
{"minMemoryGB": {"default": 6, "windows-*": 8}}
```

映射必须有 default，取值为有限正数；键只允许 default、受支持的精确平台（如 windows-arm64）或操作系统通配（如 windows-*），优先级为精确平台 → 操作系统通配 → default。不为同一权重复制平台变体。阈值必须对应插件实际使用的 context / n_ubatch 参数，参数或模型要求改变时更新 release。#90 的实测显示 Windows 的提交内存与 macOS / Linux 的虚拟预留有显著差异，因此不能只按权重大小估计。该值是最低准入条件，不保证机器当前可用内存充足，加载失败仍须正常处理。

对应覆盖 #90 的 registry 校验规则：models 能力可声明没有 config.select 引用的多变体资产组，checkRelease 验证组、变体 ID、平台、文件、哈希与 minMemoryGB 的完整性，不再要求模型组必须被恰好一个 select 引用。插件运行后的 listModels 再将其资产引用与受信任 release 交叉校验；有旧 select 的其他插件继续沿用原校验。插件安装只要求插件本身的平台 / ABI 等条件满足，不因某个可选模型内存不足就拒装整个 provider；模型列表据变体要求显示不可用原因，下载和准备前再拒绝不满足内存要求的模型。这也覆盖 HTTP 与本地模型混合的 provider。

以下是 Go 业务分发示意，不是新的 Go SDK；C 导出、内存分配、锁和 JSON envelope 的代码继续复用已有原生插件骨架：

```go
func handle(method string, raw json.RawMessage) (any, error) {
    switch method {
    case "describe":
        return Description{
            Name: "startlux", Version: "0.1.0", ABI: 1,
            Capabilities: []string{"models"},
            Methods: []string{"models.list", "models.load", "models.judge"},
        }, nil
    case "configure":
        // 只接收共享配置；引擎及模型资产统一由 models.load 接收。
        return configureRuntime(raw)
    case "models.list":
        // 返回目录，以下两个模型即使尚未下载也会列出。
        return ModelsResult{Models: []ProviderModel{
            {ID: "decision-4b-q4", Name: "StartLux Decision 4B Q4",
             Transport: "plugin", Operation: "judge",
             Assets: map[string]string{"model": "4b-q4"}},
            {ID: "decision-2b-q8", Name: "StartLux Decision 2B Q8",
             Transport: "plugin", Operation: "judge",
             Assets: map[string]string{"model": "2b-q8"}},
        }}, nil
    case "models.load":
        // 收到已下载且校验过的路径；替换驻留模型并实际试跑。
        return loadAndProbeModel(raw)
    case "models.judge":
        var req JudgeModelRequest
        if err := decodeAndValidate(raw, &req); err != nil {
            return nil, invalidRequest(err)
        }
        if !isLoaded(req.ModelID) {
            return nil, modelNotReady()
        }
        return judgeWithLlama(req)
    default:
        return nil, invalidRequestMethod()
    }
}
```

`judgeWithLlama` 在 plugin-host 内调 libllama、读 logits，转换成标准答案。例如一次调用：

```json
{
  "modelId": "decision-4b-q4",
  "input": "邮件正文……",
  "questions": [{
    "name": "urgent",
    "type": "predicate",
    "instructions": "是否需要紧急处理？"
  }]
}
```

```json
{
  "ok": true,
  "result": {
    "answers": [{"name": "urgent", "type": "predicate", "probability": 0.93}]
  }
}
```

原生插件没有 JS ctx，也不需要把 ctx.net / ctx.storage 搬进 C ABI。运行时日志不得打印输入、答案中的原始邮件或凭据。

## 8. 模型列表、身份与配置

宿主在启用后发现模型；模型选择器打开时缓存超过 1 小时则刷新，也提供显式刷新。并发刷新合并；每次结果与插件代数、配置版本绑定。成功返回空数组代表确实没有模型；刷新抛错保留上次列表并标为刷新失败；首次失败显示未获取到模型。未知模型只触发一次合并刷新，不无限请求，不猜收费模型。

宿主分配并持久保存 provider 的 service slug，继续使用现有 `slug/model-id` 引用。slug 与插件 manifest.name 的绑定跨启停、升级和卸载重装保留；名称冲突复用现有 slug 分配规则，不抢占已有用户服务。绑定放在独立的持久身份记录中，卸载时仅保留名称与 slug 的墓碑，不保留凭据、配置或资产。插件服务由宿主管理，不要求用户手动添加，也不能当普通服务编辑 URL 或协议。

默认模型、小任务模型和判断模型都使用模型引用。将 Settings.JudgeService 的已存服务 slug 迁移为新的 JudgeModel 引用：服务存在且判断配置完整时用其 slug/JudgeModel，否则保留可解释的不可用状态。空值仍表示使用默认生成模型代答；显式选择了一个不可用模型时不静默回退。#90 尚未实施的 `plugin/<name>` 特殊值不再新增。

StartLux 返回 `decision-4b-q4`、`decision-2b-q8` 等稳定模型 ID，每个引用对应的资产变体。目录、磁盘下载状态、进程加载状态是三个不同的概念；下载进度和加载状态由宿主生成，插件不在 listModels 中宣称 ready。

首版本地 judge 只通过 Settings.JudgeModel 选择。ctx.llm.decide(input, questions) 保持现有签名，不新增模型覆盖参数；其他业务调用方也不能逐请求指定另一个本地 judge。models.load / models.judge 的内部 modelId 保留，用于准确分发和校验，不是调用方的覆盖入口。仅用户显式准备并选择另一模型才触发驻留切换，不新增节流器，也不允许后台请求驱动来回装载。

切换流程保留 #90 的事务与租约原则：下载 B 时 A 继续工作；B 资产验证后，阻止新的逻辑 Ask 准入，已准入的 Ask（包括剩余单题批次）在 A 上全部排空。宿主再 models.load(B)，成功才在同一切换事务中提交用户的 JudgeModel 选择，然后恢复准入。失败则重新验证并 models.load(A) 回滚，回滚也失败就显示不可用、不继续派发。尚未准入且跟随设置的请求在准入时绑定当前选择；已绑定旧模型 / 版本但尚未执行的过期请求返回 model_unavailable，不偷偷改用 B，也不触发切回 A。单个 Ask 绝不跨模型。资产租约覆盖加载后模型的整个驻留期以及所有尚未真正结束的调用；固定引擎租约至少保持到进程退出。模型状态和推理实例缓存随配置 / 模型描述的实质变化失效，仅刷新时间变化不更换版本。

HTTP 推理实例键需包含服务身份、协议和配置版本，避免同一个 provider 的不同协议互相取消。一次运行在开始准入时固定模型描述及版本；插件被停用、撤销信任、升级或一般配置变更后，停止相关运行，绝不向旧目的地重试。上述本地模型受控切换是排空旧 Ask 后切换的例外，不通过强制取消推进切换。

### 8.1 宿主管理的下载与就绪接口

以下是 core 提供给 UI 的设计接口，不要求插件重复实现下载器：

```ts
type ModelState =
  | "not-downloaded" | "downloading" | "verifying"
  | "downloaded" | "loading" | "ready" | "error"

interface ModelStatus {
  state: ModelState
  downloadedBytes: number
  totalBytes: number
  error?: { code: string; message: string; retryable: boolean }
}

interface ListedModel {
  ref: string
  model: ProviderModel
  status: ModelStatus
}

// 对应的 Go 方法、取消与进度契约固定于 §8.2。
```

PrepareModel 合并当前版本同一模型的重复准备任务：核对受信任资产、平台与内存要求 → 显示所需下载量 → 经用户显式准备操作下载缺失文件 → 校验 → 准入切换 → models.load → ready。对于托管资产的 plugin judge，该方法是“下载并使用 / 准备并使用”的事务入口，由宿主在 load 成功后、恢复准入前提交 JudgeModel，UI 不另发一次设置写入。HTTP 模型没有本地资产，直接返回可调用状态，选择仍走普通设置入口；可调用不保证上游鉴权或网络一定正常。

下载支持 #90 的断点续传和哈希验证；失败或取消不删除其他已完成模型，不记插件故障。尚有其他请求等待的共享下载只取消当前等待者。下载完成不等于加载成功，load 失败保留已验证文件以供重试。空闲退出把 ready 改为 downloaded，下一次使用只校验和加载，不重复下载。

RemoveModelFiles 必须确认没有未结束的推理 / load 租约，必要时先停止对应闲置进程；共享固定引擎和其他模型仍引用的文件保留。它不卸载插件、不删除模型目录描述；所需文件被删除后回到 not-downloaded，共享文件仍完整时按实际情况显示 downloaded。registry 发布版本决定可下载资产范围，插件升级后目录和资产引用按新版本重新核对，不能沿用旧的信任判定。

status.error.code 是机器可读的宿主状态原因，不依赖解析 message；至少固定以下取值。它们不表示插件执行故障，UI 文案由 code 本地化：

| code | state | 含义与 UI 行为 |
| --- | --- | --- |
| model_not_ready | not-downloaded | 所需文件缺失；实现可用且准入条件满足时显示下载按钮 |
| insufficient_memory | error | 不满足当前平台内存阈值；禁止下载并使用 |
| unsupported_platform | error | 当前平台没有所需资产；禁止下载并使用 |
| update_required | error | 宿主尚未支持托管资产；提示更新，不显示下载按钮 |
| verification_failed | error | 哈希不符或含隔离标记；不得加载，提供显式重新下载入口 |

已有完整文件但尚未在当前进程加载时是 downloaded，不是 ready；Status 不因打开选择器进行全量哈希，下一次真正加载仍须 Verify。downloading / verifying / loading / ready 由宿主运行任务覆盖磁盘快照生成。普通 load 失败保留文件；verification_failed 的文件不视为已验证，只有用户显式 Prepare 才能下载修复，后台 Verify 不修复。

### 8.2 基础 API 与 #90 的固定接缝

以下 Go 类型、方法签名及语义由基础 API PR 定义，#90 不再另建一套模型目录或准备协议。§8.1 的 TypeScript 是视图形状，Go 的 ProviderModel / ListedModel 对应 §2 / §8.1 的同名描述；原生 JSON 按 §7，路径永不暴露给 UI。

```go
type ModelState string // 取值严格对应 §8.1。

type ModelError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e *ModelError) Error() string { return e.Message }

type ModelStatus struct {
	State           ModelState  `json:"state"`
	DownloadedBytes int64       `json:"downloadedBytes"`
	TotalBytes      int64       `json:"totalBytes"`
	Error           *ModelError `json:"error,omitempty"`
}

// modelAssets only manages files; the caller owns loading and readiness.
type modelAssets interface {
	Status(ctx context.Context, plugin, release string, refs map[string]string) (ModelStatus, error)
	Prepare(ctx context.Context, plugin, release string, refs map[string]string,
		progress func(ModelStatus)) (dirs map[string]string, releaseLease func(), err error)
	Verify(ctx context.Context, plugin, release string, refs map[string]string) (dirs map[string]string, releaseLease func(), err error)
	Remove(ctx context.Context, plugin, release string, refs map[string]string) error
}
```

- plugin 是 manifest.name；release 是宿主已验证且安装中的 registry release 内容摘要，不是“最新版本”、可变版本别名或插件自报值。基础 API 的模型快照预留并传递该摘要，#90 从可信 registry 解析它。refs 是该模型 assets 映射的副本；固定资产由实现从同一 release 补齐，调用方不用枚举固定资产。所有结果均绑定此 release；不能在操作途中改查最新版。升级、撤销信任或配置代数改变时，宿主取消过期任务，在 models.load / 提交状态前复核快照，过期结果不得发布。
- Status 只读取受信任元数据与磁盘快照，不下载、不启动插件、不加载模型、不重新哈希全部文件。返回值描述 not-downloaded / downloaded / error；不可用原因放在 ModelStatus.Error，Go error 为 nil。取消返回 ctx.Err()；其他无法读取状态的失败返回 Go error，由目录层转成该模型的 error 状态，不隐藏其他模型。下载字节数包含所需固定资产及所选变体，按文件去重；总量来自可信 release，不从插件列表取值。
- Prepare 仅供显式用户准备操作调用：下载缺失文件（或显式修复验证失败文件）并校验全部所需文件，然后返回目录与租约。Verify 不下载、不修复；每个新进程代及实际模型切换使用它，缺文件返回 Code=model_not_ready，坏哈希或 quarantine 返回 Code=verification_failed。两者均先检查平台、内存与 release 信任。
- Prepare / Verify 在校验前取得租约；成功时 dirs 为固定资产和所选变体目录的并集，releaseLease 非 nil、可安全重复调用；失败时返回 nil 目录与 nil releaseLease，实现自行释放已取得的资源。progress 可为 nil；回调串行报告 ModelStatus，只允许 downloading / verifying，携带整组文件的已下载字节 / 总字节，并在开始全量哈希前报告 verifying。它不报告 loading / ready，不以下载字节冒充哈希进度；运行时保存回调快照并另外管理加载状态。共享下载只取消当前等待者，最后一个等待者离开后才取消底层任务。
- 租约不是调用方 context 的附属物。Prepare / Verify 成功后，由运行时接管租约；即使 UI 取消或 Ask 已返回，也必须保留至相关原生调用真正结束、模型不再驻留，固定资产则至少至进程退出。加载 B 期间同时保留 A 的回滚租约，B 提交成功且 A 已释放后才释放 A 的模型租约；B 失败后释放 B 的时机也取决于插件是否仍持有其映射，无法确认时终止进程后释放。全局插件锁只保护快照和发布，不包住下载 / 哈希；同插件的准入门与资产租约负责串行和文件存活。
- Remove 不下载、不改设置；只删除目标变体中无租约、也不被其他已准备模型引用的文件，保留共享固定资产。仍有活动租约时返回 Code=model_unavailable，不部分删除。宿主可先停止闲置进程再重试；不能以调用方取消为由强删仍映射的文件。成功后按实际磁盘情况重算状态；其他模型仍引用同一变体时，目标仍可能显示 downloaded。

基础 API PR 在 Service 持有一个 modelAssets，统一通过 newModelAssets(s *Service) modelAssets 构造。默认实现对所有声明 assets 的模型：Status 返回 state=error、Error.Code=update_required（Go error 为 nil），Prepare / Verify / Remove 返回同码的 *ModelError，且不联网、不操作文件。#90 只替换该构造入口的实现及补齐资产侧逻辑，基础 PR 的目录、judge 调度和 UI 调用签名不变。默认实现也须优先尊重 ctx 取消。无托管资产的模型不调用此接口。

core 对 UI 的方法签名固定如下，均遵循现有 Service 的 context 风格：

```go
func (s *Service) ListModels(ctx context.Context) ([]ListedModel, error)
func (s *Service) PrepareModel(ctx context.Context, ref string) (ModelStatus, error)
func (s *Service) RemoveModelFiles(ctx context.Context, ref string) error
```

PrepareModel 等待完整准备 / 切换事务完成，成功返回 ready；失败返回最新 ModelStatus，业务失败可 errors.As 为 *ModelError，取消则返回 ctx.Err()，ABI / 执行失败保留原故障分类，不新增 task ID 或取消 RPC。UI 在现有异步调用方式中传入可取消 context；相同 release / 模型的并发等待者共享准备任务，单个等待者取消不取消其他等待者。进入不可取消的原生阶段后，等待者可先返回，但运行时继续持有槽位及租约，全部等待者取消时不得提交新的 JudgeModel 选择；若 B 已加载，按 §8 回滚 A 后才恢复准入。不同目标的准备事务按插件串行协调。

上述失败时的非零 ModelStatus 只供进程内调用使用；现有 RPC 在 error 非 nil 时不发送 result，远程 UI 必须在错误后重新 ListModels，不能依赖同时收到 status。基础 PR 在现有 rpcbind.NewCodec 中以 model_error 注册 *ModelError 的安全 typed detail（仅 Code / Message / Retryable），并以 judge_unavailable / judge_plugin 注册 ai.ErrJudgeUnavailable / ai.ErrJudgePlugin，沿用 Codec 的 typed detail + Cause 保留 errors.As / errors.Is，不新增 RPC 信封。typed detail 使用独立无 JSON tag 的字段投影，兼容现有 DecodeDetail 按 Go 字段名严格校验的规则；视图 / 原生 JSON 的小驼峰格式不改变。新方法由现有 Expose 暴露，仅允许 full 角色；PrepareModel / RemoveModelFiles 不授予 MCP 或插件脚本调用，避免绕过显式用户下载与模型选择。三者加入 contract.API / Remote 的同签名契约测试。

进度与状态变更通过现有事件总线新增 events.ModelsChanged（值为 models.changed）通知；事件只作失效通知，UI 收到后调用 ListModels 读取快照，不把它当可靠进度流，也不在事件中携带目录或业务输入。任务完成或失败也发送该通知；打开选择器时主动读取快照，允许中间通知丢失。状态错误使用固定 code 和脱敏文案，不暴露原始路径 / 上游异常。

#90 合入前，带 assets 的模型照常保留在目录里，状态为 update_required；PrepareModel 返回相同错误，UI 不显示下载按钮。基础 API PR 仍交付这些通用方法及默认实现，#90 接入真正的资产实现和下载 UI。正式 StartLux release 必须用 minGodwit 限制为支持完整托管资产能力的宿主版本。

## 9. 权限、调用范围与错误

capabilities.models 表示插件会接收所选模型的提示、上下文、工具定义，或判断输入。脚本授权卡必须明确这一点，能力变化纳入 PermissionHash；只注册模型不需要 mail:read 或 llm。其网络权限仍由 permissions.net 决定。用户启用并选择该 provider 后，模型调用不逐次弹出出站审批，与现有 translate 能力一致。

新增模型发现、请求适配、直接判断的调用范围，在交付含业务数据的参数前设置 holds_mail。listModels 可获取声明的元数据域名；prepare / judge 在此次模型调用已获授权的范围内使用网络和存储。后续普通 Action / Tool 的出站审批规则不能因此放宽。

模型回调禁止调用 ctx.llm。除此以外，宿主在调用链 context 中记录仍未返回的插件调用：Action / Tool 等调用 ctx.llm 时，若解析出的 provider 对应的脚本 VM 已在同一调用链中等待，准入前立即返回 model_reentry 错误、不入 VM 队列、不发 HTTP、不计插件故障，也不换用另一模型。这包含 A→A 和 A→B→A，避免外层等待内层、内层排队等待外层的死锁。独立调用链只是正常排队，不因 VM 正忙就被拒绝。首版不实现可重入 VM 或为每个能力另开 VM。

错误信封统一 code/message/retryable；JS 可以 throw 具有这三个字段的 JSON 对象，宿主验证其类型后作为业务错误。普通 JS Error 或无效对象属于执行失败。首版模型错误码为 invalid_request、model_unavailable、model_not_ready、model_reentry、rate_limited、upstream、internal；原生已有翻译错误码保持有效。插件内部应捕获预计的上游失败并返回业务错误，不能把解析错误、程序 bug 全部包装成 upstream。宿主生成的准入错误经异步 API 抛回 JS 时保持业务错误标记，不能被外层调用重新计为插件执行失败。

宿主状态 / 资产操作另使用 §8.1 的 insufficient_memory、unsupported_platform、update_required、verification_failed；这些是宿主诊断，不扩大插件可自行声明的业务错误集合。core 业务错误使用 §8.2 的 *ModelError，Code 保留原始原因，Message 脱敏，Retryable 不承诺后台自动重试。

基础 API PR 在 core/ai 定义 ai.ErrJudgeUnavailable 和 ai.ErrJudgePlugin。judge 的 Ask 适配层对 model_not_ready / model_unavailable 使用 errors.Join(ai.ErrJudgeUnavailable, err)，调用方可同时用 errors.Is 判断不可用、用 errors.As 取出原始 *ModelError。平台、内存、更新要求及资产校验导致的不可用也按同样规则处理并保留具体 code。分类器遇到 ErrJudgeUnavailable 停止本轮，失败 Ask 不产出部分答案、不写也不删涉及邮件的标签、不计插件失败；不回滚此前成功处理的邮件。取消保留 context 错误。坏答案、ABI 错误、执行超时和崩溃走 ai.ErrJudgePlugin 并保留插件故障分类。上述不可用或插件故障均不能伪装为 ai.ErrJudgeAnswer 或 HTTP 400，避免现有分类器将失败当作所有标签为否。

输入无效、用户取消、上游 403/429/5xx 及合法业务错误不计插件故障；坏返回值、坏答案、ABI 错误、插件执行超时和崩溃计插件失败。沿用连续故障停用机制。宿主 HTTP 上游失败和模型生成坏响应不归咎于适配插件的 ABI 稳定性。HTTP 重试只由宿主协议客户端执行；原生 models.judge 不在后台自动重复派发。

插件业务错误文案经凭据脱敏、长度限制后可展示；宿主传输 / 契约错误使用固定文案，不带原始请求、响应、异常堆栈或 URL 查询参数。retryable 只表示允许用户或上层按既有策略重试，不能跳过授权或偷偷替换模型。

## 10. 验证范围与实施边界

本需求先合入基础 API，#90 后续依赖该契约；当前均为设计分工，本文不声称已有基础 API 实现 PR。

| 部分 | 基础 API PR | #90 |
| --- | --- | --- |
| models 能力 | JS ctx.models.register、原生方法发现 / 分发、同契约校验 | StartLux 实现 models.list / load / judge |
| plugin judge 路径 | Ask 适配、每次一题、每插件准入排队、取消后槽位保持、错误映射；资产调用钩子与切换事务按本文预留并用假实现验证 | libllama 推理、真实资产租约和加载 / 回滚接入；固定引擎及模型目录统一通过 models.load.assets 交付 |
| 模型目录与准备接口 | 稳定引用、ListModels / PrepareModel / RemoveModelFiles、ModelState / ModelStatus / ModelError、通用状态通知及 update_required 默认实现 | 真实磁盘状态、进度、准备 / 删除、下载 UI |
| registry 与下载器 | 定义并接入 §8.2 的 modelAssets 边界 | assets schema、平台内存要求、可信 release 解析、下载 / 续传 / 校验、共享资产及租约实现 |
| 设置迁移 | JudgeService → JudgeModel，包含现有 HTTP judge，显式失效不静默 fallback | 不重复迁移 |
| 进程生命周期 | 通用调用、进程代数与槽位语义 | native.idle、stdin 关闭后 plugin-host 限时退出 |

基础 PR 的 plugin judge 必须用无托管资产的插件完成实际调用验证，并用假的 modelAssets 验证升级、切换、取消和租约接缝。#90 替换资产实现后，目录 / judge 调度 / UI 的既定调用签名不变；托管资产能力完成前，真实资产模型保持 update_required，不发布不可用的下载入口。两边共同验收原生取消后槽位与租约不提前释放、切换期间完整 Ask 的版本绑定，以及每进程代重新哈希。

实施至少覆盖：两种运行时的同契约模型发现；未下载模型仍在目录中；非法资产引用；五种 HTTP 路由与协议编码；插件直接 judge 的问题 / 答案校验；可选方法缺省和执行失败的区别；模型列表缓存、空列表、失败与配置代数竞争；下载进度、取消、共享下载、断点续传、失败重试；删除文件与资产租约；后台调用不暗中下载；空闲后只重新加载；prompt 不进入日志；请求头和出站检查；真实工具定义不被改写、占位工具不能执行；SDK 重试不累加适配；生成断流不提交状态；调用取消不误停用；StartLux 排队、超时、模型切换与回滚；卸载重装的稳定引用；JudgeService 迁移；不同协议并发。

新增接缝验证至少包括：内存阈值数值 / 平台匹配与非法映射；默认 update_required 模型可见且无下载按钮；Status 不做全量哈希；每进程代和实际切换重新验证固定资产及模型；quarantine 拒绝加载且不清除标记；校验可取消且不持全局插件锁；prepare / verify 成功与失败的租约归属；models.load 的精确 JSON 与文件名；发现目录不依赖资产已下载；下载期间升级不混用 release；共享下载单等待者取消；全部等待者取消后不提交选择；ErrJudgeUnavailable / ErrJudgePlugin 不进入标签否定分支；共享文件删除后状态按实际磁盘重算。跨平台内存与冷启动数据由 #90 的真实环境验证，本轮仅引用其报告。

Zen 上游实测独立验证文本生成、至少一次真实工具往返、两种生成 HTTP 协议以及免费判断模型是否可用。源码声明支持的协议不等于该免费模型此刻可用。StartLux 按 #90 保留真实 libllama 读出与参考实现的对照及资产验证。

本轮只修改本设计文档，不修改 SDK、产品代码、PR #90、registry 或插件产物，不暂存或提交。§8.2 的签名与语义是两个 PR 的固定接缝，尚未实施；其他示例中的业务辅助函数及 Go 类型是说明性的，不是已经存在的依赖。
