# 插件和 MCP 向用户提问：设计调研

调研于 2026-10-24，基于 PR godwit-email/godwit#203（`ask_user_question`、`QuestionCard`）和 go-sdk v1.8.0。只是设计，没有代码。涉及 ADR 0003、0004、0015、0018、0020。

## 结论

**提问一律是一次「先停下、答完再调用」的往返，不在调用中途挂起等待。** 扩展要问用户时，工具调用直接结束并返回「需要输入」；宿主把问题存成一行 SQLite 审批，界面显示提问卡片；用户作答等于批准一次带着答案的**重新调用**，走现有的 `decideExternal` 路径。

- MCP：只支持 2026-07-28 协议的 MRTR（SEP-2322，`input_required` 结果）。旧协议在工具调用中途发来的 `elicitation/create` 不支持。
- 插件：工具返回 `ctx.ui.ask(...)` 生成的「需要输入」结果，形状与 MRTR 对齐。不提供「await 用户」的宿主 API。
- 卡片沿用 `QuestionCard`，加上来源标识、目的地和几种字段类型。

理由：现有审批是持久的、可恢复的、至多执行一次的（ADR 0004）。一次挂在内存里的请求做不到这些，而 MRTR 本来就设计成无状态往返，正好能放进审批行。

## 1. MCP

### SDK 提供什么（go-sdk v1.8.0）

两条路径：

1. **旧路径，调用中途的服务器请求**：`ClientOptions.ElicitationHandler func(ctx, *ElicitRequest) (*ElicitResult, error)`。设置了 handler 就会声明 `elicitation` 能力（2025-11-25 及之后还会声明 `form`）。服务器在 `CallTool` 进行中发来 `elicitation/create`，handler 返回之前 `CallTool` 一直阻塞。SDK 先校验 schema，再按 schema 校验 `accept` 的内容（`client.go` 的 `elicit`）。
2. **新路径，MRTR**（`mrtr.go`，协议 `2026-07-28`，也是 SDK 的 `latestProtocolVersion`）：`CallTool` 返回 `resultType: "input_required"`，带上 `InputRequests`（id → `*ElicitParams`）和不透明的 `RequestState`。客户端重新调用 `CallTool`，带上 `InputResponses`（id → `*ElicitResult`）和原样的 `RequestState`。SDK 默认装了一个中间件，在内存里自动走完这个循环（最多 10 轮），走法是调用同一个 `ElicitationHandler`。设 `ClientOptions.MultiRoundTrip.Disabled = true` 后，`CallToolResult.NeedsInput()` 原样交给调用方。新协议的会话上，服务器端的 `ServerSession.Elicit` 不能再发起服务器请求（`assertServerInitiatedRequestAllowed`）。用 SDK 写的服务器即使是 MRTR 写法，遇到旧客户端也会退回旧路径（`serverMultiRoundTripMiddleware`）。

`ElicitResult.Action` 有三种：`accept`（带 `Content map[string]any`）、`decline`（用户明确拒绝）、`cancel`（用户关掉了，没有表态）。

### 规范允许的 schema（form 模式）

`requestedSchema` 是一个扁平的 `object`，只有顶层属性，属性都是原始类型（SDK 的 `validateElicitSchema` 会拒绝嵌套）：

| 字段 | 约束 | 卡片上的样子 |
|---|---|---|
| `string` | `minLength`、`maxLength`、`format`（email、uri、date、date-time）、`default` | 只有自由输入的问题（新加：没有选项） |
| `number` / `integer` | `minimum`、`maximum`、`default` | 数字输入 |
| `boolean` | `default` | 「是 / 否」两个选项 |
| 单选枚举 | `string` 加 `enum`，或 `oneOf: [{const, title}]`（旧的 `enumNames` 也接受） | 选项；4 个以内用现在的按钮，更多就用下拉列表 |
| 多选枚举 | `array`，`items` 带 `enum` 或 `anyOf`，`minItems` / `maxItems` | `multiSelect` |

每个属性的 `title` 对应卡片的 header，`description` 对应 description，`required` 决定能否跳过。顶层的 `message` 放在卡片最上方。规范要求服务器**不得**在 form 模式下索要敏感信息（密码、密钥）。URL 模式（打开一个网址，比如 OAuth）是另一种东西，**不声明、不支持**：它本质上是让一个服务器把用户引到任意网址。

### 映射到审批

1. 连接时设 `MultiRoundTrip.Disabled = true`，并且只声明 `elicitation.form`。
2. `callMCP` 拿到 `res.NeedsInput()`：
   - `InputRequests` 为空表示服务器在限流（load-shedding）。按工具调用失败处理，告诉模型「服务器忙」。
   - 里面有 elicitation 以外的请求（sampling、roots），或者 URL 模式：调用失败，提示「Godwit 不支持」。
   - 否则返回一个新错误 `errNeedsInput{requests, state}`。
