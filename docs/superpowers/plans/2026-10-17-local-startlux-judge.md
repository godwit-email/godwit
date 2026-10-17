# 本地决策模型 StartLux：原生插件 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用户从插件目录安装「StartLux」插件后，Godwit 下载 llama.cpp 的库和默认模型；插件设置里可以换成另一个模型，换了才下载。设置 › 模型的「判断」来源可以选 `StartLux（插件）`；#48 的标签分类器等所有 `ai.Judge` 调用方不改代码就走本地推理。默认不装、不下载。

**Why StartLux:** #64 的评测（[Laya](https://github.com/godwit-email/godwit/issues/64#issuecomment-6195378184)、[StartLux](https://github.com/godwit-email/godwit/issues/64#issuecomment-6225261345)）。99 封真实收件箱邮件、生产输入，与 gpt-6-luna 的一致率（是和一个参考模型的一致率，不是普遍意义上的准确率）：StartLux-Decision v1.1 4B Instant Q4_K_M 92.1%（阈值 0.5）、90.2%（0.8）；云端 jev 92.4%（0.8）；全部答「否」82.3%；Laya 零样本最好也只到 82.3%。

**Architecture:** 三个仓库。

- `godwit-email/plugin-startlux`（新）：Go c-shared 库，C ABI v1，`capabilities: ["judge"]`。**推理在插件内部闭环**：插件在 plugin-host 子进程里 `dlopen` llama.cpp 官方包里的 `libllama`（及其 `libggml*`），装入 GGUF，用 GGUF 自带词表分词，整段 prompt 一次 `llama_decode`，读出选项字母的 logits，换算成 `ai.Answer`。没有 HTTP、端口、API key，也没有孙进程。
- `godwit-email/godwit`：(a) `judge` 能力，`Settings.JudgeService` 可指向 `plugin/<name>`；(b) registry release 的大资产（`assets`）：库按平台、模型分变体，由插件的一个 `select` 配置项选择，保存时才下载；流式下载、断点续传、按变体存放，`configure` 时把绝对路径交给插件；(c) manifest 可声明空闲退出时间；(d) 原生调用的宿主侧排队与准入；(e) 换模型的切换闸门。
- `godwit-email/plugins`：registry 条目。库与模型镜像到 dl.godwit.email。

**Spec:** #64；`docs/superpowers/specs/2026-10-14-native-plugins-design.md` §10（为什么走插件）。本计划替代已关闭的 #83（Laya 版），并吸收了 2026-10-17 Oracle 对上一版（`llama-server` 方案）的审核意见，逐条对照代码核实过。

**已拆出去先做：** 全量重扫在 judge 中途出错时会丢标签（Oracle 第 1 条，云端 judge 同样受影响）→ #100：重扫不再预先删自动标签，拿到答案后由 `labelWrites` 替换；答不出的邮件保留原标签。

## Decisions

- **引擎：llama.cpp 官方构建，所有平台统一用 GGUF**，不用 MLX、不用 ORT。实测 4B 8 位：MLX 单封 p50 3.4 s、峰值 7.6 GB；llama.cpp + Metal 是 2.8 s、5.0 GB，准确率相同。
- **插件内调 `libllama`，不走 `llama-server`。** HTTP 只是 `llama-server` 暴露同一次前向的方式。进程内调用去掉了端口抢占、密钥传递、健康检查、pidfile 和孤儿进程；崩溃隔离不变（`libllama` 崩了只带走 plugin-host，宿主照常重启）。Godwit 主程序仍是 `CGO_ENABLED=0`；cgo 和链接只在插件仓库。官方包本来就带 `libllama` 和 `libggml*`，下载时不要 `llama-server` 等可执行文件。
- **锁定的推理参数**（评测时 `llama-server` 用的那组，换成库参数）：`n_ctx = n_batch = n_ubatch = 4096`、`n_seq_max = 1`、全部层 offload（`n_gpu_layers = 99`）、每题之前清空 KV（`llama_memory_clear`），不做 prompt 缓存或 context checkpoint。整段 prompt 必须落在一个 ubatch 里：GGUF 自带 `qwen35.attention.causal = false`，全注意力层只有一次算完才看得到整个 prompt。
- **分词用 GGUF 词表**（`llama_tokenize`，`add_special` / `parse_special` 与参考实现的 `/tokenize` 调用一致）。#64 已核实它和 HF tokenizer 在 693 个真实 prompt 上逐 id 相同；插件不带 tokenizer。
- **读出**：取最后一个位置的 logits，只看 k 个选项字母的 token id（来自 `decision_config.json`，缺任何一个就报错），`p = softmax(logit / T_题型)`。这和参考实现「+100 bias、`post_sampling_probs`、取 log 后除以 T 再 softmax」在数学上等价，但要用 Task 2 的对照测试实证，不能只靠推导。
- **锁定 llama.cpp 版本并镜像**：官方 Releases 只保留近期版本（b11549 已被撤掉）。选一个版本，把各平台包里的库文件镜像到 dl.godwit.email、固定 sha256（**在签名或改写依赖路径之后**再算）；插件仓库 vendor 同版本的 `llama.h`。升级前必须重跑 Task 2 的对照测试。
- **平台**：macOS arm64（Metal）为首发；macOS x64、Linux x64 / arm64、Windows x64 / arm64 在 Task 1 实测通过后再上架。Vulkan 包没有可用 GPU 时是否退回 CPU 由 Task 1 确认。CUDA 包超过 150 MB，不提供。
- **模型不随插件发布，是插件的一个配置项**。可选模型列在 registry 的 release 里，由 Godwit 下载：完整性由签名 registry 保证，进度、续传、隔离标记由 Godwit 统一处理，plugin-host 不碰网络和数据目录。新增模型 = 插件发一个新版本。
- **第一版模型变体**（峰值内存是 M5 Max 上的实测值，不是跨平台的保证）：
  - `4b-q4`（默认）：GGUF 2.71 GB，峰值 3.3 GB，一致率 92.1% / 90.2%（0.5 / 0.8），单封 p50 2.7 s，`minMemoryGB: 8`；
  - `2b-q8`：GGUF 2.01 GB，峰值 2.5 GB，一致率 88.9% / 85.4%，单封 p50 0.8 s，`minMemoryGB: 6`。
  - 每个变体的文件是 GGUF 加它自己的 `decision_config.json`（选项字母的 token id、按题型的温度；2B 与 4B 的不同）。
  - **不按内存自动选变体。** 选项声明 `minMemoryGB`，本机物理内存不够就不让装、不让选那一档；默认永远是 manifest 的 `default`（`4b-q4`）。本机一档都跑不了，整个插件不能装，提示缺多少内存。
