# 插件模型 Provider 与 Zen Free 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用户启用 Zen Free 插件后即可选择其免费模型；宿主同时提供 JS / 原生统一模型 API、plugin judge 路径及 #90 可直接接入的资产边界。

**Architecture:** 复用现有插件生命周期、Genkit / OpenAI / Anthropic HTTP 客户端、判断契约及模型引用。插件发现模型并适配请求，宿主执行 HTTP、解析流和控制工具。基础 API 先交付完整的无资产调用及 update_required 默认资产实现；StartLux 的真实下载 / 校验 / libllama 由 #90 接入。

**Tech Stack:** 主线 Go 1.27.1、goja、现有 Genkit / SDK、purego 原生 ABI v1、SQLite、mygo UI、testify；Zen Free 使用无 Node 运行时依赖的 TypeScript，Bun 只用于插件测试和打包。

**Spec:** `docs/superpowers/specs/2026-10-17-plugin-model-provider-design.md`，包含已接受的七条评审及删除 configure.assets 后的修订。

## Global Constraints

- 这是一份待实施计划。本轮只新增本文，不改产品代码或设计文档，不 git add / commit。实施时也先保留用户“不暂存、不提交”的约束，直至用户明确改变。
- 核对基线为 Godwit `origin/main`：`6f44bdc3a89fe4d639daca488b828e0bc5eda175`；已用只读 ls-remote 确认。当前工作分支是 `codex/fix-store-publish-package`，包含其他会话改动，缺少原生插件 / registry 主线实现。执行时从届时主线建立隔离工作树，再带入已审阅的设计与计划；不切换、重置或清理当前工作区。
- 插件代码最终放在兄弟仓库 `/Users/baran/Documents/workspace/godwit-email/plugins/zen-free/`；Godwit 仓库只放宿主支持、SDK、测试夹具和文档。两个仓库分别审阅，不把插件源码塞进 Godwit。
- HTTP 协议固定为 `openai-completions / openai-responses / anthropic-message / typesafe-systemone / openai-decisions`；路径逐项遵循设计 §2，不由 SDK 重复增加 `/v1`。
- 一个插件一个 provider；模型引用是宿主分配的 `slug/model-id`。插件 provider 不复用人工服务凭据，不因启用插件改变用户选择；模型 id 可以含 `/`。
- 回调输入、输出和模型目录各限 `1 << 20` 字节；不截断。JS 调用 30 秒，原生 load / 单题各为派发后 60 秒；排队、下载、宿主哈希不占原生预算。
- plugin judge 每插件一个槽位、每次一题、完整 Ask 绑定一次配置；本地模型仅由 `Settings.JudgeModel` 选择，`ctx.llm.decide(input, questions)` 不加模型参数。
- configure 始终为 `{"config": {...}}`；固定引擎和模型目录只在 `models.load.assets` 中交付。固定资产租约至少保持到进程退出。
- 基础 PR 定义设计 §8.2 的 `modelAssets` 和三个 Service 方法，默认返回 `update_required`。不实现 AssetVariant / 下载器 / 内存门槛解析 / 真正哈希或 quarantine 检查 / libllama / native.idle manifest / stdin 关闭后的限时退出；这些由 #90 完成。
- HTTPS、声明域名、DNS/IP 检查、禁重定向、holds_mail、PermissionHash、回入检测、凭据脱敏及真实工具集合验证是交付条件。原生自行联网无沙箱，不能宣称它只访问 net 域名。
- 不增加推理代理服务器、Node / WASM 运行时、流式原生 ABI、通用下载框架或价格 / 配额 / 推理档位字段。使用现有依赖。

## Review Focus

1. 同一插件不同协议并发导致实例互相取消，或与人工服务的凭据串用。→ Task 4 的 `TestPluginModelsProtocolIsolation`。
2. 宿主暂停等待后，原生推理仍执行，下一请求抢到槽位或资产被删除。→ Tasks 5–6 的 `TestPluginJudgeCanceledWaiter` / `TestPrepareModelLeaseLifetime`。
3. 下载期间插件升级 / 撤权，旧结果被交给新进程。→ Tasks 2、6 的 `TestPluginModelsStaleRefresh` / `TestPrepareModelStaleRelease`。
4. HTTP SDK 重试叠加占位工具，或占位工具调用获得执行权限。→ Tasks 3–4 的 `TestPluginModelRetryStartsFresh` / `TestPluginModelPlaceholderDenied`。
5. RPC 返回错误而丢失状态，或失效的 judge 被当作“标签全部为否”。→ Tasks 7–8 的 `TestPluginJudgeUnavailableKeepsLabels` / `TestModelErrorRPCRoundTrip`。

---

## 实施顺序与文件责任

Tasks 1–8 构成 Godwit 基础 API PR；Task 9 是插件仓库的 Zen Free 源码变更，依赖基础 SDK；Task 10 验证两者。#90 后续接入，不能成为 Zen Free 的前置依赖。以下 Create / Modify 都相对上述主线，不以当前旧工作分支是否有这些文件为准。

