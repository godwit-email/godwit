# Godwit 模型服务改进 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让添加和使用模型服务更顺手、更可靠：常见厂商预设、粘贴任意接口地址都能规范化、保存前探测（区分「key 被拒绝」与「服务没有模型列表」，任何时候都能手填模型 ID）、服务引用与可编辑的名称脱钩、新增 OpenAI Responses 协议、可选的「小任务模型」。

**Architecture:** 纯函数放 `core/ai`（地址规范化、端点、预设）。`core` 的模型服务增加稳定 `slug`（迁移 12 只加列和索引；回填与引用改写在 `core.New` 启动时一次性执行，同一事务内完成）。凭据复用绑定「协议 + 规范化地址」。运行时为 `openai-responses` 协议实现一个基于 openai-go Responses API 的 Genkit 模型插件（Genkit 自带 `compat_oai` 只支持 Chat Completions），按模型 ID 动态解析。设置增加 `SmallModel`，内置轻量 skill 与即时回复使用它。界面在模型服务设置页落地。

**Tech Stack:** 现有依赖；`github.com/openai/openai-go` v1.8.2 的 `responses` 包（已核实：`client.Responses.NewStreaming`、`ResponseStreamEventUnion`、显式 `Store` / `Strict` 字段、reasoning 的 `encrypted_content`）；Genkit Go v1.13.1 的插件机制（`ResolveAction` 动态解析，或首次使用前注册）与 `ai.NewReasoningPart(text, signature)`。不新增第三方依赖。

**Spec:** `docs/superpowers/specs/2026-10-08-godwit-design.md`（第 6.1、6.3 节，本计划同时修订 6.1）

**参考:** egoist/Lorca 的 provider 配置（协议枚举、预设只预填、名称 slug 作为稳定 ID、拉取 `/models` 且允许手填、本地服务可无 key）。差异：Godwit 的 key 继续存系统钥匙串；v1 不做按模型的上下文长度 / 视觉能力元数据（无使用方，记入 spec 第 10 节）。

**基线:** 从 `feat/gui`（计划 5，PR godwit-email/godwit#6）拉出 `feat/model-services`。

**评审修订（2026-10-12，Codex 计划评审）:** slug 与旧名称的冲突规则；凭据的保留 / 替换 / 清除与目的地址绑定；迁移改为「迁移加列 + 启动时事务回填」，并改写 `runs.model`、`conversations.model`；Responses 的 reasoning 项、工具调用聚合、终止状态机、`strict` 线协议值、按模型 ID 动态解析；模型列表只作推荐，手填随时可用；假服务记录原始请求；SkillModel 改为运行时返回实际模型。

### 规格解释（同步到 spec 6.1 与第 10 节）

- 接口类型三种：`openai`（Chat Completions）、`openai-responses`（Responses）、`anthropic`（Messages）。
- 引用格式 `服务/模型 ID`，「服务」是服务的 **slug**：创建时由名称生成，之后不随改名变化。**冲突规则**：slug 在所有服务的 slug 之间唯一，且不得等于**其他**服务的当前名称；改名时新名称不得等于其他服务的 slug。解析：先按 slug 精确匹配；没有命中再按名称精确匹配（兼容旧的 skill `godwit-model`）。这样一个引用字符串至多指向一个服务。
- 预设只预填表单：OpenAI、Anthropic、OpenRouter、Google Gemini（OpenAI 兼容端点）、DeepSeek、Groq、Ollama（本机，无需 key）、LM Studio（本机，无需 key）。
- 保存前的探测只说明「模型列表能否访问、列出了哪些模型」；列表返回 401 / 403 时视为 key 被拒绝并阻止保存；列表成功不代表 key 一定能生成（「测试连接」做生成验证）。模型 ID 任何时候都可以手填，拉取到的列表只是推荐。
- 「小任务模型」：设置项 `SmallModel`（空 = 用默认模型）。内置 `summarize`、`translate` 与即时回复使用它；skill 自己的 `godwit-model` 优先；Wit 与其他 skill 用默认模型。小任务模型所指的服务或模型不存在时回退默认模型；其他错误照常返回。

