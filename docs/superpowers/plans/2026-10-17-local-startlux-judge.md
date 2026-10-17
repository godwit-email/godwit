# 本地决策模型 StartLux：原生插件 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用户从插件目录安装「StartLux」插件后，Godwit 下载 llama.cpp 引擎和默认模型；插件设置里可以换成另一个模型，换了才下载。设置 › 模型的「判断」来源可以选 `StartLux（插件）`；#48 的标签分类器等所有 `ai.Judge` 调用方不改代码就走本地推理。默认不装、不下载。

**Why StartLux:** #64 的评测（[Laya](https://github.com/godwit-email/godwit/issues/64#issuecomment-6195378184)、[StartLux](https://github.com/godwit-email/godwit/issues/64#issuecomment-6225261345)）。99 封真实邮件、生产输入、以 gpt-6-luna 为准：StartLux-Decision v1.1 4B Instant Q4_K_M 一致率 92.1%（阈值 0.5），云端 jev 是 92.4%（0.8），全部答「否」是 82.3%，Laya 零样本最好也只到 82.3%。

**Architecture:** 三个仓库。

- `godwit-email/plugin-startlux`（新）：Go c-shared 库，C ABI v1，`capabilities: ["judge"]`。在 plugin-host 子进程里按需启动 llama.cpp 的 `llama-server`，监听 127.0.0.1 上的随机端口，带随机 API key；按 StartLux 的格式拼 prompt，用 `/completion` 读出选项字母的概率，换算成 `ai.Answer`。
- `godwit-email/godwit`：(a) `judge` 能力，`Settings.JudgeService` 可指向 `plugin/<name>`；(b) registry release 的大资产（`assets`）：引擎按平台、模型分变体，由插件的一个 `select` 配置项选择，保存时才下载；流式下载、断点续传、按变体存放，`configure` 时把绝对路径交给插件；(c) manifest 可声明空闲退出时间；(d) plugin-host 退出时清理自己的子进程。
- `godwit-email/plugins`：registry 条目。引擎与模型镜像到 dl.godwit.email。

**Spec:** #64；`docs/superpowers/specs/2026-10-14-native-plugins-design.md` §10（为什么走插件）。本计划替代已关闭的 #83（Laya 版计划），沿用其中经 Oracle 审核、对照代码核实过的约束。

## Decisions

- **引擎：llama.cpp 官方构建，所有平台统一用 GGUF**，不用 MLX、不用 ORT。实测 4B 8 位：MLX 单封 p50 3.4 s、峰值 7.6 GB；llama.cpp + Metal 是 2.8 s、5.0 GB，准确率相同。
- **不打补丁**：原版 `llama-server` 加 `--parallel 1 --ctx-checkpoints 0 -c 4096 -b 4096 -ub 4096 --cache-ram 0 -ngl 99`，与打了 StartLux 补丁的版本相比，693 个概率逐位相同。GGUF 自带 `qwen35.attention.causal = false`，全注意力层自动看到整个 prompt。
- **`--cache-ram 0` 是必须的**：默认的提示缓存会把内存撑到 8 GB 以上（4B 跑完约 9 GB），关掉后对准确率和速度都没有影响，因为每个 prompt 都整体重算。
- **分词交给 llama-server**：插件只拼 prompt 文本。GGUF 词表与 HF tokenizer 在 693 个真实 prompt 上的 token id 完全一致；插件不带 tokenizer。
- **锁定 llama.cpp 版本并镜像**：官方 Releases 只保留近期版本（b11549 已被撤掉）。选一个版本，把各平台的包镜像到 dl.godwit.email、固定 sha256；升级前必须重跑对照测试（Task 2 验收）。
- **平台与引擎包**（官方包压缩后的大小）：macOS arm64 12 MB、x64 11 MB（Metal）；Linux x64 Vulkan 30 MB、arm64 CPU 13 MB；Windows x64 Vulkan 32 MB、arm64 CPU 12 MB。CUDA 包超过 150 MB，不提供。Vulkan 包没有可用 GPU 时退回 CPU（Task 1 确认）。
- **模型不随插件发布，是插件的一个配置项**。可选模型列在 registry 的 release 里，由 Godwit 下载：完整性仍由签名 registry 保证，进度、续传、隔离标记由 Godwit 统一处理，plugin-host 不碰网络和数据目录。新增模型 = 插件发一个新版本。
- **第一版模型变体**（峰值内存 ≈ GGUF + 0.2–0.5 GB）：
  - `4b-q4`（默认）：GGUF 2.71 GB，峰值 3.3 GB，一致率 92.1%，单封 p50 2.7 s，`minMemoryGB: 8`；
  - `2b-q8`：GGUF 2.01 GB，峰值 2.5 GB，一致率 88.9%，单封 p50 0.8 s，`minMemoryGB: 6`。
  - 每个变体的文件是 GGUF 加它自己的 `decision_config.json`（选项字母的 token id、按题型的温度；2B 与 4B 的不同）。
  - **不按内存自动选变体。** 选项声明 `minMemoryGB`，本机物理内存不够就不让装、不让选那一档；默认永远是 manifest 的 `default`（`4b-q4`）。本机一档都跑不了，整个插件不能装，提示缺多少内存。
