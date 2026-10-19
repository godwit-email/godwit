# 本地决策模型 StartLux：原生插件 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用户从插件目录安装「StartLux」插件后，判断模型选择器里出现 `StartLux Decision 4B Q4` 和 `StartLux Decision 2B Q8`；本机内存够的那档可以「下载并使用」，Godwit 下载 llama.cpp 的库和该模型、校验、装载后设为判断模型。#48 的标签分类器等所有 `ai.Judge` 调用方不改代码就走本地推理。默认不装、不下载。

**Why StartLux:** #64 的评测（[Laya](https://github.com/godwit-email/godwit/issues/64#issuecomment-6195378184)、[StartLux](https://github.com/godwit-email/godwit/issues/64#issuecomment-6225261345)）。99 封真实收件箱邮件、生产输入，与 gpt-6-luna 的一致率（是和一个参考模型的一致率，不是普遍意义上的准确率）：StartLux-Decision v1.1 4B Instant Q4_K_M 92.1%（阈值 0.5）、90.2%（0.8）；云端 jev 92.4%（0.8）；全部答「否」82.3%；Laya 零样本最好也只到 82.3%。

**基础：#109（已合入 main，替换了 #108）。** 插件模型走现有 SDK；`ctx.models.register` / 原生 `models.*`、`plugin/<插件名>/<模型 id>` 引用（`JudgeService` 直接接受）、plugin judge 的准入与错误映射、`core/modelseam.go` 的 `modelAssets` 接缝都已在 main。#109 走精简方案，**没有**做模型状态、`PrepareModel` / `RemoveModelFiles` 和下载 UI，这些归本计划（Task 3 Step 3）。

**Architecture:** 三个仓库。

- `godwit-email/plugin-startlux`（新）：Go c-shared 库，C ABI v1，`capabilities: ["models"]`，实现 `models.list` / `models.load` / `models.judge`。**推理在插件内部闭环**：插件在 plugin-host 子进程里 `dlopen` llama.cpp 官方包里的 `libllama`（及其 `libggml*`），装入 GGUF，用 GGUF 自带词表分词，整段 prompt 一次 `llama_decode`，读出选项字母的 logits，换算成 `ai.Answer`。没有 HTTP、端口、API key，也没有孙进程。
- `godwit-email/godwit`：(a) registry release 的大资产（`assets`）：schema、按平台的内存要求、可信 release 解析、流式下载与续传、校验、共享资产与租约——即模型 API 设计 §8.2 `modelAssets` 的真实实现，替换基础 PR 的 `update_required` 默认实现；(b) 下载与准备的 UI（进度、取消、删除）；(c) manifest 可声明空闲退出时间、plugin-host 在 stdin 关闭后限时退出。
- `godwit-email/plugins`：registry 条目。库与模型镜像到 dl.godwit.email。

**#109 已做、本计划不再做的：** 模型目录（`ListPluginModels`）、引用解析、plugin judge 的每次一题与每插件准入、看门狗从派发时计、取消后槽位保持、`ai.ErrJudgeUnavailable` / `ai.ErrJudgePlugin` 映射、`models.load` 前按进程代 `Verify`。

**Spec:** #64；模型 API 设计；`docs/superpowers/specs/2026-10-14-native-plugins-design.md` §10（为什么走插件）。本计划替代已关闭的 #83（Laya 版），吸收了 2026-10-17 Oracle 对 `llama-server` 方案的审核意见。

**已拆出去先做：** 全量重扫在 judge 中途出错时会丢标签 → #100（已合入）。

## Decisions

