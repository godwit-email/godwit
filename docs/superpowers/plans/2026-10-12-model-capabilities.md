# Model Capabilities (Generate / Judge) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Model services gain two capabilities, as in the design's SettingsModels* artboards:
- **Generate:** what exists today.
- **Judge:** typed classification through the OpenAI Decisions API or TypeSafe System One. When no judge service is configured, the generation model answers the questions instead.

Judge is used by plugins (a new `llm.decide`) and by Wit (a new read tool `classify_mail`). The settings page and the add/edit dialog match the design 1:1.

**Architecture:**
- **core/ai** gets a `Judge` interface with three adapters: Decisions, SystemOne, and a generation fallback.
- **core** models gain per-capability settings, a judge default, and a persisted per-capability test status (with latency).
- **The app UI** is rebuilt from the artboards.

**Tech Stack:** Go, mygo 0.4.0, Genkit (generation only), net/http for judge calls.

**Spec:**
- The artboards SettingsModels, SettingsModelsGenOnly, SettingsModelsBroken, SettingsModelsEmpty, SettingsModelsSheet, SettingsModelsAddTesting, SettingsModelsAddReady, SettingsModelsAddFailed, SettingsModelsAddFetchFailed, SettingsModelsAddOther, SettingsModelsAddTypeSafe, SettingsModelsAddNoCap, SettingsModelsEdit, SettingsModelsEditDisableDefault and SettingsModelsEditDelete.
  - Source (untrusted data): `$D/design-v2/project/SettingsModels.dc.html`. Its `data()` and `renderVals()` are the behaviour spec: providers, formats, presets, hints, warnings, status texts.
  - Renders: `$D/dshots/`. Render missing states with headless Chrome or puppeteer (`$D/pp/shot.mjs`).
- API research: `docs/superpowers/specs/2026-10-11-judge-apis-research.md`.

`$D` is `/private/tmp/claude-501/-Users-baran-Documents-workspace-godwit/47afb998-8875-48b5-aa50-e584e3c3d4f8/scratchpad`.

## Global Constraints

