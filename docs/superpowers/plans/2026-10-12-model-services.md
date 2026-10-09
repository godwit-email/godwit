# Godwit 模型服务改进 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让添加和使用模型服务更顺手、更可靠：常见厂商预设、粘贴任意接口地址都能规范化、保存时验证 key 并拉取模型（区分「key 无效」与「服务没有模型列表接口」）、服务引用与可编辑的名称脱钩、新增 OpenAI Responses 协议、可选的「小任务模型」。

**Architecture:** 纯函数放 `core/ai`（地址规范化、预设、模型列表过滤）；`core` 的模型服务增加稳定 `slug`（迁移 12）并改用 slug 解析 `slug/模型` 引用；新增 `CheckModelService` 在保存前探测；运行时为 `openai-responses` 协议定义一个基于 openai-go Responses API 的 Genkit 模型（Genkit 自带的 `compat_oai` 只支持 Chat Completions）；设置增加 `SmallModel`，内置的轻量 skill 与即时回复使用它。界面在模型服务设置页落地这些能力。

**Tech Stack:** 现有依赖；`github.com/openai/openai-go`（已在 go.mod，v1.8.2）的 `responses` 包；Genkit Go v1.13.1 的 `genkit.DefineModel` / 插件机制。不新增第三方依赖。

**Spec:** `docs/superpowers/specs/2026-10-08-godwit-design.md`（第 6.1 节，本计划同时修订它）

**参考:** egoist/Lorca 的 provider 配置（`crates/cli/src/credentials.rs`：协议枚举 `chat-completions | responses | messages`、预设只预填表单、名称 slug 作为稳定 ID、保存时拉取 `/models` 并过滤非聊天模型、无列表接口时允许手填模型 ID、本地服务可不填 key）。差异：Godwit 的 key 继续存系统钥匙串；v1 不做按模型的上下文长度 / 视觉能力元数据（没有使用方，记入 spec 第 10 节）。

**基线:** 从 `feat/gui`（计划 5，PR godwit-email/godwit#6）拉出 `feat/model-services`。

### 规格解释（本计划采用，同步到 spec 6.1 与第 10 节）

- 接口类型三种：`openai`（OpenAI 兼容 · Chat Completions）、`openai-responses`（OpenAI 兼容 · Responses）、`anthropic`（Anthropic 兼容 · Messages）。
- 引用格式仍是 `服务/模型 ID`，其中「服务」是服务的 **slug**：创建时由名称生成（小写、空白与非法字符换成 `-`、去掉首尾 `-`，重名追加 `-2`、`-3`…），此后不随改名变化。为兼容已有的 skill `godwit-model`，解析时 slug 找不到再按名称精确匹配。
- 预设只预填表单，不是独立的服务类型：OpenAI、Anthropic、OpenRouter、Google Gemini（OpenAI 兼容端点）、DeepSeek、Groq、Ollama（本机，无需 key）、LM Studio（本机，无需 key）。
- 「小任务模型」：设置项 `SmallModel`（空 = 用默认模型）。内置的 `summarize`、`translate` 与即时回复使用它；skill 自己的 `godwit-model` 仍然优先；Wit（Ask / Assist）与其他 skill 用默认模型。

## Global Constraints

- 前五份计划的 Global Constraints 全部继续有效（Go 1.27.1、`CGO_ENABLED=0`、凭据只进钥匙串且不进日志 / 错误 / 事件、Genkit 类型只在 `core/ai`、迁移只追加、界面只经 `*core.Service`、界面尺寸与颜色只来自主题 token）。
- 错误信息不得包含 API key、响应正文或服务器给出的 `Location`（沿用 `ai.ListModels` 的做法）。
- `http` 地址只允许本机回环（沿用现有校验）；规范化不得把非回环 `http` 变成可用地址。
- 探测与拉取模型 10 秒超时，不跟随重定向（沿用 `NoRedirectClient`）。
- Responses 协议与 Chat Completions 的行为对等：流式文本、工具调用、`Respond` 恢复（审批后不重放）、取消、错误分类（`ai.Error` 的 Code）都要在同一套测试里覆盖。

## Review Focus