- **选模型用现有的 `select` 配置项**，加一个 `"asset": "<资产名>"` 属性；选项的 `value` 就是变体 ID，可选 `minMemoryGB`。`checkRelease` 交叉校验选项与变体。
- 第一版只支持原生插件提供 `judge`（不加 `ctx.judges.register`）。
- 空闲退出：manifest 新增可选 `native.idle`（秒，30–3600），插件写 120。空闲退出时 plugin-host 连同装入的模型一起退，内存归零。
- **choice 超过 26 个选项**：第一版答 `refusal`（Godwit 的校验允许）。分组淘汰读出（`wide_choice`）以后再做。
- **阈值沿用全局 `JudgeThreshold`（#82，默认 0.8）**，不加按 judge 的默认阈值。4B Q4 在 0.8 时一致率 90.2%（0.5 时 92.1%），可以接受；默认变体仍是 `4b-q4`。README 写明 StartLux 在 0.5–0.7 略好，用户可自行调低。
- **T0 门槛的 30% 硬上限只对基线（jev）自己没超过 30% 的标签生效**；基线本身就超过的标签，只看相对差（假阳率最多比基线高 5 个百分点）。「通知」：StartLux 4B Q4 是 0.34，jev 是 0.82–0.84，照此放行。其余 6 个标签都在 0–0.15。
- **换模型接受短暂停顿**：不同时装两个模型；切换期间新调用排队，失败要能回滚到旧模型。

## Open Questions（需要拍板）

1. **只有 CPU 的机器**：Linux arm64 虚拟机（6 核、纯 CPU）上，约 3400 token 的长 prompt 一题 2B 要 17.9 s、0.8B 要 9.5 s，比 M5 Max 的 Metal 慢一个数量级；一封长邮件 7 个标签就是约 2 分钟。全量回扫 4479 封在纯 CPU 上要按天算。选项：(a) 纯 CPU 平台只提供 2B，并在设置里写明慢、建议只给新邮件打标签；(b) 第一版纯 CPU 平台不上架，只上 Metal / Vulkan。x64 实机和 Vulkan 量完再定，现在的数字来自虚拟机，偏悲观也偏少。

## Global Constraints