## Global Constraints

- 前五份计划的 Global Constraints 全部继续有效（Go 1.27.1、无 cgo 构建、凭据只进钥匙串且不进日志 / 错误 / 事件、Genkit 类型只在 `core/ai`、迁移只追加、界面只经 `*core.Service`、界面尺寸与颜色只来自主题 token）。
- 验证命令分开写：`CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go test ./...`，以及 `go test -race ./...`（race 需要 cgo，按现有 CI 在非 Windows 上跑）。
- 错误信息、日志、事件里不得出现 API key、响应正文、服务器给出的 `Location`、reasoning 的加密内容。
- `http` 地址只允许本机回环；不跟随重定向（沿用 `NoRedirectClient`）；探测 10 秒超时。
- **凭据只发往它所属的地址**：已存的 key 只在协议与规范化地址都不变时自动复用；地址或协议变了，必须重新输入 key 或明确选择「不使用 key」。
- Responses 协议与 Chat Completions 行为对等：流式文本、工具调用、`Respond` 恢复（审批后不重放）、取消、错误分类都在同一套参数化测试里覆盖。

## Review Focus

1. **已有数据升级与名称冲突**：旧服务 `OpenAI` 与 `openai` 并存、中文名称、改名后再恢复中断的运行 —— 升级后每个旧引用（设置、运行、对话、skill）仍指向原来的服务，不会静默换到另一个服务。→ Task 2 `TestBackfillSlugCollisions`、`TestResumeAfterUpgradeAndRename`。
2. **改了地址的旧服务**：编辑服务把 Base URL 改成另一个地址再探测 / 保存 —— 新地址绝不会收到旧 key。→ Task 3 `TestKeyNotSentToNewAddress`。
3. **粘贴各种地址**：空白、结尾斜杠、`/v1/chat/completions`、`/responses`、`/v1/messages`、查询串 —— 规范化到根地址；非回环 `http` 仍被拒绝。→ Task 1 `TestNormalizeBaseURL`。
4. **Responses 下推理模型的工具续接与审批恢复**：reasoning 项随历史回传（加密内容不展示不记录）、多工具交错事件不重复、中断批准后不重放。→ Task 5 `TestResponsesReasoningCarried`、`TestResponsesInterruptResume`。
5. **截断与失败的流**：`response.failed` / `response.incomplete` / 没有完成事件就 EOF —— 不当作成功保存，也不执行未确认的工具调用。→ Task 5 `TestResponsesTerminalStates`。

---

## File Structure

```
core/ai/baseurl.go          ValidKind、NormalizeBaseURL、Endpoint、Presets
core/ai/listmodels.go       （修改）错误分类；不再按子串过滤
core/ai/responses.go        openai-responses 协议的 Genkit 插件 / 模型
core/ai/runtime.go          （修改）按 Kind 选择插件；Responses 动态解析模型
core/ai/runtime.go 的分类入口 （修改）识别 Responses 适配器的安全错误
core/models.go              （修改）slug、按 slug 解析、名称冲突校验、凭据操作
core/models_slug.go         slugify 与启动时回填 / 引用改写
core/models_check.go        CheckModelService 与 ServiceCheck
core/store/schema.go        （修改）迁移 12：model_services.slug + 唯一索引
core/settings.go            （修改）SmallModel
core/aiservice.go           （修改）小任务模型的解析；RunSkill 的首个事件带实际模型
core/skills.go, core/skills/skill.go  （修改）Tier 元数据
core/skills/builtin/{summarize,translate}/SKILL.md  （修改）godwit-model-tier: small
core/internal/testllm/      （修改）原始请求记录（model、body）、/responses 流式、/models 状态码可控
app/settings_models.go      （修改）预设、端点预览、凭据操作、保存流程、手填模型、协议选择、小任务模型
app/reader_ai.go            （修改）结果标注用运行返回的实际模型
```

---

### Task 1: 地址规范化、端点预览、厂商预设、模型列表错误分类

**Files:**
- Create: `core/ai/baseurl.go`
- Modify: `core/ai/listmodels.go`
- Test: `core/ai/baseurl_test.go`, `core/ai/listmodels_test.go`