| 区域 | 落点与责任 |
| --- | --- |
| 模型 DTO / JS 注册 | `core/plugins/models.go`、`core/plugins/host.go`、`sdk/godwit.d.ts` |
| 原生握手 / 实际进程绑定 | `core/extensions.go`、`core/plugins/native.go`、现有 native 测试夹具；ABI 函数表不变 |
| 目录 / provider 身份 | `core/pluginmodels.go`、`core/models_slug.go`、`core/store/schema.go`、现有 registry 读取 |
| 推理 HTTP 管线 | `core/modelhttp.go`、`core/ai/modelhttp.go`、`core/plugins/net.go`、现有 AI runtime / judge |
| plugin judge / 准备事务 | `core/modeljudge.go`、`core/modelassets.go`；不单独建立另一个模型注册中心 |
| 设置 / 调用方 | `core/settings.go`、`core/models.go`、`core/pluginhost.go`、`core/labeler.go`、`core/conversations.go` 及全部直接 AI 调用点 |
| UI / RPC | 现有 `app/settings_models*.go`、权限卡 / 事件刷新、`rpcbind`、`contract`、双语 i18n |
| Zen Free | 插件仓库 `zen-free/` 的源码、Bun 测试、构建脚本、说明和许可证 |

### Task 1：模型契约与 JS / 原生能力发现

**Files:** Create `core/plugins/models.go`、`core/plugins/models_test.go`、`core/modelassets.go`、`core/modelassets_test.go`；Modify `core/plugins/{manifest.go,host.go,types.go,vm.go,manifest_test.go,host_test.go,native_fake.go}`、`core/{extensions.go,native_test.go,settings.go}`、`sdk/godwit.d.ts`、`sdk/native/godwit_plugin.h`、`core/plugins/testdata/sdk-types/index.ts`。

**Interfaces:** 在 plugins 定义设计 §2–3、§7 的 `ProviderModel / ModelsResult / ModelHTTPRequest / PrepareModelRequest / ModelLoadRequest / JudgeModelRequest / JudgeModelResult` Go DTO；判断字段复用现有 `plugins.Question / Answer`（它们已经是 ai 类型别名）。Go 用平面判别 DTO 并严格验证 transport 对应字段，core 用 `type ProviderModel = plugins.ProviderModel` 复用。ModelLoadRequest 只有 ModelID（JSON modelId）和 Assets（JSON assets）两个字段，Assets 为 map[string]string。新增方法：

```go
func (p *Plugin) ListModels(ctx context.Context, inv Invocation) (ModelsResult, error)
func (p *Plugin) PrepareModel(ctx context.Context, inv Invocation, req PrepareModelRequest) (ModelHTTPRequest, error)
func (p *Plugin) JudgeModel(ctx context.Context, inv Invocation, req JudgeModelRequest) (JudgeModelResult, error)
```

- [ ] **写失败测试并运行。** `TestModelsRegistration` 断言初始化只能注册一次、必须有 models capability、初始化不可联网；`TestModelsDescriptionValidation` 覆盖重复 id、未知协议、非法 URL、混入不属于 transport 的字段、JS assets、超过 1 MiB。
  ```go
  require.Error(t, duplicateRegistrationErr)
  require.Error(t, jsManagedAssetsErr)
  require.ErrorIs(t, oversizedResultErr, ErrTooLarge)
  ```
  Run: `go test ./core/plugins -run 'TestModels' -count=1`；预期新契约测试因尚未实现而失败，排除环境错误。
- [ ] **实现最小契约。** knownCapabilities 加 models，PermissionHash 沿用能力哈希；newCtx 加 models.register，goja.Callable 只留在 VM 内。列出 plugin judge 却没有 judge 回调是契约错误；prepare 缺省原样，回调失败不得伪装为缺省。新增模型发现 / 适配 / judge invocation kinds，模型回调拒绝 ctx.llm。
- [ ] **先定义跨任务宿主类型。** 在 core/modelassets.go 原样定义设计 §8.2 的 ModelState / ModelStatus / ModelError / modelAssets 和 newModelAssets 的 update_required 默认实现，先以 TestModelAssetsDefault 验证不下载、不操作文件且尊重取消。同时定义 ListedModel，字段为 Ref string、Model ProviderModel、Status ModelStatus，JSON 分别为 ref / model / status。Settings 先增加 JudgeModel 字段，保留旧 JudgeService；迁移和删除旧字段统一留给 Task 7，避免中间任务或现有 app 无法编译。core.ProviderModel 复用上述别名；plugins 层返回其业务错误，进入 core 时转换成 ModelError，不让 plugins 导入 core。
- [ ] **扩展原生握手。** `setUpNative` 验证 models 的 describe.methods：list 必需，其他支持情况显式记录；获得目录后再验证 judge / load 要求。旧 translate 没 methods 仍可启动；configure JSON 不变。C 头文件只补 JSON 方法文档，不改 ABI 布局。扩展现有 FakeNativeOpener 返回 models 方法结果、业务错误和可控阻塞。
- [ ] **验证。** `go test ./core/plugins ./core -run 'TestModels|TestNative|TestManifest|TestPermission|TestEgress' -count=1`；补 SDK 夹具并复用现有 esbuild 编译检查，确认无运行时函数 / goja 对象越出 VM。

### Task 2：统一目录、稳定 provider 身份与版本快照