- 主仓库：`CGO_ENABLED=0` 下 `go build ./...`、`go test ./...` 通过；vet、gofmt 干净；不加依赖。ABI 版本不变（v1）：新方法和 `configure` 的新字段都是增量。
- judge 契约：请求 `{"input": "<text>", "questions": [ai.Question...]}`，响应 `{"answers": [ai.Answer...]}`。核心照常 `validateQuestions` / `validateAnswers`。
- **失败分类**：插件崩溃、看门狗超时、ABI 错误（`plugins.IsNativeFailure`）与答案不合契约，都算插件失败（计入三次停用），以 `ErrJudgePlugin` 报出，**不是** `ai.ErrJudgeAnswer`。调用方自己的取消或截止**不算**插件失败，也不计数。插件返回的错误信封原样展示、不计数。失败计数在 `validateAnswers` 之后、用一个独立于调用方的 context 记。
- **请求大小与分批**：**每次调用只发 1 题**。StartLux 每题本来就是一次单独的前向，合批没有计算上的好处；而纯 CPU 上一题可达约 18 s，16 题一批会超过 60 秒看门狗。发给插件前检查序列化后的请求 ≤ 1 MiB，超出直接拒绝、不计失败。
- **不能复用前缀缓存**：同一封邮件 7 个标签的 evidence 相同，但全注意力层是双向的，后面的 token 会改变前面 token 的表示，所以不能把 evidence 的 KV 缓存给下一题用；每题都要整段重算。
- **排队与时间预算（宿主侧）**：现在 `nativeInFlight = 4` 个调用同时在途、各自从发出起计 60 秒看门狗，调用方截止后调用被丢到后台继续跑。插件内部是串行的，4 个调用排队时后面的要等前面的全部跑完，后面的调用会白白超时、甚至触发看门狗杀掉别人的调用。所以：
  - judge 能力的调用在宿主侧先过一个**每插件一个的准入队列**（并发 1），看门狗从**真正发给插件**时开始计；
  - 预算分三段：排队（跟调用方的截止走，排队中取消直接出队、不发给插件）、冷启动（首次装模型，单独计，上限 60 秒）、推理（每次调用 1 题，落在 60 秒看门狗内）。`Ask` 的总超时不再按题数估：插件 judge 只受调用方 context 和每次调用的看门狗约束；
  - 已发出的调用在 ABI v1 下没法中止：调用方取消时宿主不再等它，但准入槽位要等插件真正返回（或看门狗杀进程）才释放，避免下一批叠上去。
- **prompt 长度**：每个 prompt 必须 ≤ 4080 token（4096 − 16 余量）。题目说明和选项描述本身没有长度上限，所以先用空 evidence 拼出固定部分：固定部分就超限 → 这批题作为**输入错误**拒绝（不计失败，也不当作拒答，因为标签分类器会把拒答算成「否」）；否则按 Unicode 边界截短 evidence，最多重试 3 次。
- **插件内部串行**：一个 `llama_context`，一把锁，一次一题。
- 资产：只给 listed 插件下载；大小与 sha256 来自签名 registry；流式写盘，不整个读进内存；中断可续传；校验不过就删，不允许「仍然安装」。固定资产（库）安装时下载；变体只下载配置里选中的那个。
- **资产纳入信任身份**：「已收录」比对的是版本、manifest、插件库**和本平台的全部资产描述**（`listsRelease`、`isReviewedPackage`、`applyRegistry`）。为此插件行除了「所选变体 → 目录」的绑定，还要**单独保存一份审核过的资产描述的规范化哈希**（含没选中的变体）；registry 撤掉或替换任何资产，等同吊销这个 release。
- **加载时校验**：每个新进程启动前都按固定 sha256 校验所选资产的每个文件，目录里不能有多余文件或符号链接。校验要放在**懒重启也会走的那条路径**上（空闲退出后再次调用走的是 `OnStart`，不是 `loadNative`），并且在 `pluginsMu` 之外做、可取消、有自己的时间预算，不挤占 `loadNative` 的 10 秒启动时限。只有设置页的「已下载」状态可以用 size + mtime 缓存。实测 4B Q4 约 1 s（见 Task 1）。资产文件带隔离标记也算校验不通过。
- 插件只从 `configure` 得到资产路径；plugin-host 仍不读数据目录。`configure` 只检查文件存在，不装模型（装模型放到第一次调用，避开 10 秒启动时限）。
- **Registry 兼容**：release 带 `minGodwit`，取实现本计划的 Godwit 版本。v1.3.0 起的客户端（#81）会把读不了的 release 放一边、显示「需要更新」。

## Review Focus

1. **选了 StartLux 但插件停用、卸载、资产缺失或被改**：判断返回明确错误（`ErrJudgeUnavailable` / `ErrJudgePlugin`），不静默退回生成模型；标签分类器本轮不写也不删标签。注意区分「停用 / 不受信任」（不可用）和「空闲退出、可以懒重启」（可用）。
2. **插件答错或崩溃**：用户已有的自动标签不被删；连续三次后插件停用；错误信封、调用方取消都不让插件停用。
3. **排队**：4 个接近看门狗上限的调用并发、排队中取消、冷启动、看门狗恢复，都不产生误判的失败计数，也不让一个调用的超时杀掉另一个。
4. **大文件下载**：断网、退出应用后都能续传；sha256 不符就删并报错；插件升级、资产不变时不重新下载；卸载后无引用的资产被删。
5. **换模型**：下载期间判断仍用旧模型；下载失败、取消或试跑失败，配置不变、旧模型还在并且能用；成功后才提交并删旧模型。
6. **读出与参考实现一致**：分层对照（见 Task 2），2B 和 4B 两种配置、三种题型都覆盖。
7. **内存**：空闲退出后归零；Godwit 退出时 plugin-host 在限定时间内也退出。

---

## File Structure

