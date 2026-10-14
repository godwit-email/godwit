# Godwit 原生动态库插件设计

日期：2026-10-14。状态：设计，未实施。首个使用方是社区翻译插件 DLX（由另一个 agent 实施，不在本文范围）。

标 `[我的决定]` 的是本文作者在用户未明说处做的取舍；标 `[未验证]` 的还没有实验支撑。

## 1. 目标与边界

**目标**：用户在 Godwit 里点一下安装 DLX 插件，就能用它翻译邮件；不装 Go、不编译、不配端口。DLX 作为社区插件单独发布，不进应用核心。

**已定方向**（用户给定）：原生动态库 + 稳定 C ABI + JSON 负载，参考 CLIProxyAPI 的插件机制（commit `0d18d3b`），只取 Godwit 现在需要的最小子集。这是**可信代码执行机制，不是沙箱**。

**非目标**：

- 通用全功能扩展系统。v1 只有一种能力 `translate`；能力列表是为了以后加 `judge` 之类，不是现在就设计它们。
- 热卸载、同进程多插件共存、作者签名体系、评分评论等商店功能。
- 约束原生代码的行为。文档和界面都不能暗示有这种约束。

**反目标**（做到了也算失败）：

- 先加载动态库再从它读名称或能力来展示给用户。
- 完整性校验失败后，用「风险提示 + 仍然继续」绕过去。
- 把脚本插件的 `mail` / `net` 权限卡片照搬到原生插件上，让用户以为那是强制的。

## 2. 推荐方案与关键取舍

### 2.1 一句话

原生插件 = `plugin.json` + 一个平台专用动态库。核心（`core`）在**加载前**完成全部校验与授权，然后为每个启用的原生插件启动一个子进程 `godwit plugin-host`，由子进程 `dlopen` 并通过 C ABI 调用；核心与子进程之间走 stdio 上的 JSON 行协议。可信名单是一个 ed25519 签名的 `registry.json`，由独立的 `godwit-email/plugins` 仓库维护、CI 签名后发布，原生插件和脚本插件都在里面；两种插件通过同一个能力层接入翻译流程（2.7）；翻译流程新增「翻译方式」设置，在内置模型翻译与启用的插件之间选择。

### 2.2 取舍一：子进程宿主，而不是在主进程里 dlopen  `[已确认 2026-10-14]`

验证原型是在主进程里 dlopen 的。本文改为子进程，理由：

| 问题 | 主进程 dlopen | 子进程 `godwit plugin-host` |
|---|---|---|
| 插件崩溃 | 邮件客户端一起崩 | 子进程死，核心记一次失败，重启或停用 |
| 停用 / 卸载 / 升级 | Go c-shared 不能安全 dlclose，必须重启应用；Windows 上加载中的 DLL 无法删除 | kill 子进程即卸载，升级不用重启应用 |
| 内存 | 插件运行时常驻主进程（原型约 47 MiB） | 可以单独观测，以后可空闲退出 |
| 多个 Go 动态库共存（未验证） | 同一进程多个 Go 运行时 | 每个插件一个进程，问题不存在 |
| 取消调用 | ABI 没有取消；只能等 | 超时直接 kill |
| 代价 | 无 | 一个隐藏子命令 + 约 150 行进程客户端，照 `core/mcp_proc.go` 和 #64 的 laya worker 写 |

**C ABI 不变**（仍是原型验证过的 `godwit_plugin_init` + 函数表），只是调用它的是子进程。「用户决定了动态库方向」与子进程不冲突：插件作者交付的仍是动态库。

被否的更懒方案：让插件直接是一个可执行文件，走 MCP 一样的 stdio。它更省代码，但用户已定动态库方向，且动态库让宿主控制进程本身（可执行文件是插件自己的，Godwit 只能 exec 它）。本文不采用，记在这里供对比。

### 2.3 取舍二：宿主不用 cgo，用 purego  `[macOS 已验证：E1；Linux / Windows 待 E3]`

原型的宿主用 cgo 写了 `loader.c`，并据此认为「macOS 宿主 dlopen 需要 CGO」。但 mygo 本身就在无 cgo 下用 `purego.Dlopen` 加载 libghostty-vt、sqlite 和 Metal（`plugins/terminal/internal/vt/open_unix.go`、`plugins/sqlite/ffi_unix.go`），Windows 用 `syscall.LoadLibrary`。purego v0.11.1 已经是间接依赖。所以 CI 的 `CGO_ENABLED=0` 策略不变，只需把 purego 改成直接依赖。

E1 已在 macOS arm64 上验证：`CGO_ENABLED=0` 的宿主用 purego 加载 Go c-shared 的 DLX，调用、并发、超时 kill、崩溃隔离、真实翻译都通过，`-race` 无报告（见 §6 P0）。Linux 上 purego 无 cgo 时走 fakecgo，Windows 走 `syscall.LoadLibrary`，这两个平台等 E3。

### 2.4 取舍三：可信名单签名发布到 R2，而不是网站反代  `[已确认 2026-10-14]`

用户设想：插件仓库根目录放 JSON/YAML，网站反代，应用读网站接口。本文调整两点：

1. **签名**：仓库 CI 用 ed25519 私钥（GitHub Secret）对 `registry.json` 签名，公钥编进应用（同 mygo 更新器把 `publicKey` 写在 `mygo.config.ts` 的做法）。这样「名单来源可验证」不依赖谁控制 CDN、R2 或反代；Go 标准库 `crypto/ed25519` 直接验，不加依赖。
2. **发布位置**：CI 把 `registry.json` 和 `registry.json.sig` 上传到现有 R2 桶 `godwit-downloads` 的 `plugins/` 下，应用从 `https://dl.godwit.email/plugins/registry.json` 读。`site/` 不需要加代码（`release.yml` 已经有上传 R2 的凭据模式）。如果仍希望网站反代 GitHub 原文件，只是 `site/src/worker.ts` 多一条路由，核心不受影响；签名让两种方式一样安全。

文件名 `registry.json`。格式用 JSON 不用 YAML：Go 标准库有 JSON，没有 YAML。

**脚本插件也进同一个 registry**。区别只在产物和审核的含义（4.2、3.8）：