**Files:** Create `core/pluginmodels.go`、`core/pluginmodels_test.go`；Modify `core/{models_slug.go,extensions.go,registry.go,service.go}`、`core/store/schema.go`、相关 slug / store 测试。

**Interfaces:** `func (s *Service) ListModels(ctx context.Context) ([]ListedModel, error)`；补齐设计要求的显式刷新入口 `func (s *Service) RefreshModels(ctx context.Context) ([]ListedModel, error)`，保留 §8.2 已固定的三个方法签名。二者统一返回人工服务及插件的可选模型。内部 `modelSnapshot` 包含插件名、provider slug、描述、配置版本、runningPlugin.gen 和不可变 release 摘要；它不含 goja 对象。

- [ ] **写失败测试并运行。** `TestPluginModelsCatalog` 列出未下载模型；`TestPluginModelsStableSlug` 覆盖与人工服务重名、重启、升级、卸载重装、模型 id 内含 `/`；`TestPluginModelsStaleRefresh` 在刷新中升级 / 撤权后不发布旧结果。
  ```go
  require.Equal(t, before.Ref, afterReinstall.Ref)
  require.Empty(t, stalePublishedModels)
  require.Equal(t, 1, concurrentRefreshCalls)
  ```
  Run: `go test ./core -run 'TestPluginModels' -count=1`；预期失败。
- [ ] **实现持久身份。** 增加 `plugin_model_providers(plugin TEXT PRIMARY KEY, slug TEXT NOT NULL UNIQUE)`，slug 分配查询人工服务、其历史名称保留和该表；人工服务新增也检查此表。卸载只保留这张身份墓碑，不保留配置、凭据或资产。不得用负 service ID 或插件名哈希冒充人工服务 ID。
- [ ] **实现缓存与刷新。** ListModels 使用 1 小时目录缓存，过期 / 首次触发合并刷新；RefreshModels 显式刷新并合并并发。成功空列表清空，失败保留旧目录并标记刷新失败，未知模型只触发一次刷新。回调在 pluginsMu 外执行，发布时复核快照，时间变化不增加模型版本；启停、配置、授权、升级及 registry 撤销使相关运行失效。
- [ ] **绑定 release。** 从已验证 registry 找到当前安装文件对应的 release，用其规范 JSON（Go json.Marshal，忽略 Parsed）SHA-256 作为摘要；按摘要保留不可变快照，不按最新版查。侧载没有 registry release 时不得提供托管下载。当前基础默认实现允许保留 asset 引用显示 update_required；真实引用 / AssetVariant 交叉校验由 #90 的资产实现完成，不伪造 registry assets schema。
- [ ] **验证。** `go test ./core ./core/store -run 'TestPluginModels|TestModelSlug|TestMigration|TestRegistry' -count=1`；补刷新失败状态的非邮件诊断字段，不将凭据或请求写入缓存。

### Task 3：每次 HTTP 尝试的适配、安全校验与流式出站

**Files:** Create `core/modelhttp.go`、`core/modelhttp_test.go`、`core/ai/modelhttp.go`、`core/ai/modelhttp_test.go`；Modify `core/ai/types.go`、`core/plugins/{net.go,net_test.go}`、`core/pluginhost.go`。

**Interfaces:** ai 增加 `ModelSession{ID string; CreatedAt int64}`（Unix 毫秒）、`type RequestPreparer func(context.Context, *http.Request) (*http.Request, error)` 及 `func PreparingClient(base *http.Client, prepare RequestPreparer) *http.Client`。plugins 增加 `func (c *Client) DoModel(ctx context.Context, allowed []string, req *http.Request) (*http.Response, []Hop, error)`，只暴露经策略检查的流，不复用会缓冲响应的 fetch。core 的 `prepareModelHTTP(ctx context.Context, snap modelSnapshot, meta ai.ModelSession, req *http.Request) (*http.Request, error)` 连接插件 DTO 与 SDK 的 HTTP 请求。

- [ ] **写失败测试并运行。** `TestPluginModelRetryStartsFresh` 同一 SDK 请求被重试时，每次回调看到原始工具列表、requestId 不同；`TestPluginModelEgress` 覆盖私有 DNS、混合 DNS、跨域 URL、userinfo、重定向、非法头；`TestPluginModelInputLimit` 检查超限发生在回调前且不计故障。
  ```go
  require.Equal(t, originalBody, secondAttemptInput)
  require.NotEqual(t, firstRequestID, secondRequestID)
  require.Zero(t, privateDestinationCalls)
  ```
  Run: `go test ./core/ai ./core/plugins ./core -run 'TestPluginModel(Retry|Egress|Input)|TestPreparingClient' -count=1`；预期失败。