```
godwit（主仓库）
core/plugins/manifest.go        knownCapabilities 加 "judge"；NativeSpec.Idle；ConfigField.Asset、选项 minMemoryGB
core/plugins/registry.go        RegistryRelease.Assets、Asset、AssetVariant、AssetFile；checkRelease 交叉校验
core/plugins/native.go          judge 调用的准入队列；看门狗从发出时计；取消与失败分开
core/plugins/native_fake.go     judge：按题型返回固定答案；可配错误信封、坏答案、慢答、卡死
core/plugins/native_host.go     stdin 关闭后限时退出（不无限等卡住的调用）
core/registry.go                安装时下载固定资产与选中变体；listsRelease / isReviewedPackage / applyRegistry 纳入资产
core/assets.go（新）            可续传下载、<data>/plugin-assets/ 存放与租约、加载前校验、清理与待删记录
core/extensions.go              configure 带 assets；换变体的切换闸门；nativeIdle 取 manifest；卸载清资产
core/store/schema.go            plugins 表新列 assets（绑定）、assets_digest（审核过的描述）；待删资产表
core/judges.go（新）            nativeJudge、ListJudges、pluginJudge、ErrJudgePlugin、ErrJudgeUnavailable
core/models.go                  resolveJudge 的 plugin/ 分支
core/settings.go                JudgeService 允许 "plugin/<name>"
core/ai/judge.go                导出 NewBatchJudge(ask, batch, timeout)
core/labeler.go                 ErrJudgeUnavailable / ErrJudgePlugin 让这一轮停下（重扫丢标签已由 #100 修掉）
app/settings_models*.go         判断下拉列出插件
app/settings_extensions.go      安装确认显示总大小；资产 select 附大小、所需内存与已下载；保存时显示进度、可取消
sdk/native/godwit_plugin.h      judge、probe 方法；configure 的 assets
docs/registry/                  模板与审核清单加 assets

plugin-startlux（新仓库，骨架照 plugin-dlx）
main.go、bridge.c、protocol.go   ABI v1 envelope：describe / configure / judge / probe
llama.go、llama.c               dlopen libllama（路径来自 assets.engine）、vendored llama.h、上下文参数、tokenize、decode、取 logits
prompt.go                       StartLux 的 prompt 格式（照 startlux_decide/prompt.py）与 Qwen 对话模板（关思考）
readout.go                      字母 logits → 温度 softmax → noul / choice / score 换算成 ai.Answer
config.go                       解析 decision_config.json
testdata/                       参考实现生成的 prompt、token id、概率（公开或合成样例）；judge 契约样例
scripts/reference.py            用 startlux_decide 生成 testdata（STARTLUX_LLAMACPP_BIDIR=server）
scripts/package.py、smoke.py    同 DLX
eval/                           #64 的评测脚本（不含邮件）：生产输入、基线、打分
plugin.json                     {"name":"startlux","native":{"abi":1,"idle":120},"capabilities":["judge"],
                                 "config":[{"key":"model","type":"select","asset":"model","default":"4b-q4",
                                            "options":[{"value":"4b-q4","minMemoryGB":8,…},
                                                       {"value":"2b-q8","minMemoryGB":6,…}],"label":…}]}
```

---

### Task 0（已完成）: 评测

- [x] Laya 零样本不通过；StartLux 通过。结果在 #64。
- [x] Registry 向前兼容（#81，v1.3.0）。
- [x] 重扫不丢标签（#100）。

### Task 1: 分发与跨平台实测（不写产品代码，最先做）

分发路径走不通，整个方案要改，所以放在最前面。

- [x] **macOS 分发闸门**（2026-10-17，CI 公证的 Godwit 1.5.0 arm64 包，Developer ID + hardened runtime + `disable-library-validation`，llama.cpp b11707 官方包；测试插件在 plugin-host 里 `dlopen` libllama、装 4B Q4、跑一次前向）：
  - **能加载**：官方库是 ad-hoc 签名，不用重签、不用公证，被签名的 Godwit 照常加载；用内嵌的 Metal 库，4B Q4 装载 0.56 s。
  - **带隔离标记就加载不了**：`library load disallowed by system policy`。Godwit 不开沙盒、没设 `LSFileQuarantineEnabled`，用 `net/http` 自己下载的文件不带隔离标记；资产校验时**发现隔离标记就当作被改动**，提示重新下载，绝不自己去掉标记。
  - **不联网也能加载**：给进程禁掉网络后照常跑（整机断网没测；不带隔离标记的文件 Gatekeeper 本来就不检查）。
  - **要下载的文件**：7 个 dylib，约 11 MB：`libllama`、`libggml`、`libggml-base`、`libggml-cpu`、`libggml-blas`、`libggml-metal`、`libggml-rpc`。按 install name 存成 `lib*.0.dylib`、放在一个平铺目录里，靠 `@loader_path` 互相找到，不用符号链接、不用改依赖路径；sha256 就是官方文件本身的。
  - 新目录里的库第一次 `dlopen` 约 1.5 s（系统校验签名），之后约 15 ms。
  - **重新哈希**：Go sha256 约 2.7 GB/s，4B Q4 约 1 s、2B Q8 约 0.7 s（M5 Max）。每个进程代都完整校验可以接受，不需要折中方案；慢机器在跨平台实测里再量。