| | 原生插件 | 脚本插件 |
|---|---|---|
| 产物 | 每个平台一个动态库 | 一个平台无关的 `.js` 文件（作者自己 bundle 好） |
| 「已审核」意味着 | 这个文件可以加载；首装确认一次风险后直接启用 | 这个文件经过审核；**权限仍由用户授权**（现有权限卡片），因为权限是用户对数据的同意，审核替代不了 |
| 升级 | 目录版本一键升级，不再确认 | 权限不变则保持启用（现有行为），权限变了待授权（现有行为） |

### 2.5 取舍四：发布产物是裸动态库，不打包  `[我的决定]`

每个版本每个平台发布一个裸文件（`dlx-0.1.0-darwin-arm64.dylib`），`plugin.json` 的内容随 PR 写进 `registry.json` 并经审核。应用安装时自己把 manifest 写成 `plugin.json`、把库放成固定名 `plugin.dylib` / `plugin.so` / `plugin.dll`，再走现有 `InstallPlugin(dir)`。好处：不解 zip（没有 zip-slip、没有多余文件），名单里的 `sha256` 就是**那一个文件**的哈希，和「信任绑定具体文件」一一对应；一份 manifest 服务所有平台。名单外安装则是用户选一个目录（现有「安装」按钮），目录里放 `plugin.json` 和 `plugin.<ext>`。

### 2.6 取舍五：复用现有的「授权绑定哈希」机制

现有脚本插件用 `permission_hash` / `granted_hash` 表达「用户授权过的权限集」。原生插件把 `permission_hash` 定义为 **`source_hash`**（安装快照里 `plugin.json` + 库文件的哈希）。于是：文件变了 → `NeedsGrant` 重新为真 → 不能启用，直到重新审核或用户重新授权。不用新表，`GrantPermission`、`SetPluginEnabled`、`checkPluginFiles`、版本目录、`startPlugins` 的清理照旧。

要改的一处：`InstallPlugin` 的 upsert 今天写的是 `enabled = enabled AND permission_hash = excluded.permission_hash`。原生插件的 `permission_hash` 每个版本都变，照旧的话**每次升级都会变成待授权**。所以 `InstallPlugin` 增加一个参数 `trust string`（`""` 或 `"listed"`）。为 `"listed"` 时总是写 `trust = 'listed'`；对原生插件还在同一条 upsert 里写 `granted_hash = excluded.permission_hash, enabled = 1`（脚本插件不授权，见 3.8），切换与授权是一条语句，不会出现「新文件已切换、授权还是旧的」的中间态。名单外安装传 `""`，行为与今天一样。

新增一列 `trust`（`listed` | `user`）记录**这次授权是怎么来的**：`listed` 表示安装时文件哈希命中了签名名单；`user` 表示用户看过风险提示后自己授权。这就是「审核可信」和「用户已授权」的区分。

### 2.7 统一的能力层，两种运行时各一个适配器  `[2026-10-14 按用户意见加入]`

用户的设想是 app → 插件层 → 适配层 → JS / 动态库。本文采纳，统一点放在**能力**这一层，不放在 `ctx` 宿主 API 这一层：

```
app（阅读页翻译按钮、设置里的「翻译方式」）
  └─ core：RunSkill("translate") 按 Settings.Translator 分流；ListTranslators 列出可选渠道
       ├─ 内置：模型翻译（现有流式路径，原样保留）
       └─ translator 接口  translate(ctx, req) (res, error)
            ├─ jsTranslator      → goja VM，调用插件 ctx.translators.register 注册的函数
            └─ nativeTranslator  → NativePlugin 子进程，调用 C ABI 的 "translate"
```

**统一的部分**：

- 能力契约：`translate` 的请求 `{texts, source, target}`、响应 `{texts, source}`、错误码集合，对 JS 和原生完全相同（4.4）。JS 侧的类型写进 `sdk/godwit.d.ts`。
- 能力声明：两种插件都在 manifest 里写 `capabilities: ["translate"]`。设置页据此列出渠道，**不需要先加载插件**。JS 插件在初始化时注册了没声明的能力，或声明了没注册，都算加载失败。
- 生命周期：安装、授权、启停、配置、卸载、失败计数、registry，都是同一张 `plugins` 表和同一组 `Service` 方法，按 manifest 分流。

**不统一的部分**：`ctx.mail` / `ctx.net` / `ctx.llm` / `ctx.storage` 这些宿主 API 只给 JS 插件。原因：

- 它们存在的意义是让沙箱里的代码在受控范围内办事。原生代码本来就能直接读文件、联网，给它 `ctx.net` 不增加任何约束，只增加 ABI 面。
- 把它们搬到 C ABI 需要宿主回调（插件持有宿主的函数指针），是 ABI v2 级别的改动。真有需要时（例如原生插件要调用户选的模型）再加；子进程协议是双向 JSON 行，到时宿主回调只是多一种消息。

**模型翻译不塞进 `translator` 接口**：它是流式的（`ai.Event` 通道，带 `NO_TRANSLATION` 前缀缓冲和缓存），插件是一次性返回。强行统一要么让插件假装流式，要么让模型失去流式。所以分流点就在 `runSkill` 里一个 `if`，渠道列表对用户是统一的。

**JS 插件提供翻译时的外发规则** `[我的决定]`：新调用种类 `KindTranslate`。它拿到 manifest 声明的全部主机，**不逐次审批**，并且在交给插件前把插件标为 `holds_mail`。理由是用户在设置里把它选为翻译方式，并且每次自己点击「翻译」，这本身就是对「把这封邮件交给这个插件、它会发往声明的主机」的同意；安装时的权限卡片多一行「会收到你要翻译的邮件内容，并可发往上面列出的地址」。这和 Action / Tool 的「同时有 mail:read 和 net 时每次审批」不同，需要你确认。

## 3. 用户流程

### 3.1 安装（名单内）

1. 设置 › 扩展 › 插件 › 「插件目录」区块列出 `registry.json` 里当前平台有产物、`abi` 与应用兼容、`minGodwit` 满足的插件（不满足的灰显并说明原因）。信息全部来自名单，不来自插件文件。
2. 点「安装」→ 一个简短确认（现有 `confirmView` 样式）：

   > **安装原生插件 DLX 0.1.0**
   > 原生插件以 Godwit 的全部权限运行：能读取你的邮件、账户凭据和本机文件，访问任何网络地址，运行程序，也可能让 Godwit 崩溃；Godwit 无法限制它。这个文件经过 Godwit 插件目录审核。
   > [安装]  [取消]

   已装同名插件且它是 `trust = 'user'`（用户自己装的）时，按钮文案改为「替换为目录版本」，确认框多一句「将替换你自己安装的 dlx」。替换后 `trust = 'listed'`。`[我的决定]`