**Interfaces:**
- Produces:
  - `const KindOpenAI = "openai"`、`KindResponses = "openai-responses"`、`KindAnthropic = "anthropic"`；`func ValidKind(k string) bool`
  - `func NormalizeBaseURL(kind, raw string) (string, error)`：去首尾空白、查询串、片段与结尾 `/`；`openai` / `openai-responses` 去掉结尾的 `/chat/completions`、`/completions`、`/responses`、`/models`、`/embeddings`；`anthropic` 去掉结尾的 `/v1/messages`、`/v1/models`、`/messages`、`/v1`；只接受 `https`，或 host 为回环（`localhost`、`127.0.0.0/8`、`::1`）的 `http`；拒绝 userinfo
  - `func Endpoint(kind, base string) string`：`openai` → `/chat/completions`；`openai-responses` → `/responses`；`anthropic` → `/v1/messages`（拼在 base 后）
  - `type Preset struct { ID, Name, Kind, BaseURL, HelpURL string; KeyRequired bool }`；`func Presets() []Preset`：

    | ID | Kind | BaseURL | HelpURL |
    |---|---|---|---|
    | `openai` | `openai-responses` | `https://api.openai.com/v1` | `https://platform.openai.com/api-keys` |
    | `anthropic` | `anthropic` | `https://api.anthropic.com` | `https://console.anthropic.com/settings/keys` |
    | `openrouter` | `openai` | `https://openrouter.ai/api/v1` | `https://openrouter.ai/settings/keys` |
    | `gemini` | `openai` | `https://generativelanguage.googleapis.com/v1beta/openai` | `https://aistudio.google.com/apikey` |
    | `deepseek` | `openai` | `https://api.deepseek.com` | `https://platform.deepseek.com/api_keys` |
    | `groq` | `openai` | `https://api.groq.com/openai/v1` | `https://console.groq.com/keys` |
    | `ollama` | `openai` | `http://localhost:11434/v1` | `https://docs.ollama.com/api/openai-compatibility`（无需 key） |
    | `lmstudio` | `openai` | `http://localhost:1234/v1` | `https://lmstudio.ai/docs/developer/openai-compat`（无需 key） |

  - `ListModels` 错误分类：`var ErrModelAuth = errors.New("the model service rejected the key")`（401 / 403）、`var ErrNoModelList = errors.New("the model service has no model list")`（404 / 405 / 501）；其他状态保持现有安全文案；**不再过滤**任何模型（列表只作推荐，排序后原样返回）；`openai-responses` 与 `openai` 用同一个 `/models`

- [ ] **Step 1: 写失败的测试**：`TestNormalizeBaseURL`（表驱动：`" https://api.openai.com/v1/ "`、`https://x/v1/chat/completions?x=1#y`、`https://x/v1/responses`、`https://api.anthropic.com/v1/messages`、`http://localhost:11434/v1`、`http://[::1]:8080/v1` 通过并得到根地址；`http://example.com/v1`、`https://u:p@x/v1`、`ftp://x`、空串拒绝）；`TestEndpoint`；`TestPresets`（ID 唯一、Kind 合法、BaseURL 经 `NormalizeBaseURL` 不变、`HelpURL` 都是 https）；`TestListModelsErrorKinds`（httptest：401 / 403 → `ErrModelAuth`、404 → `ErrNoModelList`、500 → 原文案；错误不含 key、正文）；`TestListModelsReturnsAll`（包含 `text-embedding-3-small` 也照样返回）。
- [ ] **Step 2: 运行，确认失败**：`go test ./core/ai/ -run 'Normalize|Endpoint|Presets|ListModels'` → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行，确认通过** → PASS；`go test -race ./core/...`
- [ ] **Step 5: Commit**：`git commit -m "feat(ai): base URL normalization, endpoint preview, vendor presets, model list error kinds"`

---

### Task 2: 稳定的服务 slug（迁移 12 + 启动回填）