- **选模型用现有的 `select` 配置项**，加一个 `"asset": "<资产名>"` 属性；选项的 `value` 就是变体 ID，可选 `minMemoryGB`。`checkRelease` 交叉校验选项与变体。
- 第一版只支持原生插件提供 `judge`（不加 `ctx.judges.register`）。
- 空闲退出：manifest 新增可选 `native.idle`（秒，30–3600），插件写 120。空闲退出连带结束 llama-server，内存归零。
- **阈值不归本计划**：标签阈值由另一个进行中的需求做成可配置。数据显示 StartLux 的最佳阈值在 0.5–0.7、jev 在 0.8 左右，默认阈值应按 judge 分别设，这一点交给那个需求。
- **choice 超过 26 个选项**：第一版答 `refusal`。StartLux 的分组淘汰读出（`wide_choice`）以后再做。

## Open Questions（需要拍板）

1. **T0 门槛里「通知」的 30% 硬上限**：4B Q4 在「通知」上的假阳率是 0.34（阈值 0.5），超过上限；jev 在同一标签上是 0.82–0.84。其余 6 个标签都在 0–0.15。选项：对基线本身就超过上限的标签改为只比相对差；或者先改「通知」的标签定义再重测。
2. **CPU-only 机器**：Windows 和 Linux 上没测。如果 4B 单封超过 60 秒的调用上限，是只提供 2B，还是在这类平台上不提供插件。Task 1 量出数字后再定。

## Global Constraints

- 主仓库：`CGO_ENABLED=0` 下 `go build ./...`、`go test ./...` 通过；vet、gofmt 干净；不加依赖。ABI 版本不变（v1）：新方法和 `configure` 的新字段都是增量。
- judge 契约：请求 `{"input": "<text>", "questions": [ai.Question...]}`，响应 `{"answers": [ai.Answer...]}`。核心照常 `validateQuestions` / `validateAnswers`。
- **失败分类**：插件崩溃、超时、ABI 错误（`plugins.IsNativeFailure`）与答案不合契约，都算插件失败（计入三次停用），并以 `ErrJudgePlugin` 报出，**不是** `ai.ErrJudgeAnswer`，所以标签分类器不会把它当成「全部为否」去删标签。插件返回的错误信封原样展示、不计数。失败计数在 `validateAnswers` 之后记。
- **请求大小**：核心按 16 题分批，整次 `Ask` 共用一个截止时间；发给插件前检查序列化后的请求 ≤ 1 MiB，超出直接拒绝、不计失败。
- **时间预算**：实测 4B 每题最慢约 1.6 s。`Ask` 的超时取 `max(15 s, 题数 × 3 s)`；每次调用 16 题，落在 60 秒看门狗之内。冷启动（拉起 llama-server、加载模型）计入第一次调用。
- **prompt 长度**：每个 prompt 必须整个装进 `-ub 4096`，否则原版服务会分段算、破坏双向注意力。插件用 `/tokenize` 计数，超过 4096 − 16 时按比例截短 state 重算；绝不发超长 prompt。
- **插件内部串行**：llama-server 只有一个槽位；宿主可能同时送 4 个调用（`nativeInFlight`），插件用一把锁一次跑一题。
- **llama-server 的边界**：只监听 127.0.0.1；端口由插件选空闲端口；每次启动生成随机 `--api-key`；关掉 web UI 与 slots 端点（`--no-webui --no-slots`，后者默认是开的）；`--metrics`、`--props` 保持默认关闭。
- 资产：只给 listed 插件下载；大小与 sha256 来自签名 registry；流式写盘，不整个读进内存；中断可续传；校验不过就删，不允许「仍然安装」。固定资产（引擎）安装时下载；变体只下载配置里选中的那个。
- **资产纳入信任身份**：「已收录」比对的是版本、manifest、库文件**和本平台的全部资产描述**（`listsRelease`、`isReviewedPackage`、`applyRegistry`）；registry 撤掉或替换某个资产，等同吊销这个 release。
- **加载时校验**：每个新进程启动前（含空闲退出、崩溃后的懒重启）和每次换模型时，按固定 sha256 校验所选资产的每个文件；目录里不能有多余文件或符号链接。只有设置页的「已下载」状态可以用 size + mtime 缓存。
- 插件只从 `configure` 得到资产路径和一个可写的运行目录；plugin-host 仍不读数据目录。
- **Registry 兼容**：release 带 `minGodwit`，取实现本计划的 Godwit 版本。v1.3.0 起的客户端（#81）会把读不了的 release 放一边、显示「需要更新」；1.2.x 已不在支持范围。