3. 下载到 `DataDir/plugins/.staging-*/`，`sha256` 不匹配名单 → 删除并报错「下载的文件与插件目录记录不符，已丢弃」，**没有「仍然继续」**。
4. 校验通过 → 写 `plugin.json` 与 `plugin.<ext>` → `InstallPlugin(dir, "listed")`（见 2.6）→ 启动子进程，`describe` 交叉核对 manifest，`configure` 下发配置。启动失败 → 现有 `startOrDisable` 停用，行留着，用户看得到原因。
5. 若 manifest 有 `config`，插件行展开显示配置表单（现有 `configView`）；secret 进钥匙串，和脚本插件一样。

### 3.2 安装（名单外）

1. 点页面右上角现有「安装」→ 选目录 → `InstallPlugin(dir)` 识别出 `native` manifest。
2. 安装后行处于「待授权」（现有 `NeedsGrant` 状态与 `statusPill`），展开是风险卡片：

   > **这个文件不在 Godwit 审核过的插件目录里。**
   > 它以 Godwit 的全部权限运行（同上一段）。只有在你信任它的来源时才继续。
   > 文件：plugin.dylib · SHA-256 `3f9a…c21e`
   > [仍然安装并启用]（Destructive）  [暂不启用]

3. 「仍然安装并启用」→ `GrantPermission`（`trust = 'user'`）→ `SetPluginEnabled(true)`。授权只对这个 `source_hash` 有效。

### 3.3 启用 / 停用

- 开关即现有开关。启用 = 启动子进程并完成 `describe` / `configure`；任一步失败 → 现有 `startOrDisable` 把行停用并记 `disabled_reason = load_failed`，展示在行内 `kit.Alert`。
- **失败计数**（新逻辑，今天的 `pluginFailed` 只计数不重启）`[我的决定]`：只有 ABI 层错误、超时、子进程退出算一次 `pluginFailed`；插件返回的 `ok: false` 信封（限流、上游错误、不支持的语言）是正常业务结果，不计数，否则三次限流就会停用 DLX。子进程退出后不立即重启；下一次调用时懒启动。连续三次失败照现有规则停用。
- 停用 = kill 子进程。**停用后代码已从进程中消失**（子进程没了），这点和脚本插件「停用后不再调用但 VM 对象仍在」不同；文档和界面用「已停止」即可，不需要区分。
- 同一个文件（`source_hash` 不变）启停多少次都不再弹风险提示。

### 3.4 翻译

1. 设置 › 通用 › 语言区块新增「翻译方式」下拉：`模型（默认）`、`DLX（插件）`…；只列出已启用且 `capabilities` 含 `translate` 的原生插件。选了插件但它后来被停用 → 下拉显示「DLX（已停用）」，翻译按钮仍显示，点击后报「翻译插件已停用」。
2. 阅读页「翻译」按钮、选区翻译：行为不变。走插件时，`context` 事件的 `Model` 写 `plugin/dlx`，现有 `modelOf` 按 `/` 切出 `dlx`，标签 `reader.ai.translated` 的模型位显示 `dlx`，不改界面代码。
3. 「无需翻译」语言：插件返回检测到的源语言，核心按 `NoTranslate` 判断，和模型路径一样显示「无需翻译」。
4. 错误：插件错误信封的 `message` 原样进 `aiRun.err`，由现有 `runState` 展示（它已有重试按钮）；子进程崩溃显示「翻译插件意外退出，请重试」，三次后显示「翻译插件已停用」。
5. 草稿翻译（撰写页 `translate-draft`）：P4 接入，之前仍走模型。

### 3.5 升级

- 名单刷新（打开扩展页、每 24 小时一次）发现更高版本 → 行内显示「有更新 0.2.0」按钮 → 走 3.1 的 3–4 步：新版本目录、`InstallPlugin(dir, "listed")` 一条语句切换并授权、旧子进程 kill、新子进程启动；切换失败则一切照旧（现有语义）。不需要重启应用，也不再弹风险提示：目录版本的信任来自审核，而首装时用户已经确认过「原生插件有全部权限」。`[我的决定]`
- 不自动升级，用户点了才升。
- `user` 授权的插件，如果它的文件哈希后来出现在名单里，名单刷新时把 `trust` 改为 `listed`（只升不降，文件不变，没有风险）。
- 名单外插件没有自动升级：用户再选一次目录，等于重新安装 + 重新授权。

### 3.6 卸载

现有「卸载」按钮：kill 子进程 → 删行（级联删配置、KV）→ 删钥匙串 secret → 删目录。Windows 上因为子进程已退出，DLL 不再被占用，可以删。

### 3.7 撤销信任

- 名单维护者从 `registry.json` 删掉某个 release（或整个插件）并发布。
- 应用下次刷新名单：对每个 `trust = 'listed'` 的已装插件，用它库文件的 `sha256` 查名单；查不到 → `enabled = 0`，`disabled_reason = trust_revoked`，`granted_hash = ''`，发 `PluginsChanged`。行内提示「Godwit 插件目录已撤销对这个文件的信任」，用户可按 3.2 的流程以 `user` 身份重新授权。
- 名单刷新失败（离线）不改变任何已装插件的状态；缓存的名单继续用于展示。

### 3.8 目录里的脚本插件

1. 「插件目录」里脚本插件和原生插件列在一起，标签分别是「脚本」和「原生」。
2. 点「安装」→ 下载那个 `.js` → `sha256` 校验（失败同样不可绕过）→ 写 `plugin.json` 和 `<main>` → `InstallPlugin(dir, "listed")`。
3. 安装后进入现有的待授权状态，显示现有的权限卡片，上面多一行「已审核：Godwit 插件目录」。用户点「允许」才启用。`"listed"` 对脚本插件只写 `trust` 列，不授权。
4. 撤销：和原生插件一样，`trust_revoked` 停用。

## 4. 数据格式

### 4.1 manifest（`plugin.json`，原生）

```json
{
  "name": "dlx",
  "version": "0.1.0",
  "description": { "default": "Translate with DeepLX", "zh-CN": "用 DeepLX 翻译" },
  "native": { "abi": 1 },
  "capabilities": ["translate"],
  "config": [
    { "key": "endpoint", "type": "text", "label": { "default": "Endpoint", "zh-CN": "接口地址" } }
  ]
}
```