**Files:**
- Create: `core/models_slug.go`
- Modify: `core/store/schema.go`（迁移 12）、`core/models.go`、`core/service.go`（`New` 里调用回填）、`core/conversations.go`（新运行在 `runs.model` 保存规范化的 slug 引用；`conversations.model` 目前不使用，不改）、`app/settings_models.go`（删除改名时改写 `DefaultModel` 的代码，保留「取消勾选 / 删除服务时清理引用」并按 slug 处理 `DefaultModel`（`SmallModel` 的同类清理在 Task 7）；显示「引用名」）
- Test: `core/models_slug_test.go`, `core/store/schema_test.go`, `core/conversations_test.go`, `app/settings_models_test.go`

**Interfaces:**
- Produces:
  - `ModelService.Slug string`
  - `func slugify(name string) string`：转小写；ASCII 字母、数字保留，其余连续字符换成单个 `-`，去首尾 `-`；结果为空时用 `service`
  - `func (s *Service) newSlug(ctx, name string, exceptID int64) (string, error)`：从 `slugify(name)` 起，若与任何服务的 slug 相同、或等于**其他**服务的名称，就追加 `-2`、`-3`…
  - 迁移 12（纯 SQL）：`ALTER TABLE model_services ADD COLUMN slug TEXT`、`CREATE UNIQUE INDEX model_services_slug ON model_services(slug)`（`NULL` 不冲突）
  - 启动回填 `func (s *Service) backfillSlugs(ctx) error`（`core.New` 打开数据库后、启动 worker 之前调用，幂等）：读取 `slug IS NULL` 的服务；按 `id` 顺序分配 slug —— **名称本身已是合法 slug 且不与其他服务名称冲突的直接用名称**（保证 `openai` 这类旧引用仍指向原服务），其余按 `newSlug`；在同一个 `Transaction` 里写入：各行 `slug`；`settings` 里 `key = 'app'` 的 JSON 只改 `DefaultModel` 字段（按 `map[string]json.RawMessage` 改写，保留其他字段与未知字段）；`runs.model` 与 `conversations.model` 中「服务部分精确等于某个服务名称」的引用改为对应 slug。全部成功才提交
  - `resolveModel`：服务部分先按 slug 精确匹配，再按名称精确匹配；`claimRun` 与对话保存的 `model` 存「slug/模型」形式（解析后的规范引用）
  - `AddModelService`：用 `newSlug` 生成；`UpdateModelService`：改名不改 slug，新名称不得等于其他服务的 slug（返回校验错误）

- [ ] **Step 1: 写失败的测试**：`TestSlugify`（`"OpenAI"` → `openai`、`"Local Gateway!"` → `local-gateway`、`"我的 服务"` → `service`）；`TestBackfillSlugCollisions`（迁移前插入 `OpenAI`（id 1）与 `openai`（id 2），`DefaultModel = "openai/m1"`；回填后 id 2 的 slug 是 `openai`、id 1 是 `openai-2`，`DefaultModel` 仍解析到 id 2；两个中文名服务得到 `service` 与 `service-2`）；`TestBackfillPreservesSettingsFields`（未知 JSON 字段保留）；`TestBackfillRewritesRunRefs`；`TestBackfillIdempotent`（重启再跑无变化）；`TestBackfillRollsBack`（注入第 N 条语句失败 → 全部未变）；`TestRenameKeepsSlug`；`TestRenameRejectsOtherSlug`；`TestResolveLegacyName`；`TestResumeAfterUpgradeAndRename`（旧库中有 `interrupted` 的运行引用旧名称 → 升级 → 改名 → 批准恢复成功）；app：`TestRenameServiceKeepsDefault`、`TestDeselectDefaultClears`（取消勾选默认模型仍清除 `DefaultModel`，对 `SmallModel` 同样处理）。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行，确认通过** → PASS；`go test -race ./core/... ./app/...`
- [ ] **Step 5: Commit**：`git commit -m "feat(core): model services keep a stable slug for references (migration 12 + startup backfill)"`

---

### Task 3: 保存前探测与凭据操作

**Files:**
- Create: `core/models_check.go`
- Modify: `core/models.go`（`ModelServiceInput` 的凭据操作、Kind 与地址规范化）、`core/internal/testllm/server.go`（`/models` 状态码可控；记录每个请求的 `Authorization` / `x-api-key` 是否出现）
- Test: `core/models_check_test.go`, `core/models_test.go`

