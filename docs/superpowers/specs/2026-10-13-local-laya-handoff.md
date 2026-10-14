# 本地决策模型（Laya）：移交报告

日期：2026-10-13。读者：接手后续工作的 agent。本文只写已经实测过的事实、已经做出的决定和剩下的工作。推断会标 `[推断]`，没验证过的会标 `[未验证]`。

## 1. 目标与边界

- 把 Laya 作为**可选的本地决策模型**，补充现有的云端 judge（`core/ai/judge.go`：OpenAI Decisions、TypeSafe System One）。
- 用户在设置里主动选择后，下载我们预设好的模型。默认不下载，也不启用。
- 接入方式：新增一个 `ai.Judge` 实现，上层调用方不用改。
- 本地模型的输出只作为参考信号，不能直接触发动作（原因见 §3.3）。

## 2. 已做的决定

| 决定 | 原因 |
|---|---|
| 只预设一个模型：社区 8-bit 的权重，**改成外部数据格式**后再发布 | 和 fp32 的答案 282/284 相同，连跑 3 次结果不变；改格式后空闲常驻约 200 MB |
| 不提供 fp32、官方动态 int8、4-bit | fp32 体积和内存是 8-bit 的两倍，质量相同；官方动态 int8 同一输入跑多次会改答案（23/284）；4-bit 的 noul 假阳率 0.94 |
| ORT 锁定 **1.27.1** | 换成 1.31.0 后，同一输入 logits 最大差 0.22 |
| ORT 运行时**随应用打包签名**，不让用户下载 | hardened runtime 下，Team ID 不同或带隔离标记的 dylib 都会被拒绝加载（§4.2） |
| 推理放在**子进程** `godwit laya-worker` 里，空闲就退出 | ORT 跑过长输入后内存不会还给系统，释放 Session 和 Env 也不行（§4.3） |
| 子进程内：关闭 arena 和内存复用，每次只送一道题 | 多道题合并送进去不会变快，内存却会大涨（§4.4） |
| 建议输入上限 512 token | 约 225 ms，子进程常驻约 330 MB；1024 token 时约 565 ms、约 600 MB |

## 3. 模型事实

### 3.1 文件

来源：`yehor-oleksiuk/laya-multilingual-onnx`，commit `4603ff7b55790fa1226809207465f7b26bbaae66`，许可证 apache-2.0（README 写明 base model `convaiinnovations/laya-multilingual` 也是 Apache 2.0；**原权重的许可证没有独立核实**）。

| 文件 | 字节 | sha256 |
|---|---:|---|
| `model_int8.onnx` | 540,524,684 | `22fdf8b58fb9242d03902f8af4bf339d456732bb1eb0af49c79180f594903631` |
| `model_fp32.onnx` | 1,290,008,958 | `cb782bd4d6f8a02b467a906e859c9d6cde562878956a7e00043312999d75d444` |
| `tokenizer.json` | 34,363,188 | `609d8f4c067cd3950f88594c5a802616cea245823836ef5848ee4fc40aab5b6f` |

int8 图的实际内容：95 个 `MatMulNBits`，`bits=8`、`block_size=32`；另有 50 个 MatMul 留在 fp32（决策头）。

**README 有几处和文件不符，以文件为准**：README 写的输入名是 `type_ids/lengths/n_opts`，实际不是；附带的 `reproduce_int8.py` 写 block 64，实际是 32。

### 3.2 图的接口

| 名称 | 类型 | 形状 |
|---|---|---|
| `input_ids` | int64 | [B, L] |
| `attention_mask` | int64 | [B, L] |
| `marker_pos` | int64 | [B, K] |
| `marker_mask` | bool | [B, K] |
| `qtype` | int64 | [B] |
| 输出 `logits` | float32 | [B, K] |
| 输出 `act_logits` | float32 | [B, 2] |

上下文最长 1024 token。

**下面几点还不清楚，接手后要先查原仓库 `https://github.com/NandhaKishorM/laya`（`build_sequence` / `collate`）确认：**