## Review Focus

1. **选了 StartLux 但插件停用、卸载、资产缺失或被改**：判断返回明确错误（`ErrJudgeUnavailable` / `ErrJudgePlugin`），不静默退回生成模型；标签分类器本轮不写也不删标签。
2. **插件答错或崩溃**：用户已有的自动标签不被删；连续三次后插件停用；错误信封不让插件停用。
3. **llama-server 不留孤儿**：空闲退出、停用、卸载、超时、Godwit 退出或崩溃之后，都没有 llama-server 进程残留；下次启动时清掉上次异常退出留下的进程。
4. **大文件下载**：断网、退出应用后都能续传；sha256 不符就删并报错；插件升级、资产不变时不重新下载；卸载后无引用的资产被删。
5. **换模型**：下载期间判断仍用旧模型；下载失败、取消或试跑失败，配置不变、旧模型还在；成功后才提交并删旧模型（Windows 上删不掉就记下、下次启动再删）。
6. **读出与参考实现一致**：同一批 prompt 上，插件的概率与 StartLux 参考实现相差不超过 1e-6。
7. **内存**：常驻 ≈ GGUF + 0.5 GB；空闲退出后归零。

---

## File Structure

```
godwit（主仓库）
core/plugins/manifest.go        knownCapabilities 加 "judge"；NativeSpec.Idle；ConfigField.Asset
core/plugins/registry.go        RegistryRelease.Assets、Asset、AssetVariant、AssetFile；checkRelease 交叉校验
core/plugins/native_fake.go     judge：按题型返回固定答案；可配错误信封、坏答案、慢答
core/plugins/native_host.go     plugin-host 退出（stdin 关闭、空闲）前结束自己的进程组
core/registry.go                安装时下载固定资产与选中变体；listsRelease / isReviewedPackage / applyRegistry 纳入资产
core/assets.go（新）            可续传下载、<data>/plugin-assets/ 存放与租约、加载前校验、清理与待删记录
core/extensions.go              configure 带 assets 与 runtime_dir；换变体的准备、试跑、提交；nativeIdle 取 manifest；卸载清资产
core/store/schema.go            plugins 表新列 assets；待删资产表
core/judges.go（新）            nativeJudge、ListJudges、pluginJudge、ErrJudgePlugin、ErrJudgeUnavailable
core/models.go                  resolveJudge 的 plugin/ 分支
core/settings.go                JudgeService 允许 "plugin/<name>"
core/ai/judge.go                导出 NewBatchJudge(ask, batch, timeout)
core/labeler.go                 插件不可用或失败时保留标签；重扫前确认 judge 能跑
app/settings_models*.go         判断下拉列出插件
app/settings_extensions.go      安装确认显示总大小；资产 select 附大小与已下载；保存时显示进度、可取消
sdk/native/godwit_plugin.h      judge、probe 方法；configure 的 assets 与 runtime_dir
docs/registry/                  模板与审核清单加 assets

plugin-startlux（新仓库，骨架照 plugin-dlx）
main.go、bridge.c、protocol.go   ABI v1 envelope：describe / configure / judge / probe
server.go                       拉起与看护 llama-server：空闲端口、随机 API key、固定参数、健康检查、pidfile、退出清理
prompt.go                       StartLux 的 prompt 格式（照 startlux_decide/prompt.py）与 Qwen 对话模板（关思考）
readout.go                      /completion 读出、温度 softmax、noul / choice / score 换算成 ai.Answer
config.go                       解析 decision_config.json
testdata/                       参考实现生成的 prompt 与概率（公开或合成样例）、judge 契约样例
scripts/reference.py            用 startlux_decide 生成 testdata 的参考值
scripts/package.py、smoke.py    同 DLX
eval/                           #64 的评测脚本（不含邮件）：生产输入、基线、打分
plugin.json                     {"name":"startlux","native":{"abi":1,"idle":120},"capabilities":["judge"],
                                 "config":[{"key":"model","type":"select","asset":"model","default":"4b-q4",
                                            "options":[{"value":"4b-q4","minMemoryGB":8,…},
                                                       {"value":"2b-q8","minMemoryGB":6,…}],"label":…}]}
```