**Interfaces:**
- Consumes：Task 1 的 `ValidKind`、`NormalizeBaseURL`、`Endpoint`、`ListModels`、`ErrModelAuth`、`ErrNoModelList`
- Produces:
  - `ModelServiceInput` 增加 `KeyAction string`：`keep`（沿用已存 key）、`replace`（用 `APIKey`）、`clear`（删除已存 key，变为无 key）；新增服务时 `keep` 无效
  - 规则：`keep` 只在协议与规范化后的地址都与已存的一致时允许；否则返回 `ErrKeyNeedsConfirm`（界面据此要求重新输入或选择「不使用 key」）
  - `type ServiceCheck struct { BaseURL, Endpoint string; Models []string; ListSupported bool }`
  - `func (s *Service) CheckModelService(ctx context.Context, id int64, in ModelServiceInput) (ServiceCheck, error)`：规范化地址；按凭据规则取 key（`keep` 且地址或协议变了 → `ErrKeyNeedsConfirm`，不发请求）；`ListModels`：成功 → 列表 + `ListSupported`；`ErrNoModelList` → `ListSupported = false`、无错误；`ErrModelAuth` → 返回它；其他错误返回 `ListModels` 已脱敏的错误。不写数据库
  - `var ErrModelAuth = ai.ErrModelAuth`、`var ErrKeyNeedsConfirm = errors.New(...)` 导出到 core
  - `AddModelService` / `UpdateModelService`：规范化地址、`ValidKind`、执行凭据操作；`Models` 为空时报错（至少一个模型，可手填）
- `TestModelService` 对 `openai-responses` 的验收放在 Task 5

- [ ] **Step 1: 写失败的测试**：`TestCheckModelService`（正常 → 列表；`/models` 404 → `ListSupported == false` 无错误；401 → `ErrModelAuth`；200 空列表 → 无错误、空列表；地址带 `/chat/completions` → 返回根地址与端点）；`TestKeyNotSentToNewAddress`（两个假服务：服务建在 A 上带 key；编辑时地址改为 B、`KeyAction = keep` → `ErrKeyNeedsConfirm`，B 收到 0 个带 key 的请求；`replace` 后 B 收到新 key；`clear` 后 B 收到无 key 请求）；`TestKeepKeySameAddress`；`TestAddServiceNormalizes`；`TestAddServiceNeedsModels`；回归：`TestAddServiceKeyless`（本机无 key 服务可以保存——可能在现有代码上已通过，作为回归保留）。
- [ ] **Step 2: 运行，确认失败** → FAIL（回归测试除外）
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行，确认通过** → PASS；`go test -race ./core/...`
- [ ] **Step 5: Commit**：`git commit -m "feat(core): check a model service before saving; keys only go to the address they belong to"`

---

### Task 4: 假服务支持 Responses 与原始请求记录

**Files:**
- Modify: `core/internal/testllm/server.go`（及拆分出的 `responses.go`）、`core/coretest/coretest.go`（导出需要的新字段）
- Test: `core/internal/testllm/responses_test.go`

**Interfaces:**
- Produces:
  - `Request` 增加 `Model string`、`Body json.RawMessage`（原始请求体）、`Path string`；三种协议都填
  - `Start(t, "openai-responses")`：`GET /models` 与 `POST /responses`（SSE）。按脚本 `Turn` 输出：`response.created` → 文本时逐段 `response.output_text.delta` → 工具调用时 `response.output_item.added`（`type: function_call`，`id: fc_*`，`call_id: call_*`，`name`）+ 若干 `response.function_call_arguments.delta` + `response.function_call_arguments.done` + `response.output_item.done` → 可选 reasoning 项（`type: reasoning`，带 `encrypted_content`）→ `response.completed`（含 usage）；`Turn` 增加 `Reasoning string`（加密内容占位）、`Fail string`（输出 `response.failed`）、`Incomplete bool`（`response.incomplete`）、`Truncate bool`（不发终止事件直接断开）、`Status int`（HTTP 错误，持续返回直到脚本前进，支持 SDK 重试）
  - **请求校验**：对 `/responses` 检查 `store` 显式为 `false`；`input` 里每个 `function_call_output.call_id` 都能在之前的 `function_call` 中找到；`output` 是字符串；上一回合带 reasoning 时本回合的 `input` 里有同一 reasoning 项（含 `encrypted_content`）；工具定义带显式 `strict`。校验失败返回 400 并在 `Requests()` 里标记，让测试失败而不是拿到脚本里的成功回答
  - 也给 `/chat/completions` 增加同样的 `call_id` 对应校验（不影响现有测试的合法请求）