- `qtype` 的数值和题型怎么对应。目前代码里写的 `QChoice=0, QScore=1, QNoul=2` 是**假设**，没有核实。
- 序列怎么拼：state、题干、选项的顺序，用哪些特殊 token，marker token 是什么，`marker_pos` 指向哪里。
- noul 的 K 是多少（1 还是 2），score 的 K 是不是等于级数。
- `act_logits` 是什么意思：[推断] 可能是“拒答/弃权”一类的动作头。
- 怎么从 logits 算出概率：choice 用 softmax，score 用期望值，noul 用 sigmoid 还是二分类 softmax？
- 温度都是 `[1,1,1]`，没有拟合过。

### 3.3 质量（来自前一份报告：zh-CN，219 条，284 题，短正文）

| | fp32 | 社区 8-bit |
|---|---:|---:|
| choice 准确率 | 0.858 | 0.858 |
| noul 准确率 | 0.582 | 0.582 |
| noul 假阳率 | 0.71 | 0.71 |
| choice+noul ECE | 0.114 | 0.137 |
| 平均置信度 / 实际命中率 | 0.896 / 0.799 | 0.896 / 0.799 |

- noul 明显偏向判“是”，置信度普遍偏高。
- 社区 README 引用的外部评测：Laya 零样本接近随机水平，jev-decision-index 上 32 个模型里得 13.4 分；建议先在自己的数据上做 Platt scaling 校准。
- 评测题全是 4–53 字的短中文，**和邮件场景不匹配**。

## 4. 实测结果（Apple M5 Max，64 GB，macOS；ORT 1.27.1 官方 osx-arm64；Go 1.27.1，`CGO_ENABLED=0`）

### 4.1 Go 调用 ORT 的正确性

- 用 purego 调用 ORT C API，和 Python ORT 1.27.0 比，输出**逐位相同**（PyPI 上没有 1.27.1）。测试形状：1×128×6，3×512×4。
- 外部数据格式和单文件格式的输出逐位相同。
- 同一进程里重复跑，输出逐位不变。
- 关闭 prepacking 后输出最大差 3.17，关闭 KleidiAI 后最大差 3.7e-4。所以**这两个选项都不要动**。

### 4.2 macOS 能否加载 ORT 库（用 ad-hoc 签名测试）

| 主程序 | dylib | 结果 |
|---|---|---|
| 未开 hardened runtime | 官方原样 | 能加载 |
| hardened runtime | 官方原样，或重新 ad-hoc 签名 | 拒绝：`different Team IDs` |
| hardened runtime + `cs.disable-library-validation` | 官方原样 | 能加载 |
| 任意 | 带 `com.apple.quarantine` | 拒绝：`library load disallowed by system policy` |
| 任意 | 去掉签名 | 拒绝：`missing code signature` |

- hardened runtime 下必须用绝对路径：`relative path not allowed in hardened program`。
- ORT 官方的 dylib 是 adhoc + linker-signed，没有 Team ID，大小 38,502,216 字节。
- **尚未验证**：用 Developer ID 签名、开启 hardened runtime 的正式包，能否加载随包一起签过名的 dylib。本机没有 Developer ID 证书。[推断] 能加载，依据是 Team ID 相同，而且 mygo 的 terminal 插件用同样方式加载 libghostty-vt。
- 不推荐用 `disable-library-validation`：它会关掉整个应用的库校验。

### 4.3 内存

| 题数 × token | 单文件：空闲常驻 | 外部数据：峰值 | 外部数据：空闲常驻 |
|---|---:|---:|---:|
| 1 × 128 | 1502 MB | 247 MiB | 205 MB |
| 1 × 512 | 约 1.5 GB | 334 MB | 318 MB |
| 1 × 1024 | 1578 MB | 712 MB | 678 MB |
| 4 × 1024 | — | 2.57 GB | 2.46 GB |
| 8 × 1024 | 6.2 GB | 5.3 GB | 5.06 GB |

- 单文件格式时，ORT 会把权重整份复制进堆内存；外部数据格式改为内存映射。转换方法：`onnx.save_model(m, path, save_as_external_data=True, all_tensors_to_one_file=True, location="<name>.onnx.data", size_threshold=1024)`。转换后 `.onnx` 约 2.2 MB，`.onnx.data` 约 538 MB。`.data` 的文件名写在图里，两个文件必须放在同一目录，并保持原名。
- 1 × 1024 跑完后依次释放，phys_footprint 变化如下：