- **引擎：llama.cpp 官方构建，所有平台统一用 GGUF**，不用 MLX、不用 ORT。实测 4B 8 位：MLX 单封 p50 3.4 s、峰值 7.6 GB；llama.cpp + Metal 是 2.8 s、5.0 GB，准确率相同。
- **插件内调 `libllama`，不走 `llama-server`。** 进程内调用去掉了端口抢占、密钥传递、健康检查、pidfile 和孤儿进程；崩溃隔离不变（`libllama` 崩了只带走 plugin-host，宿主照常重启）。Godwit 主程序仍是 `CGO_ENABLED=0`；cgo 和链接只在插件仓库。下载时不要 `llama-server` 等可执行文件。
- **锁定的推理参数**：`n_ctx = n_batch = n_ubatch`，取 1024 / 2048 / 4096 里第一个装得下 prompt 的（见 Task 1「计算缓冲」）、`n_seq_max = 1`、全部层 offload（`n_gpu_layers = 99`）、每题之前清空 KV（`llama_memory_clear`），不做 prompt 缓存或 context checkpoint。整段 prompt 必须落在一个 ubatch 里：GGUF 自带 `qwen35.attention.causal = false`，全注意力层只有一次算完才看得到整个 prompt。
- **分词用 GGUF 词表**（`llama_tokenize`，`add_special` / `parse_special` 与参考实现的 `/tokenize` 调用一致）。#64 已核实它和 HF tokenizer 在 693 个真实 prompt 上逐 id 相同；插件不带 tokenizer。
- **读出**：取最后一个位置的 logits，只看 k 个选项字母的 token id（来自 `decision_config.json`，缺任何一个就报错），`p = softmax(logit / T_题型)`。这和参考实现「+100 bias、`post_sampling_probs`、取 log 后除以 T 再 softmax」在数学上等价，但要用 Task 2 的对照测试实证。
- **锁定 llama.cpp 版本并镜像**：官方 Releases 只保留近期版本（b11549 已被撤掉）。选一个版本，把各平台包里的库文件镜像到 dl.godwit.email、固定 sha256；插件仓库 vendor 同版本的 `llama.h`。升级前必须重跑 Task 2 的对照测试。
- **平台**：macOS arm64（Metal）为首发；macOS x64、Linux x64 / arm64、Windows x64 / arm64 在 Task 1 实测通过后再上架。CUDA 包超过 150 MB，不提供。
- **每个模型变体是模型目录里的一个模型**，不是插件配置项：`models.list` 返回 `decision-4b-q4`、`decision-2b-q8`，各自用 `assets: {"model": "<变体 ID>"}` 引用 registry 里 `model` 资产组的变体；固定的 `engine` 资产组由宿主按 release 补齐。插件只声明 id / name / 执行方式 / 资产引用，大小、平台、内存要求由宿主从 registry 读。新增模型 = 插件发一个新版本。
- **第一版模型变体**（峰值内存是 M5 Max 上的实测值，不是跨平台的保证）：
  - `4b-q4`：GGUF 2.71 GB，峰值 3.3 GB，一致率 92.1% / 90.2%（0.5 / 0.8），单封 p50 2.7 s，`minMemoryGB: {"default": 8}`；
  - `2b-q8`：GGUF 2.01 GB，峰值 2.5 GB，一致率 88.9% / 85.4%，单封 p50 0.8 s，`minMemoryGB: {"default": 6, "windows-*": 8}`（Windows 值待 Task 1 实机确认）。
  - 每个变体的文件是 GGUF 加它自己的 `decision_config.json`（选项字母的 token id、按题型的温度；2B 与 4B 的不同）。
  - **不按内存自动选变体**：内存不够的那档在目录里显示 `insufficient_memory`、不给下载；插件本身照常可装（模型 API 设计 §7）。
- **判断模型由 `Settings.JudgeModel` 选择**（基础 PR 的迁移）；本地 judge 只能这样被选中，调用方不能逐请求指定另一个本地模型，所以不会有后台请求驱动的来回装载。插件同时只驻留一个模型。
- 空闲退出：manifest 新增可选 `native.idle`（秒，30–3600），插件写 120。空闲退出时 plugin-host 连同装入的模型一起退，内存归零；目录里该模型从 `ready` 回到 `downloaded`。
- **choice 超过 26 个选项**：第一版答 `refusal`（Godwit 的校验允许）。分组淘汰读出（`wide_choice`）以后再做。
- **阈值沿用全局 `JudgeThreshold`（#82，默认 0.8）**，不加按 judge 的默认阈值。README 写明 StartLux 在 0.5–0.7 略好，用户可自行调低。
- **T0 门槛的 30% 硬上限只对基线（jev）自己没超过 30% 的标签生效**；基线本身就超过的标签，只看相对差（假阳率最多比基线高 5 个百分点）。「通知」：StartLux 4B Q4 是 0.34，jev 是 0.82–0.84，照此放行。

## Open Questions（需要拍板）

1. **只有 CPU 的机器**：Linux arm64 虚拟机（6 核、纯 CPU）上，约 3400 token 的长 prompt 一题 2B 要 17.9 s、0.8B 要 9.5 s，比 M5 Max 的 Metal 慢一个数量级；一封长邮件 7 个标签就是约 2 分钟。全量回扫 4479 封在纯 CPU 上要按天算。选项：(a) 纯 CPU 平台只提供 2B，并在设置里写明慢、建议只给新邮件打标签；(b) 第一版纯 CPU 平台不上架，只上 Metal / Vulkan。CI runner 的 x64 / arm64 数字（Task 1）证实了这个量级：2K token 的邮件一题 2B 7–36 s、4B 24–47 s。Vulkan 没有设备可测。
2. **Windows 的内存门槛**：4K 档的提交量 2B 8–10 GB、4B 10–11 GB（Task 1）。选项：(a) `windows-*` 两档都提到 12；(b) Windows 上 context 封顶 2K（更长的 evidence 截短），门槛维持 8，代价是长邮件只读前半段。

## Global Constraints