`ParseManifest` 规则追加：

- `native` 与 `main` 二选一；有 `native` 时 `main`、`permissions` 必须缺省（原生代码没有可执行的权限表达，写了就是误导）。
- `native.abi` 必须等于应用支持的 ABI 版本（v1 = 1），否则拒绝安装（在加载前）。
- `capabilities` 对原生插件必须非空，对脚本插件可以缺省；每项在已知集合里（v1 只有 `translate`）。脚本插件声明了 `translate` 就必须在初始化时用 `ctx.translators.register` 注册，反之亦然（2.7）。
- 库文件名固定 `plugin.dylib` / `plugin.so` / `plugin.dll`，由当前平台决定，不在 manifest 里。
- `config` 规则不变（secret 进钥匙串）。注意：配置值通过 `configure` 交给原生代码，secret 也在内；风险文案已覆盖「凭据」。

大小限制：`plugins.Limits{File, Total}` 作为 `Snapshot` / `HashDir` 的参数；脚本插件 5 / 20 MiB 不变，原生 64 / 80 MiB。`InstallPlugin` 先用大限制快照、读 manifest 判定类型，脚本插件再按小限制复核（`files()` 走一遍即可）。

### 4.2 可信名单（`registry.json`）

```json
{
  "schema": 1,
  "updated": "2026-10-14T08:00:00Z",
  "plugins": [
    {
      "name": "dlx",
      "title": { "default": "DLX", "zh-CN": "DLX 翻译" },
      "summary": { "default": "Translate mail with DeepLX, no API key needed." },
      "homepage": "https://github.com/godwit-email/plugin-dlx",
      "releases": [
        {
          "version": "0.1.0",
          "minGodwit": "1.2.0",
          "manifest": { "name": "dlx", "version": "0.1.0", "native": { "abi": 1 }, "capabilities": ["translate"], "config": [] },
          "artifacts": {
            "darwin-arm64":  { "url": "https://github.com/godwit-email/plugin-dlx/releases/download/v0.1.0/dlx-darwin-arm64.dylib", "sha256": "ed6b0873…b89", "size": 11355106 },
            "darwin-amd64":  { "url": "…", "sha256": "…", "size": 0 },
            "windows-amd64": { "url": "…", "sha256": "…", "size": 0 },
            "linux-amd64":   { "url": "…", "sha256": "…", "size": 0 }
          }
        }
      ]
    }
  ]
}
```

脚本插件的条目（同一个 `plugins` 数组）：

```json
{
  "name": "deepl",
  "title": { "default": "DeepL" },
  "summary": { "default": "Translate with your DeepL API key." },
  "homepage": "https://github.com/someone/godwit-deepl",
  "releases": [
    {
      "version": "0.1.0",
      "minGodwit": "1.2.0",
      "manifest": { "name": "deepl", "version": "0.1.0", "main": "index.js", "capabilities": ["translate"],
                    "permissions": { "net": ["api.deepl.com", "api-free.deepl.com"] },
                    "config": [{ "key": "apiKey", "type": "secret", "label": "DeepL API Key" }] },
      "artifacts": { "any": { "url": "https://github.com/someone/godwit-deepl/releases/download/v0.1.0/index.js", "sha256": "…", "size": 18342 } }
    }
  ]
}
```

- 原生插件的 release 只有平台键；脚本插件的 release 只有 `any`，且 `manifest.main` 是不带目录的单个 `.js` 文件名，应用把产物写成这个名字。大小沿用脚本插件的 5 MiB。
- 平台键与 `site/src/download.ts` 的 `targets` 一致：`darwin-arm64`、`darwin-amd64`、`windows-amd64`、`windows-arm64`、`linux-amd64`、`linux-arm64`。缺的平台就是不支持。
- `manifest` 必须能通过 `ParseManifest`，且 `name` / `version` 与外层一致；原生插件的 `native.abi` 与应用一致。CI 在签名前跑同一个校验（Go 小程序或 `godwit` 的隐藏子命令）。
- `registry.json.sig`：ed25519 对 `registry.json` 原始字节的签名，base64。应用：下载两者 → 验签 → `updated` 不早于缓存 → 替换缓存 `DataDir/plugins/registry.json`（原子写）。任一步失败 → 保留旧缓存，界面提示「插件目录暂时无法更新」。
- 公钥是 `core` 里的常量（与更新器密钥不同，缩小泄露影响）。
- 下载：`net/http`，只允许 `https`，响应最多 `size`（或 64 MiB），30 秒连接 / 5 分钟总超时，不走环境代理之外的特殊逻辑（和更新器一样是应用自己的出站）。

### 4.3 授权记录（`plugins` 表）

迁移只追加一列：

```sql
ALTER TABLE plugins ADD COLUMN trust TEXT NOT NULL DEFAULT ''
```

原生插件一行的含义：

| 列 | 原生插件取值 |
|---|---|
| `manifest` | 4.1 的 JSON（含 `native`，由此判断类型，不加 `kind` 列） |
| `source_hash` | 快照哈希（`plugin.json` + `plugin.<ext>`） |
| `permission_hash` | **等于 `source_hash`** |
| `granted_hash` | 授权时的 `permission_hash`；文件变了就对不上 → `NeedsGrant` |
| `trust` | `listed`（安装时命中名单）/ `user`（用户授权）/ `''`（未授权） |
| `disabled_reason` | 现有值 + `trust_revoked` |

`PluginInfo` 加 `Native bool`、`Trust string`、`Capabilities []string`，列表与界面据此分支。

### 4.4 C ABI（`sdk/native/godwit_plugin.h`，交给插件作者）

```c
#include <stddef.h>
#include <stdint.h>

#define GODWIT_PLUGIN_ABI 1

typedef struct { void *ptr; size_t len; } godwit_buffer;

/* method：NUL 结尾 UTF-8；req：JSON，宿主持有，调用返回后失效；
 * out：插件分配的 JSON 响应信封，宿主读完后调用 free_buffer 归还。
 * 返回 0 表示 out 有效（信封里 ok 可能为 false）；非 0 表示 ABI 层失败，out 无效。 */
typedef int  (*godwit_call_fn)(const char *method, const uint8_t *req, size_t req_len, godwit_buffer *out);
typedef void (*godwit_free_fn)(void *ptr, size_t len);

typedef struct {
    uint32_t       abi_version;   /* 插件填 GODWIT_PLUGIN_ABI */
    godwit_call_fn call;
    godwit_free_fn free_buffer;
} godwit_plugin_api;

/* 唯一导出符号。host_abi 是宿主支持的版本；不等于 GODWIT_PLUGIN_ABI 时返回非 0，不得填 api。 */
int godwit_plugin_init(uint32_t host_abi, godwit_plugin_api *api);
```