---

### Task 0（已完成）: 评测

- [x] Laya 零样本不通过；StartLux 通过（「通知」的硬上限除外，见 Open Questions）。结果在 #64。
- [x] Registry 向前兼容（#81，v1.3.0）。

### Task 1: 跨平台引擎实测（不写产品代码）

- [ ] 在 Windows x64（有 GPU 和只有 CPU 各一台）、Linux x64（同上）上，用锁定版本的官方包跑 `eval/` 的 99 封：每种机器记录 2B 和 4B 的一致率、单封 p50 / 最大、峰值内存。
- [ ] 确认 Vulkan 包在没有可用 GPU 时自动退回 CPU、不报错；确认 macOS x64 用 Metal 还是 CPU。
- [ ] 按结果回答 Open Question 2，写进 #64。

### Task 2: plugin-startlux 骨架与读出

- [ ] 建仓库（Apache-2.0），复制 DLX 的 `bridge.c`、`protocol.go`、`sdk/godwit_plugin.h`、CI 矩阵（darwin-arm64、darwin-amd64、linux-amd64、linux-arm64、windows-amd64、windows-arm64）。
- [ ] `prompt.go`：照 `startlux_decide/prompt.py` 移植，包括 system 一行、`Evidence / Question / Options` 块、字母编号、noul 的 `yes` / `no`、选项只有 id 时只显示 id、纯字母或数字 id 在全部有描述时隐藏、score 由低到高、最多 26 个选项。对话模板写成常量：`<|im_start|>system\n…<|im_end|>\n<|im_start|>user\n…<|im_end|>\n<|im_start|>assistant\n<think>\n\n</think>\n\n`。
- [ ] `readout.go`：`/completion` 的请求体与参考实现一致：`prompt`（文本）、`n_predict 1`、`n_probs k`、`post_sampling_probs true`、`top_k k`、对 k 个字母各加 +100 的 `logit_bias`、`cache_prompt false`、`seed 0`，以及中性采样参数（`temperature 1`、`top_p 1`、`min_p 0`、`typical_p 1`、各类惩罚为 0）。取回的概率取对数，除以 `temperature_by_type[题型]` 后做 softmax。noul 的 `Probability` 是「yes」的概率；choice 取最大的选项并给出各选项概率；score 取期望等级并给出各级概率。
- [ ] `server.go`：第一次 `judge` 或 `probe` 时启动 llama-server（固定参数见 Decisions；`--host 127.0.0.1 --port <空闲端口> --api-key <随机> --no-webui --no-slots`），等 `/health` 就绪；进程写入 `runtime_dir` 下的 pidfile，启动前先结束 pidfile 里残留的进程（确认它确实是 llama-server 再动手）。Linux 上设 `Pdeathsig`。
- [ ] `configure`：读 `assets.engine`（引擎目录）、`assets.model`（变体目录：`model.gguf` 与 `decision_config.json`）、`runtime_dir`，只检查文件存在，不启动引擎。模型路径变了就在锁内结束旧引擎，下次调用用新的。
- [ ] `probe {}`：在锁内拉起引擎并跑一道固定题，宿主换模型时用它试跑。
- [ ] 验收：
  - `scripts/reference.py` 用 startlux_decide 为一组公开或合成样例生成 prompt 文本与概率；Go 拼出的 prompt 逐字节相同，概率相差 ≤ 1e-6（CI 各平台都跑，下载锁定版本的引擎和 0.8B Q8_0 模型并缓存）。
  - 一组超长样例：每个 prompt 都 ≤ 4096 token，截短后的结果仍过契约。
  - 4 个并发 `judge` 的结果与串行相同。
  - `smoke.py` 经 Godwit 真实的 `ServeNative` 跑通 `describe`、`configure`、`probe`、`judge`。
  - plugin-host 被杀（SIGKILL 和 Windows 上的 TerminateProcess）后，下一次启动会清掉残留的 llama-server。