- [ ] **实现 request-only wrapper。** 在 RoundTripper 边界从 SDK 本次原始请求构造副本，每次尝试调用 prepare，禁止改写原始 Body / GetBody。返回结果重新编码，宿主重算 Content-Length / Host，过滤 hop-by-hop 等传输头；User-Agent 在模型管线允许。原有 ctx.net.fetch 的 User-Agent 策略保持原样。
- [ ] **实现结构保护。** 按协议验证 model、stream=true、必需字段、原始工具名称 / schema / 关联选择字段；允许增加占位工具，不允许移除 / 替换真实工具。prepare 输入和输出均计 1 MiB；完整原生结果 / JS 输出同样受限，不以原生更大默认缓冲代替此上限。
- [ ] **实现安全流式管线。** DoModel 复用 Client 的 check / dial / Transport，禁代理、禁重定向、不读取完整推理响应。授权和快照发送前复核；先设置 holds_mail 再把输入交付插件；沿用安全目的地审计和脱敏，不在日志中写提示、响应正文或敏感查询参数。
- [ ] **验证。** 重跑上述测试及 `go test ./core/plugins -run 'TestNet|TestEgress' -count=1`；可控流在 EOF 前即交付首块，取消关闭连接，不改变普通 fetch 行为。

### Task 4：五种 HTTP 协议、实例隔离与会话身份

**Files:** Modify `core/ai/{types.go,runtime.go,runtime_test.go,responses.go,judge.go,judge_test.go,judge_decisions.go,judge_systemone.go}`、`core/{models.go,conversations.go,conversations_test.go,approvals.go,pluginhost.go}`、`core/store/schema.go`；Create `core/modelhttp_integration_test.go`。

**Interfaces:** `ai.ResolvedModel` 增加 `Provider string`（人工服务为空，插件为 manifest.name）；不向该可比较结构加入回调 / map。消费 Task 3 的 ModelSession，`ai.Request` 增加 `Session ModelSession`。Engine 新增 `Prepare func(context.Context, ResolvedModel, ModelSession, *http.Request) (*http.Request, error)`；Current 调整为 `func(context.Context, ResolvedModel) (int64, error)` 并修复全部调用 / 测试。宿主安装回调，人工服务直接沿原路径执行。

- [ ] **写失败测试并运行。** `TestPluginModelsHTTPProtocols` 表驱动五条路径，检查 Anthropic 最终无双重 /v1；`TestPluginModelsProtocolIsolation` 同 provider chat / responses 并发、升级后都停止，且不串人工服务 key；`TestPluginModelSessionResume` 覆盖工具续轮、审批恢复和重启。
  ```go
  require.Equal(t, "/inference/v1/messages", receivedPath)
  require.Equal(t, beforeSession, resumedSession)
  require.NotEqual(t, providerCredentials, manualCredentials)
  ```
  Run: `go test ./core/ai ./core -run 'TestPluginModelsHTTP|TestPluginModelsProtocol|TestPluginModelSession' -count=1`；预期失败。
- [ ] **复用协议客户端。** 将三个生成 protocol 映射到现有 KindOpenAI / KindResponses / KindAnthropic，将两种 judge 映射到 JudgeFormat；插件请求经 PreparingClient 才进入原有解析器。为插件实例注入的内层 RoundTripper 调用 Client.DoModel，以当前请求 context / 快照绑定出站检查和响应流；不能在 prepare 后回到普通 Transport 绕过策略。人工服务保留现有客户端。HTTP judge 沿用原重试、批处理和超时，不套 plugin judge 的单题 / 60 秒规则。无 API key 时不能从环境或人工服务取凭据。
- [ ] **修正实例键。** engine 实例键包含 provider 身份（人工 service ID 或插件 Provider）、Kind、Version；API key 轮换沿用现有退休实例逻辑。Forget(id) 遍历该人工服务全部实例，新增 `ForgetProvider(name string)` 清理插件全部协议；Current 根据对应身份查询版本。不得让插件沿仅查询 model_services 的旧回调通过或误失败。
- [ ] **持久会话身份。** conversations 增加独立随机 `model_session_id` 和毫秒 `model_session_created_at`，老行首次使用时原子填充；不直接外发数据库 ID / 机器身份。重启恢复读同一字段，工具续轮使用同一 Session。无 conversation 的任务在宿主创建一次性 Session，HTTP judge 的同一 Ask 重试共用它；每次真实 HTTP 尝试的 requestId 由 core 用 crypto/rand 新建。
- [ ] **检查全部调用点。** 用 `rg 'ai.Request\{|\.Run\(|NewJudge\(|\.Current' core` 修复一并涉及的 skills / quick replies / Assist / actions 和恢复路径，避免只有聊天请求获得会话字段。session 元数据通过每次 run 的 context 交给 HTTP wrapper，不写全局可变当前模型。
- [ ] **验证工具与流。** `TestPluginModelPlaceholderDenied` 断言占位 / 未知工具执行次数为 0，大小写不做模糊映射；`TestPluginModelTruncatedStream` 在三种生成流截断时无新状态提交，所有失败沿用上一轮状态。
- [ ] **验证。** `go test ./core/ai ./core -run 'TestPluginModel|TestResponses|TestEngine|TestJudge|TestConversation|TestApproval' -count=1`。

### Task 5：plugin judge、实际进程绑定与回入保护

**Files:** Create `core/modeljudge.go`、`core/modeljudge_test.go`；Modify `core/plugins/{native.go,native_test.go,vm.go,host.go,native_fake.go}`、`core/{pluginhost.go,extensions.go}`、`core/ai/judge.go`。