**版本协商**：`host_abi != GODWIT_PLUGIN_ABI` → 插件返回非 0 → 宿主报 `abi_incompatible`。宿主侧还要求 `api->abi_version == 1` 且两个函数指针非空。加载前已由 manifest 的 `native.abi` 挡过一次，这里是第二道。

**内存责任**：请求缓冲归宿主，仅在调用期间有效；响应缓冲归插件分配，宿主复制后立刻 `free_buffer`。两边都不保留对方的指针。请求上限 1 MiB，响应上限 4 MiB，超出即 ABI 层错误。

**不跨边界的东西**：Go interface、map、slice、channel、context、Go error、回调函数指针。v1 没有宿主回调（插件不需要调宿主）。

**并发**：宿主可能并发调用 `call`（原型验证过 1024 次本地并发）；插件必须线程安全。宿主侧每插件最多 4 个在途调用。

**取消**：ABI 没有取消。宿主放弃等待后，插件那次调用继续跑到结束；单次调用超过 60 秒宿主 kill 子进程（这就是子进程的意义）。

**关闭**：没有 `shutdown` 入口。宿主先关 stdin，给 2 秒，再 kill 进程组（照 `mcpProc.Close`）。插件不要依赖析构。

**方法与 JSON**（信封照 CLIProxyAPI 的 `pluginabi.Envelope`）：

```jsonc
// 响应信封
{ "ok": true,  "result": { … } }
{ "ok": false, "error": { "code": "unsupported_language", "message": "…", "retryable": false } }

// describe → 加载后交叉核对 manifest，不用于展示
req: {}
res: { "name": "dlx", "version": "0.1.0", "abi": 1, "capabilities": ["translate"] }

// configure → 启动后、每次配置保存后
req: { "config": { "endpoint": "https://…" } }
res: {}

// translate
req: { "texts": ["段落一", "段落二"], "source": "", "target": "zh-CN" }   // source 空 = 自动检测
res: { "texts": ["…", "…"], "source": "en" }                              // source 是检测到的语言，BCP 47
```

错误码集合（v1）：`invalid_request`、`unsupported_language`、`rate_limited`、`upstream`、`internal`。未知方法返回 `invalid_request`。`describe` 的 `name`/`version`/`capabilities` 与 manifest 不一致 → 加载失败。

### 4.5 核心 ↔ 子进程协议

`godwit plugin-host --library <绝对路径> --sha256 <hex>`，stdio 上每行一个 JSON。路径必须是绝对路径：hardened 程序的 dlopen 拒绝相对路径（E2），`versionDir` 的结果先过一次 `filepath.Abs`。

```jsonc
→ { "id": 1, "method": "translate", "params": { … } }
← { "id": 1, "ok": true, "result": { … } }
← { "id": 1, "ok": false, "error": { "code": "…", "message": "…" } }
```

子进程只做四件事：读文件→算 `sha256`→不等于参数则退出码 3 不加载；`dlopen` + `godwit_plugin_init`；把每行的 `params` 字节原样交给 `call`，把信封原样加 `id` 写回；stdin 关闭则退出。它不读数据目录、不碰钥匙串、环境只有 `PATH`（照 `startMCPProcess`）。

## 5. 需要改的地方

