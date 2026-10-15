# 本地决策模型 Laya：原生插件 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用户从插件目录安装「Laya」后，Godwit 下载 ORT、tokenizer 和默认模型；插件设置里可以换成 registry 列出的其他模型（例如未量化的 fp32），换了才下载。设置 › 模型的「判断」来源可以选 `Laya（插件）`；#48 的标签分类器等所有 `ai.Judge` 调用方不改代码就走本地推理。默认不装、不下载。

**Architecture:** 三个仓库。

- `godwit-email/plugin-laya`（新）：Go c-shared 库，C ABI v1，`capabilities: ["judge"]`。在 plugin-host 子进程里用 purego 加载 ORT，tokenizer / 序列 / 解码用上游 `laya-go`。
- `godwit-email/godwit`：(a) `judge` 能力，`Settings.JudgeService` 可指向 `plugin/<name>`；(b) registry release 的大资产（`assets`）：固定资产安装时下载，模型等可选资产分成「变体」，由插件的一个 `select` 配置项选择、保存时才下载；流式、断点续传、按变体存放，`configure` 时把变体目录的绝对路径交给插件；(c) manifest 可声明空闲退出时间。
- `godwit-email/plugins`：registry 条目；模型与 ORT 托管在 dl.godwit.email。

**Spec:** #64（调研、任务 T0–T7、风险）；`docs/superpowers/specs/2026-10-14-native-plugins-design.md` §10（为什么走插件）。本计划替代 #64 的 T3（随应用打包 ORT）与 T6 的子进程客户端；`laya-spike` 分支的 `core/ai/laya/client.go`、`worker.go`、`cmd/godwit/laya.go` 不合入，`ort*.go`、`testdata/reference.json`、`TestLiveParity` 搬进插件仓库。

**Reviews:** 2026-10-15 Oracle（GPT-6 Astra Pro）审过第一版，本版吸收了经代码核实成立的意见：registry 向前兼容、插件契约错误不能当「否」、题数分批、换模型的事务性与并发、资产纳入信任与吊销、加载时校验、续传存储、启动预算、联调方式、T0 门槛。

## Decisions