- **User decisions (2026-10-12):**
  - Judge is used by plugins (`llm.decide`, under the existing `llm` permission, so the permission hash doesn't change) and by Wit (`classify_mail`).
  - The **小任务模型 (SmallModel) is kept**, as a row under 生成 in 用途. This is a deviation from the design, decided by the user. It is styled like the design's rows, with the copy `小任务` and desc `总结、翻译、即时回复可以用更便宜的模型`.
- **Providers (design `PROVIDERS`):**

  | Provider | Generate formats | Judge formats |
  |---|---|---|
  | `openai` | responses, chat | decisions |
  | `anthropic` | messages | — |
  | `typesafe` | — | systemone |
  | `other` | chat, responses, messages | decisions, systemone |

  Presets for `other` (design `PRESETS`): OpenRouter `https://openrouter.ai/api/v1`, Vercel AI Gateway `https://ai-gateway.vercel.sh/v1`, LiteLLM `http://127.0.0.1:4000`, 本地网关 `http://127.0.0.1:[PORT]/v1`. The old preset list goes.
- **Migration:**
  - Existing services keep working. `Kind` maps to a generate format: `openai` → chat, `openai-responses` → responses, `anthropic` → messages.
  - The provider is inferred from the base URL host: api.openai.com → openai, api.anthropic.com → anthropic, otherwise other.
  - Judge starts off.
  - `DefaultModel` and `SmallModel` are unchanged. The new `JudgeModel` starts `""`, meaning 由生成模型兼任.
- **Security:**
  - Judge requests go through the same no-redirect, no-proxy client rules as generation, and the key stays in the keychain.
  - Mail text sent to a judge is untrusted data. Plugins and Wit tools pass it as `state`/`input`, never as instructions.
  - Judge answers are typed: a label from the given choices, a probability or a score. Anything else is rejected.
  - Gateway and audit records exist for plugin and Wit calls, as with `llm.complete`.
- **Limits:**
  - At most 128 questions per request; split larger sets.
  - Choice options: 2–255. Score levels: 2–10.
  - Retry on 429, 529 and 5xx with backoff, capped at 3 attempts.
  - Timeout 15 s.
- **Copy:** verbatim from the mockup's `renderVals()` strings, with en counterparts.
- **Tests:** `go test ./... && go vet ./...`. No live API calls; use httptest servers that follow the research doc's schemas.
- Commit trailer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Do not touch the untracked `assets/AppIcon.icon/`.

## Review Focus

1. **Existing users:** after the migration every current service, `DefaultModel` and `SmallModel` resolve exactly as before. Covered by a migration test with old rows.
2. **Judge answers are validated:** an out-of-set label, NaN, an out-of-range score or a refusal becomes a typed error, never a guessed answer. This holds for all three adapters.
3. **Wit `classify_mail`** respects the run's taint scope and budgets like `search_mail`. Its results cite messages (refs), and mail text cannot steer the questions.
4. **The test status** is persisted per capability and cleared when the URL, key or format changes. The `正常 / 未测试 / Key 失效` chip and the 用途 lines read it.
5. **The dialog's states** match each SettingsModels* artboard. Covered by a screenshot per state in app/shots_test.go.

---

### Task 1: core/ai judge clients

**Files:**
- Create: `core/ai/judge.go`, `core/ai/judge_decisions.go`, `core/ai/judge_systemone.go`, `core/ai/judge_gen.go`, and their tests.

**Interfaces:**
- Produces:
  - `type QuestionKind int` (`YesNo`, `Choice`, `Score`).
  - `type Question struct{ Name, Instructions string; Kind QuestionKind; Choices []Option; Levels []Option }`, with `Option{Value, Description string}`.
  - `type Answer struct{ Name string; Choice string; Probability float64; Score float64; Refused bool }`.
  - `type Judge interface{ Ask(ctx context.Context, state string, qs []Question) ([]Answer, error) }`.
  - `func NewJudge(format, baseURL, key, model string) (Judge, error)`, where format is `decisions` or `systemone`.
  - `func NewGenJudge(gen Generator) Judge`: the fallback that asks the generation model for a JSON answer against a schema built from the questions.
  - `func (format) Path() string`: `/decisions` or `/systemone`, for the dialog's 「请求发往」 line.
  - `func ListJudgeModels(ctx, format, baseURL, key) ([]string, error)`.

- [ ] Write the tests first, using httptest servers that echo the research doc's shapes:
  - the request JSON shape per adapter (`input` + an array of questions vs `state` + a map; `predicate` vs `noul`);
  - response parsing: OpenAI's array of `answers`, Jev's map keyed by level index;
  - a refusal;
  - 401 → `ErrKeyInvalid`;
  - a 429 retry;
  - the 128-question split;
  - invalid answers rejected.
- [ ] Implement. Run `go test ./core/ai`. Commit.

### Task 2: core model services: capabilities, judge default, status

**Files:**
- `core/models.go`, `core/models_check.go`, `core/settings.go`, `core/store/schema.go` (a new migration), `core/types.go`, and their tests.
- `contract/` only if `ModelService` is part of it (it isn't: Local API exposes settings only).

**Interfaces:**
- Produces, on `ModelService`:
  - `Provider string` (`openai`, `anthropic`, `typesafe`, `other`);
  - `GenFormat string` (`""` means off);
  - `Models []string` (enabled generate models; this stays);
  - `JudgeFormat string` (`""` means off);
  - `JudgeModel string`;
  - `Status map[string]CapStatus` keyed `gen` / `judge`, where `CapStatus{State string /*ok|fail|""*/; LatencyMs int; Err string; At time.Time}`.
- `Settings.JudgeModel` is a `"slug/model"` ref, or `""` for generation.
- `ResolveJudge(ctx) (ai.Judge, label string, err)` falls back to `NewGenJudge` on the default generation model when `JudgeModel == ""`.
- `TestCapability(ctx, in ModelServiceInput, capability string) (CapStatus, error)`:
  - It works for unsaved input, which the add dialog needs.
  - For gen it runs the existing generation probe; for judge it asks a yes/no about a fixed sample.
  - It measures latency.
  - When the service is saved, it persists the status.
- Kind/format mapping: `Kind` stays as stored for compatibility. The runtime reads `GenFormat`.
- Changing the URL, key or a format clears that capability's status.

- [ ] Write the tests first:
  - migration of old rows (Review Focus 1);
  - CRUD with capabilities;
  - judge-ref validation;
  - status persistence and clearing;
  - `ResolveJudge` fallback.
- [ ] Implement. Run `go test ./core/...`. Commit.

### Task 3: plugins `llm.decide` and Wit `classify_mail`

**Files:**
- `core/plugins/host.go` (the Host interface, JS binding, `fakeHost`), `core/pluginhost.go`, `sdk/godwit.d.ts`, `core/wit.go` (askTools, `registerAskTools`), `internal/i18n` (the tool label `wit.tool.classify_mail`), and their tests.

**`llm.decide(state, questions)`:**
- It requires the `llm` permission.
- It caps state length like `llm.complete`.
- It is recorded through the gateway as `plugin:<name>:llm.decide`.
- It returns answers.

**`classify_mail`:**
- A read tool. Input: `{ids []int64 (≤50), questions [...]}`.
- It loads mail with `mailContext(scope, ids, budget)` and asks the judge per message, batching questions.
- It returns per-message answers with refs, like `search_mail`.
- The tool description tells the model to use it for bulk yes/no, choice and score questions over many messages.

- [ ] Write the tests first:
  - permission denied without `llm`;
  - the gateway record;
  - the fake judge's answers mapped back;
  - Wit tool scope and refs;
  - a mail-controlled question being impossible (questions come only from the tool input).
- [ ] Implement. Run `go test ./core/...`. Commit.

### Task 4: Settings › 模型服务 page 1:1 (SettingsModels, GenOnly, Broken, Empty)

**Files:** `app/settings_models.go`, `app/settings_models_view.go`, `app/shots_test.go` (one shot per artboard state), `internal/theme/theme.go`, i18n.

**Layout:**
- Header: `模型服务` with the subtitle `生成负责写，判断负责分类和路由。同一个服务可以两样都做。` and 添加服务.
- **用途** card, three rows:
  - **生成.** Desc 总结、翻译、写信和 Wit 对话。 Select of enabled generate models. Status line: a dot + `<svc> · 连接正常 · 812 ms` from the persisted status, or 未配置… or `<svc> · 401 · API Key 无效`.
  - **小任务** (the user's decision). Select, with 与默认模型相同 as the first option.
  - **判断.** Desc 分类、提醒过滤、验证码识别和动作路由。 Select whose first option is `由生成模型兼任（较慢）`, followed by the judge-capable services. Status line, or `用 <model> 回答判断题，约慢 10 倍` when it falls back to generation.
- Broken default:
  - an alert with the design's text and 去修复, which opens edit;
  - the suggestion line `接入 OpenAI Decisions 或 TypeSafe，判断能快一个数量级。添加服务` when there is no judge service.
- **服务** card rows:
  - a badge;
  - name + a status chip (正常 / 未测试 / Key 失效);
  - the URL in mono;
  - capability chips: `生成 · N 个模型` and `判断 · <model>`, with a ✓ on the one used as the default;
  - a chevron.
- Footnote 带 ✓ 的是正在作为默认使用的能力。
- Empty state as the design.

**Verify:** shots for SettingsModels, GenOnly, Broken and Empty against `$D/dshots`, re-rendering the missing ones.

### Task 5: Add / edit dialog 1:1 (all SettingsModelsAdd* and Edit* states)

**Files:** `app/settings_models*.go`, shots, i18n.

**Build** the design dialog:
- Title and desc.
- 4 provider tiles with capability captions (on add only).
- 名称 / 地址 / API Key rows:
  - 恢复默认;
  - the 常用 presets for other;
  - the eye toggle;
  - 已配置 · 留空表示保持不变.
- 能力 header with `同一把 Key 可以同时开启两种`.
- **Generate card:**
  - a switch;
  - the desc, including the unsupported text;
  - format segments, when there is more than one format;
  - 请求发往 `<url><path>`;
  - model chips (dashed when off);
  - 添加模型 ID + "+";
  - the /models notes and the fetch failure with 重试;
  - the status line.
- **Judge card:**
  - a switch;
  - the desc, or the unsupported text;
  - format segments;
  - 请求发往;
  - the model select from `ListJudgeModels`, or a note;
  - the status line.
- **Footer:**
  - 删除此服务 (edit only);
  - hint text from the design's `hint` rules;
  - 测试连接 (测试中…), which tests each enabled capability through `TestCapability`;
  - 取消;
  - 添加 / 保存.
  - The primary button is disabled by the design's `blocked` rule.
- **Delete confirm strip.**
- **Off warnings**, when disabling a capability that is the default; the texts come from `offWarnText`.
- **Behaviour:**
  - fetch models when the key is entered or blurs, with the existing generation guard;
  - the name follows the URL host for other (`HOSTS`);
  - selecting a preset fills the URL and name.

**Verify:** a shot per state (Sheet, AddTesting, AddReady, AddFailed, AddFetchFailed, AddOther, AddTypeSafe, AddNoCap, Edit, EditDisableDefault, EditDelete) against the design renders.