| 位置 | 改动 |
|---|---|
| `core/plugins/manifest.go` | `Manifest.Native *Native{ABI uint32}`、`Capabilities []string`；`ParseManifest` 的二选一与 ABI 校验；`PermissionHash` 对原生返回 `""`（调用方改用 `source_hash`） |
| `core/plugins/install.go` | `files` / `HashDir` / `Snapshot` 接 `Limits`；`NativeLimits`；`LibraryName()` 按 GOOS 返回固定名 |
| `core/plugins/native.go`（新） | `NativePlugin`：启动 `plugin-host`、JSON 行客户端、`Call(ctx, method, params) (json.RawMessage, error)`、在途计数、超时 kill、退出通知、懒重启。约 200 行 + 测试（用本测试二进制当假子进程，照 `core/mcp_proc.go` 与 laya-spike 分支的 `client_test.go`） |
| `cmd/godwit/pluginhost.go`、`pluginhost_unix.go`、`pluginhost_windows.go`（新） | 子命令；purego `Dlopen` + `RegisterFunc`（unix）/ `syscall.LoadLibrary` + `NewLazyProc`（windows）；函数指针调用用 `purego.SyscallN` |
| `sdk/native/godwit_plugin.h`（新） | 4.4 的头文件 |
| `core/extensions.go` | `InstallPlugin(ctx, dir, trust)`：原生分支跳过 `Build`，`permission_hash = hash`，`trust == "listed"` 时写 `trust`，原生插件同一条 upsert 授权并启用（2.6）；现有调用方（`app/settings_extensions.go` 的 `install`、rpc、测试）传 `""`。`runningPlugin` 今天绑死 goja（`p *plugins.Plugin`、`registerPluginTools`、`startHooks`、`stopPlugin` 里的 `VM().Close()`）：加一个字段 `native *plugins.NativePlugin`，`loadPlugin` / `stopPlugin` 在入口按 manifest 分流，原生分支不注册工具和 Hook。`SetPluginConfig`：原生插件保存后重发 `configure`；`pluginInfo` 填 `Native/Trust/Capabilities`；`pluginFailed` 只由 ABI 错误、超时、退出调用（3.3）。`ListPlugins` 每次都重算所有插件文件的哈希（已有 `shortcut` 注释），64 MiB 的库会让扩展页变慢：原生插件按 `(size, mtime)` 缓存哈希，变了再算 |
| `core/pluginstore.go`（新） | `registry` 类型、`RefreshPluginRegistry`（下载、验签、防回滚、缓存、撤销检查）、`ListPluginRegistry`、`InstallPluginFromRegistry(name)`（下载、校验、staging、`InstallPlugin`、`trust='listed'`、启用）。公钥常量。约 250 行 + 测试（httptest + 测试密钥对） |
| `core/store/schema.go` | 追加 `trust` 列 |
| `core/settings.go` | `Translator string`（`""` 或 `plugin:<name>`）；`validate` 用 `skills.ValidName` |
| `core/models.go` | `TaskModel("translate")`：`Translator` 指向插件时返回 `"plugin/<name>"`；插件未启用/未运行返回新错误 `ErrTranslatorUnavailable`（不在 `refreshTasks` 的隐藏集合里，按钮仍显示、运行时报错） |
| `core/aiservice.go` | `runSkill`：`name == "translate"` 且 `Translator` 指向插件时，**在 `resolveTask` 之前**分流（否则没配模型的用户会先得到 `ErrAINotConfigured`）。插件分支：`mailTarget` + `bind` 照旧；正文取 `body_text`，为空则 `mail.HTMLToText(body_html)`（`mailForModel` 的 `Text` 只有 `body_text`，纯 HTML 邮件会是空的）；selection 用 `t.Text`；按空行切段 → `Call("translate")` → `NoTranslate` 判定 → 一次 `text` 事件 + `done`；cache key 用 `("plugin", name, version, lang, keep)` 代替模型三元组；`context` 事件 `Model = "plugin/<name>"`；审计照旧写 `gateway.Record` |
| `core/translators.go`（新） | `translator` 接口、`nativeTranslator`、`jsTranslator`、`ListTranslators`（内置模型 + 声明了 `translate` 且已启用的插件）。约 80 行 |
| `core/plugins/vm.go`、`host.go`、`types.go` | `ctx.translators.register`；`KindTranslate`；`EgressFor` 对 `KindTranslate` 返回声明的全部主机、不审批（2.7）；`Plugin.Translate(ctx, inv, req)` |
| `sdk/godwit.d.ts` | `TranslateRequest` / `TranslateResult` / `ctx.translators` 类型 |
| `core/pluginhost.go` | 不动（原生插件没有 ctx API） |
| `rpc/roles.go` | 新方法默认只在 `full` 角色；`mcp` 允许名单不动 |
| `app/settings_extensions.go` | 插件行：`Native` 时权限卡片换成风险卡片 + 信任行（「已审核：Godwit 插件目录」/「你已授权此文件」）+ 「原生」pill；`NeedsGrant` 且 `Native` 时按钮改「仍然安装并启用」（Destructive）；新增「插件目录」区块（列表、安装、更新、不可用原因）；`trust_revoked` 的提示文案 |
| `app/settings_general.go` | 语言区块加「翻译方式」`settingSelect` |
| `app/app.go` | `PluginsChanged` 事件也触发 `refreshTasks()`（现在只在设置变化时触发） |
| `internal/i18n/zh.go`、`en.go` | 新文案 |
| `mygo.config.ts` + `entitlements.plist`（新） | `macos.entitlements` 加 `com.apple.security.cs.disable-library-validation`（见 7.1） |
| `go.mod` | purego 改直接依赖 |
| 新仓库 `godwit-email/plugins` | `registry.json`、`schema.md`、审核清单、CI（校验 → 签名 → 上传 R2） |

不改的：`core/plugins/vm.go`、`host.go`、`net.go`、hooks、beforeSend、Card、Tool。原生插件不参与这些。

## 6. 分阶段计划

### P0 实验

2026-10-14 在 macOS 27 arm64、Go 1.27.1 上完成 E1、E2、E4。实验代码在本地分支 `experiment/native-p0-e3` 的 `experiments/native-p0/`：插件是 DLX（`0f2b50d`）套上 4.4 的 ABI，加 `probe.sleep`、`probe.crash`；宿主是一个 `CGO_ENABLED=0` 的程序，`plugin-host` 子命令就是 4.5 的子进程，不带子命令时扮演核心。

- [x] **E1 purego 宿主**：通过。
  - `go version -m` 确认 `CGO_ENABLED=0`，purego v0.11.1；宿主对 linux / windows 的 amd64 / arm64 和 darwin/amd64 都能交叉编译。
  - 子进程加载、`describe`、坏请求返回 `invalid_request`、1024 次并发（16 路在途）、超时 1 秒 kill 进程组后下次调用懒重启、插件里未捕获的 panic 只杀子进程且 stderr 尾部带上 panic 信息、stdin 关闭后子进程自己退出、运行中删库文件（unix 可以）和退出后删文件，全部通过。
  - `-race` 版本（无 cgo 也能开）主进程和子进程都没有报告。
  - 三次真实翻译：「Hello, world!」「See you tomorrow.」→「你好，世界！」「明天见。」，插件返回检测到的源语言 `en`。耗时 1607 / 583 / 1021 ms。
  - 子进程常驻内存：加载后约 21 MiB，1024 次调用后约 31 MiB，真实翻译后约 29 MiB（`ps` RSS，仅子进程）。
  - 加载到 `describe` 返回：一个新文件第一次加载约 370 ms（系统对新代码的评估），之后同一文件 11–16 ms。
- [x] **E2 签名与库验证**：通过，结论见 7.1。用本机的 Developer ID Application 证书签宿主（`--options runtime --timestamp`），测试五种库。
  - 只开 hardened runtime：只加载同一 Team ID 签名的库，ad-hoc 库被拒（「different Team IDs」）。
  - 再加 `com.apple.security.cs.disable-library-validation`：ad-hoc 库可以加载。**这个 entitlement 必要且充分**。
  - 没签名的库：所有宿主都拒绝（arm64 不运行无签名代码）。插件 CI 用 Go 链接器默认的 ad-hoc 签名就够了。
  - 带 `com.apple.quarantine`、系统从没见过的库：所有宿主都拒绝（「library load disallowed by system policy」）。把内容写成一个新文件后就能加载。同一份代码哈希评估过之后，带隔离标记的副本也能加载，所以实验必须用新编译的库。
  - hardened 程序 dlopen 不接受相对路径，宿主必须传绝对路径（4.5）。
  - 没做：公证（本机没有 notarytool 凭据）；打成 `.app` 后的同样测试（库验证按进程的签名判断，裸二进制与包内主程序一样，但没有实测）。