| 步骤 | 默认设置 | 关闭 arena |
|---|---:|---:|
| 跑完空闲 | 679 MB | 541 MB |
| 释放 Session | 680 MB | 386 MB |
| 释放 Env | 680 MB | 386 MB |
| `malloc_zone_pressure_relief` 后 | 680 MB（回收 0 MiB） | 386 MB |

  不释放的部分是 597 MB 的 `Malloc Large`。打开 arena 收缩（`memory.enable_memory_arena_shrinkage=cpu:0`）反而更高：1024 token 时 873 MB。
- 8 × 1024 时关闭 arena，峰值从 5.3 GB 降到 3.16 GB。

### 4.4 延迟（外部数据格式，4 线程，跑一次热身后取 p50）

| 形状 | 耗时 |
|---|---:|
| 1 × 128 | 46 ms（8 线程 35 ms） |
| 1 × 512 | 218–228 ms（8 线程 172 ms） |
| 1 × 1024 | 557–594 ms（8 线程 494 ms） |
| 4 × 1024 一次送入 | 2.4–2.7 s |
| 8 × 1024 一次送入 | 4.1–5.3 s，比 8 次单题依次跑（约 4.5 s）还慢 |

模型加载 0.12–0.13 s。

### 4.5 子进程端到端（真实 `godwit laya-worker` + `laya.Client`，外部数据格式）

| 场景 | 耗时 |
|---|---:|
| 冷启动 + 1 题 × 128 token | 177–187 ms（新编译二进制第一次启动 810 ms，[推断] 是 macOS 的首次检查） |
| 子进程已在跑，128 / 512 token | 46 / 224–228 ms |
| 6 题 × 512 依次跑 | 1.34–1.36 s |
| 空闲退出后再冷启动 + 512 | 359 ms |

- 子进程运行时 phys_footprint 314–337 MB；空闲退出后为 0。
- 以上都是文件已在系统缓存里的数字，磁盘冷读没有测。

### 4.6 平台

- ORT 1.27.1 发布的平台：osx-arm64、win-x64、win-arm64、linux-x64、linux-aarch64，**没有 osx-x86_64**。
- Intel Mac 和 universal 构建要另作决定（§6 T6）。
- Windows 和 Linux 都没有实测。

## 5. 已有代码（未提交，在当前 worktree）

| 文件 | 内容 |
|---|---|
| `core/ai/laya/ort.go` | purego 调用 ORT C API：校验版本是否为 `ORTVersion`，开 Session（关 arena 和内存复用、最高级图优化），`run` 跑一道题（B=1，mask 全为 1）。`slot*` 常量是 1.27.1 头文件里 `struct OrtApi` 成员的序号，**升级 ORT 时必须重新生成**（方法见 §7）。`cptr` 用来把 C 地址转成 Go 指针，以通过 `go vet` |
| `core/ai/laya/ort_unix.go` / `ort_windows.go` | 加载库，处理路径编码：Unix 用 UTF-8；Windows 用 UTF-16，配合 `LoadLibraryEx(LOAD_WITH_ALTERED_SEARCH_PATH)`，**只编译通过，未运行** |
| `core/ai/laya/worker.go` | 子进程一侧 `Serve(r, w, Config)` 和题目检查 `Input.Validate`（token 数 1..1024、marker 在范围内、qtype 0..2）。通信协议是每行一个 JSON：先回一行 `{"ready":true}` 或 `{"error":"…"}`；之后每收到一道题 `{"input_ids":[…],"marker_pos":[…],"qtype":n}`，回一行 `{"logits":[…],"act_logits":[a,b]}` 或 `{"error":"…"}`；stdin 关闭后退出 |
| `core/ai/laya/client.go` | 主进程一侧 `Client{Command, Idle}`：首次 `Run` 时才启动子进程；题目一道一道排队；空闲超过 `Idle` 后关闭 stdin，1 s 内不退出就 kill；子进程死了下次自动重启；`ctx` 取消时 kill 子进程；子进程的错误包装成 `ErrWorker`，并带上 stderr 最后 4 KB；启动超时 30 s |
| `core/ai/laya/client_test.go` | 测试程序自身充当假子进程（`LAYA_TEST_WORKER`），覆盖：空闲退出、崩溃重启、取消、错误回答保留进程、启动失败、题目检查边界。`TestLiveParity` 需要设置 `GODWIT_LAYA_ORT`、`GODWIT_LAYA_MODEL`（都用绝对路径）才会跑 |
| `core/ai/laya/testdata/reference.json` | Python ORT 1.27.0 对社区 int8 生成的 3 道随机 token 题（64×6 qtype0、40×2 qtype2、300×5 qtype1）及对应 logits；容差 1e-4 |
| `cmd/godwit/laya.go`、`run.go` | 隐藏子命令 `godwit laya-worker --lib ABS --model ABS --threads N`（不在 usage 里） |
| `go.mod` | purego 从间接依赖改为直接依赖 |