- 主仓库：`CGO_ENABLED=0` 下 `go build ./...`、`go test ./...` 通过；vet、gofmt 干净；不加依赖。ABI 版本不变（v1）。
- **以模型 API 设计为准**：原生方法与 JSON（§7）、`modelAssets` 签名与语义（§8.2）、错误码与映射（§9）、准入与切换（§5、§8）。本计划与它冲突时改本计划。
- **原生 JSON**：`configure` 永远只是 `{"config": {...}}`；引擎和模型目录只通过 `models.load` 的 `{"modelId": "...", "assets": {"engine": "<绝对路径>", "model": "<绝对路径>"}}` 交给插件；目录里的文件名就是 registry 的 `AssetFile.name`。
- **每次调用只发 1 题**（基础 PR 的 Ask 适配保证）。StartLux 每题本来就是一次单独的前向；纯 CPU 上一题可达约 18 s，落在 60 秒看门狗内。
- **不能复用前缀缓存**：同一封邮件 7 个标签的 evidence 相同，但全注意力层是双向的，每题都要整段重算。
- **prompt 长度**：每个 prompt 必须 ≤ 4080 token（4096 − 16 余量）。先用空 evidence 拼出固定部分：固定部分就超限 → `invalid_request` 错误信封（输入错误，不计插件失败，也不当作拒答，因为标签分类器会把拒答算成「否」）；否则按 Unicode 边界截短 evidence，最多重试 3 次。
- **插件内部串行**：一个 `llama_context`，一把锁，一次一题；`models.judge` 遇到未装载的 `modelId` 返回 `model_not_ready`。
- **资产**：只给 listed 插件下载；大小、sha256、平台、`minMemoryGB` 来自签名 registry；流式写盘，不整个读进内存；中断可续传；校验不过就删，不允许「仍然使用」。只有用户显式「下载并使用」才下载（`Prepare`）；后台路径只 `Verify`，缺文件就 `model_not_ready`。
- **资产纳入信任身份**：「已收录」比对的是版本、manifest、插件库**和本平台的全部资产描述**（`listsRelease`、`isReviewedPackage`、`applyRegistry`）；`modelAssets` 的所有操作绑定已安装 release 的内容摘要。registry 撤掉或替换任何资产（包括没下载的变体），等同吊销这个 release。
- **加载前校验**（`Verify`）：每个新进程代（含空闲退出后的懒重启）和每次切换模型，都按 sha256 校验固定引擎和所选模型的每个文件；目录里不能有多余文件或符号链接；文件带 `com.apple.quarantine` 算 `verification_failed`，宿主不自己去掉标记。校验在 `pluginsMu` 之外、持租约、可取消；不以 size + mtime 代替哈希，只有 `Status` 可以用磁盘快照。实测 4B Q4 约 1 s。
- **Registry 兼容**：release 带 `minGodwit`，取同时包含基础 PR 和本计划的 Godwit 版本。

## Review Focus

1. **资产与信任**：sha256 不符、多余文件、符号链接、隔离标记、registry 改了未下载变体的描述，都不能让模型被加载；吊销后目录里该插件的模型不可用。
2. **大文件下载**：断网、退出应用后都能续传；插件升级、资产不变时不重新下载；下载途中插件升级不混用 release；多个等待者共享一个下载，一个取消不影响其他；卸载或 `RemoveModelFiles` 后无租约的文件被删，共享的引擎保留。
3. **内存门槛**：按平台匹配（精确平台 → 系统通配 → default）；不够的那档不给下载、插件本身照常可装。
4. **读出与参考实现一致**：分层对照（见 Task 2），2B 和 4B 两种配置、三种题型、三档 context 都覆盖。
5. **内存与退出**：空闲退出后归零；Godwit 退出时 plugin-host 在限定时间内也退出。

---

## File Structure