1. **已有数据升级**：名称含空格 / 大写 / 中文的旧服务（如「我的 服务」），升级后 `DefaultModel` 与 skill 覆盖仍然解析到同一服务；重名生成的 slug 不冲突。→ Task 2 `TestMigration12Slugs`、`TestResolveLegacyName`。
2. **粘贴各种地址**：带空白、结尾斜杠、`/v1/chat/completions`、`/responses`、`/v1/messages`、查询串与片段 —— 规范化到根地址；非回环 `http` 仍被拒绝。→ Task 1 `TestNormalizeBaseURL`。
3. **本机无 key、无 `/models` 的服务**（如某些网关）：可以手填模型 ID 保存；需要 key 的服务返回 401 / 403 时明确提示且不保存。→ Task 3 `TestCheckModelService`，Task 6 `TestSaveWithTypedModels`、`TestSaveRejectsBadKey`。
4. **Responses 下的工具调用与审批恢复**：中断后批准，恢复的回合不重放已执行的调用，回答完整。→ Task 4 `TestResponsesInterruptResume`。
5. **小任务模型被删除或未设置**：回退默认模型；默认也没有时报「AI 未配置」。→ Task 5 `TestSmallModelFallback`。

---

## File Structure

```
core/ai/baseurl.go          NormalizeBaseURL、Endpoint、Presets
core/ai/listmodels.go       （修改）过滤非聊天模型；区分 401/403 与无列表接口
core/ai/responses.go        openai-responses 协议的 Genkit 模型
core/ai/runtime.go          （修改）按 Kind 选择插件 / 模型
core/models.go              （修改）slug、按 slug 解析、CheckModelService、Kind 校验
core/models_check.go        CheckModelService 与 ServiceCheck
core/store/schema.go        （修改）迁移 12：model_services.slug
core/settings.go            （修改）SmallModel
core/aiservice.go           （修改）小任务模型的解析
core/skills/builtin/        （修改）summarize、translate 标记为小任务
core/internal/testllm/      （修改）假服务支持 /responses 流式与可选的 /models 404
app/settings_models.go      （修改）预设、地址预览、保存前探测、手填模型、协议选择、小任务模型
```

---

### Task 1: 地址规范化、端点预览、厂商预设、模型列表过滤

**Files:**
- Create: `core/ai/baseurl.go`
- Modify: `core/ai/listmodels.go`
- Test: `core/ai/baseurl_test.go`, `core/ai/listmodels_test.go`

**Interfaces:**
- Produces:
  - `const KindOpenAI = "openai"`、`KindResponses = "openai-responses"`、`KindAnthropic = "anthropic"`；`func ValidKind(k string) bool`
  - `func NormalizeBaseURL(kind, raw string) (string, error)`：去首尾空白、去查询串与片段、去结尾 `/`；`openai` / `openai-responses` 去掉结尾的 `/chat/completions`、`/completions`、`/responses`、`/models`、`/embeddings`；`anthropic` 去掉结尾的 `/v1/messages`、`/v1/models`、`/messages`、`/v1`；只接受 `https`，或 host 为回环地址（`localhost`、`127.0.0.0/8`、`::1`）的 `http`；拒绝 userinfo
  - `func Endpoint(kind, base string) string`：`openai` → `base + "/chat/completions"`；`openai-responses` → `base + "/responses"`；`anthropic` → `base + "/v1/messages"`
  - `type Preset struct { ID, Name, Kind, BaseURL, HelpURL string; KeyRequired bool }`；`func Presets() []Preset`：`openai`（`openai-responses`，`https://api.openai.com/v1`）、`anthropic`（`https://api.anthropic.com`）、`openrouter`（`openai`，`https://openrouter.ai/api/v1`）、`gemini`（`openai`，`https://generativelanguage.googleapis.com/v1beta/openai`）、`deepseek`（`openai`，`https://api.deepseek.com`）、`groq`（`openai`，`https://api.groq.com/openai/v1`）、`ollama`（`openai`，`http://localhost:11434/v1`，无需 key）、`lmstudio`（`openai`，`http://localhost:1234/v1`，无需 key）；`HelpURL` 指向各家获取 key 的页面
  - `ListModels` 的错误分类：`var ErrModelAuth = errors.New("the model service rejected the key")`（401 / 403）、`var ErrNoModelList = errors.New("the model service has no model list")`（404 / 405 / 501）；其他 HTTP 状态保持现有文案；`openai*` 结果过滤 ID 含 `embedding`、`whisper`、`tts`、`dall-e`、`moderation`、`transcribe`、`image`、`audio`、`realtime`、`search` 的模型；`openai-responses` 与 `openai` 用同一个 `/models`