已通过：`go vet ./...`；`go test ./cmd/godwit ./core/ai/...`；`go test -race ./core/ai/laya`；`TestLiveParity`（单文件和外部数据两种格式）；`GOOS=windows` / `GOOS=linux` 下的 build 和 vet。

### 已知限制

- `Client.Close` 之后再调用 `Run` 会重新启动子进程；没有“永久关闭”状态。
- `Idle == 0` 表示每次 `Run` 完就关闭子进程。
- `Run` 一次只送一道题，没有批量接口，这是有意的（§4.4）。
- 主进程要传给子进程的路径（`os.Executable()`、ORT 库、模型）都还没接线。

## 6. 剩余工作

按依赖排序。每一项都写了验收标准。

### T1 弄清序列格式和题型语义（阻塞 T2、T3）

- 读原仓库的 `build_sequence`、`collate`、输出解码和温度，补全 §3.2 列出的未知项。
- 修正 `QChoice/QScore/QNoul` 的取值，补齐解码规则。
- 验收：用 Python 原实现对一组真实的 state/questions 产出 token、marker 和概率，写进 testdata 作为 T2、T3 的参考。

### T2 Go 分词（纯 Go，`CGO_ENABLED=0`）

- 实现能加载 `tokenizer.json` 的分词器（mmBERT；`tokenizer_config`：bos/cls=`<bos>`，eos/sep=`<eos>`，pad=`<pad>`，mask=`<mask>`）。可以先评估现成的纯 Go 库，也可以自己实现 tokenizer.json 用到的那部分 pipeline。不能用依赖 cgo 的库。
- 验收：对一份多语言语料逐 token 和 Python `tokenizers` 结果一致。语料至少要有中文、英文、日文、emoji、URL、长邮件、全角字符和空白边界；一致率要求 100%。

### T3 拼序列和 `ai.Judge` 实现

- `ai.Question` 转成 Laya 输入：choice 对应 `Choices`（图允许 2–255 个，要确认模型训练时的上限）；score 对应 `Levels`（2–10 级）；`YesNo` 对应 noul。输出转成 `ai.Answer`，经过 `validateAnswers`。
- 截断：超过上限（建议 512）时怎么截掉 state。先去掉引用历史和签名，再按 token 截断。
- 每道题单独调用一次 `Client.Run`；云端的 `maxQuestions=128` 不适用于本地，要单独设上限。
- 验收：对 T1 的参考数据，概率在 1e-4 以内；`ai.Judge` 的契约测试（拒答、题数、错误）通过。

### T4 打包 ORT

- 按 mygo 的原生库机制（`mygo-plugin.json`：各平台的 URL 和 sha256；构建时下载、放进 resources、一起签名，参见 mygo 的 `docs/distribution.md` 和 terminal 插件）把 ORT 1.27.1 放进应用。运行时用绝对路径定位 dylib/dll。
- Windows：确认 ORT 1.27.1 发行包里需要哪些 DLL [未验证]。
- 验收：**在有 Developer ID 的机器上**，`mygo build` 出的正式签名包（hardened runtime，不加 `disable-library-validation`）里，`laya-worker` 能加载 ORT 并跑通 `TestLiveParity` 里的题。不通过就回到 §4.2 重新决策。

### T5 模型发布和下载

- 用官方权重复现社区的量化配方（MatMulNBits 8-bit、block 32、对称；词表 fp16；决策头 MatMul 留 fp32），再转成外部数据格式，产物放到我们自己的地址，并固定 sha256。不直接依赖个人发布的仓库。
- 验收：复现出的图和社区 int8 对比，choice、score、noul 的答案在前一份报告的题库上与 fp32 一致率 ≥ 282/284。
- 下载器：用 Go `net/http` 下载（不会打上隔离标记），支持断点续传、sha256 校验、原子替换；存到 data dir 下。界面显示总大小：`.onnx` + `.onnx.data` + `tokenizer.json`，约 575 MB。如果用户自己放进带隔离标记的文件，要能识别并提示。
- 验收：下载中途断网后能续传；文件被篡改时拒绝加载。