```
godwit（主仓库，只列本计划的改动）
core/plugins/manifest.go        NativeSpec.Idle
core/plugins/registry.go        RegistryRelease.Assets、Asset、AssetVariant（含 minMemoryGB 平台映射）、AssetFile；checkRelease 校验
core/plugins/native_host.go     stdin 关闭后限时退出（不无限等卡住的调用）
core/registry.go                listsRelease / isReviewedPackage / applyRegistry 纳入资产；release 内容摘要
core/assets.go（新）            modelAssets 的真实实现：Status / Prepare / Verify / Remove、续传下载、<data>/plugin-assets/ 存放与租约、待删记录；替换 newModelAssets
core/extensions.go              nativeIdle 取 manifest；卸载时释放该插件的资产引用
core/store/schema.go            资产绑定与审核过的资产描述摘要；待删资产表
app/ 模型设置相关               判断模型列表里本地模型的大小、所需内存、下载进度、取消、删除文件
docs/registry/                  模板与审核清单加 assets
cmd/godwit-registry             check --fetch 下载并校验资产

plugin-startlux（新仓库，骨架照 plugin-dlx）
main.go、bridge.c、protocol.go   ABI v1 envelope：describe（含 methods）/ configure / models.list / models.load / models.judge
llama.go、llama.c               dlopen libllama（路径来自 models.load 的 assets.engine）、vendored llama.h、按 prompt 长度分档建 context、tokenize、decode、取 logits
prompt.go                       StartLux 的 prompt 格式（照 startlux_decide/prompt.py）与 Qwen 对话模板（关思考）
readout.go                      字母 logits → 温度 softmax → noul / choice / score 换算成 ai.Answer
config.go                       解析 decision_config.json
testdata/                       参考实现生成的 prompt、token id、概率（公开或合成样例）；judge 契约样例
scripts/reference.py            用 startlux_decide 生成 testdata（STARTLUX_LLAMACPP_BIDIR=server）
scripts/package.py、smoke.py    同 DLX
eval/                           #64 的评测脚本（不含邮件）：生产输入、基线、打分
plugin.json                     {"name":"startlux","version":"0.1.0","native":{"abi":1,"idle":120},"capabilities":["models"],"config":[]}
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
- [x] **Windows 分发闸门（部分）**（2026-10-17，Windows 11 专业版 arm64 虚拟机，6 核、4 GB 内存；`CGO_ENABLED=0` 交叉编译的未签名 Godwit，b11707 `win-cpu-arm64` 官方包）：
  - **能加载**：Defender 实时保护开着，plugin-host 用 `LoadLibraryEx(LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR)` 加载下载目录里的 `llama.dll` 正常，没被拦。文件不带「来自网络」标记（和 `net/http` 下载一致）。
  - **Smart App Control 没测**：这台机器上它是关闭的，而它只能在全新安装的系统上打开。仍需一台开着 SAC 的干净 Windows，并且要用正式签名的 Godwit。
  - **取后端加载函数要从 `ggml.dll` 取**：Windows 的 `GetProcAddress` 不会顺着依赖查找，`ggml_backend_load_all_from_path` 要用 `GetModuleHandle("ggml.dll")` 取，从 `llama.dll` 取不到。
  - **要下载的文件**：6 个，约 5.4 MB（`llama.dll`、`ggml.dll`、`ggml-base.dll`、`ggml-cpu.dll`、`ggml-rpc.dll`、`libomp.dll`）。
  - **4 GB 的虚拟机跑不动 n_ctx 4096**，见下面的「计算缓冲」。速度数字因为内存不足一直在换页，不可信，没记录。
- [x] **计算缓冲**（三个平台都量过）：llama.cpp 按 `n_ubatch` 预留计算缓冲，大约**每 1K token 1 GB，和模型大小基本无关**。CPU 上 0.8B 是 1K 0.99 GB、2K 1.99 GB、4K 4.2 GB；Metal 上 4B 在 4K 时是 4.07 GB。
  - macOS 和 Linux 上这块缓冲大多只是预留，没有真正占用：4B Q4 在 4K 时用 `footprint` 量到的是 770 MB 脏页加 2.6 GB 映射的模型，合计约 3.4 GB，和 #64 记录的 3.3 GB 一致。
  - **Windows 不允许超额提交**，整块缓冲都算已提交内存：4 GB 的虚拟机上，0.8B 开 4K、2B 开 2K 都申请失败（2B 是 `GGML_ASSERT` 直接让进程崩溃）。
  - **对策：按 prompt 长度选 context 大小**。`n_ctx = n_ubatch` 取 1024 / 2048 / 4096 里第一个装得下这个 prompt 的；需要更大时重建 context，空闲退出时释放。大多数标签题落在 2K 以内，Windows 上提交量就是约 2 GB 而不是 4 GB。整个 prompt 仍在一个 ubatch 里，不影响双向注意力；Task 2 的对照测试要覆盖三档 context，确认概率不受 n_ctx 影响。
  - **`minMemoryGB` 按平台算**：Windows 上至少要能放下模型加 4K 计算缓冲（2B 约 6.3 GB、4B Q4 约 7 GB）。registry 用模型 API 设计 §7 的平台映射表达（如 `{"default": 6, "windows-*": 8}`）；现在的 8 / 6 在 macOS 和 Linux 上够用，Windows 的值用 8 GB 以上的 Windows 实机确认。
- [x] **CI runner 实测**（2026-10-19，GitHub 托管 runner，都是 3–4 核虚拟机、无可用 GPU；b11707，plugin-startlux 一次性分支 `measure`，跑完已删）：43 个合成用例，每题单独计时，峰值内存在 macOS / Linux 取 `maxrss`，Windows 取峰值提交量。数字在 plugin-startlux 的 `docs/parity.md`。
  - **能跑**：6 个平台（macOS arm64 / x64、Linux x64 / arm64、Windows x64 / arm64）都能装载、三档 context 都能答。macOS x64 只有 CPU 后端（没有 Metal）。
  - **和 Metal 参考不再逐位一致**：CPU 后端的概率差最大 1.2e-2（2B）到 4.9e-2（4B），超过 1e-5；但 0.8 阈值上没有一个判定翻转，0.5 上只有 Linux arm64 的 2B 有 4 个选项跨线。跨平台的验收改成「阈值上的判定一致」，1e-5 只对同一后端成立。
  - **纯 CPU 很慢**：一题在 1K 档 1–17 s、2K 档 7–47 s、4K 档 15–102 s（4B 是 2B 的 2–3 倍）。一封 2K token 左右的邮件 7 个标签，2B 约 1–4 分钟、4B 约 3–5.5 分钟。真机核数多一些会快，但量级不变。
  - **Windows 提交量大**：4K 档 2B 峰值提交 8.2 GB（x64）/ 10.2 GB（arm64），4B 是 10.0 / 11.0 GB；macOS / Linux 的常驻内存 2B ≤ 4.6 GB、4B ≤ 6.2 GB。现在的 `windows-*: 8` 不够。
  - **7 GB 内存的 macOS arm64 runner** 跑 4B 峰值 4.4 GB、没有换页；但它的 Metal 是虚拟化的，速度不代表 Apple 芯片真机。
- [ ] **真机实测**（推迟：手上没有设备，发布后由测试用户补）：带 GPU 的 Windows / Linux（Vulkan）、8 GB 内存的 Apple 芯片 Mac、开着 Smart App Control 的 Windows。用 `eval/` 的 99 封记录一致率、单封 p50 / 最大、整个进程树的峰值内存（含 GPU 分配）、冷启动时间。
- [ ] **长时间运行**（推迟，同上）：在笔记本上实测首轮扫描 4479 封（现在的 3.4 小时只是按 p50 推算）：耗电、发热、界面是否卡、能否暂停与恢复；推理线程数和优先级怎么设才不抢前台。量一次空闲退出后重新哈希 3 GB 资产的耗时。
- [ ] 确认 Vulkan 包在没有可用 GPU 时自动退回 CPU（推迟，同上）。
- [ ] 按结果回答 Open Question 1，写进 #64。

### Task 2: plugin-startlux 骨架、推理与读出

**进度（2026-10-17）**：本地仓库 `~/Documents/workspace/godwit-email/plugin-startlux`（未推到 GitHub）已完成骨架、libllama 绑定（`engine.c`，dlopen + 按需取函数指针）、prompt 与读出移植、`models.list` / `models.load` / `models.judge`。对照结果（llama.cpp b11707、M5 Max、Metal，见该仓库 `docs/parity.md`）：2B Q8 和 4B Q4 在 43 个合成用例与 693 个真实 prompt 上，prompt 逐字节相同、token 逐个相同，概率最大差 1.6e-6；三档 context 之间无差别。`scripts/smoke.py` 经 Godwit 真实的 plugin-host 跑通全部方法、8 个并发调用和 sha256 拒绝。还没做：CI 矩阵与各平台构建、`scripts/package.py`、Linux / Windows 上跑对照测试；llama.h 没有构建号接口，版本靠 registry 固定的 sha256 保证。

- [ ] 建仓库（Apache-2.0），复制 DLX 的 `bridge.c`、`protocol.go`、`sdk/godwit_plugin.h`、CI 矩阵（先 darwin-arm64，其他平台随 Task 1 的结论加）。
- [ ] `describe`：`capabilities: ["models"]`、`methods: ["models.list", "models.load", "models.judge"]`。`configure`：只接收 `{"config": {}}`，什么都不装。
- [ ] `models.list`：返回 `decision-4b-q4`、`decision-2b-q8` 两个 `{"transport": "plugin", "operation": "judge", "assets": {"model": "<变体 ID>"}}`；不联网、不读文件，没下载也照常列出。
- [ ] `models.load`：收到 `modelId` 和 `assets.engine` / `assets.model` 两个已验证目录。首次时 `dlopen` 引擎目录下的 `libllama`，按 vendored `llama.h` 取函数指针，再调 `ggml_backend_load_all_from_path(<引擎目录>)` 加载后端模块（Windows 上从 `ggml.dll` 取这个函数）；核对构建号与 vendored 头文件一致，不一致就报错。装入 `model.gguf`、读 `decision_config.json`，跑一道固定题验证，成功才返回 `{}`；已驻留别的模型时先释放它。
- [ ] `llama.go`：context 按 prompt 长度在 1024 / 2048 / 4096 三档里选，需要更大时重建，其余参数见 Decisions。
- [ ] `models.judge`：`modelId` 不是当前驻留的模型 → `model_not_ready`；否则在锁内逐题推理。
- [ ] `prompt.go`：照 `startlux_decide/prompt.py` 移植，包括 system 一行、`Evidence / Question / Options` 块、字母编号、noul 的 `yes` / `no`、选项只有 id 时只显示 id、纯字母或数字 id 在全部有描述时隐藏、score 由低到高（等级文字构造与参考实现逐字相同）、最多 26 个选项。对话模板写成常量：`<|im_start|>system\n…<|im_end|>\n<|im_start|>user\n…<|im_end|>\n<|im_start|>assistant\n<think>\n\n</think>\n\n`。
- [ ] `readout.go`：字母 token id 来自 `decision_config.json`，缺一个就报错；`p = softmax(logit / T)`，T 必须是正的有限数；predicate → noul 映射；choice 平局时保持参考实现的顺序；score 给出**从 0 起算**的期望等级与各级概率。
- [ ] 长度：先拼空 evidence 的固定部分，超过 4080 token → `invalid_request`；否则截短 evidence（见 Global Constraints）。
- [ ] 验收（分层，各自设容差）：
  - **prompt 与 token**：Go 拼出的 prompt 与参考实现逐字节相同；`llama_tokenize` 的 id 与参考实现 `/tokenize` 逐个相同。
  - **读出数学**：给定 logits，Go 与 `readout.py` 的概率相差 ≤ 1e-9。
  - **同版本同后端**：同一台机器、同一锁定版本、同一后端，插件进程内推理与参考实现（显式设 `STARTLUX_LLAMACPP_BIDIR=server`）对 2B 和 4B 两种配置、三种题型、三档 context 的概率相差 ≤ 1e-5。#64 里「693 个概率逐位相同」是 `llama-server` 路径的结论，库路径要重新跑这 693 个 prompt 实证。
  - **各平台质量回归**：各平台跑 `eval/` 的 99 封，一致率与 macOS 基准的差距在 Task 1 量出的容差内；不要求跨平台 1e-6。
  - 一组超长样例：每个 prompt 都 ≤ 4080 token；固定部分超限的样例返回 `invalid_request`。
  - 插件仓库 `testdata/judge-contract/` 的答案样例全部通过 Godwit 的 `validateAnswers`。
  - `smoke.py` 经 Godwit 真实的 `ServeNative` 跑通 `describe`、`configure`、`models.list`、`models.load`、`models.judge`。

### Task 3（主仓库）: 资产与 `modelAssets` 实现

基础 PR 定义了 `modelAssets` 接口和 `newModelAssets(s *Service) modelAssets` 构造入口，默认实现对所有带 `assets` 的模型返回 `update_required`。本任务提供真实实现并替换构造入口；基础 PR 的目录、judge 调度、切换事务和 UI 调用签名都不改。

registry 里 0.1.0 的资产大致是：

```json
"assets": [
  {"name": "engine", "platforms": ["darwin-arm64"], "variants": [
    {"id": "b11707-metal", "files": [{"name": "libllama.0.dylib", "url": "…", "sha256": "…", "size": 3529184}, {"name": "libggml.0.dylib", …}, …（共 7 个）]}]},
  {"name": "model", "variants": [
    {"id": "4b-q4", "minMemoryGB": {"default": 8}, "files": [{"name": "model.gguf", "url": "…", "sha256": "…", "size": 2710000000}, {"name": "decision_config.json", …}]},
    {"id": "2b-q8", "minMemoryGB": {"default": 6, "windows-*": 8}, "files": [{"name": "model.gguf", "url": "…", "sha256": "…", "size": 2010000000}, {"name": "decision_config.json", …}]}]}
]
```

**Interfaces — Produces:**
- `RegistryRelease.Assets []Asset`；`Asset{Name string; Platforms []string（空=全部）; Variants []AssetVariant}`；`AssetVariant{ID string; MinMemoryGB（数值，或带 default 的平台映射）; Files []AssetFile}`；`AssetFile{Name, URL, SHA256 string; Size int64}`。`checkRelease` 校验：名字与变体 ID 符合 `skills.ValidName`、不重复；文件名单层、跨平台可用、同一变体内不重复；URL https、sha256 小写 64 位、size > 0；每个支持的平台上同名资产恰好一个；`minMemoryGB` 为有限正数，映射必须有 `default`，键只允许 `default`、受支持的精确平台、`<os>-*`。不再要求多变体资产被 `config.select` 引用。
- 插件运行后 `models.list` 的资产引用与已安装 release 交叉校验：引用的资产组和变体必须存在。
- 侧载的插件声明 `assets` → 安装时报「只能从插件目录安装」。
- `minMemoryGB` 匹配顺序：精确平台 → `<os>-*` → `default`；读本机物理内存比较。不够 → `Status` 为 `insufficient_memory`，`Prepare` 拒绝；当前平台没有所需资产 → `unsupported_platform`。
- 存放：`<data>/plugin-assets/<变体摘要>/<文件名>`，摘要 = 该变体文件列表（名字 + sha256，排序后）的 sha256。macOS 上不打隔离标记（`net/http` 下载本来就不打）。持久化资产绑定，以及本平台全部资产描述的规范化摘要（吊销判断用）。
- 续传暂存：`<data>/plugin-assets/.partial/<文件 sha256>`，与安装用的 `.download-` 临时目录分开，启动时不删；同一 sha256 同时只有一个下载者，多个等待者共享，最后一个等待者离开才取消。续传前重新哈希已有部分；`206` 必须带匹配的 `Content-Range`，收到 `200` 从头写，`416` 删掉重下；超过 size 立刻中止。大文件用无总时限、30 秒无进展即断的超时。暂存超过 7 天、或磁盘剩余不足总大小时清理并报错。
- `Status`：只读可信元数据和磁盘快照，不下载、不起插件、不全量哈希；字节数按文件去重、包含固定资产。
- `Prepare`：仅用户显式操作；下载缺失文件（或显式修复 `verification_failed` 的文件）、校验全部所需文件，返回 `dirs`（固定资产与所选变体目录的并集）和可重复调用的 `releaseLease`；`progress` 串行报告 `downloading` / `verifying`，开始全量哈希前报告 `verifying`。失败时自行释放已取得的资源。
- `Verify`：不下载、不修复；缺文件 `model_not_ready`，坏哈希、多余文件、符号链接、隔离标记 `verification_failed`。
- `Remove`：只删目标变体中无租约、不被其他已准备模型引用的文件，保留共享的引擎；仍有租约 → `model_unavailable`，不部分删除。Windows 上删除失败写进待删表，下次启动重试。
- 所有操作绑定传入的 release 摘要；升级、吊销后过期的结果不发布。

- [x] **Step 1: 失败的测试**（`core/assets_test.go`、`core/assets_darwin_test.go`、`core/plugins/registry_test.go`；`coretest.RegistryServer` 现在支持 Range 和故障注入）。没写：下载 200 MiB 时堆增长的测试（下载是流式写盘，没整个读进内存）。
  - `checkRelease`：`minMemoryGB` 的数值与平台映射、缺 `default`、非法键、非正数；多变体资产不再要求 `select`。
  - `Status`：未下载 → `model_not_ready`；内存不足（模拟 7 GB 对 `default: 8`，以及 `windows-*` 覆盖）→ `insufficient_memory`；平台无资产 → `unsupported_platform`；已下载 → `downloaded` 且不做全量哈希。
  - `Prepare`：固定资产与所选变体都校验通过才成功；任一 sha256 不符 → 全部丢弃；未选中的变体从不请求；进度先 `downloading` 后 `verifying`。
  - 续传：下载到一半断开、以及中途关掉 Service 再开 → 用 `Range` 续传，最终 sha256 正确；服务器回 `200`、`416`、不匹配的 `Content-Range` → 各自正确处理；磁盘空间不足 → 报错并清理。
  - 共享下载：两个等待者请求同一个文件 → 只下载一次；一个取消，另一个照常完成；都取消才停止底层下载。
  - `Verify`：改动文件内容但保留 size 与 mtime → `verification_failed`；目录里多一个文件或符号链接 → 同样；文件带 `com.apple.quarantine`（macOS）→ 同样且标记仍在；缺文件 → `model_not_ready`；校验期间不持 `pluginsMu`、可取消。
  - 租约：`Prepare` / `Verify` 成功后租约存活到 `releaseLease`；失败不残留租约；有租约时 `Remove` 返回 `model_unavailable` 且不删任何文件。
  - 吊销与升级：registry 改了**没下载的**变体的 sha256 → 该 release 被吊销；升级到资产不变的新版本 → 不发资产请求；下载途中升级 → 旧 release 的结果不发布。
  - 删除：被占用的目录删除失败 → 记入待删表，重启后删掉；删掉一个模型后共享引擎还在、另一个模型仍是 `downloaded`。
  - 内存：下载 200 MiB 测试资产时堆的峰值增长 < 16 MiB。
- [x] **Step 2: 实现** `core/plugins/assets.go`（格式与校验）、`core/assets.go`（`modelAssets` 实现与卸载清理）、`core/registry.go`（资产纳入信任身份）。§8.2 的类型暂时声明在 `core/modelseam.go`，基础 PR 合入时保留一份。简化的地方都在代码里用 `shortcut:` 注释标明：同一插件的两个模型共用一个变体时，删一个会连带删掉；Windows 上删不掉的目录不进待删表，等下次删除或启动清理时重试；下载前不预先检查磁盘剩余空间，写满时报错并删掉半截文件。
- [x] **接线**：`pluginjudge.go` 调 `Verify` 时传插件行的 `assets_digest`；端到端测试 `TestPluginJudgeWithHostedAssets`（签名 registry 安装带资产的 release → 未下载时 `model_not_ready` → `Prepare` 后 `models.load` 收到校验过的目录）。
- [x] **Step 3: 下载与设置页**：`ListPluginModels` 给带资产的模型附 `Files`（`ModelStatus`）；`PrepareModel` / `PrepareModelWithProgress`（后者只在 app 内用，不开放 RPC）、`RemoveModelFiles`，完成后发 `models.changed`；RPC 错误带 `model_error` 类型。设置 › 模型服务新增「本地模型」分区：大小、状态，下载（后台进行、带进度、可取消，离开页面不中断）、删除文件、重新下载（校验失败时），内存不够、平台不支持、需要更新时只写原因。截图 `SettingsModelsLocal`。没做：下载完自动设为判断模型（用户仍在「判断」下拉里选）；一个模型被插件装载时删除会提示「正在使用」，要先停用插件或换模型。
- [x] **Step 4:** `docs/registry/README.md` 加 `assets` 格式与审核要点；`cmd/godwit-registry check --fetch` 下载并校验资产。

### Task 4（主仓库）: 空闲时间与退出

推理在 plugin-host 里，没有孙进程要清理；剩下的风险是 Godwit 已经退出、plugin-host 还卡在一次推理里，一直占着几个 GB。

- [x] `NativeSpec.Idle int`（秒）：0 = 缺省 `nativeIdle`；否则 30–3600，越界报 manifest 错误。`loadNative` 用它设 `NativeOptions.Idle`。
- [x] plugin-host 在 stdin 关闭后最多再等进行中的调用 10 秒（`nativeDrain`），到时还没完就返回，`main` 随即 `os.Exit`。原来 `ServeNative` 在 EOF 后无限期 `wg.Wait()`。
- [x] 测试：`TestServeNativeExitsWhenACallHangs`（假库卡在一次调用里、stdin 关闭 → 限时内返回）；`TestParseManifestNative*`（`idle` 解析，29、3601、-1 被拒）。空闲退出后的懒重启沿用现有 `TestNativeReconfiguredAfterRestart`；重启后 `Verify` + `models.load` 随 Task 3 和基础 PR 的接入再测。

### Task 5: 发布

- [ ] 锁定一个 llama.cpp 版本：各平台官方包解出 `libllama` 与依赖库，镜像到 dl.godwit.email、固定 sha256；用这个版本生成 Task 2 的 golden fixture，重跑 `eval/` 的 99 封，确认一致率与 #64 记录的相同。
- [ ] 两个模型变体的 GGUF 和各自的 `decision_config.json`，从 `startlux-models` 的 Hugging Face 仓库按 commit 锁定后镜像，固定 sha256；`minMemoryGB` 按 Task 1 的实测填。
- [ ] plugin-startlux 的 release CI 生成带 `assets` 与 `minGodwit` 的 `registry-release.json`。
- [ ] README：两个模型的磁盘与内存、各平台实测速度、首轮扫描耗时（Task 1 实测值）、所有推理都在本机；数字写成「与参考模型的一致率」。

### Task 6: 上线

- [ ] 端到端（macOS Developer ID 签名公证包、Linux、Windows 各一次，用真实 registry 的测试副本）：从插件目录装插件 → 判断模型列表出现两个 StartLux 模型 → 对 4B「下载并使用」→ 新邮件被打标签 → 换成 2B「下载并使用」、判断照常 → 空闲 120 秒后 plugin-host 退出、内存归零、下一封邮件懒重启后照常 → 删除 4B 的文件、引擎保留 → 卸载后无进程、无资产。
- [ ] 向 `godwit-email/plugins` 提交 registry 条目。

## 第一版不做

无缝切换模型（接受短暂停顿，事务由基础 PR 实现）、按内存自动选变体、没实测过的平台、跨平台 1e-6 一致性、宽选项（> 26）、MLX、CUDA、原生流式生成。

## Dependencies

基础模型 API PR 合入是 Task 2 联调、Task 3、Task 6 的前提；在它合入前，插件仓库可以先用 `ServeNative` 冒烟测试与手工路径开发，Task 3 的下载器与 registry 部分可以按模型 API 设计 §8.2 的签名先写、用假的调用方测试。Task 1 最先做，实测结论决定 Task 5 提供哪些平台、变体和 `minMemoryGB`。Task 4 与基础 PR 无关，可随时做。Task 3 完成后，用 `coretest.RegistryServer` 起一个签名的测试 registry 联调（侧载插件不能声明资产）。Task 5 依赖 Task 3 的 registry 格式；Task 6 依赖全部。