**Interfaces:** 复用 `ai.Judge.Ask`，新增 `ai.ValidateAnswers(qs []Question, got []Answer) ([]Answer, error)` 导出现有校验逻辑，并定义 ai.ErrJudgeUnavailable / ai.ErrJudgePlugin。原生增加 `NativeProcess.WithProcess(ctx context.Context, fn func(NativeSession) error) error`；`NativeSession` 包含 `Generation uint64`、`Done <-chan struct{}`、`Call func(context.Context, string, any) (json.RawMessage, error)`。它在一次实际进程上完成 describe/configure 后固定进程并阻止 idle，Call 不自动切换到重启的新进程；Done 在该进程真正退出时关闭。

- [ ] **写失败测试并运行。** `TestPluginJudgeOneQuestion` 输入 3 题，实际 3 次单题且按原序返回；`TestPluginJudgeBatchAtomic` 第二题失败返回 nil 答案；`TestPluginJudgeCanceledWaiter` 调用方取消后第二个 Ask 仍不能进入真实调用。
  ```go
  require.Len(t, eachCall.Questions, 1)
  require.Nil(t, partialAnswers)
  require.Equal(t, 1, maxConcurrentJudgeCalls)
  ```
  Run: `go test ./core ./core/plugins -run 'TestPluginJudge|TestNativeSession' -count=1`；预期失败。
- [ ] **实现调度。** 每插件一个协调器，完整 Ask 在准入时读一次当前选择和版本；所有单题执行完才交出 Ask 所属准入。排队取消直接移除。执行原生调用的内部 worker 与等待者 context 分离，调用者取消只停止等待；worker 等实际结果 / 进程退出 / 看门狗终止后才释放外部槽位，不能仅依赖现有 NativeProcess 的 4-slot semaphore。
- [ ] **绑定进程。** WithProcess 在实际进程建立后、Verify 之前 pin 住该进程，保证校验与 load / judge 属于同一 Generation；idle / 崩溃后的新进程清空 loaded 状态，第一次使用重新 Verify + load。复用当前原生派发看门狗，不新增 sleep 轮询；不实现 #90 的 idle manifest 或 stdin 退出政策。
- [ ] **实现回入保护。** 调用链 context 记录尚未返回的插件 VM；模型回调禁 ctx.llm，Action / Tool 的 A→A、A→B→A 在入队前返回 model_reentry，独立调用链正常排队。异步 JS 拒绝保持业务错误标记，不在外层重新计失败。测试两个循环和独立并发。
- [ ] **错误边界。** 合法业务错误转换成 core.ModelError；坏答案、ABI、超时、崩溃标记 ErrJudgePlugin，取消和 model_reentry 不计故障。输入和问题先验证，不把输入超限当拒答。
- [ ] **验证。** `go test ./core/plugins ./core -run 'TestPluginJudge|TestNativeSession|TestModelReentry' -count=1`；使用信号通道 / 测试时钟验证并发与超时，不写依赖机器速度的 sleep 断言。

### Task 6：固定资产接缝与准备 / 切换事务，默认不下载

**Files:** Modify Task 1 的 `core/modelassets.go`、`core/modelassets_test.go`，以及 `core/{service.go,pluginmodels.go,modeljudge.go}`、`core/events/bus.go`。

**Interfaces:** 原样采用设计 §8.2 的 `ModelState / ModelStatus / ModelError / modelAssets`、`newModelAssets(s *Service) modelAssets`、`PrepareModel(ctx, ref) (ModelStatus, error)`、`RemoveModelFiles(ctx, ref) error`；不删 release 参数、不改 progress func(ModelStatus)。测试在 core 包内替换 Service 的 modelAssets 字段，不新增公开测试接口或 RPC。

- [ ] **写失败测试并运行。** `TestModelAssetsDefault` 检查所有 asset 模型可见但 update_required；`TestPrepareModelSwitch` 验证 A 的完整 Ask 排空后 load B；`TestPrepareModelLeaseLifetime` 取消、失败、回滚和崩溃均不提前释放固定资产。
  ```go
  require.Equal(t, "update_required", status.Error.Code)
  require.Zero(t, realNetworkCalls)
  require.False(t, engineLeaseReleasedBeforeProcessDone)
  ```
  Run: `go test ./core -run 'TestModelAssets|TestPrepareModel|TestRemoveModelFiles' -count=1`；预期失败。