- [x] **Linux 分发闸门**（2026-10-17，Ubuntu 26.04.1 LTS arm64 虚拟机，6 核、3 GB 内存、无 GPU；`CGO_ENABLED=0` 交叉编译的 Godwit，b11707 `ubuntu-arm64` 官方包）：
  - **能加载**：官方 so 带 `$ORIGIN` 的 RUNPATH，平铺目录、按 soname 存成 `libllama.so.0` 等即可，不用符号链接；依赖系统的 `libstdc++` / `libgcc_s`（Ubuntu 自带）。
  - **CPU 后端是运行时加载的模块**：Linux 包把 CPU 后端拆成 8 个按指令集区分的 `libggml-cpu-armv*.so`，必须先调 `ggml_backend_load_all_from_path(<引擎目录>)`，否则没有后端可用。Windows 包也是这种结构；macOS 包把后端直接链接进来，调用这个函数也无害。Task 2 的 `llama.go` 统一先调它。
  - **要下载的文件**：12 个、约 14 MB（`libllama.so.0`、`libggml.so.0`、`libggml-base.so.0`、`libggml-rpc.so` 加 8 个 CPU 后端）。
  - **纯 CPU 速度与内存**（`n_ctx = n_batch = n_ubatch = 4096`）：0.8B Q8 短 prompt 0.04 s、3401 token 9.5 s，峰值 2.1 GB；2B Q8 3401 token 17.9 s，峰值 2.9 GB。3 GB 内存的虚拟机低于两档的 `minMemoryGB`，4B 没测。
- [ ] **Windows 分发闸门**：干净机器上开着 Defender 和 Smart App Control，plugin-host 加载下载来的 `llama.dll` / `ggml*.dll` 是否被拦。
- [ ] **跨平台实测**：Windows x64（有 GPU 和只有 CPU 各一台）、Linux x64（同上）、一台 8 GB 内存的 Mac，用锁定版本跑 `eval/` 的 99 封：每种机器记录 2B 和 4B 的一致率、单封 p50 / 最大、整个进程树的峰值内存（含 GPU 分配）、冷启动时间。
- [ ] **长时间运行**：在笔记本上实测首轮扫描 4479 封（现在的 3.4 小时只是按 p50 推算）：耗电、发热、界面是否卡、能否暂停与恢复；推理线程数和优先级怎么设才不抢前台。量一次空闲退出后重新哈希 3 GB 资产的耗时。
- [ ] 确认 Vulkan 包在没有可用 GPU 时自动退回 CPU；确认 macOS x64 用 Metal 还是 CPU。
- [ ] 按结果回答 Open Question 1，写进 #64。

### Task 2: plugin-startlux 骨架、推理与读出

- [ ] 建仓库（Apache-2.0），复制 DLX 的 `bridge.c`、`protocol.go`、`sdk/godwit_plugin.h`、CI 矩阵（先 darwin-arm64，其他平台随 Task 1 的结论加）。
- [ ] `llama.go` / `llama.c`：`dlopen` `assets.engine` 目录下的 `libllama`，按 vendored `llama.h` 取函数指针，再调 `ggml_backend_load_all_from_path(<引擎目录>)` 加载后端模块（版本与镜像的库一致，启动时核对 `llama_print_system_info` 或构建号，不一致就拒绝）。第一次 `judge` / `probe` 时装模型、建 context（参数见 Decisions），之后复用；空闲退出时随进程释放。
- [ ] `prompt.go`：照 `startlux_decide/prompt.py` 移植，包括 system 一行、`Evidence / Question / Options` 块、字母编号、noul 的 `yes` / `no`、选项只有 id 时只显示 id、纯字母或数字 id 在全部有描述时隐藏、score 由低到高（等级文字构造与参考实现逐字相同）、最多 26 个选项。对话模板写成常量：`<|im_start|>system\n…<|im_end|>\n<|im_start|>user\n…<|im_end|>\n<|im_start|>assistant\n<think>\n\n</think>\n\n`。
- [ ] `readout.go`：字母 token id 来自 `decision_config.json`，缺一个就报错；`p = softmax(logit / T)`，T 必须是正的有限数；predicate → noul 映射；choice 平局时保持参考实现的顺序；score 给出**从 0 起算**的期望等级与各级概率。
- [ ] 长度：先拼空 evidence 的固定部分，超过 4080 token → 输入错误信封；否则截短 evidence（见 Global Constraints）。
- [ ] `configure`：读 `assets.engine`（库目录）、`assets.model`（变体目录：`model.gguf` 与 `decision_config.json`），只检查文件存在。模型路径变了就在锁内释放旧模型，下次调用装新的。
- [ ] `probe {}`：在锁内装模型并跑一道固定题，宿主换模型时用它试跑。
- [ ] 验收（分层，各自设容差）：
  - **prompt 与 token**：Go 拼出的 prompt 与参考实现逐字节相同；`llama_tokenize` 的 id 与参考实现 `/tokenize` 逐个相同。
  - **读出数学**：给定 logits，Go 与 `readout.py` 的概率相差 ≤ 1e-9。
  - **同版本同后端**：同一台机器、同一锁定版本、同一后端，插件进程内推理与参考实现（`STARTLUX_LLAMACPP_BIDIR=server` 显式设置，默认的 `auto` 在原版服务上会走打补丁的请求）对 2B 和 4B 两种配置、三种题型的概率相差 ≤ 1e-5。#64 里「693 个概率逐位相同」是 `llama-server` 路径的结论，库路径要重新跑这 693 个 prompt 实证。
  - **各平台质量回归**：各平台跑 `eval/` 的 99 封，一致率与 macOS 基准的差距在 Task 1 量出的容差内；不要求跨平台 1e-6。
  - 一组超长样例：每个 prompt 都 ≤ 4080 token；固定部分超限的样例返回输入错误。
  - `smoke.py` 经 Godwit 真实的 `ServeNative` 跑通 `describe`、`configure`、`probe`、`judge`。