- [ ] **Step 1: 写失败的测试**：`TestNormalizeBaseURL`（表驱动：`" https://api.openai.com/v1/ "`、`https://x/v1/chat/completions?x=1#y`、`https://x/v1/responses`、`https://api.anthropic.com/v1/messages`、`http://localhost:11434/v1` 通过；`http://example.com/v1`、`https://u:p@x/v1`、`ftp://x` 拒绝）；`TestEndpoint`；`TestPresets`（ID 唯一、Kind 合法、URL 能通过 `NormalizeBaseURL` 且不变）；`TestListModelsErrorKinds`（httptest：401 → `ErrModelAuth`、404 → `ErrNoModelList`、500 → 原文案，错误里不含 key 与响应正文）；`TestListModelsFiltersNonChat`。
- [ ] **Step 2: 运行，确认失败**：`go test ./core/ai/ -run 'Normalize|Endpoint|Presets|ListModels'` → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行，确认通过** → PASS；`go test -race ./core/...`
- [ ] **Step 5: Commit**：`git commit -m "feat(ai): base URL normalization, endpoint preview, vendor presets, model list error kinds"`

---

### Task 2: 稳定的服务 slug（迁移 12）

**Files:**
- Modify: `core/store/schema.go`（迁移 12）、`core/models.go`、`core/settings.go`（如需）、`app/settings_models.go`（删除改名时改写 `DefaultModel` 的逻辑与对应测试，改为显示 slug）
- Test: `core/models_test.go`, `core/store/schema_test.go`, `app/settings_models_test.go`

**Interfaces:**
- Produces:
  - `ModelService.Slug string`（创建时生成，之后不变）；`func slugify(name string) string`（规则见规格解释；结果为空时用 `service`）；重名时追加 `-2`、`-3`…
  - 迁移 12：`ALTER TABLE model_services ADD COLUMN slug TEXT`；为每行按 `id` 顺序生成 slug（Go 代码中的迁移步骤：迁移框架若只支持 SQL，就在同一事务里用 `UPDATE … SET slug = ?` 逐行写入，由迁移函数计算）；`CREATE UNIQUE INDEX model_services_slug ON model_services(slug)`；把 `settings` 里的 `DefaultModel` 从「名称/模型」改写为「slug/模型」（名称找不到的保持原样）
  - `resolveModel`：`ParseModelRef` 得到的服务部分先按 slug 精确匹配，找不到再按名称精确匹配（兼容旧 skill 覆盖）；`UpdateModelService` 改名不改 slug
  - 界面：服务列表与编辑表单显示 slug（「引用名：`<slug>`」，用于写 skill 的 `godwit-model`）；默认模型选择存 `slug/模型`；删除计划 5 Task 9 里改名改写 `DefaultModel` 的代码（删除服务时清空引用的逻辑保留）

- [ ] **Step 1: 写失败的测试**：`TestSlugify`（`"OpenAI"` → `openai`、`"我的 服务"` → `service`（全为非 ASCII 时）、`"Local Gateway!"` → `local-gateway`）；`TestAddServiceSlugUnique`（两个名为 `OpenAI` 的尝试中第二个因名称唯一失败；名为 `Open AI` 与 `open-ai` 的两个服务 slug 为 `open-ai` 与 `open-ai-2`）；`TestRenameKeepsSlug`（改名后 `DefaultModel` 不变仍能解析）；`TestMigration12Slugs`（迁移前插入两行旧服务与 `DefaultModel = "My Service/m1"`，迁移后 slug 生成、`DefaultModel == "my-service/m1"`、可解析）；`TestResolveLegacyName`（skill 覆盖写旧名称仍解析）；app：`TestRenameServiceKeepsDefault`（改名后设置里的默认模型不变、仍显示正确）。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行，确认通过** → PASS；`go test -race ./core/... ./app/...`
- [ ] **Step 5: Commit**：`git commit -m "feat(core): model services keep a stable slug for references (migration 12)"`

---

### Task 3: 保存前探测（CheckModelService）

**Files:**
- Create: `core/models_check.go`
- Modify: `core/models.go`（`ModelServiceInput` 接受新 Kind 与规范化后的地址）
- Test: `core/models_check_test.go`

**Interfaces:**
- Consumes：Task 1 的 `NormalizeBaseURL`、`ListModels`、`ErrModelAuth`、`ErrNoModelList`
- Produces:
  - `type ServiceCheck struct { BaseURL string /*规范化后*/; Endpoint string; Models []string; ListSupported bool }`
  - `func (s *Service) CheckModelService(ctx context.Context, id int64, in ModelServiceInput) (ServiceCheck, error)`：规范化地址（失败 → 校验错误）；key 为空且 `id != 0` 时用已存的 key；调用 `ListModels`：成功 → `ListSupported = true`；`ErrNoModelList` → `ListSupported = false`、无错误；`ErrModelAuth` → 返回 `ErrModelAuth`（导出到 core：`var ErrModelAuth = ai.ErrModelAuth`）；网络错误原样返回。不写数据库
  - `AddModelService` / `UpdateModelService`：保存前用 `NormalizeBaseURL` 规范化；`Kind` 用 `ai.ValidKind` 校验；允许空 key；`Models` 为空时报错「至少选择或填写一个模型」
  - `TestModelService` 对 `openai-responses` 也可用