- [ ] **接入默认资产供应器。** 复用 Task 1 的实现，Service 初始化时持有供应器，目录层对 asset 模型调用 Status；验证 Status 的不可用原因不变成整份目录失败。不解析未来资产 schema、不访问磁盘模型、不创建 downloader。
- [ ] **实现通用协调器。** 消费假的 / 将来的真实供应器，按 release + model + 配置代数合并等待者；下载时 A 可运行，哈希 / Prepare 回调更新 downloading / verifying，进入切换门排空 A，然后仅用 `models.load` 交付完整目录。load 成功才原子写 JudgeModel / ready；取消所有等待者不提交选择，必要时回滚 A。
- [ ] **实现生命周期。** Prepare 的验证结果仅在同进程代连续使用；如果期间重新启动，重新 Verify。load / judge 和固定引擎通过 NativeSession.Done / 实际调用完成信号控制租约；无法确认 B 是否仍持有映射时终止进程后释放。Remove 先协调停止闲置进程，活动租约拒绝且不部分删除；共享文件仍在时不硬标 not-downloaded。
- [ ] **写并发接缝测试。** `TestPrepareModelStaleRelease` 验证等待期间升级 / 撤权不发布结果；`TestPrepareModelSharedCancel` 单等待者取消不取消他人、全部取消不改设置；`TestPrepareModelRollbackFailed` 回滚失败停止派发。假实现记录 Verify 的模型、release、次数，覆盖每进程代、A→B、回滚；每次原生 load 接收的键 / 目录逐项精确比对。
- [ ] **验证。** `go test ./core -run 'TestModelAssets|TestPrepareModel|TestRemoveModelFiles|TestPluginJudge' -count=1`；状态变更发布使用 `events.ModelsChanged = "models.changed"`，事件只失效快照，不承载路径或可靠进度流。

### Task 7：JudgeModel 迁移与全部业务调用方

**Files:** Modify `core/{settings.go,settings_test.go,models.go,models_test.go,pluginhost.go,pluginhost_test.go,labeler.go,labeler_test.go}`、`core/store/schema.go`、`app/{settings_models.go,settings_models_view.go,settings_models_test.go}`、全部引用 Settings.JudgeService 的 contract / app 夹具。此任务同时修复 UI 的旧字段引用；Task 8 再加入插件目录 UI。

**Interfaces:** 完成 Task 1 新增的 `Settings.JudgeModel string` 的迁移并移除 JudgeService；正常值使用完整模型引用。`resolveJudge(ctx)` 保持当前返回签名 `(ai.Judge, string, bool, error)`，在真正 Ask 准入时绑定插件模型快照，不在排队前捕获旧配置。复用 Task 5 的 ai.ErrJudgeUnavailable / ai.ErrJudgePlugin。

- [ ] **写失败测试并运行。** `TestJudgeModelMigration` 覆盖完整 HTTP 服务、已删服务、judge 关闭、未配置和迁移重复执行；`TestPluginJudgeUnavailableKeepsLabels` 覆盖 not_ready / unavailable / 坏答案；`TestJudgeModelExplicitNoFallback` 确认显式失效不调用默认生成模型。
  ```go
  require.ErrorIs(t, unavailableErr, ai.ErrJudgeUnavailable)
  require.NotErrorIs(t, unavailableErr, ai.ErrJudgeAnswer)
  require.Equal(t, labelsBefore, labelsAfter)
  ```
  Run: `go test ./core -run 'TestJudgeModel|TestPluginJudgeUnavailable' -count=1`；预期失败。
- [ ] **迁移旧设置。** 在一次 schema migration 中将 settings.key='app' 的 JSON JudgeService 转换为 slug/JudgeModel，再移除旧字段；值为空保持空。指向缺失 / judge 关闭服务时保留非空失效引用：有已存模型用其引用；模型已丢失则保留 `slug/`，不造可能撞名的模型 id，解析时返回 ErrJudgeUnavailable。Settings 允许未改动的历史失效值随其他设置保存，但对新输入的非空引用做完整语法校验；只有用户明确清空才使用 fallback。若已经有非空 JudgeModel，则保留新选择，不覆盖它。测试打开旧 DB 前后 JSON 和设置读取；不改 model_services 的 JudgeModel 字段，它仍描述人工服务的判断模型。
- [ ] **统一选择。** 空 JudgeModel 仍用默认生成模型代答；显式引用可以是 HTTP judge、生成模型代答或 plugin judge，按模型描述分发。托管本地候选通过 PrepareModel 的事务提交，普通 SetSettings 不得先写入另一个未准备的本地模型。旧配置绑定请求失效返回 model_unavailable，不驱动自动切回。
- [ ] **修复调用方。** 用 `rg 'JudgeService|resolveJudge|ErrJudgeAnswer|unanswerable' core app contract` 更新分类器、分类工具、脚本 decide、设置提示和测试。不可用和 ErrJudgePlugin 停止失败 Ask，不写 / 删标签；不能 join ErrJudgeAnswer 或 HTTP 400。已经成功处理的邮件不回滚。
- [ ] **验证。** `go test ./core ./core/store -run 'TestJudgeModel|TestPluginJudge|TestLabel|TestLLMDecide|TestSettings' -count=1`；确认用户取消不改变插件连续故障计数，原有人工 HTTP judge 校验仍有效。

### Task 8：模型选择 UI、RPC 与只读状态诊断

**Files:** Modify `app/{settings_models.go,settings_models_view.go,settings_models_test.go,settings_extensions.go,settings_extensions_test.go,app.go}`、现有 app 事件泵所在文件、`internal/i18n/{en.go,zh.go}`、`rpcbind/{bind.go,bind_test.go}`、`rpc/roles_test.go`、`contract/{api.go,contract_test.go}`；Create `contract/models_test.go`。

**Interfaces:** 四个宿主方法 ListModels / RefreshModels / PrepareModel / RemoveModelFiles 保持上述签名且只给 full；不改 MCP allowlist。rpcbind 用 model_error typed detail + judge_unavailable / judge_plugin Cause；独立无 JSON tag 的安全字段投影兼容 DecodeDetail。UI 失败后重新 ListModels，不使用 RPC 不会发送的失败 result。