### Task 3（主仓库）: judge 能力

**Interfaces — Produces:**
- `knownCapabilities = []string{"translate", "judge"}`；脚本插件声明 `judge` → manifest 错误。
- `ai.NewBatchJudge(ask func(ctx context.Context, input string, qs []Question) ([]Answer, error), batch int, timeout func(n int) time.Duration) Judge`：现有 `judge{}` 加批大小与按题数的超时，`NewJudge` 沿用 `maxQuestions` 和 15 秒。
- `Settings.JudgeService`：`""`、服务 slug，或 `"plugin/<name>"`。
- `var ErrJudgeUnavailable = errors.New("the judge plugin is not running")`、`var ErrJudgePlugin = errors.New("the judge plugin failed")`；rpc code `judge_unavailable`、`judge_plugin`。
- `nativeJudge.Ask`：内层 `NewBatchJudge(call, 16, …)`；`ai.ErrJudgeAnswer` 或 `IsNativeFailure` → `pluginFailed` 并包成 `ErrJudgePlugin`；`*NativeError` 原样；成功 → `pluginSucceeded`。
- `resolveJudge`：`plugin/` 前缀 → `pluginJudge(name)`，label `plugin/<name>`，`gen == false`；不在运行 → `ErrJudgeUnavailable`，不退回生成模型。
- 标签分类器：`ErrJudgeUnavailable`、`ErrJudgePlugin` 让这一轮停下、不写也不删；`RescanLabels` 在删除自动标签之前先确认插件在运行、资产校验通过（全量重扫会先删自动标签，这是现有行为）。

- [ ] **Step 1: 失败的测试**（`core/judges_test.go`，原生夹具 `coretest.NativePluginFixture`）
  - `TestPluginJudge`：选 `plugin/startlux` → 标签分类与脚本插件的 `ctx.llm.decide` 得到假库答案；label 为 `plugin/startlux`。
  - `TestPluginJudgeBatches`：17 题分两批、128 题分 8 批，都在一个截止时间内；超过 1 MiB 的输入被拒、不计失败。
  - `TestPluginJudgeUnavailable`：停用后 `resolveJudge` 返回 `ErrJudgeUnavailable`；没有模型调用；分类器不写也不删标签。
  - `TestPluginJudgeBadAnswerKeepsLabels`：已有自动标签的消息；假库少答一题或给出概率 1.5 → 标签还在、消息仍待分类；连续三次后插件停用；`rate_limited` 信封十次后仍启用。
  - `TestRescanWithStoppedPluginJudge`：插件停用时 `RescanLabels(all=true)` 报错，且一个自动标签都没删。
  - `TestJudgeServiceValidate`：`plugin/Bad Name` 拒绝，`plugin/startlux` 接受。
  - 契约：插件仓库 `testdata/judge-contract/` 的样例全部通过 `validateAnswers`。
- [ ] **Step 2: 实现**，`go test ./core/...` 通过。
- [ ] **Step 3: 设置页**：判断下拉在服务之后列出 `StartLux（插件）`，停用时标「已停用」。
- [ ] **Step 4: 头文件**写 `judge`、`probe` 方法与契约。

### Task 4（主仓库）: 大资产

registry 里 0.1.0 的资产大致是：

```json
"assets": [
  {"name": "engine", "platforms": ["darwin-arm64"], "variants": [
    {"id": "b11700-metal", "files": [{"name": "llama-server", "url": "…", "sha256": "…", "size": 0}, {"name": "libllama.dylib", …}, …]}]},
  {"name": "model", "variants": [
    {"id": "4b-q4", "files": [{"name": "model.gguf", "url": "…", "sha256": "…", "size": 2710000000}, {"name": "decision_config.json", …}]},
    {"id": "2b-q8", "files": [{"name": "model.gguf", "url": "…", "sha256": "…", "size": 2010000000}, {"name": "decision_config.json", …}]}]}
]
```