- [ ] **Step 1: 写失败的测试**：`TestCheckModelService`（假服务：正常 → 模型列表与 `ListSupported`；`/models` 返回 404 → `ListSupported == false` 且无错误；401 → `ErrModelAuth`；地址带 `/chat/completions` → 返回规范化地址与端点；编辑已有服务时空 key 用已存 key）；`TestAddServiceNormalizes`；`TestAddServiceKeyless`；`TestAddServiceNeedsModels`。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**（`testllm` 增加选项让 `/models` 返回指定状态码，并能检查请求是否带 key）
- [ ] **Step 4: 运行，确认通过** → PASS；`go test -race ./core/...`
- [ ] **Step 5: Commit**：`git commit -m "feat(core): check a model service before saving it"`

---

### Task 4: OpenAI Responses 协议

**Files:**
- Create: `core/ai/responses.go`
- Modify: `core/ai/runtime.go`、`core/internal/testllm/server.go`（`POST /responses` 流式事件，脚本格式与 `/chat/completions` 共用 `Turn`）
- Test: `core/ai/responses_test.go`，以及把现有 runtime 的协议对等测试参数化到三种 Kind

**Interfaces:**
- Produces:
  - `openai-responses` 的 Genkit 模型：用 `genkit.DefineModel`（或等价的插件 `Init` 注册）实现 `ai.ModelFunc`，底层是 `github.com/openai/openai-go/responses` 的流式接口（`client.Responses.NewStreaming`）；客户端选项与现有 `compat_oai` 路径相同（`option.WithBaseURL`、`WithAPIKey`、清除环境变量里的身份、同一个 `http.Client`）
  - 映射：Genkit 的 system / user / model / tool 消息 ↔ Responses 的 `input` 项（`message`、`function_call`、`function_call_output`，`call_id` 与 Genkit 的 tool request `Ref` 一一对应）；工具定义 ↔ `tools[].type = "function"`（JSON Schema 原样传，沿用现有 schema 预处理）；流式 `response.output_text.delta` → 文本块；`response.function_call_arguments.done` / `response.output_item.done` → 工具请求；`response.completed` 的 `usage` → Genkit usage；错误事件与 HTTP 错误映射到现有 `ai.Error` 的 Code（401/403 → `unauthorized`、429 → `rate_limited`、5xx → `unavailable`）
  - 不使用 `previous_response_id` / 服务端存储（`store: false`），每次请求带完整历史，保持与 Chat Completions 一样由 Godwit 持有对话状态
  - `runtime.go`：`Kind == "openai-responses"` 时注册这个模型；其余不变
- 先做一次最小编译验证（Step 1 之前）：确认 openai-go v1.8.2 的 `responses` 包里流式 API 与事件类型名，把确认结果写进实现报告

- [ ] **Step 1: 写失败的测试**：`TestResponsesStreamsText`；`TestResponsesToolCall`（脚本：调用 `search_mail` 再回答）；`TestResponsesInterruptResume`（写工具中断 → 批准 → 恢复的请求里带有 `function_call_output`，没有重复执行，回答完整）；`TestResponsesErrors`（401 / 429 / 500 → 对应 Code，错误不含 key）；`TestResponsesCancel`；把 runtime 已有的协议测试（文本、工具、中断恢复、取消）改成对 `openai`、`openai-responses`、`anthropic` 三种 Kind 各跑一遍。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行，确认通过** → PASS；`go test -race ./core/...`
- [ ] **Step 5: Commit**：`git commit -m "feat(ai): OpenAI Responses protocol for model services"`

---

### Task 5: 小任务模型

**Files:**
- Modify: `core/settings.go`、`core/aiservice.go`、`core/models.go`（`resolveModel` 增加用途）、`core/skills/builtin/summarize/SKILL.md`、`core/skills/builtin/translate/SKILL.md`、`core/skills/skill.go`（读取元数据）
- Test: `core/aiservice_test.go`, `core/settings_test.go`