- [ ] **写失败测试并运行。** `TestPluginModelsSelector` 检查启用后出现、默认选择不变、生成 / judge 分类正确、插件 URL / 协议不可编辑；`TestAssetModelsUpdateRequired` 有模型行和更新提示但没有下载按钮；`TestModelErrorRPCRoundTrip` 检查完整错误分类和状态重读。
  ```go
  require.ErrorIs(t, remoteErr, ai.ErrJudgeUnavailable)
  require.ErrorAs(t, remoteErr, &modelErr)
  require.Equal(t, "model_not_ready", modelErr.Code)
  require.False(t, showDownloadForUpdateRequired)
  ```
  Run: `go test ./rpcbind ./contract ./app -run 'TestPluginModels|TestAssetModels|TestModelError' -count=1`；预期失败。
- [ ] **接入目录与选择。** 复用现有设置选择器 / 服务卡，不让用户手建插件服务；打开读取目录，显式刷新调用 RefreshModels，models.changed 只触发重新读取。显示刷新失败和不可用原因，所有新文案走双语 i18n / theme tokens。基础 API 不绘制尚未实现的实际下载 / 删除控件；#90 再接入真实状态 UI。
- [ ] **更新授权。** models 能力卡说明会接收提示 / 上下文 / 工具或判断输入，能力变化重新授权；模型回调调用前设置 holds_mail。测试后续普通 Action / Tool 不因 provider 授权自动得到业务外发权限。
- [ ] **补 RPC 契约。** contract.API / Remote 增加同签名方法，业务失败测试仅依赖 error + 重新读取状态。MCP 请求四个新方法均被拒绝，模型 ID / 目录路径不会出现在不该包含它们的错误详情。事件丢失后重新打开选择器仍能恢复快照。
- [ ] **验证。** `go test ./rpcbind ./rpc ./contract ./app ./internal/i18n -count=1`，必要时 UI 测试沿现有 harness；不以 UI 测试绕过 core 授权校验。

### Task 9：在 plugins 仓库实现 Zen Free

**Files:** 在插件仓库 Create `zen-free/{plugin.json,package.json,README.md,LICENSE,build.ts}`、`zen-free/src/{index.ts,models.ts,request.ts}`、`zen-free/test/{models.test.ts,request.test.ts,host.test.ts}` 及小型脱敏 fixtures。打包产物生成到 `zen-free/dist/`，不把完整远程目录固化进产品代码。

**Interfaces:** SDK 由 Task 1 提供；源码默认导出 `(ctx: Ctx) => void`，只注册一个 ModelProvider。`discoverFreeModels(ctx: Ctx): Promise<ProviderModel[]>`，`adaptZenRequest(call: PrepareModelRequest): ModelHTTPRequest` 为插件内部纯适配边界。plugin.json 的 main 为 index.js，构建复制 manifest 并打包到 dist/index.js；源码无 Node import / 全局 fetch / Headers / WebCrypto / WASM 依赖。

**参考固定版本：** `magpie-community/plugins@0b58032fa37b57e7a003ea5570d3674dc52fedea/packages/zen-free`（本轮只读检查）；记录 MIT 来源，按需保留原版权。不要整包移植其 Node crypto、SSE 解析、工具模糊匹配或截断容错。

- [ ] **写失败测试并运行。** `models.test.ts` 覆盖明确免费且未退役的目录、收费 / retirement 过滤、metadata 故障、空目录与协议解析；`request.test.ts` 覆盖每种生成协议保留真实工具、仅补缺失占位工具、固定 public 凭据、会话稳定和请求唯一。
  ```ts
  expect(paidModelIds).toEqual([])
  expect(second.sessionHeader).toBe(first.sessionHeader)
  expect(second.requestHeader).not.toBe(first.requestHeader)
  expect(originalRequest).toEqual(beforeAdaptation)
  ```
  Run（插件仓库）: `bun test ./zen-free/test`；预期失败。