**Interfaces — Produces:**
- `RegistryRelease.Assets []Asset`；`Asset{Name string; Platforms []string（空=全部）; Variants []AssetVariant}`；`AssetVariant{ID string; Files []AssetFile}`；`AssetFile{Name, URL, SHA256 string; Size int64}`。`checkRelease` 校验：名字与变体 ID 符合 `skills.ValidName`、不重复；文件名单层、跨平台可用、同一变体内不重复；URL https、sha256 小写 64 位、size > 0；每个支持的平台上同名资产恰好一个。
- 只有一个变体的资产是固定资产；多于一个变体的资产必须被 manifest 里恰好一个带 `asset` 的 `select` 配置项引用，选项值与变体 ID 集合相同、`default` 在其中。
- **`minMemoryGB`**：select 选项可声明正整数；安装和保存配置时读本机物理内存，不够的变体拒绝（不自动改选别的）。设置页灰掉并写「需要 ≥ N GB 内存」。默认变体本机不够 → 安装确认里必须另选一档够格的，否则不能装。一档都不够 → 整个插件不能装。侧载同样按这个门槛拦。
- 侧载的插件声明了带 `asset` 的配置项 → 安装时报「只能从插件目录安装」。
- 存放：`<data>/plugin-assets/<变体摘要>/<文件名>`，摘要 = 该变体文件列表（名字 + sha256，排序后）的 sha256。可执行文件下载后设 0o755；macOS 上不打隔离标记（`net/http` 下载本来就不打）。插件行新列 `assets`（JSON：资产名 → 变体摘要）。
- 续传暂存：`<data>/plugin-assets/.partial/<文件 sha256>`，与安装用的 `.download-` 临时目录分开，启动时不删；同一 sha256 同时只有一个下载者。续传前重新哈希已有部分；`206` 必须带匹配的 `Content-Range`，收到 `200` 从头写，`416` 删掉重下；超过 size 立刻中止。大文件用无总时限、30 秒无进展即断的超时。暂存超过 7 天、或磁盘剩余不足总大小时清理并报错。
- 租约：运行中的进程、正在准备的候选变体、正在进行的下载，各自持有所用变体的租约；只删除无插件行引用且无租约的目录。Windows 上删除失败写进待删表，下次启动重试，不影响已提交的切换。
- `configure` 请求：`{"config": {...}, "assets": {"<资产名>": "<变体目录绝对路径>"}, "runtime_dir": "<插件专用的可写目录>"}`；没有资产的插件不带这两个字段。
- **换模型**（`SetPluginConfig` 改了带 `asset` 的项）：
  1. 在 `pluginsMu` 之外下载并校验新变体（进度回调同安装，可取消），持租约。
  2. 对运行中的进程先 `configure` 候选、再 `probe`；`probe` 成功才继续，失败则恢复旧配置。
  3. 按 `registryMu → pluginsMu` 的顺序加锁，确认 `installDir`、配置版本、启用与信任状态这期间没变，然后一起提交配置与资产绑定。
  4. 提交后释放旧变体的租约，尝试删除。
  插件停用时保存：只下载、校验和提交，不起进程。应用在第 1–3 步之间退出：配置仍是旧的，候选目录被清理或继续续传。
- 升级：沿用当前选择的变体；新版本不再列出它时退回 `default` 并在 `PluginInfo` 里提示。
- 进度：`PluginInstallProgress` 的 `download` 阶段按全部文件累计 `Done/Total`；安装确认显示引擎加默认变体的总大小（约 2.7–2.8 GB）。