- [ ] **Step 1: 写失败的测试**：`TestResponsesFakeStream`（直接用 openai-go 客户端请求假服务，逐个事件解析正确）；`TestResponsesFakeValidates`（缺 `store:false`、未知 `call_id`、缺 reasoning 回传 → 400）；`TestRequestRecordsModel`（三种协议都记录 `Model` 与原始 body）。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行，确认通过** → PASS；`go test -race ./core/...`（现有使用 testllm 的测试全部仍通过）
- [ ] **Step 5: Commit**：`git commit -m "test(core): fake model service speaks Responses and records raw requests"`

---

### Task 5: OpenAI Responses 协议

**Files:**
- Create: `core/ai/responses.go`
- Modify: `core/ai/runtime.go`（按 Kind 选插件；错误分类入口）、`core/models.go`（`ValidKind` 已含 `openai-responses`）
- Test: `core/ai/responses_test.go`，并把 runtime 的协议对等测试参数化到三种 Kind

**Interfaces:**
- Consumes：Task 4 的假服务
- Produces:
  - 一个 Genkit 插件（如 `type responsesPlugin struct{ … }`），对 `godwit-responses/<任意模型 ID>` **按需解析**模型（实现插件的 `ResolveAction`，或在每个模型首次使用前加锁注册；模型 ID 可含 `/`，如 OpenRouter 的 `openai/gpt-x`）；同一个 Engine 实例里可以先后使用两个不同模型；能力声明：多轮、工具、system role、流式
  - 客户端：`option.WithBaseURL`、`WithAPIKey`、清除环境变量里的身份、与 `compat_oai` 路径相同的 `http.Client`
  - 请求：`store: false`（显式）、`include: ["reasoning.encrypted_content"]`；每次带完整历史；工具 `type: "function"`，`strict` 显式映射（插件 / MCP 工具 `false`，内置严格工具按其设置），JSON Schema 沿用现有预处理
  - 历史映射：system → `instructions`；user / model 文本 → `message` 项；Genkit tool request → `function_call`（`call_id` = tool request 的 `Ref`，`name`，`arguments` JSON 字符串）；tool response → `function_call_output`（`call_id`，`output` 为 JSON 编码的字符串）；reasoning → `ai.NewReasoningPart("", encryptedContent)` 存入 model 消息（加密内容放 signature，`id` 放 part 的 metadata），回传时还原为 `reasoning` 项且顺序与原输出一致。reasoning 不显示、不进日志、不进事件
  - 流式：`response.output_text.delta` → 文本块（只在这里拼文本，最终消息不重复拼接）；工具调用按 `output_index` / `item_id` 聚合，只在 `response.output_item.done`（`type: function_call`）时生成一个 ToolRequest，`Ref` 用 `call_id`（不是 `fc_*` 的 item id）；多工具交错正确
  - 终止状态机：只有 `response.completed` 才算成功；`response.failed` → 错误；`response.incomplete`（含 `max_output_tokens`）→ 以「输出被截断」错误结束；没有终止事件就 EOF → 错误；任何非成功结束都不产生工具请求、不保存为完成的回合
  - 错误：适配器只返回固定安全文案的 Genkit status 错误（401 / 403 → `UNAUTHENTICATED`，429 → `RESOURCE_EXHAUSTED`，5xx / 网络 → `UNAVAILABLE`，失败 → `INTERNAL`；`response.incomplete` 与 Chat Completions 的 `length` 一样走 `CodeIncomplete`），由现有分类映射到 `ai.Error` 的 Code；不含正文、key、`Location`
  - `TestModelService` 对 `openai-responses` 可用