**Interfaces:**
- Produces:
  - `Settings.SmallModel string`（默认空；格式同 `DefaultModel`，校验同 `DefaultModel`）
  - skill 元数据 `godwit-model-tier: small`（只认 `small`）；`SkillInfo.Tier string`
  - 解析顺序：skill 的 `godwit-model` → `Tier == "small"` 或即时回复时用 `SmallModel` → `DefaultModel`；`SmallModel` 指向的模型不存在时回退 `DefaultModel`（不报错）；都没有 → `ErrAINotConfigured`
  - `SkillInfo.Model` 仍只反映 `godwit-model`；新增 `func (s *Service) SkillModel(ctx context.Context, name string) (string, error)` 返回实际会用的 `slug/模型`（界面标注结果用）
- 内置 `summarize`、`translate` 加 `godwit-model-tier: small`；即时回复（`QuickReplies`）用小任务模型

- [ ] **Step 1: 写失败的测试**：`TestSmallModelUsedForSummarize`（`SmallModel = test/m2`、默认 `test/m1` → summarize 请求的模型是 m2，Ask 仍是 m1）；`TestQuickRepliesUseSmallModel`；`TestSmallModelFallback`（`SmallModel` 指向已删除的模型 → 用默认；默认为空 → `ErrAINotConfigured`）；`TestSkillOverrideBeatsTier`；`TestSkillModel`。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行，确认通过** → PASS；`go test -race ./core/...`
- [ ] **Step 5: Commit**：`git commit -m "feat(core): a small-task model for summaries, translations and quick replies"`

---

### Task 6: 模型服务设置页

**Files:**
- Modify: `app/settings_models.go`、`app/reader_ai.go`（结果标注改用 `SkillModel`）、`internal/i18n/zh.go`、`internal/i18n/en.go`
- Test: `app/settings_models_test.go`, `app/reader_ai_test.go`
- 画板：`SettingsModels.dc.html`

**Interfaces:**
- Consumes：Task 1 `Presets`、`Endpoint`、`NormalizeBaseURL`（经 core 导出的薄包装：`func (s *Service) ModelPresets() []ai.Preset` 与 `func NormalizeModelBaseURL(kind, raw string) (string, error)`，界面不直接 import `core/ai` 的函数以外的东西——`core/ai` 的类型可用）；Task 3 `CheckModelService`；Task 5 `SmallModel`、`SkillModel`
- Produces:
  - 「添加服务」先选预设（8 个 + 「自定义」），预填接口类型、名称、Base URL；需要 key 的预设显示「获取 API Key ↗」（`HelpURL`）
  - 接口类型三选一：OpenAI 兼容（Chat Completions）/ OpenAI 兼容（Responses）/ Anthropic 兼容
  - Base URL 失去焦点时规范化并显示「将请求 `<Endpoint>`」；规范化失败显示原因
  - 保存：先 `CheckModelService`：`ErrModelAuth` → 字段下「API Key 无效或没有权限」，不保存；`ListSupported` 为真 → 显示拉取到的模型供勾选（已有选择保留）；为假 → 提示「这个服务没有提供模型列表，请手动填写模型 ID」并显示可增删的模型 ID 输入；然后 `Add/UpdateModelService`
  - 本机预设（Ollama、LM Studio）的 API Key 标为「可选」
  - 小任务模型选择：通用里的「默认模型」下方（或模型服务页内）增加「小任务模型（总结、翻译、即时回复）」，选项为「与默认模型相同」+ 全部模型，写 `SmallModel`（设置写队列）
  - 阅读区的总结 / 翻译结果标注改为 `SkillModel` 返回的实际模型

- [ ] **Step 1: 写失败的测试**：`TestAddFromPreset`（选 Ollama → 名称、地址、类型预填，key 可选）；`TestBaseURLNormalizedOnBlur`（粘贴 `…/v1/chat/completions` → 输入框变根地址，显示端点）；`TestSaveWithTypedModels`（假服务 `/models` 404 → 出现手填提示，填 `m1` 保存成功）；`TestSaveRejectsBadKey`（401 → 字段错误，`ListModelServices` 没有新增）；`TestResponsesKindSelectable`；`TestSmallModelSetting`（选择后 `GetSettings().SmallModel` 更新；阅读区总结标注为小任务模型）。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行，确认通过** → PASS；`go test -race ./...`
- [ ] **Step 5: Commit**：`git commit -m "feat(app): presets, endpoint preview, check before save, Responses and the small-task model in settings"`

---

## Spec 同步

Task 2 开始前更新 spec 6.1：三种接口类型、slug 引用规则与兼容旧名称、预设、保存前探测与无列表接口时手填、小任务模型；第 10 节记录「按模型的上下文长度 / 视觉能力元数据」暂不做（目前没有使用方）。