3. Wit 的执行层（无论是运行中的受信任工具，还是 `decideExternal` 里已批准的调用）收到 `errNeedsInput` 时，**新建一行待审批**：`Tool` 仍是这个 MCP 工具，`Args` 是 `{args, inputRequests, requestState}`，`Binding` 是同一个 `version:definitionHash`，来源 `mcp:<name>`。然后 run 中断，原调用的审批记为 done，告诉模型「等待用户回答 <服务器> 的问题」，并且不允许模型自己补答。
4. 用户作答（`AnswerQuestion` 的扩展版，答案按 schema 转成有类型的 `Content`）就是批准这一行。执行就是 `decideExternal`：重新检查 binding，然后用 `InputResponses` 和 `RequestState` 再调一次 `CallTool`。结果回到模型的方式和今天一样（`mcpUntrusted`）。如果又返回 `input_required`，就再建一行，**每个调用最多 3 轮**，超过就失败。
5. 逐题跳过可选字段：这些字段不放进答案。全部跳过就拒绝这次调用，**不再联系服务器**（MRTR 是无状态的，服务器不需要收尾），并告诉模型用户没有回答。`decline` 和 `cancel` 暂不发送。如果某个服务器需要知道用户拒绝了，再加。

### 重启、取消、超时

- **重启**：问题行留在 SQLite 里。答完后在新连接上带 `RequestState` 重调。MRTR 要求服务器把需要的状态放进 `RequestState`，所以在规范上行得通。服务器如果拒绝了过期状态，就是一次普通失败，会告诉模型。重调属于 external 执行，崩在执行中时，照 ADR 0004 记为 `uncertain`，不重试。
- **取消**：`CancelRun` 把待答问题和其他待审批一样关闭，不联系服务器。MRTR 是无状态的，服务器那边没有挂着的东西。
- **超时**：等待用户的时候没有任何连接或请求开着，所以不需要用户超时，问题可以像其他审批一样一直挂着。`mcpCallTimeout`（60 秒）仍然只约束每一次实际调用。
- **服务器版本或工具定义变了**：binding 不符，问题行被标为 stale（今天就是这样）。

### 被否决的选项：支持旧的调用中途 `elicitation/create`

可行的做法是：handler 建一行「live」问题，等用户回答期间暂停 `mcpCallTimeout`，另设比如 10 分钟的上限，到时回 `cancel`；启动时把残留的 live 行标为 stale。不做的原因：

- **请求只在内存里**：重启、断线或服务器被停，请求就丢了。卡片和调用的结局只能靠一套新的「live」状态补救，ADR 0004 的至多一次也得为它开例外。
- **归属不清**：handler 的 `ctx` 不说明这个 elicitation 属于哪次 `CallTool`。stdio 上同一服务器有并发调用时，没法可靠地挂到某个 run 的某次调用上。
- **运行中的受信任调用**会让 run 停在「running」状态等用户，而现在的界面和 `recoverRuns` 都假设等待中的 run 是「interrupted」。
- 新版 SDK 写的服务器在新协议下自动改用 MRTR，旧路径正在退出。

代价：只支持旧协议的服务器问不了问题。它们得到的是「客户端不支持 elicitation」（没声明能力；如果服务器还是发了，handler 回 JSON-RPC 错误，而不是替用户回 `cancel`）。需要的话，以后可以在**仅 approved external 调用**上补一个有上限的 live 路径。

## 2. 插件

现状：脚本插件的宿主调用（`ctx.mail.*`、`ctx.llm.*`）是 VM 里的 Promise。可 VM 一次只跑一个调用（`VM.Call` 走一个 channel），外部调用的上限是 `externalTimeout` 60 秒。插件如果 `await ctx.ui.askUser()` 等用户几分钟，它的翻译、Hook 和其他工具都会被堵住，重启后还得重放 JS 状态。所以**不做 await 式 API**。

做法和 MCP 一样是往返：

```js
ctx.tools.register({ name: "book_room", /* ... */ async run(args, input) {
  if (!input?.answers) return ctx.ui.ask({ message: "订哪间？", schema: {/* 同 MCP requestedSchema */}, state: "..." })
  // input.answers：{ action: "accept" | "decline" | "cancel", content }
}})
```

- `ctx.ui.ask` 只生成一个带标记的返回值，宿主据此识别，插件不能伪造别的形状。字段直接用 MCP 的 `requestedSchema` 子集，这样卡片只需要一个转换器。
- `state` 是插件自己的不透明字符串（有大小上限），存在审批行里，重调时原样还回去。插件的内存状态不需要跨重启存活。
- 原生插件（ADR 0015、0018）：在工具结果的 JSON 信封里加同样的 `input_required` 字段，两种运行时的请求和响应保持一致。ADR 0018 说宿主 API 只给 JS，这里不冲突：这是一种**结果形状**，不是宿主回调。
- 只有**工具**可以问。Action 由用户亲手触发，有它自己的界面；Hook、`message.received`、翻译、模型这些后台调用没有地方显示卡片。它们返回 `ask` 时一律按错误处理。