- [ ] **Step 1: 写失败的测试**：`TestResponsesStreamsText`；`TestResponsesToolCall`；`TestResponsesParallelTools`（两个调用交错的事件 → 恰好两个 ToolRequest，`Ref` 是 `call_*`）；`TestResponsesReasoningCarried`（带 reasoning 的工具续接：下一请求回传同一 reasoning 项；事件与日志里没有加密内容）；`TestResponsesInterruptResume`（读工具已执行 + 写工具待批准 → 新 Engine 恢复 → 写工具执行一次、读工具不重复、回答完整；请求里 `function_call_output` 齐全）；`TestResponsesTerminalStates`（failed / incomplete / 截断 EOF → 错误、无工具执行、回合未保存为完成）；`TestResponsesErrors`（401 / 403 / 429 / 500 / 200 内 SSE 错误 / 重定向 → 对应 Code，错误不含 key 与正文）；`TestResponsesCancel`；`TestResponsesTwoModelsOneEngine`（同一服务先用 `m1` 再用 `vendor/m2`）；`TestResponsesStrictFlag`（插件工具 `strict:false` 出现在原始请求中）；协议对等：runtime 已有的文本 / 工具 / 中断恢复 / 取消测试对三种 Kind 各跑一遍；`TestModelServiceResponses`（`TestModelService` 成功）。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行，确认通过** → PASS；`go test -race ./core/...`
- [ ] **Step 5: Commit**：`git commit -m "feat(ai): OpenAI Responses protocol for model services"`

---

### Task 6: 小任务模型

**Files:**
- Modify: `core/settings.go`、`core/aiservice.go`、`core/models.go`（`resolveModel` 增加用途参数）、`core/skills.go`（`SkillInfo.Tier`）、`core/skills/skill.go`（读取 `godwit-model-tier`）、`core/skills/builtin/summarize/SKILL.md`、`core/skills/builtin/translate/SKILL.md`、`core/conversations.go` 或 skill 流（首个事件带实际模型）
- Test: `core/aiservice_test.go`, `core/settings_test.go`, `core/skills_test.go`

**Interfaces:**
- Produces:
  - `Settings.SmallModel string`（默认空）；`Settings.validate` 对 `DefaultModel` 与 `SmallModel` 都只做格式校验（能被 `ParseModelRef` 解析或为空），不查服务是否存在
  - skill 元数据 `godwit-model-tier: small`（只认 `small`）；`SkillInfo.Tier string`；内置 `summarize`、`translate` 加上它
  - 解析顺序：skill 的 `godwit-model` → `Tier == "small"` 或即时回复时用 `SmallModel` → `DefaultModel`。`SmallModel` 为空，或其服务 / 模型不存在（`ErrModelServiceNotFound`、`ErrModelNotFound`）时回退 `DefaultModel`；数据库、钥匙串等其他错误照常返回；都没有 → `ErrAINotConfigured`
  - `RunSkill` 流的第一个事件（`context`）携带实际使用的模型引用：`ai.Event` 增加 `Model string`（`slug/模型`）；界面用它标注结果（不另做查询，避免与运行之间设置变化导致标注不准）
- 即时回复（`QuickReplies`）用小任务模型；缓存键已包含模型，保持

- [ ] **Step 1: 写失败的测试**：`TestSmallModelUsedForSummarize`（`SmallModel = test/m2`、默认 `test/m1` → summarize 请求的 `Model == "m2"`（读 `Requests()[i].Model`），Ask 仍是 m1）；`TestQuickRepliesUseSmallModel`；`TestSmallModelFallback`（指向已删除的模型 → 用默认；默认为空 → `ErrAINotConfigured`）；`TestSmallModelOtherErrorsSurface`（钥匙串出错不被吞掉）；`TestSkillOverrideBeatsTier`；`TestRunSkillReportsModel`（首个事件的 `Model`）；`TestSettingsValidateModelRefs`。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行，确认通过** → PASS；`go test -race ./core/...`
- [ ] **Step 5: Commit**：`git commit -m "feat(core): a small-task model for summaries, translations and quick replies"`