### T6 平台决策

- Intel Mac 没有 ORT 官方包：自己编译 ORT，或者 Intel Mac 上不提供这个功能。universal 构建里 dylib 只能放 arm64，需要处理。
- 验收：做出决定，界面在不支持的平台上隐藏或禁用这个选项。

### T7 设置和接入

- 在设置的模型页增加“本地模型”：下载、删除、显示大小和状态；启用后作为 judge 的一种选择。
- 主进程创建 `laya.Client`：`Command = [os.Executable(), "laya-worker", "--lib", <abs>, "--model", <abs>]`，`Idle` 约 60 s，应用退出时调用 `Close`。
- 验收：在应用里启用后，对一封真实邮件发起判断能得到结果；空闲后 `laya-worker` 进程消失。

### T8 邮件场景评测和校准（决定这个功能是否有价值）

- 用真实邮件建评测集：中英混合、长正文、引用链，带标注。在上面量准确率、ECE，以及截断策略的效果。
- 在这份数据上拟合温度或 Platt scaling，定下阈值。
- 验收：报告列出各题型的准确率和校准前后的 ECE；明确哪些判断可以交给本地模型做。

## 7. 复现方法

T0 的临时目录 `/tmp/laya-spike` 已被系统清理，需要时按下面重建：

```sh
B=https://huggingface.co/yehor-oleksiuk/laya-multilingual-onnx/resolve/4603ff7b55790fa1226809207465f7b26bbaae66
curl -LO $B/model_int8.onnx && curl -LO $B/tokenizer.json
curl -LO https://github.com/microsoft/onnxruntime/releases/download/v1.27.1/onnxruntime-osx-arm64-1.27.1.tgz && tar xzf onnxruntime-osx-arm64-1.27.1.tgz
uv run --python 3.12 --with onnx python -c "import onnx; onnx.save_model(onnx.load('model_int8.onnx'), 'ext/model_int8.onnx', save_as_external_data=True, all_tensors_to_one_file=True, location='model_int8.onnx.data', size_threshold=1024)"
GODWIT_LAYA_ORT=$PWD/onnxruntime-osx-arm64-1.27.1/lib/libonnxruntime.1.27.1.dylib GODWIT_LAYA_MODEL=$PWD/ext/model_int8.onnx \
  go test ./core/ai/laya -run TestLiveParity -v
```

- Python 对照用 `onnxruntime==1.27.0`（PyPI 上没有 1.27.1），能和 Go 输出逐位相同；1.31.0 不行。
- 内存用 `footprint -p <pid>` 看 `phys_footprint`；峰值用 `/usr/bin/time -l` 的 `peak memory footprint`。
- 签名矩阵：`codesign -f -s - -o runtime [--entitlements dlv.plist] <bin>`；隔离标记：`xattr -w com.apple.quarantine "0081;<hex time>;Safari;" <dylib>`；dylib 要放在绝对路径下。
- `OrtApi` 序号：解析 `include/onnxruntime_c_api.h` 里 `struct OrtApi { … }`，按 `ORT_API2_STATUS` / `ORT_API_STATUS` / `ORT_API_T` / `ORT_CLASS_RELEASE`（加 `Release` 前缀）/ `ORT_API_CALL*` 声明的顺序从 0 开始计数；1.27.1 一共 422 个成员。

## 8. 风险汇总

| 风险 | 影响 | 处理 |
|---|---|---|
| 正式签名包加载不了 ORT | 方案不成立 | T4 最先验证 |
| Go 分词和 Python 不一致 | 所有测量数字作废 | T2 要求 100% 逐 token 一致 |
| 零样本质量低、noul 偏向判“是” | 功能没有价值或误导用户 | T8 校准；输出只作参考，不直接触发动作 |
| 长邮件截断丢失关键信息 | 判断出错 | T3 截断策略 + T8 评测 |
| 8 GB 机器内存紧张 | 卡顿或被系统杀掉 | 512 token 上限、每次一题、空闲退出；没有实测 |
| 第三方量化图来源不可控 | 供应链风险 | T5 自己复现并托管 |