- [ ] **Step 1: 失败的测试**（`core/registry_test.go` 的 https 测试服务器）
  - 固定资产、默认变体与库都校验通过才安装；任一 sha256 不符 → 全部丢弃、`ErrRegistryMismatch`；未选中的变体从不请求。
  - 下载到一半断开、以及中途关掉 Service 再开 → 用 `Range` 续传，最终 sha256 正确；服务器回 `200`、`416`、不匹配的 `Content-Range` → 各自正确处理；磁盘空间不足 → 报错并清理。
  - 两个并发安装或换模型请求同一个文件 → 只下载一次。
  - 换变体：成功 → 新目录、旧目录被删、`configure` 收到新路径；下载失败、取消、`probe` 失败 → 配置仍是旧变体、旧目录还在；切换期间并发的 `judge` 调用都得到答案。
  - 删除被占用的目录失败 → 记入待删表，重启后删掉。
  - 升级到资产不变的新版本 → 不发资产请求；升级时所选变体保持不变；新版本不再列出当前变体 → 退回 `default` 并提示。
  - 吊销：registry 改了某个资产的 sha256 → 已安装的插件被吊销；撤掉整个资产同样。
  - 卸载 → 无引用、无租约的变体目录被删。
  - 加载时校验：改动资产文件的内容但保留 size 与 mtime → 下次进程启动前被发现，插件不启动并提示重新安装；目录里多出一个文件或符号链接 → 同样。
  - `minMemoryGB`：模拟 8 GB 机器可装默认 `4b-q4`；模拟 7 GB 不能装 `4b-q4`，可改选 `2b-q8` 后装上；模拟 5 GB 一档都不给装；保存配置选不够格的变体被拒、配置不变。
  - 内存：下载 200 MiB 测试资产时堆的峰值增长 < 16 MiB。
- [ ] **Step 2: 实现** `core/assets.go` 与 `core/registry.go`、`core/extensions.go` 的改动；`fetchRegistryFile` 保持给小文件用。
- [ ] **Step 3: 设置页**：带 `asset` 的 `select` 在选项旁显示大小、所需内存与「已下载」；本机不够的选项灰掉、写「需要 ≥ N GB 内存」；保存时显示进度、可取消。
- [ ] **Step 4:** `docs/registry/` 模板与审核清单加 `assets`；`cmd/godwit-registry check --fetch` 下载并校验资产。

### Task 5（主仓库）: 空闲时间与子进程清理

- [ ] `NativeSpec.Idle int`（秒）：0 = 缺省 `nativeIdle`；否则 30–3600，越界报 manifest 错误。`loadNative` 用它设 `NativeOptions.Idle`。
- [ ] plugin-host 在 stdin 关闭（空闲退出、停用、Godwit 退出）时，先结束自己进程组里的其他进程再退出（Unix：`kill(-pgid)`；Windows 已由 Job 的 `KILL_ON_JOB_CLOSE` 覆盖）。
- [ ] 测试：假库启动一个子进程；空闲退出、停用、Godwit 正常退出后，子进程都不在了；进程按设定时间退出，下次调用重启并重新 `configure`。

### Task 6: 发布

- [ ] 锁定一个 llama.cpp 版本：各平台官方包解出 `llama-server` 及其依赖库，镜像到 dl.godwit.email、固定 sha256；用这个版本重跑 Task 2 的对照测试和 `eval/` 的 99 封，确认一致率与 #64 记录的相同。
- [ ] 两个模型变体的 GGUF 和各自的 `decision_config.json`，从 `startlux-models` 的 Hugging Face 仓库按 commit 锁定后镜像，固定 sha256。
- [ ] plugin-startlux 的 release CI 生成带 `assets` 与 `minGodwit` 的 `registry-release.json`。
- [ ] README：两个模型的磁盘与内存、各平台实测速度、首轮扫描耗时的估计（4B 每封约 2.7 s）、所有推理都在本机。

### Task 7: 上线

- [ ] 端到端（macOS Developer ID 签名包、Linux、Windows 各一次，用真实 registry 的测试副本）：从插件目录装插件 → 下载引擎与默认模型 → 判断来源选 StartLux → 新邮件被打标签 → 换成另一个模型、判断照常 → 空闲 120 秒后 plugin-host 与 llama-server 都退出 → 卸载后无进程、无资产。
- [ ] 向 `godwit-email/plugins` 提交 registry 条目。

## Dependencies

Task 1 与 Task 2–5 并行；Task 1 的结论决定 Task 6 提供哪些平台和变体。插件仓库先用 `ServeNative` 冒烟测试与手工路径开发；主仓库的 judge 能力用假库测试。Task 4 完成后，用 `coretest.RegistryServer` 起一个签名的测试 registry 联调（侧载插件不能声明资产）。Task 6 依赖 Task 4 的 registry 格式；Task 7 依赖全部。