- **Registry 向前兼容先行**（[#81](https://github.com/godwit-email/godwit/pull/81)）：客户端读不了的 release 放进 `Unsupported`、不让整份 registry 失败；发布工具仍严格。1.2.0 是唯一带严格解析器的版本。Laya 的每个 release 写 `minGodwit`（实现本计划的版本），并且等 #81 所在版本发布一段时间后才上架。已发布的 release 永不修改。
- 第一版只支持原生插件提供 `judge`；脚本插件声明 `judge` 仍是未知能力（不加 `ctx.judges.register`）。
- 空闲退出：manifest 新增可选 `native.idle`（秒，30–3600），缺省沿用 `nativeIdle`（10 分钟）。Laya 写 60。
- T0 只剩邮件评测；签名一项已由 #68 的 `disable-library-validation` 与 DLX 的 Developer ID 实测覆盖（ORT 依赖链在 Task 7 的端到端里再确认）。
- 不支持的平台（darwin-amd64、windows-arm64）在 registry 里不列产物，插件目录照现有逻辑显示「无法安装」。
- **模型不随插件，是插件的一个配置项**。可选模型列在 registry 的 release 里，由 Godwit 下载（不是插件自己下载）：完整性仍由签名 registry 保证，进度、续传、隔离标记由 Godwit 统一处理，plugin-host 仍不碰网络与数据目录。新增一个模型 = 插件发一个新版本（CI 自动，代码可不变），每个插件构建只配它测过的模型。
- **选模型用现有的 `select` 配置项**，加一个 `"asset": "<资产名>"` 属性；选项的 `value` 就是变体 ID，标签写在 manifest 里，现有的 `Accepts` 与设置页不用为新类型改。大小与「已下载」由设置页从已安装的 release 读出后附在选项旁。`checkRelease` 交叉校验：选项值与该资产的变体 ID 集合完全相同、`default` 是其中之一。
- 第一版模型：`int8`（社区配方复现，约 540 MB，默认）与 `fp32`（未量化，约 1.29 GB）。按 #64 §3.5，二者准确率相同、fp32 的 ECE 略好（0.114 对 0.137），但那组数字的 pipeline 可能与上游不一致（#64 §3.5 的提醒），以 T0 重测为准；代价是磁盘 2.4 倍、常驻内存更高（Task 6 实测后写进选项说明）。以后的微调模型也走同一条路。
- **阈值不归本计划**：标签阈值由另一个进行中的需求做成用户可配置。本计划的校准目标是让 Laya 的概率与云端 judge 同刻度，使同一个阈值在换 judge 后含义大致不变；不在 README 里写推荐阈值。

## Global Constraints

- 主仓库：`CGO_ENABLED=0` 下 `go build ./...`、`go test ./...` 通过；vet、gofmt 干净；不加依赖。ABI 版本不变（v1）：新方法和 `configure` 的新字段都是增量。
- judge 契约：请求 `{"input": "<text>", "questions": [ai.Question...]}`，响应 `{"answers": [ai.Answer...]}`。核心照常 `validateQuestions` / `validateAnswers`。
- **失败分类**：插件崩溃、超时、ABI 错误（`plugins.IsNativeFailure`）与答案不合契约，都算插件失败（计入三次停用），并且以 `ErrJudgePlugin` 报出，**不是** `ai.ErrJudgeAnswer`，所以标签分类器不会把它当成「全部为否」去删标签；插件返回的错误信封原样展示、不计数（同 translate）。失败计数在 `validateAnswers` 之后记。
- **请求大小**：插件批大小 16（核心按 16 分批，整次 `Ask` 共用一个截止时间）；发给插件前检查序列化后的请求 ≤ 1 MiB，超出直接返回 `ErrJudgeQuestion` 类错误、不计失败。`input` 与给云端 judge 的相同；截断到 512 token 是插件的事。
- **时间预算**：`configure` 只记录路径并检查文件存在（落在 10 秒启动时限内），不加载模型；Session 在第一次 `judge` 时加载（实测 0.12 s，冷盘另测）。整次 `Ask` 的超时取 `max(15 s, 题数 × 1 s)`，单次调用仍受 60 秒看门狗约束。
- **插件内部串行**：宿主可能同时送 4 个调用（`nativeInFlight`），插件内部用一把锁一次跑一题；Session 的替换也在这把锁下。
- 资产：只给 listed 插件下载；大小与 sha256 来自签名 registry；流式写盘，不整读进内存；下载中断可续传；校验不过就删，不允许「仍然安装」。固定资产在安装时下载；变体只下载配置里选中的那个（安装时用 `default`，升级时沿用当前选择）。
- **资产纳入信任身份**：「已收录」比对的是版本、manifest、库文件**和本平台的全部资产描述**（`listsRelease`、`isReviewedPackage`、`applyRegistry`）；registry 撤掉或替换某个资产，等同吊销这个 release。
- **加载时校验**：每个新进程启动前（含空闲退出、崩溃后的懒重启）和每次换模型时，按固定 sha256 校验所选资产的每个文件；目录里不能有多余文件或符号链接。只有设置页的「已下载」状态可以用 size + mtime 缓存。
- 插件只从 `configure` 得到资产路径；plugin-host 仍不读数据目录。

## Review Focus

1. **选了 Laya 但插件停用、卸载、资产缺失或被改**：判断返回明确错误（`ErrJudgeUnavailable` / `ErrJudgePlugin`），不静默退回生成模型；标签分类器本轮不写也不删标签，消息留在待分类状态。
2. **插件答错或崩溃**：用户已有的自动标签不被删；连续三次后插件停用；错误信封不让插件停用。
3. **大文件下载**：断网、退出应用后都能续传；sha256 不符就删并报错；插件升级、资产不变时不重新下载；卸载后无引用的资产被删。
4. **换模型**：从 int8 换到 fp32，下载期间判断仍用 int8；下载失败、取消或试跑失败，配置不变、int8 仍在；成功后才提交并删 int8（Windows 上删不掉就记下、下次启动再删，不回滚切换）。
5. **吊销**：registry 改了资产的 sha256 或撤掉资产，已安装的插件被吊销。
6. **内存**：推理只在 plugin-host 子进程；空闲 60 秒后进程退出。

---

## File Structure

```
godwit（主仓库）
core/plugins/manifest.go        knownCapabilities 加 "judge"；NativeSpec.Idle；ConfigField.Asset
core/plugins/registry.go        RegistryRelease.Assets、Asset、AssetVariant、AssetFile；checkRelease 交叉校验 select 与变体
core/plugins/native_fake.go     judge：按题型返回固定答案；可配错误信封、坏答案、慢答
core/registry.go                安装时下载固定资产与选中变体；listsRelease / isReviewedPackage / applyRegistry 纳入资产
core/assets.go（新）            可续传下载、<data>/plugin-assets/ 存放与租约、加载前校验、清理与待删记录
core/extensions.go              configure 带 assets；SetPluginConfig 换变体的准备—试跑—提交；nativeIdle 取 manifest；卸载清资产
core/store/schema.go            plugins 表新列 assets；待删资产表
core/judges.go（新）            nativeJudge、ListJudges、pluginJudge、ErrJudgePlugin
core/models.go                  resolveJudge 的 plugin/ 分支、ErrJudgeUnavailable
core/settings.go                JudgeService 允许 "plugin/<name>"
core/ai/judge.go                导出 NewBatchJudge(ask, batch, timeout)
core/labeler.go                 插件不可用或失败时保留标签；重扫前确认 judge 能跑
app/settings_models*.go         判断下拉列出插件
app/settings_extensions.go      安装确认显示总大小；资产 select 附大小与已下载；保存时显示进度、可取消
sdk/native/godwit_plugin.h      judge 方法、configure 的 assets
docs/registry/                  模板与审核清单加 assets
internal/i18n、app/errors.go、rpcbind/bind.go   文案与 wire code

plugin-laya（新仓库，骨架照 plugin-dlx）
main.go、bridge.c、protocol.go   ABI v1 envelope：describe / configure / judge
ort.go、ort_unix.go、ort_windows.go   来自 laya-spike：purego 调 ORT 1.27.1
judge.go                        Question → laya 序列 → logits → ai.Answer 形状；一把锁串行
truncate.go                     去引用历史与签名，截 512 token
calib.go                        predicate 温度（Task 7）
testdata/                       reference.json、上游 parity fixtures、公开或合成的邮件样例、judge 契约样例
scripts/quantize.py             两个变体的导出、量化、外部数据格式
scripts/package.py、smoke.py    同 DLX
eval/                           T0 / T7 评测：Laya 用 Python（上游 laya）；基线用一个小 Go 程序调 godwit 的 ai.NewGenJudge（gpt-6-luna）
plugin.json                     {"name":"laya","native":{"abi":1,"idle":60},"capabilities":["judge"],
                                 "config":[{"key":"model","type":"select","asset":"model","default":"int8",
                                            "options":[{"value":"int8",…},{"value":"fp32",…}],"label":…}]}
```

---

### Task R（主仓库，已开 PR）: registry 向前兼容

- [x] [#81](https://github.com/godwit-email/godwit/pull/81)：`ParseRegistry` 把读不了的 release 放进 `Unsupported`，release 之外的未知字段忽略；`Pick` 给出 `godwit:<minGodwit>` / `unsupported`；`CheckRegistry` 供 `godwit-registry check/sign` 严格校验；`docs/registry` 加「已发布的 release 不改」。
- [ ] 合入后尽快发补丁版本。

### Task 0: 邮件评测（go/no-go，不写产品代码）

在 `plugin-laya/eval/` 完成，结果写进 #64。邮件不入库，只入库标注与指标。

- [ ] 50–100 封真实邮件，人工标注 #48 的 7 个预设标签；按 7:3 分成拟合集与留出集（拟合集给 Task 7）。
- [ ] 输入用生产路径：`labelState(m)` 的输出（导出一个小工具或测试辅助生成），再按 Task 2 的截断规则处理。
- [ ] 上游 Python `laya` fp32 跑 noul。
- [ ] **基线**：同一批邮件走 Godwit 的 `GenJudge` 提示词，生成模型是本地网关 `http://127.0.0.1:9317/v1`（OpenAI Chat Completions 格式）上的 `gpt-6-luna`。这个网关不提供 Decisions（`/v1/decisions` 返回 501），所以基线只能走 `GenJudge`。`gpt-6-luna` 跑在 OpenAI 云上，评测集里不方便外发的邮件先剔除。
- [ ] 每个标签记录准确率、假阳率；Laya 另记 ECE 和 0.3–0.9 若干阈值下的假阳率（`GenJudge` 的概率只有 0/1，这两项对基线没有意义）；两边都附 p50 延迟。
- [ ] 确认原权重许可、`act_logits` 含义、choice 训练上限。
- [ ] **判定**（在留出集上，Laya 用 0.5 作阈值）：三条都满足才继续，否则停，关闭 #64 并记录原因。
  - 每个标签的假阳率不高于基线加 5 个百分点；
  - 任一标签的假阳率不超过 30%；
  - 7 个标签的平均准确率不低于基线减 5 个百分点（基线是前沿云模型，本地模型只要求接近）。

### Task 1: plugin-laya 骨架与 ORT

- [ ] 建仓库（Apache-2.0），复制 DLX 的 `bridge.c`、`protocol.go`、`sdk/godwit_plugin.h`、CI 矩阵（darwin-arm64、linux-amd64、linux-arm64、windows-amd64）。
- [ ] 搬入 `laya-spike` 的 `ort*.go`、`testdata/reference.json`、`TestLiveParity`；`run` 保持一次一题、关 arena 与内存复用。
- [ ] `configure`：读 `assets.ort`、`assets.tokenizer`、`assets.model`（各自变体目录的绝对路径；插件按约定的文件名打开，如 `<model>/model.onnx`，`.onnx.data` 在同目录），只检查文件存在；路径缺失返回 `invalid_request`。插件不认识变体名，只认路径。
- [ ] 推理锁：第一次 `judge` 时在锁内加载 ORT 与 Session；`configure` 换了模型路径时，在锁内关旧 Session，下次调用加载新的。
- [ ] 新增方法 `probe {}`：在锁内加载当前模型并跑一道固定题，返回 `{}` 或错误信封。宿主换模型时用它试跑（Task 4）。
- [ ] 验收：`TestLiveParity` 在 CI 各平台 1e-4 内通过（CI 下载 ORT 与模型并缓存）；Windows 上确认 ORT 所需的全部 DLL 都在资产里并能加载；`smoke.py` 经 Godwit 真实 `ServeNative` 跑通 `describe`/`configure`/`probe`；4 个并发 `judge` 结果与串行相同。

### Task 2: judge 方法

- [ ] 依赖 `github.com/NandhaKishorM/laya/laya-go` 锁 commit（PR #1169 合并前锁其分支 commit，必要时 vendor）。
- [ ] `Predicate` → noul（K=2，取 P(true)）；`Choice` → choice（K=选项数，超出训练上限答 `refusal`）；`Score` → score（期望值作 `Score`，各级概率进 `Probabilities`）。
- [ ] 截断：去掉引用历史（`>` 行、`On … wrote:` / `在 … 写道：` 之后）与签名（`-- ` 之后），再按 token 截到 512 单窗；不用 `predict_long`。
- [ ] 每题一次推理；单次请求题数上限 16，超出返回 `invalid_request`（宿主按 16 分批，正常不会触发）。
- [ ] 验收：Go 与 Python 在公开或合成的样例上 token、marker、概率一致（1e-4）；`testdata/judge-contract/` 里的请求与插件响应是一组固定样例，主仓库的契约测试（Task 3）用同一组样例跑 `validateAnswers`。

### Task 3（主仓库）: judge 能力

**Interfaces — Produces:**
- `knownCapabilities = []string{"translate", "judge"}`；脚本插件声明 `judge` → manifest 错误（第一版只支持原生）。
- `ai.NewBatchJudge(ask func(ctx context.Context, input string, qs []Question) ([]Answer, error), batch int, timeout time.Duration) Judge`：现有 `judge{}` 加批大小参数，`NewJudge` 用 `maxQuestions`。
- `Settings.JudgeService`：`""`、服务 slug，或 `"plugin/<name>"`（`validate` 放开这个前缀，名字过 `skills.ValidName`）。
- `var ErrJudgeUnavailable = errors.New("the judge plugin is not running")`；`var ErrJudgePlugin = errors.New("the judge plugin failed")`；rpc code `judge_unavailable`、`judge_plugin`。
- `nativeJudge.Ask`：内层 `NewBatchJudge(call, 16, …)`；返回后按错误分类：`ai.ErrJudgeAnswer` 或 `IsNativeFailure` → `pluginFailed` 并包成 `ErrJudgePlugin`；`*NativeError` 原样；成功 → `pluginSucceeded`。
- `resolveJudge`：`plugin/` 前缀 → `pluginJudge(name)`，label `plugin/<name>`，`gen == false`；不在运行 → `ErrJudgeUnavailable`，不退回生成模型。
- 标签分类器：`ErrJudgeUnavailable`、`ErrJudgePlugin` 让这一轮停下、不写也不删；`RescanLabels` 在删除自动标签之前先确认插件在运行、资产校验通过（全量重扫会先删自动标签，这是现有行为）。
- `ListJudges`（给设置页）：已装且声明 `judge` 的插件及运行状态。

- [ ] **Step 1: 失败的测试**（`core/judges_test.go`，原生夹具 `coretest.NativePluginFixture`）
  - `TestPluginJudge`：选 `plugin/laya` → 标签分类与脚本插件的 `ctx.llm.decide`（`pluginHost.LLMDecide`）得到假库答案；label 为 `plugin/laya`。
  - `TestPluginJudgeBatches`：17 题分两批、128 题分 8 批，都在一个截止时间内；单个超过 1 MiB 的输入（含 JSON 转义）被拒、不计失败。
  - `TestPluginJudgeUnavailable`：停用后 `resolveJudge` 返回 `ErrJudgeUnavailable`；没有模型调用；分类器不写也不删标签。
  - `TestPluginJudgeBadAnswerKeepsLabels`：已有自动标签的消息；假库少答一题 / 概率 1.5 → 标签还在、消息仍待分类；连续三次后插件 `DisabledReason == "failures"`；`rate_limited` 信封十次后仍启用。
  - `TestRescanWithStoppedPluginJudge`：插件停用时 `RescanLabels(all=true)` 报错且一个自动标签都没删。
  - `TestJudgeServiceValidate`：`plugin/Bad Name` 拒绝，`plugin/laya` 接受。
  - 契约：插件仓库 `testdata/judge-contract/` 的样例全部通过 `validateAnswers`。
  - manifest：脚本插件声明 `judge` 加载失败。
- [ ] **Step 2: 实现**，`go test ./core/...` 通过。
- [ ] **Step 3: 设置页**：判断下拉在服务之后列出 `Laya（插件）`，停用时标「已停用」；`app/settings_models_test.go` 覆盖。
- [ ] **Step 4: 头文件**写 `judge`、`probe` 方法与契约。

### Task 4（主仓库）: 大资产

registry 里 Laya 0.1.0 的资产大致是：

```json
"assets": [
  {"name": "ort", "platforms": ["darwin-arm64"], "variants": [
    {"id": "v1-27-1", "files": [{"name": "libonnxruntime.1.27.1.dylib", "url": "…", "sha256": "…", "size": 0}]}]},
  {"name": "tokenizer", "variants": [{"id": "v1", "files": [{"name": "tokenizer.json", "url": "…", "sha256": "…", "size": 34363188}]}]},
  {"name": "model", "variants": [
    {"id": "int8", "files": [{"name": "model.onnx", …}, {"name": "model.onnx.data", …}]},
    {"id": "fp32", "files": [{"name": "model.onnx", …}, {"name": "model.onnx.data", …}]}]}
]
```

**Interfaces — Produces:**
- `RegistryRelease.Assets []Asset`；`Asset{Name string; Platforms []string（空=全部）; Variants []AssetVariant}`；`AssetVariant{ID string; Files []AssetFile}`；`AssetFile{Name, URL, SHA256 string; Size int64}`。`checkRelease` 校验：名字与变体 ID 符合 `skills.ValidName`、不重复；文件名单层、跨平台可用、同一变体内不重复；URL https、sha256 小写 64 位、size > 0；每个支持的平台上同名资产恰好一个。
- 只有一个变体的资产是固定资产；多于一个变体的资产必须被 manifest 里恰好一个带 `asset` 的 `select` 配置项引用，且选项值与变体 ID 集合相同、`default` 在其中；否则 release 无效（发布被拒，旧客户端按 #81 跳过）。
- 侧载的插件（没有 registry 资产）声明了带 `asset` 的配置项 → 安装时报「只能从插件目录安装」。
- 存放：`<data>/plugin-assets/<变体摘要>/<文件名>`，摘要 = 该变体文件列表（名字 + sha256，排序后）的 sha256；同一变体的文件同目录、保持原名（ORT 外部数据按相对名查找）。插件行新列 `assets`（JSON：资产名 → 变体摘要）。
- 续传暂存：`<data>/plugin-assets/.partial/<文件 sha256>`，与安装用的 `.download-` 临时目录分开，启动时不删；同一 sha256 同时只有一个下载者。续传前重新哈希已有前缀；`206` 必须带匹配的 `Content-Range`，收到 `200` 从头写，`416` 删掉重下；超过 size 立刻中止。大文件用无总时限、30 秒无进展即断的超时。暂存超过 7 天或磁盘剩余不足总大小时清理并报错。
- 租约：运行中的进程、正在准备的候选变体、正在进行的下载各持有所用变体的租约；删除只针对无插件行引用且无租约的目录。Windows 上删除失败写进待删表，下次启动重试，不影响已提交的切换。
- `configure` 请求：`{"config": {...}, "assets": {"<资产名>": "<变体目录绝对路径>"}}`；没有资产的插件不带该字段。
- **换模型**（`SetPluginConfig` 改了带 `asset` 的项）：
  1. 在 `pluginsMu` 之外下载并校验新变体（进度回调同安装，可取消），持租约。
  2. 用候选配置起一个临时进程（或对运行中进程先 `configure` 候选再 `probe`），`probe` 成功才继续；失败则恢复旧配置，候选目录留给清理。
  3. 按 `registryMu → pluginsMu` 的顺序加锁，确认 `installDir`、配置版本、启用与信任状态在这期间没变，然后一起提交配置与资产绑定，发布新的运行快照。
  4. 提交后释放旧变体租约，尝试删除。
  插件停用时保存：只下载、校验和提交，不起进程。应用在第 1–3 步之间退出：配置仍是旧的，候选目录被清理或继续续传。
- 升级：沿用当前选择的变体；新版本不再列出它时退回 `default` 并在 `PluginInfo` 里提示。
- 进度：`PluginInstallProgress` 的 `download` 阶段按全部文件累计 `Done/Total`；安装确认显示固定资产加默认变体的总大小。

- [ ] **Step 1: 失败的测试**（`core/registry_test.go` 的 https 测试服务器）
  - 固定资产、默认变体与库都校验通过才安装；任一 sha256 不符 → 全部丢弃、`ErrRegistryMismatch`；未选中的变体从不请求。
  - 下载到一半断开、以及中途关掉 Service 再开 → 用 `Range` 续传，最终 sha256 正确；服务器回 `200`、`416`、不匹配的 `Content-Range` → 各自正确处理；磁盘空间不足 → 报错并清理。
  - 两个并发安装或换模型请求同一个文件 → 只下载一次。
  - 换变体：成功 → 新目录、旧目录被删、`configure` 收到新路径；下载失败、取消、`probe` 失败 → 配置仍是旧变体、旧目录还在；切换期间并发的 `judge` 调用都得到答案。
  - 删除被占用的目录失败 → 记入待删表，重启后删掉。
  - 升级到资产不变的新版本 → 不发资产请求；升级时 fp32 仍是 fp32；新版本不再列出当前变体 → 退回 `default` 并提示。
  - 吊销：registry 改了某个资产的 sha256 → 已安装的插件被吊销；撤掉整个资产同样。
  - 卸载 → 无引用、无租约的变体目录被删。
  - 加载时校验：改动资产文件的内容但保留 size 与 mtime → 下次进程启动前被发现，插件不启动并提示重新安装；目录里多出一个文件或符号链接 → 同样。
  - manifest / release：带 `asset` 的 `select` 选项值与变体不符、`default` 不在其中、引用不存在的资产、侧载插件带 `asset` 项 → 各自的错误。
  - 内存：下载 200 MiB 测试资产时堆的峰值增长 < 16 MiB（下载过程中采样 `runtime.ReadMemStats`）。
- [ ] **Step 2: 实现** `core/assets.go` 与 `core/registry.go`、`core/extensions.go` 的改动；`fetchRegistryFile` 保持给小文件用。
- [ ] **Step 3: 设置页**：带 `asset` 的 `select` 在选项旁显示大小与「已下载」，保存时显示进度、可取消。
- [ ] **Step 4:** `docs/registry/` 模板与审核清单加 `assets`；`cmd/godwit-registry check --fetch` 下载并校验资产。

### Task 5（主仓库）: manifest 空闲时间

- [ ] `NativeSpec.Idle int`（秒）：0 = 缺省 `nativeIdle`；否则 30–3600，越界 manifest 错误。
- [ ] `loadNative` 用它设 `NativeOptions.Idle`；测试用假库验证进程按设定时间（测试里缩放）退出、下次调用重启并重新 `configure`，且空闲退出后判断下拉里仍显示为可用。

### Task 6: 模型与 ORT 发布

- [ ] `scripts/quantize.py`：官方 `convaiinnovations/laya-multilingual` 导出 ONNX，得到两个变体，都转外部数据格式（`model.onnx` + `model.onnx.data`）：
  - `fp32`：导出后不量化。
  - `int8`：MatMulNBits 8-bit、block 32、对称；词表 fp16；决策头 50 个 MatMul 留 fp32。
- [ ] 验收（这里用的是 #64 §3.5 那组 284 道 zh-CN 短文本题，和 T0 的邮件集不是一回事）：`int8` 与社区 int8 答案一致 ≥ 282/284；`fp32` 与上游 Python safetensors 推理 1e-4 内一致；两者同输入多次逐位一致。
- [ ] 每个变体实测：磁盘、`phys_footprint` 峰值与空闲常驻、512 token p50、冷盘首次调用耗时；在 8 GB 机器上各跑一次 Task 7 的端到端。结果写进选项标签与 README（fp32 若常驻明显超过 1 GB，标签注明「需要较多内存」）。
- [ ] ORT 1.27.1 各平台库、tokenizer 与两个模型变体上传 dl.godwit.email，固定 sha256。
- [ ] plugin-laya release CI 生成带 `assets` 与 `minGodwit` 的 `registry-release.json`（`ort` 按平台、`tokenizer`、`model` 两个变体）。以后加模型（例如微调版）= 加一个变体并发插件新版本。

### Task 7: 校准与上线

- [ ] 在 T0 拟合集上为 predicate 拟合温度，两个变体各一份，写进 `calib.go`；在留出集上验收：ECE 不高于云端 judge（#64 记录的 jev 为 0.246），0.3–0.9 各阈值下的假阳率记录在 #64，供阈值配置需求参考。choice 与 score 沿用上游温度 `[1,1,1]`，作为已知限制写进 README。
- [ ] 端到端（macOS Developer ID 签名包、Linux、Windows 各一次，用真实 registry 的测试副本）：从插件目录装 Laya → 下载默认 int8 → 判断来源选 Laya → 新邮件被打标签 → 插件设置里换成 fp32、判断照常 → 空闲 60 秒后 plugin-host 退出 → 卸载后无进程、无资产。
- [ ] 确认 #81 所在版本已发布一段时间，然后向 `godwit-email/plugins` 提交 registry 条目；README 写清隐私（全部本地）、每个模型的磁盘与内存、不支持的平台。

## Dependencies

Task R 最先（已开 PR）。Task 0 决定是否继续。Task 1–2（插件仓库）与 Task 3、5（主仓库）可并行：插件仓库先用 `ServeNative` 冒烟测试与手工路径开发，主仓库的 judge 能力用假库测试。Task 4 完成后，用 `coretest.RegistryServer` 起一个签名的测试 registry 做联调（不用侧载，侧载插件不能声明资产）。Task 6 依赖 Task 4 的 registry 格式；Task 7 依赖全部。