---

### Task 7: 模型服务设置页

**Files:**
- Modify: `app/settings_models.go`、`app/settings_general.go`（如小任务模型放在通用页）、`app/reader_ai.go`、`internal/i18n/zh.go`、`internal/i18n/en.go`
- Test: `app/settings_models_test.go`, `app/reader_ai_test.go`
- 画板：`SettingsModels.dc.html`

**Interfaces:**
- Consumes：core 导出的薄包装 `func (s *Service) ModelPresets() []ai.Preset`、`func NormalizeModelBaseURL(kind, raw string) (string, error)`、`func ModelEndpoint(kind, base string) string`（界面只用 `core/ai` 的类型与常量）；Task 3 `CheckModelService`、`KeyAction`、`ErrModelAuth`、`ErrKeyNeedsConfirm`；Task 6 `SmallModel`、`ai.Event.Model`
- Produces:
  - 「添加服务」先选预设（8 个 + 「自定义」）预填接口类型、名称、Base URL；需要 key 的预设显示「获取 API Key ↗」；本机预设的 API Key 标为「可选」
  - 接口类型三选一：OpenAI 兼容（Chat Completions）/ OpenAI 兼容（Responses）/ Anthropic 兼容
  - Base URL 失去焦点时规范化，显示「将请求 `<Endpoint>`」；失败显示原因
  - 编辑已有服务的 API Key：占位「已配置 · 留空表示保持不变」；地址或协议改动后，若 key 留空则提示「地址已改变：请重新输入 API Key，或选择不使用 Key」（选项「不使用 Key」= `clear`），不再自动沿用
  - 保存流程：①「探测」（`CheckModelService`，请求时记下表单快照的编辑代次）→ ② 选择 / 输入模型（拉取到的列表作推荐勾选；**总有**「手动添加模型 ID」输入；`ListSupported == false` 时提示「这个服务没有提供模型列表，请手动填写模型 ID」；空列表同样提示）→ ③ 提交 `Add/UpdateModelService`。探测结果只在表单快照未变时采用；`ErrModelAuth` → 字段下「API Key 被拒绝（401/403）」，不进入第 ②③ 步
  - 显示服务的「引用名：`<slug>`」（可复制，用于 skill 的 `godwit-model`）
  - 小任务模型选择：「小任务模型（总结、翻译、即时回复）」，选项「与默认模型相同」+ 全部模型，写 `SmallModel`（设置写队列）；取消勾选或删除服务时与默认模型一样清理
  - 阅读区总结 / 翻译的模型标注改用 `context` 事件里的 `Model`

- [ ] **Step 1: 写失败的测试**：`TestAddFromPreset`（选 Ollama → 预填，key 可选）；`TestBaseURLNormalizedOnBlur`；`TestManualModelAlwaysAvailable`（列表可用时仍可手填一个未列出的 ID 并保存）；`TestSaveWithTypedModels`（`/models` 404 → 提示，手填 `m1` 保存成功）；`TestSaveRejectsBadKey`（401 → 字段错误，`ListModelServices` 没有新增）；`TestAddressChangeRequiresKeyDecision`（改地址后 key 留空 → 提示；选「不使用 Key」→ 保存后无 key）；`TestStaleProbeIgnored`（探测进行中修改地址 → 旧结果不被采用）；`TestResponsesKindSelectable`；`TestSmallModelSetting`（选择后 `GetSettings().SmallModel` 更新；阅读区总结标注为小任务模型）。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行，确认通过** → PASS；`go test -race ./...`；`CGO_ENABLED=0 go build ./...`
- [ ] **Step 5: Commit**：`git commit -m "feat(app): presets, endpoint preview, probe before save, key decisions, Responses and the small-task model in settings"`

---

## Spec 同步

Task 2 开始前更新 spec 6.1：三种接口类型、slug 引用与冲突规则（兼容旧名称）、预设、保存前探测与手填模型、凭据只发往所属地址、小任务模型；第 10 节记录「按模型的上下文长度 / 视觉能力元数据」暂不做。