### Task 3（主仓库）: judge 能力与排队

**Interfaces — Produces:**
- `knownCapabilities = []string{"translate", "judge"}`；脚本插件声明 `judge` → manifest 错误。
- `ai.NewBatchJudge(ask func(ctx context.Context, input string, qs []Question) ([]Answer, error), batch int, timeout func(n int) time.Duration) Judge`：现有 `judge{}` 加批大小与按题数的超时，`NewJudge` 沿用 `maxQuestions` 和 15 秒。
- `Settings.JudgeService`：`""`、服务 slug，或 `"plugin/<name>"`。
- `var ErrJudgeUnavailable = errors.New("the judge plugin is not running")`、`var ErrJudgePlugin = errors.New("the judge plugin failed")`；rpc code `judge_unavailable`、`judge_plugin`。
- `nativeJudge.Ask`：内层 `NewBatchJudge(call, 1, …)`（不设按题数的总超时）；`ai.ErrJudgeAnswer` 或 `IsNativeFailure` → `pluginFailed` 并包成 `ErrJudgePlugin`；`*NativeError` 原样；调用方取消原样返回、不计数；成功 → `pluginSucceeded`。
- `NativeProcess` 的准入：可按方法设并发上限（judge 为 1）；排队中的调用随调用方取消出队；看门狗从发给插件时开始计；已发出调用的槽位等插件返回或进程被杀才释放。
- `resolveJudge`：`plugin/` 前缀 → `pluginJudge(name)`，label `plugin/<name>`，`gen == false`；停用、不受信任或未安装 → `ErrJudgeUnavailable`，不退回生成模型；空闲退出的进程算可用。
- 标签分类器：`ErrJudgeUnavailable`、`ErrJudgePlugin` 让这一轮停下、不写也不删。

- [ ] **Step 1: 失败的测试**（`core/judges_test.go`、`core/plugins/native_test.go`，原生夹具 `coretest.NativePluginFixture`）
  - `TestPluginJudge`：选 `plugin/startlux` → 标签分类与脚本插件的 `ctx.llm.decide` 得到假库答案；label 为 `plugin/startlux`。
  - `TestPluginJudgeBatches`：7 题拆成 7 次调用，结果顺序与题目一致；超过 1 MiB 的输入被拒、不计失败。
  - `TestPluginJudgeUnavailable`：停用后 `resolveJudge` 返回 `ErrJudgeUnavailable`；没有模型调用；分类器不写也不删标签。空闲退出后仍可用，下次调用懒重启。
  - `TestPluginJudgeBadAnswerKeepsLabels`：已有自动标签的消息；假库少答一题或给出概率 1.5 → 标签还在、消息仍待分类；连续三次后插件停用；`rate_limited` 信封十次后仍启用。
  - `TestNativeAdmission`：假库每题慢答；4 个调用并发、每个都接近看门狗上限 → 依次完成、没有看门狗误杀；排队中的调用被取消 → 从没发给插件、不计失败；冷启动慢 → 不算推理超时；假库卡死 → 看门狗杀进程、重启后下一次调用成功。
  - `TestJudgeServiceValidate`：`plugin/Bad Name` 拒绝，`plugin/startlux` 接受。
  - 契约：插件仓库 `testdata/judge-contract/` 的样例全部通过 `validateAnswers`。
- [ ] **Step 2: 实现**，`go test ./core/...` 通过。
- [ ] **Step 3: 设置页**：判断下拉在服务之后列出 `StartLux（插件）`，停用时标「已停用」。
- [ ] **Step 4: 头文件**写 `judge`、`probe` 方法与契约。

### Task 4（主仓库）: 大资产与换模型

registry 里 0.1.0 的资产大致是：