- [ ] **实现发现。** 用 ctx.net.fetch 请求 `https://opencode.ai/inference/v1/models`，只接受其实际列出的模型，再用 `https://models.opencode.ai/api.json` 和参考 docs URL 补免费 / 协议信息。声明这三个域名；元数据降级只能使用明确可信的最近信息，首次不能把参考 bootstrap 当作真实可用目录。模型列表主请求失败抛业务错误，让宿主保留旧目录；成功空目录如实返回空。协议不能确认时不猜收费或未知协议模型。
- [ ] **实现三种生成适配。** 基址固定 `https://opencode.ai/inference/v1`，公共凭据 public；沿参考源码设置协议相关 User-Agent / x-opencode-* / Anthropic 头。参考版本 OpenCode 1.19.0、runtime/bun/1.3.14、provider-utils chat=4.0.23 / responses=4.0.40 / anthropic=4.0.46，作为可验证的兼容常量而非宿主真实版本信息。只保留必要头，不外发工作目录 / 机器 ID。
- [ ] **实现会话转换。** ses_ 前缀用宿主 sessionCreatedAt 和 sessionId 构造稳定 OpenCode 形状，msg_ 使用 requestId 和本次时间；禁止取邮件 / 第一条 prompt 作为身份。只用 goja 已支持的数值 / 字符串运算，不依赖 Node crypto / BigInt；测试低 12 位 counter 和 14 位字母数字尾部、VM 重建后同一宿主会话稳定。
- [ ] **实现占位工具。** 仅补缺失的 bash / edit / glob / grep / read，参数分别为 command / filePath / pattern / pattern / filePath，并明确不可用；真实同名工具逐字保留。Godwit 继续负责流解析和工具授权，插件不解析返回工具名或吞掉截断流。
- [ ] **免费 judge 门槛。** 参考实现当前主动排除了 SystemOne，不能从 bootstrap 的 jev 名称推断可用。只有 Task 10 实测通过的免费 judge 才加入目录；若上游不可用，宿主的两种 judge 协议仍由本地模拟服务测试交付，插件不列未经验证的判断模型。记录验证结果，不给用户“已支持免费 judge”的虚假承诺。
- [ ] **打包与宿主验证。** `bun run --cwd zen-free build` 生成一份无外部 import 的 JS + manifest，大小不超过现有 script artifact 5 MiB；用 Godwit 实际 goja 插件加载跑 models.list / prepare。单纯 Bun 测试通过不代表沙箱兼容。README 说明启用即可用、第三方接收输入、非官方接口及限流可能、无配额保证。

### Task 10：集成验收与交付给 #90

**Files:** Godwit 新增 `core/coretest/testdata/models-demo/{plugin.json,index.ts}`、`core/pluginmodels_integration_test.go`；原生 fake 继续复用 Task 1；插件仓库修改 `zen-free/README.md` 记录可复现验证方式。只有形成真实不可变发布产物后才准备 registry 条目，不写假的 URL / SHA / size。

**Interfaces:** 全部使用已交付的 SDK / Service API，不新增临时直连入口。#90 只替换 newModelAssets 的真实供应器、补资产 schema / 下载 UI / 生命周期，并实现 StartLux models.list / load / judge；不改 configure 或重新迁移 JudgeService。

- [ ] **整合前验证。** 在隔离工作树执行 `go test ./core/... ./rpc/... ./rpcbind/... ./contract/... ./app/... ./internal/i18n/...`；对新增并发逻辑执行 `go test -race ./core ./core/plugins ./core/ai -run 'TestPluginModel|TestPluginJudge|TestPrepareModel|TestModelReentry|TestNativeSession' -count=1`。失败先修根因，再重跑受影响范围，不无条件重复全仓耗时测试。
- [ ] **端到端夹具。** `TestPluginModelsEndToEnd` 用实际脚本 provider 完成目录 → 选择 → 工具往返 / judge；用假原生模型走 describe.methods → configure(config only) → models.list → models.load(固定引擎 + 模型) → 单题 judge。比较 JS 与原生的契约结果，检查未下载不隐式下载、默认 update_required 与 RPC 状态一致。
- [ ] **真实 Zen 验证。** 使用安装后插件、公共测试文本和只产生固定测试输出的无副作用工具，验证文本生成、至少一次真实工具往返、上游当时可用的两种生成协议，以及免费 judge 是否可用；记录时间、模型 id、协议和结果，不保存业务提示或凭据。若上游没有第二种协议或受限，明确记录外部阻塞，不把源码声明当实测通过，不通过收费模型完成验收。
- [ ] **兼容性复查。** 手动人工服务 / OAuth、默认模型 / 小任务 / judge、审批恢复、插件禁用 / 升级、旧 translate ABI 均回归。检查三种协议并发，确认其他已存在 provider 的凭据及错误行为不变。
- [ ] **交付边界。** 对 #90 提供本计划与设计路径，列出已完成的基础接口及对应测试；无需在本轮发跨会话消息。源码 / 构建可审阅后，再按用户授权处理提交、PR、发布或 registry 更新；任何后续创建的 PR 按工具要求附加到任务。正式 StartLux release 的 minGodwit 指向完整托管资产能力版本。

## 计划自检与执行前注意

- 设计 §1–3 / §7 对应 Tasks 1–2、9；§4 对应 Tasks 3–4；§5 / §8 的准入与资产对应 Tasks 5–7；§8.1–8.2 / §9 对应 Tasks 6、8；§10 的验证分摊至各任务和 Task 10。真实下载 / 哈希 / 内存平台策略 / quarantine / StartLux 硬件校准明确归 #90，未遗漏成基础 PR 内的隐式工作。
- Task 2 的 RefreshModels 是为落实已批准的“显式刷新”而具体化的新增宿主方法；§8.2 原有三个签名保持不变。Task 5 的 NativeSession 是进程内 pin / 完成通知，不是新插件 ABI 或新的取消协议。
- 此计划未执行测试、未构建插件、未向 Zen 发起推理请求。所有测试名是待新增验收项，不能据此声称实现已完成。
- 计划审阅后建议在同一实施会话顺序推进 Tasks 1–8，再接 Task 9，最后 Task 10；这些任务共享模型身份、HTTP 上下文和准入契约，不宜在接口尚未落实时拆成多个并行实现。