- [ ] **E3 Windows / Linux**：本机没有可用的虚拟机环境（只有一台关着的 Windows UTM 虚拟机，没有 mingw）。已在本地分支 `experiment/native-p0-e3` 准备好工作流 `.github/workflows/native-p0-e3.yml`，只在推送这个分支时运行：ubuntu x64 / arm64、windows x64 / arm64（arm64 的 Windows 没有自带 mingw，只作参考），跑同一个 driver、拒绝加载与 ABI 检查、Linux 的 race 与最低 glibc。**需要推送这个分支**。
- [x] **E4 ABI 不兼容**：通过。宿主传 `host_abi = 2`，插件 `init` 返回 -1，子进程以退出码 4 报「incompatible ABI」，不崩。另外：哈希不符时退出码 3、不 dlopen，只有构造函数的库**没有执行**；授权的哈希下构造函数执行，然后因为没有入口被拒（证明校验必须在加载前）；文件改一个字节被拒。

E3 通过后开始 P1。E3 失败则只影响对应平台：该平台的 registry 条目不发布，P1 照常。

### P1 核心加载链路

- [ ] manifest 原生分支与测试（二选一、ABI、capabilities、限制）。
- [ ] `plugin-host` 子命令与 `NativePlugin` 客户端（假子进程测试：正常、崩溃后懒重启、超时 kill、stdin 关闭退出、哈希不符拒绝）。
- [ ] `InstallPlugin(dir, trust)` / `runningPlugin` / `loadPlugin` / `stopPlugin` / `SetPluginConfig` 的原生分支；`permission_hash = source_hash`；`trust` 列；`pluginFailed` 只计 ABI 错误、超时、退出；`ListPlugins` 的哈希缓存。
- [ ] 扩展页：风险卡片、`user` 授权按钮、「原生」pill、信任行。
- [ ] `mygo.config.ts` entitlement。

验收：选目录安装原型 dylib → 待授权 → 风险提示 → 授权启用 → 子进程在跑；改动库文件任意一个字节 → 重启后 `files_changed` 停用，再次启用要求重新授权；插件返回 `ok: false` 不计失败；三次崩溃后停用；卸载后目录与进程都没了。全部在 `go test ./...`（CGO_ENABLED=0）里用夹具覆盖，真实 dylib 只做手动冒烟。

### P2 名单与目录

- [ ] 新仓库：`registry.json`、校验程序、签名与上传 CI、审核清单（看源码、看构建是否可复现、核对 sha256）。
- [ ] `core/pluginstore.go`：拉取、验签、防回滚、缓存、撤销。
- [ ] `InstallPluginFromRegistry`：下载、哈希、staging、安装、`trust='listed'`。
- [ ] 扩展页「插件目录」区块与「有更新」。

验收：假 registry（httptest + 测试密钥）→ 列表显示 → 安装 → 一键启用；从 0.1.0 升到 0.2.0 后仍是启用状态、`trust = 'listed'`、没有待授权；篡改 registry 字节 → 验签失败、旧缓存继续用；篡改下载产物 → 丢弃报错，无法绕过；从 registry 删掉 release → 刷新后该插件 `trust_revoked` 停用，重新授权为 `user` 后可启用；`updated` 回退的 registry 被拒。

### P3 翻译接入

- [ ] `Settings.Translator` 与设置界面。
- [ ] `translator` 接口与两个适配器（2.7）；`TaskModel` / `runSkill` 的插件路径、缓存键、`NoTranslate`、错误。
- [ ] 脚本插件：`ctx.translators.register`、`KindTranslate` 与它的外发规则、SDK 类型、权限卡片多出的一行。
- [ ] `refreshTasks` 监听 `PluginsChanged`。

验收：契约测试（`contract/`）分别用假原生插件（夹具子进程直接实现协议，不需要真 dylib）和夹具脚本插件跑通同一组用例：选插件 → 阅读页翻译得到插件译文、标签显示插件名；源语言在 `NoTranslate` → 「无需翻译」；插件停用 → 按钮仍在、错误提示正确；切回模型 → 行为与今天一致。手动：真 DLX 插件翻译一封真实邮件。

### P4 升级与草稿翻译

- [ ] 名单版本比较与「更新」按钮（复用 P2 安装路径）。
- [ ] `translate-draft` 走插件：`richtext.FromComposeHTML` → 逐块翻译文本 → `ToHTML` → 现有 `proposeUpdate` 提案流程。
- [ ] 可选：子进程空闲 10 分钟退出、按需拉起。

验收：从 0.1.0 升到 0.2.0 不重启应用、旧目录被清、配置保留；草稿翻译保留段落 / 列表 / 链接结构。

## 7. 平台与生命周期事实

### 7.1 macOS

- 正式包由 `mygo build` 以 Developer ID 签名并开 hardened runtime（`codesignArgs` 加 `--options runtime`）。E2 实测：这时库验证只放行同一 Team ID 签名的库，第三方插件必然被拒；加上 `com.apple.security.cs.disable-library-validation` 后放行，且这个 entitlement 必要且充分。`mygo.config.ts` 的 `macos.entitlements` 支持。Apple 把它列为 hardened runtime 允许的例外，公证应当接受 `[推断，未实测]`。
- **entitlement 加在哪** `[我的决定]`：v1 加在 `godwit` 主程序上，因为子进程就是 `godwit plugin-host`，同一个可执行文件。代价是主进程也不再校验它加载的库是谁签的：能写入 Godwit 安装目录的本机程序，可以换进一个 ad-hoc 签名的库，继承 Godwit 获得的系统权限（通知、将来可能的通讯录等）。另一种做法是做一个只有加载功能的小程序 `godwit-plugin-host`，放进 `resources/<platform>/bin/`，用 mygo 的 `macos.helperEntitlements` 只给它这个 entitlement，主程序保留库验证。成本是一个新的 `cmd/` 和发布流程里每个平台多一次 `go build`。钥匙串在两种做法下没有区别：go-keyring 在 macOS 上通过 `/usr/bin/security` 读写，同一用户的任何进程本来就能读到这些条目。要更严，就换成小程序，P1 之前定。
- 带 `com.apple.quarantine`、系统没评估过的库会被拒。安装路径总是写**新文件**：registry 下载由 `net/http` 写出，不带隔离标记；名单外安装由 `Snapshot` 逐字节复制，xattr 不跟过来。这意味着名单外插件**不经过 Gatekeeper**，替代它的就是本文的风险提示和按哈希授权。这一点写进风险文案之外的开发者文档即可，用户文案不变。
- 新文件第一次加载约 370 ms，之后 11–16 ms（E1），首次启用会稍慢。
- 没有 osx-x86_64 的 ORT 之类问题不在本文范围；DLX 是纯 Go，六个平台都能交叉编译（c-shared 需要各平台的 C 工具链，这是插件仓库 CI 的事）。