```json
"assets": [
  {"name": "engine", "platforms": ["darwin-arm64"], "variants": [
    {"id": "b11707-metal", "files": [{"name": "libllama.0.dylib", "url": "…", "sha256": "…", "size": 3529184}, {"name": "libggml.0.dylib", …}, …（共 7 个）]}]},
  {"name": "model", "variants": [
    {"id": "4b-q4", "files": [{"name": "model.gguf", "url": "…", "sha256": "…", "size": 2710000000}, {"name": "decision_config.json", …}]},
    {"id": "2b-q8", "files": [{"name": "model.gguf", "url": "…", "sha256": "…", "size": 2010000000}, {"name": "decision_config.json", …}]}]}
]
```

**Interfaces — Produces:**
- `RegistryRelease.Assets []Asset`；`Asset{Name string; Platforms []string（空=全部）; Variants []AssetVariant}`；`AssetVariant{ID string; Files []AssetFile}`；`AssetFile{Name, URL, SHA256 string; Size int64}`。`checkRelease` 校验：名字与变体 ID 符合 `skills.ValidName`、不重复；文件名单层、跨平台可用、同一变体内不重复；URL https、sha256 小写 64 位、size > 0；每个支持的平台上同名资产恰好一个。
- 只有一个变体的资产是固定资产；多于一个变体的资产必须被 manifest 里恰好一个带 `asset` 的 `select` 配置项引用，选项值与变体 ID 集合相同、`default` 在其中。
- **`minMemoryGB`**：select 选项可声明正整数；安装和保存配置时读本机物理内存，不够的变体拒绝（不自动改选别的）。设置页灰掉并写「需要 ≥ N GB 内存」。默认变体本机不够 → 安装确认里必须另选一档够格的，否则不能装。一档都不够 → 整个插件不能装。
- 侧载的插件声明了带 `asset` 的配置项 → 安装时报「只能从插件目录安装」。
- 存放：`<data>/plugin-assets/<变体摘要>/<文件名>`，摘要 = 该变体文件列表（名字 + sha256，排序后）的 sha256。macOS 上不打隔离标记（`net/http` 下载本来就不打）。插件行新列 `assets`（JSON：资产名 → 变体摘要）与 `assets_digest`（本平台全部资产描述的规范化哈希，吊销判断用）。
- 续传暂存：`<data>/plugin-assets/.partial/<文件 sha256>`，与安装用的 `.download-` 临时目录分开，启动时不删；同一 sha256 同时只有一个下载者。续传前重新哈希已有部分；`206` 必须带匹配的 `Content-Range`，收到 `200` 从头写，`416` 删掉重下；超过 size 立刻中止。大文件用无总时限、30 秒无进展即断的超时。暂存超过 7 天、或磁盘剩余不足总大小时清理并报错。
- 租约：运行中的进程、正在准备的候选变体、正在进行的下载，各自持有所用变体的租约；只删除无插件行引用且无租约的目录。Windows 上删除失败写进待删表，下次启动重试，不影响已提交的切换。
- `configure` 请求：`{"config": {...}, "assets": {"<资产名>": "<变体目录绝对路径>"}}`；没有资产的插件不带这个字段。
- **换模型的切换闸门**（`SetPluginConfig` 改了带 `asset` 的项）。现有的 `SetPluginConfig` 先提交再 `configure`，`configureNative` 在 RPC 成功之前就发布了重启用的配置，都不能原样用：
  1. 在 `pluginsMu` 之外下载并校验新变体（进度回调同安装，可取消），持租约。期间判断照常用旧模型。
  2. 关闭这个插件的准入（新调用排队），等已准入的调用跑完。
  3. 对运行中的进程 `configure` 候选、再 `probe`。
  4. `probe` 成功：按 `registryMu → pluginsMu` 的顺序加锁，确认 `installDir`、配置版本、启用与信任状态这期间没变，然后一起提交配置与资产绑定、发布给重启路径，打开准入。
  5. `probe` 失败或状态变了：`configure` 回旧模型**并再 `probe` 一次**，成功才打开准入；回滚也失败 → 插件报不可用（`ErrJudgeUnavailable`），等用户处理。
  6. 提交后释放旧变体的租约，尝试删除。
  插件停用时保存：只下载、校验和提交，不起进程。应用在第 1–4 步之间退出：配置仍是旧的，候选目录被清理或继续续传。
- 升级：沿用当前选择的变体；新版本不再列出它时退回 `default` 并在 `PluginInfo` 里提示。
- 进度：`PluginInstallProgress` 的 `download` 阶段按全部文件累计 `Done/Total`；安装确认显示库加默认变体的总大小（约 2.7–2.8 GB）。