零代码的替代方案：工具返回错误，比如「缺少 room 参数，请向用户询问」，模型再用 `ask_user_question` 去问。这能用，但插件没法确认答案真的来自用户，模型可能自己编一个。凡是要用户亲自确认的场景，都需要宿主中转的提问。可以在插件文档里把它列为不需要确认时的写法。

## 3. 卡片

沿用 `QuestionCard`（`web/src/wit/Panel.tsx`）和 `questionnaire.tsx`，补充这些：

- **来源标识**，放在标题行，不能被内容覆盖：「MCP 服务器 *名字* 在问」或「插件 *名字* 在问」，用宿主的图标和名字，不用服务器自报的名字或图标。`message` 和各个字段的文本都按**纯文本**显示（不渲染 Markdown 和链接），视觉上和 Wit 自己的提问区分开。
- **目的地**一行：「你的回答会发给 mcp:*名字*」，插件则写插件名，插件声明了 `net` 主机时一并列出。
- 固定提示：「Godwit 不会在这里要你的密码或验证码」。
- 字段类型：除了现有的选项和多选，新增纯文本、数字、日期、是 / 否，以及多于 4 个选项时用的下拉列表。按 schema 做前端校验，后端再校验一次。
- 三个结局：提交（accept）、拒绝（decline）、关闭或跳过（cancel）。必填题不能单独跳过。

## 4. 安全（ADR 0003、0004）

- **谁能问**：只有正在 Wit run 里执行的 MCP 或插件**工具调用**能问，而且只能问用户，问题落在发起这个 run 的会话里。后台事件、翻译、模型请求、Action 都不能问。每个调用最多 3 轮，每轮的字段数和文本长度有上限（沿用 `ask_user_question` 的 1–4 题，扩展字段先限制在 8 个以内）。
- **没有自动作答**：问题只能由 `AnswerQuestion` 的扩展版接受。「全部允许」「本次对话都允许」和受信任工具的标记都不替用户作答（#203 的 `ResolveAll` 已经跳过问题，扩展的问题沿用这一点）。没有默认答案自动提交。旧协议在调用中途发来的表单不会被自动回 `cancel` 或 `decline`，而是得到协议错误（客户端没有 handler）。
- **外发与 taint**：答案是一次外发，目的地就是这个扩展（`mcp:<name>`，或者插件及其 net 主机）。用户可能把邮件里的内容填进答案，而且原调用所在的 run 可能读过邮件。重调走 external 执行，所以仍然受 ADR 0004 的出口检查。区别在于：**作答卡片本身就是这次外发的审批**，卡片上显示目的地，用户不必再批一次。对插件来说，重调前和今天一样先标 `holds_mail`（如果 run 读过邮件）。
- **至多一次**：重调是一次新的 external 执行，崩在执行中就记 `uncertain`，不再重发（ADR 0004）。工具可以用调用 ID `approval:<id>` 自己去重。
- **不发信**（ADR 0003）：这个机制不给扩展新增任何能力，只是把用户的回答交给扩展。
- **审计**：服务器每次提问写一行 `input_required` 审计，重调照常写 `executed` / `failed`。答案按用户填写的原样存在审批行里（要原样发出，所以不做 Redact），审计和日志里不记答案。
- **模型看不到答案**：MCP 和插件的答案只发给扩展，不进入模型上下文。模型只看到扩展返回的结果，而这个结果照常标为不可信数据。

## 5. 实施顺序

前三步（MCP）已实现：表单存在 `approvals.input` 列（`core/mcp_question.go`），卡片读 `Approval.form`。

1. 后端的通用「扩展提问」审批：`Args` 的形状、`AnswerQuestion` 支持带类型的内容、binding 检查、轮数上限、`ResolveAll` 跳过。
2. MCP 的 MRTR：关掉自动中间件，`callMCP` 处理 `NeedsInput`，两条执行路径建问题行；用 `mcpTestTransport` 起一个 MRTR 测试服务器。
3. 卡片：来源、目的地、新字段类型。
4. 插件：`ctx.ui.ask`，工具重调时带 `input`，原生插件的信封字段，插件文档。

## 待定

- 先只做 MCP 吗？插件提问目前没有具体的使用者。
- 扩展提问的字段上限：8 个是否合适？超过 4 个选项时用下拉列表，还是放宽卡片？
- 旧协议的服务器是否需要上面那条被否决的 live 路径（取决于用户实际在用的服务器）。