### 7.2 Windows / Linux

- Windows：`syscall.LoadLibrary` 不需要 cgo；加载中的 DLL 不能删除或覆盖，子进程先退出再删解决（E3 的 `file_lock` 步骤专门验证）。Go c-shared 的 DLL 需要 mingw 构建 `[未验证，E3]`。
- Linux：purego `Dlopen` 不需要 cgo；c-shared 产物链接 glibc，最低 glibc 版本由插件仓库的构建镜像决定，写进 `registry.json` 的 `summary` 里即可，应用不检测。

### 7.3 生命周期

- 不 `dlclose`。子进程生命周期就是库的生命周期。
- 「停用」= 子进程已退出 = 代码不在任何进程里。「卸载」= 停用 + 删文件。「升级」= 新目录 + 行切换 + 停旧起新。三者都不需要重启应用。
- 应用退出：`Service.Close` → `stopPlugins` → 关 stdin、等 2 秒、kill 进程组。
- 子进程与主进程通过 `setProcessGroup` 绑在一起（照 MCP）；主进程被强杀时子进程成孤儿，这是 MCP 今天同样的限制，不另处理。

## 8. 验证计划

| 项 | 怎么验 | 阶段 |
|---|---|---|
| 加载前授权 | 夹具库只有 C constructor（原型已有）：未授权时不产生标记文件；授权后产生 | P1 单测 |
| 文件变化 | 改一个字节 → `checkPluginFiles` 失败 → 停用、`NeedsGrant` | P1 单测 |
| 名单外默认不加载 | 安装后 `enabled = 0`、`trust = ''`，`SetPluginEnabled` 返回 `ErrNeedsGrant` | P1 单测 |
| 下载完整性 | 篡改产物 → 丢弃；界面无绕过按钮（界面测试断言没有该按钮） | P2 单测 + app 测试 |
| 名单可验证 | 坏签名 / 旧 `updated` / 坏 JSON 各一例 → 旧缓存保留 | P2 单测 |
| 撤销 | 从 registry 删 release → `trust_revoked` | P2 单测 |
| ABI 不兼容 | E4 + 子进程单测（假库返回非 0） | P0 / P1 |
| 并发 | 夹具子进程 4 路并发 + 真 dylib 1024 次（原型脚本） | P1 / 手动 |
| 真实翻译 | 真 DLX 插件翻一封邮件；契约测试用假插件 | P3 |
| 崩溃隔离 | 假子进程 `os.Exit(2)` → 失败计数、下次调用懒重启、三次后停用；主进程存活 | P1 单测 |
| 业务错误不停用 | 假插件连续返回 `rate_limited` 十次 → 仍启用 | P1 单测 |
| 纯 HTML 邮件 | `body_text` 为空的邮件走插件翻译得到非空请求 | P3 单测 |
| 平台打包 | macOS 签名矩阵已测（E2）；公证与 `.app` 内实测在第一次带 entitlement 的发布；Windows / Linux 走 E3 工作流 | P0 / P1 |
| 无 cgo | CI 现有 `CGO_ENABLED=0` 的 build / test 不改 | 持续 |

## 9. 尚未验证的问题

P0 已回答：无 cgo 宿主在 macOS 上加载 Go c-shared 库可行（E1）；`disable-library-validation` 必要且充分，隔离标记会拒绝加载，复制成新文件可以消除（E2）；ABI 不兼容能干净失败（E4）。

仍需实验：

1. Windows mingw 构建的 Go c-shared DLL 被无 cgo 宿主 `LoadLibrary` 是否正常；Linux 上 purego 的 fakecgo 加载 Go c-shared 库是否正常；Linux 最低 glibc（E3，等推送实验分支）。
2. 公证：带 `disable-library-validation` 的包能否通过公证（`release.yml` 的 macOS job 跑一次即可）。

不阻塞方向、但要记住的：

- 内存和加载时间都是单机短时测量（E1：子进程约 21–31 MiB，新文件首次加载约 370 ms），不是稳态；子进程常驻意味着这部分内存长期存在，P4 的空闲退出是对策。
- DLX 依赖非公开的 DeepL 接口，随时可能失效或限流；这是插件自己的错误码 `upstream` / `rate_limited`，核心只负责展示。
- 两个 Go 运行时共处的长期稳定性（信号处理、GC 线程）没有做过数小时的运行。
- 8 GB 内存机器未测。

## 10. 与 #64（本地决策模型 Laya）的关系

#64 需要：把 ONNX Runtime 动态库打进应用并签名（T3）、下载约 575 MB 模型（T4）、子进程跑推理（§4.3 的内存结论）、实现 `ai.Judge`（T2）。本机制能承接其中一部分：

- **能**：一个社区插件「laya」声明 `capabilities: ["judge"]`，自带 ORT 和 Go/Rust 胶水，通过同一个 C ABI 暴露 `judge` 方法（请求 / 响应就是 `ai.Question` / `ai.Answer` 的 JSON）。它天然跑在子进程里，正好是 #64 自己得出的「推理放子进程、空闲退出」结论；ORT 的 dylib 由插件库在子进程里加载，同一个 entitlement 覆盖；T3 的「随应用打包签名」整个消失，应用核心不再背 ORT。
- **要加的**：（a）`judge` 能力与 `ai.Judge` 的插件实现（`resolveJudge` 多一个来源，和本文 `Translator` 同样的「设置里选来源」模式）；（b）大资产下载：模型不适合作为「产物」走 64 MiB 的安装路径，需要 `registry.json` 的 release 多一个 `assets` 列表（url / sha256 / size），应用按同样的校验下载到插件目录，`configure` 时把路径告诉插件。这两项都是本文机制的直接延伸，不改 ABI。
- **不能替代的**：#64 的 go/no-go 是模型在邮件 yes/no 题上准不准（T0 评测），和加载机制无关；ORT 1.27.1 没有 osx-x86_64 产物，插件在该平台就是「不支持」。

结论：先按本文做翻译；`judge` 能力等 #64 的 T0 评测有结果再加，到时主要工作是 (a)(b) 两项，不用重做信任和加载。