- [ ] **Step 1: 失败的测试**（`core/registry_test.go` 的 https 测试服务器）
  - 固定资产、默认变体与库都校验通过才安装；任一 sha256 不符 → 全部丢弃、`ErrRegistryMismatch`；未选中的变体从不请求。
  - 下载到一半断开、以及中途关掉 Service 再开 → 用 `Range` 续传，最终 sha256 正确；服务器回 `200`、`416`、不匹配的 `Content-Range` → 各自正确处理；磁盘空间不足 → 报错并清理。
  - 两个并发安装或换模型请求同一个文件 → 只下载一次。
  - 换变体：成功 → 新目录、旧目录被删、`configure` 收到新路径；下载失败、取消、`probe` 失败 → 配置仍是旧变体、旧目录还在、回滚后的 `probe` 通过、之后的 `judge` 得到答案；回滚也失败 → `ErrJudgeUnavailable`；切换期间排队的 `judge` 调用都在切换后得到答案，没有一个调用横跨两个模型。
  - 删除被占用的目录失败 → 记入待删表，重启后删掉。
  - 升级到资产不变的新版本 → 不发资产请求；升级时所选变体保持不变；新版本不再列出当前变体 → 退回 `default` 并提示。
  - 吊销：registry 改了**没被选中的**变体的 sha256 → 已安装的插件也被吊销；撤掉整个资产同样。
  - 卸载 → 无引用、无租约的变体目录被删。
  - 加载时校验：改动资产文件的内容但保留 size 与 mtime → 下次进程启动前被发现（包括空闲退出后的懒重启），插件不启动并提示重新安装；目录里多出一个文件或符号链接 → 同样；校验期间不持 `pluginsMu`。
  - `minMemoryGB`：模拟 8 GB 机器可装默认 `4b-q4`；模拟 7 GB 不能装 `4b-q4`，可改选 `2b-q8` 后装上；模拟 5 GB 一档都不给装；保存配置选不够格的变体被拒、配置不变。
  - 内存：下载 200 MiB 测试资产时堆的峰值增长 < 16 MiB。
- [ ] **Step 2: 实现** `core/assets.go` 与 `core/registry.go`、`core/extensions.go` 的改动；`fetchRegistryFile` 保持给小文件用。
- [ ] **Step 3: 设置页**：带 `asset` 的 `select` 在选项旁显示大小、所需内存与「已下载」；本机不够的选项灰掉、写「需要 ≥ N GB 内存」；保存时显示进度、可取消；切换时短暂显示「正在切换模型」。
- [ ] **Step 4:** `docs/registry/` 模板与审核清单加 `assets`；`cmd/godwit-registry check --fetch` 下载并校验资产。

### Task 5（主仓库）: 空闲时间与退出

推理在 plugin-host 里，没有孙进程要清理；剩下的风险是 Godwit 已经退出、plugin-host 还卡在一次推理里，一直占着几个 GB。

- [ ] `NativeSpec.Idle int`（秒）：0 = 缺省 `nativeIdle`；否则 30–3600，越界报 manifest 错误。`loadNative` 用它设 `NativeOptions.Idle`。
- [ ] plugin-host 在 stdin 关闭时**先启动一个限时**（10 秒），再等进行中的调用；到时还没完就直接 `os.Exit`。现在 `ServeNative` 在 EOF 后无限期 `wg.Wait()`。
- [ ] 测试：假库卡在一次调用里，关闭 stdin（模拟 Godwit 退出、不重启）→ plugin-host 在限时内退出；空闲按设定时间退出，下次调用重启并重新 `configure`。

### Task 6: 发布

- [ ] 锁定一个 llama.cpp 版本：各平台官方包解出 `libllama` 与依赖库（按 Task 1 的结论改写路径、重签），镜像到 dl.godwit.email、固定 sha256；用这个版本生成 Task 2 的 golden fixture，重跑 `eval/` 的 99 封，确认一致率与 #64 记录的相同。
- [ ] 两个模型变体的 GGUF 和各自的 `decision_config.json`，从 `startlux-models` 的 Hugging Face 仓库按 commit 锁定后镜像，固定 sha256。
- [ ] plugin-startlux 的 release CI 生成带 `assets` 与 `minGodwit` 的 `registry-release.json`。
- [ ] README：两个模型的磁盘与内存、各平台实测速度、首轮扫描耗时（Task 1 实测值）、所有推理都在本机；数字写成「与参考模型的一致率」。

### Task 7: 上线

- [ ] 端到端（macOS Developer ID 签名公证包、Linux、Windows 各一次，用真实 registry 的测试副本）：从插件目录装插件 → 下载库与默认模型 → 判断来源选 StartLux → 新邮件被打标签 → 换成另一个模型、判断照常 → 空闲 120 秒后 plugin-host 退出、内存归零 → 卸载后无进程、无资产。
- [ ] 向 `godwit-email/plugins` 提交 registry 条目。

## 第一版不做

无缝切换模型（接受短暂停顿）、按内存自动选变体、没实测过的平台、跨平台 1e-6 一致性、宽选项（> 26）、MLX、CUDA。

## Dependencies

Task 1 最先做，它的分发结论决定方案是否成立，实测结论决定 Task 6 提供哪些平台和变体。Task 2–5 可在 Task 1 的 macOS 闸门通过后并行。插件仓库先用 `ServeNative` 冒烟测试与手工路径开发；主仓库的 judge 能力与排队用假库测试。Task 4 完成后，用 `coretest.RegistryServer` 起一个签名的测试 registry 联调（侧载插件不能声明资产）。Task 6 依赖 Task 4 的 registry 格式；Task 7 依赖全部。
