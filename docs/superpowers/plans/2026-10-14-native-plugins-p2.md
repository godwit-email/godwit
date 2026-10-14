# 原生插件 P2：签名的插件目录 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 应用读取一个 ed25519 签名的 `registry.json`（原生插件与脚本插件都在里面），在扩展页列出当前平台能装的插件；一键安装 / 升级会下载产物、校验 `sha256` 与大小（不符不可绕过）、以 `trust = 'listed'` 安装（原生插件直接授权并启用，脚本插件仍走权限卡片）；名单撤销的文件自动停用；插件仓库有校验、签名、密钥生成工具和发布工作流模板。

**Architecture:** `core/plugins/registry.go` 只管格式：解析与严格校验、版本比较、为本机挑选 release。`core/registry.go` 管来源：下载、验签、防回滚、缓存、撤销与升级信任、从目录安装、24 小时后台刷新。信任参数只走内部的 `installPlugin(ctx, dir, trust)`；公开的 `InstallPlugin` 不变，Local API 拿不到 `listed`。工具 `cmd/godwit-registry` 只依赖 `core/plugins`，插件仓库的 CI 用 `go run` 调它。

**Tech Stack:** Go 标准库 `crypto/ed25519`、`net/http`；不加依赖。

**Spec:** `docs/superpowers/specs/2026-10-14-native-plugins-design.md`（§2.4、2.5、2.6、3.1、3.5、3.7、3.8、4.2、5 的 `core/pluginstore.go` 行与插件仓库行、6 P2）。P1 计划 `docs/superpowers/plans/2026-10-14-native-plugins-p1.md` 已完成，本计划在它之上（分支 `claude/native-plugins-p2` 叠在 P1 分支上）。

## Global Constraints

- `CGO_ENABLED=0` 下 `go build ./...`、`go test ./...` 通过；`go vet`、`gofmt` 干净；不加依赖。
- 平台键：`darwin-arm64`、`darwin-amd64`、`windows-amd64`、`windows-arm64`、`linux-amd64`、`linux-arm64`（与 `site/src/download.ts` 一致）；本机键 = `runtime.GOOS + "-" + runtime.GOARCH`。脚本插件的产物键只有 `any`。
- 签名：ed25519，对 `registry.json` 原始字节；`registry.json.sig` 是签名的 base64（标准编码，可带结尾换行）。
- 生产来源：`https://dl.godwit.email/plugins/registry.json` 与同目录 `.sig`。公钥常量在 `core/registry.go`。**公钥在用户生成密钥前为空**：此时 `Options.Registry` 为 nil，目录功能关闭，界面不显示目录区块（见「需要用户做的事」）。
- 下载：只允许 `https`；`registry.json` 最大 4 MiB、`.sig` 最大 1 KiB；产物必须恰好 `size` 字节且 `sha256` 相符，`size` 不超过该类插件的单文件上限（原生 64 MiB、脚本 5 MiB）；单个请求 5 分钟超时；不跟随到非 https 的重定向。
- 防回滚：新名单的 `updated` 早于缓存的就拒绝；任何失败都保留旧缓存、不改变已装插件。
- 信任：`trust` 取 `''` / `user` / `listed`。下载完整性失败没有任何绕过入口。
- 用户可见文案中英两份；core 的错误用哨兵映射到 `app/errors.go` 的文案表。

## Review Focus

1. **伪造或篡改的名单**：签名不对、签名对但 JSON 被截断、旧名单重放，都不能改变列表或已装插件。→ Task 2 `TestRegistryRejectsBadSource`。
2. **名单与下载文件不一致**：大小对 sha 不对、sha 对大小多一个字节、服务器返回 404，安装失败且不留下任何插件行或目录。→ Task 3 `TestInstallFromRegistryMismatch`。
3. **Local API 绕过审核**：通过 `godwit call` 能否把任意目录装成 `listed`。→ Task 3 `TestPublicInstallIsNeverListed`。
4. **撤销后插件还在跑**：撤销必须停掉运行中的子进程，重启后也不再启动。→ Task 2 `TestRegistryRevokes`。
5. **用户手动停用的插件被升级偷偷打开**。→ Task 3 `TestRegistryUpdateKeepsUserDisabled`。

---

## File Structure

```
core/plugins/registry.go        Registry 类型、ParseRegistry、CompareVersions、PlatformKey、(*RegistryPlugin).Pick
core/registry.go                RegistrySource、Refresh/List/InstallFromRegistry、撤销与升级信任、后台刷新
core/extensions.go              installPlugin(ctx, dir, trust)；脚本插件也记 library_hash（main 文件的 sha）
core/service.go                 Options.Registry、Options.AppVersion；启动后台刷新
core/coretest/coretest.go       WithRegistry、RegistryFixture（签好名的假目录 + https 测试服务器）
cmd/godwit/gui.go、headless.go   传 Registry（公钥非空时）与 AppVersion
cmd/godwit-registry/main.go     check / sign / keygen
docs/registry/                  插件仓库模板：README（审核清单）、registry.json 示例、publish.yml
app/settings_extensions.go      插件目录区块、安装确认、信任行、撤销提示
app/errors.go                   ErrRegistryMismatch 等的文案
internal/i18n/zh.go、en.go       文案
```

---

### Task 1: 名单格式

**Files:** Create `core/plugins/registry.go`；Test `core/plugins/registry_test.go`

**Interfaces — Produces:**
- `type Registry struct { Schema int; Updated time.Time; Plugins []RegistryPlugin }`（JSON：`schema`、`updated`、`plugins`）
- `type RegistryPlugin struct { Name string; Title, Summary Text; Homepage string; Releases []RegistryRelease }`
- `type RegistryRelease struct { Version, MinGodwit string; Manifest json.RawMessage; Artifacts map[string]Artifact; Parsed Manifest \`json:"-"\` }`（JSON：`version`、`minGodwit`、`manifest`、`artifacts`）
- `type Artifact struct { URL, SHA256 string; Size int64 }`（JSON：`url`、`sha256`、`size`）
- `func ParseRegistry(data []byte) (Registry, error)`：未知字段报错；`schema == 1`；插件名合规且不重复；每个 release：`version` 非空不重复，`manifest` 过 `ParseManifest(raw, name)` 且 `name`/`version` 与外层一致，结果存进 `Parsed`；原生：产物键都是已知平台键、至少一个、没有 `any`；脚本：产物只有 `any`，`Parsed.Main` 是不含 `/` 的 `.js` 文件名；产物 `url` 以 `https://` 开头、`sha256` 是 64 位小写十六进制、`0 < size ≤` 该类单文件上限；`homepage` 为空或 `https://`；`minGodwit` 为空或能被 `CompareVersions` 比较。
- `func CompareVersions(a, b string) int`：可带前缀 `v`；按点分数字比较，缺的段当 0；`-` 后的预发布段：有的小于没有的，都有时按字符串比较；不能解析的数字段当 0。
- `func PlatformKey() string`
- `func (p RegistryPlugin) Pick(platform, appVersion string) (rel RegistryRelease, art Artifact, reason string)`：在 `native.abi == NativeABI`（脚本插件不看）、有该平台（脚本看 `any`）产物、`appVersion` 为 `""` 或 `"dev"` 或 `≥ minGodwit` 的 release 里取版本最高的；都不行时 `reason` 是 `"platform"`、`"abi"` 或 `"godwit:<minGodwit>"`（按最高版本 release 不满足的第一项）。

- [ ] **Step 1: 失败的测试**
  - `TestParseRegistryValid`：一个含原生 `dlx`（两个 release、两个平台）与脚本 `deepl`（`any`）的名单通过；`Parsed.IsNative()` 正确。
  - `TestParseRegistryRejects` 表驱动：`schema:2`；重复插件名；manifest 名字与外层不同；manifest 版本与 release 不同；原生带 `any`；原生产物键 `plan9-amd64`；脚本带平台键；脚本 `main:"lib/x.js"`；`url:"http://…"`；`sha256` 63 位；`size:0`；原生 `size` 65 MiB；脚本 `size` 6 MiB；未知字段 `extra`。
  - `TestCompareVersions`：`1.2.0 < 1.10.0`、`v1.2 == 1.2.0`、`1.0.0-beta < 1.0.0`、`1.0.0-alpha < 1.0.0-beta`、`0.0.0-p0 < 0.0.1`。
  - `TestPick`：取兼容的最高版本；只有 `abi:2` 的 release 时 reason `abi`（构造时绕过 `ParseRegistry`，直接拼 `RegistryRelease`）；没有本平台时 `platform`；`minGodwit:"9.0.0"` 且 app `1.2.0` 时 `godwit:9.0.0`；app `dev` 时不看 `minGodwit`。
- [ ] **Step 2:** `CGO_ENABLED=0 go test ./core/plugins -run 'Registry|CompareVersions|Pick'` → FAIL
- [ ] **Step 3:** 实现
- [ ] **Step 4:** 同上 → PASS
- [ ] **Step 5:** Commit `feat(plugins): the plugin registry format`

---

### Task 2: 名单来源、缓存与撤销

**Files:** Create `core/registry.go`；Modify `core/service.go`、`core/coretest/coretest.go`、`cmd/godwit/gui.go`、`cmd/godwit/headless.go`；Test `core/registry_test.go`

**Interfaces:**
- Consumes: Task 1 全部。
- Produces:
  - `type RegistrySource struct { URL string; PublicKey ed25519.PublicKey; Client *http.Client }`（`Client` nil = 带 5 分钟超时的默认客户端）
  - `var DefaultRegistry *RegistrySource`：公钥常量为空时为 nil。`registryURL = "https://dl.godwit.email/plugins/registry.json"`、`registryPublicKey = ""`（base64）。
  - `Options.Registry *RegistrySource`、`Options.AppVersion string`；`gui.go`、`headless.go` 传 `Registry: core.DefaultRegistry, AppVersion: app.Version`。
  - 哨兵：`ErrRegistryOff`（未配置来源）、`ErrRegistrySignature`（验签失败）、`ErrRegistryStale`（`updated` 回退）、`ErrRegistryMismatch`（下载产物不符，Task 3 用）。
  - `func (s *Service) RefreshPluginRegistry(ctx) error`：GET `URL` 与 `URL + ".sig"` → 验签 → `ParseRegistry` → 与缓存比 `updated` → 原子写 `DataDir/plugin-registry.json` 与 `.sig`（`store.WriteFileAtomic`）→ `applyRegistry`。失败记下错误供 `ListPluginRegistry` 显示，返回错误，不改缓存。
  - `func (s *Service) ListPluginRegistry(ctx) (RegistryView, error)`：读缓存并**再次验签**（缓存文件被改也不信）。
    - `type RegistryView struct { On bool; Updated time.Time; Stale string; Entries []RegistryEntry }`：`On` 来源已配置；`Stale` 上次刷新失败的原因（成功后清空）。
    - `type RegistryEntry struct { Name string; Title, Summary plugins.Text; Homepage string; Native bool; Version, Unavailable, Installed, InstalledTrust string; Update bool }`：`Version`/`Unavailable` 来自 `Pick`；`Installed` 已装版本；`Update` = 已装且 `trust == listed` 且 `CompareVersions(Version, Installed) > 0`。
  - `applyRegistry(ctx, reg)`（持 `pluginsMu`）：
    - 撤销：`trust = 'listed'` 的行，在同名插件的任一 release 里找不到 `sha256 == library_hash` 且平台键为本机（脚本为 `any`）的产物 → `UPDATE … SET enabled = 0, disabled_reason = 'trust_revoked', granted_hash = '', trust = ''`、`stopPlugin`、发 `PluginsChanged`。
    - 升级信任：`trust = 'user'` 的行，同名、同版本的 release 里本机产物 `sha256 == library_hash` → `trust = 'listed'`。
  - 后台：`Options.Registry != nil` 时 `New` 起一个循环（同 `startLabeler` 的 `begin`/`end` 写法）：等 `registryFirst`（包级变量，默认 1 分钟），刷新，然后每 24 小时刷新；失败只记录。
  - `disabledRevoked = "trust_revoked"` 加进 `disabled_reason` 常量。
  - `coretest`：`func WithRegistry(src *core.RegistrySource) Option`；`type RegistryServer struct{ Src *core.RegistrySource; … }`，`func NewRegistryServer(t) *RegistryServer`（`httptest.NewTLSServer`，自己的 ed25519 密钥，`Src.Client = srv.Client()`）；`(*RegistryServer).Publish(t, reg map[string]any)`（序列化、签名、放到 `/registry.json` 与 `.sig`）；`(*RegistryServer).Serve(path string, body []byte)`（产物）；`(*RegistryServer).URL(path) string`；`(*RegistryServer).Tamper(fn func([]byte) []byte)`（改已发布的 JSON 而不重新签名）。

- [ ] **Step 1: 失败的测试**（`core/registry_test.go`，`core.New(… Options{DataDir, Registry: rs.Src, AppVersion: "1.2.0"})` + `SetNativeHostForTest`）
  - `TestRegistryOff`：没有来源时 `RefreshPluginRegistry` 返回 `ErrRegistryOff`，`ListPluginRegistry` 的 `On == false`。
  - `TestRegistryList`：发布 `dlx`（本机产物）与 `deepl`（脚本）→ 刷新 → 两项，`dlx.Native`，`Version == "0.1.0"`，`Unavailable == ""`；只有别的平台产物的 `other` → `Unavailable == "platform"`。
  - `TestRegistryRejectsBadSource`：先发布 `updated` = T1 并刷新成功；再分别 (a) `Tamper` 改一个字节 → `ErrRegistrySignature`；(b) 发布 `updated` = T0 < T1 → `ErrRegistryStale`；(c) `.sig` 返回 404 → 错误；每次之后 `ListPluginRegistry` 仍是 T1 的内容且 `Stale != ""`。另外：直接改写缓存文件一个字节 → `ListPluginRegistry` 返回 `ErrRegistrySignature`。
  - `TestRegistryRevokes`：用 Task 3 的 `InstallPluginFromRegistry` 装好并运行 `dlx`（假库带 `record`）→ 发布不含它的新名单并刷新 → `Enabled == false`、`DisabledReason == "trust_revoked"`、`NeedsGrant`、`Trust == ""`；关掉重开 Service → 仍未启用。（本测试在 Task 3 实现后才能通过；Task 2 先写好并标 `t.Skip("needs Task 3")`，Task 3 去掉 skip。）
  - `TestRegistryPromotesUserFile`：从文件夹装同一份 `dlx` 文件并授权（`trust == "user"`）→ 发布含该 sha 的名单并刷新 → `Trust == "listed"`。
- [ ] **Step 2:** `CGO_ENABLED=0 go test ./core -run Registry` → FAIL
- [ ] **Step 3:** 实现
- [ ] **Step 4:** 同上 → PASS（`TestRegistryRevokes` 为 SKIP）
- [ ] **Step 5:** Commit `feat(core): fetch, verify and cache the signed plugin registry`

---

### Task 3: 从目录安装与升级

**Files:** Modify `core/registry.go`、`core/extensions.go`；Test `core/registry_test.go`

**Interfaces:**
- Consumes: Task 1、2。
- Produces:
  - `func (s *Service) InstallPluginFromRegistry(ctx, name string) (PluginInfo, error)`：读已验签的缓存 → 找到插件 → `Pick` → 不可用返回 `fmt.Errorf("plugin %s: %s", name, reason)` → 下载产物到 `DataDir/plugins/.download-<random>/`（读到 `size+1` 为止；长度或 `sha256` 不符返回包了 `ErrRegistryMismatch` 的错误，目录删掉）→ 写 `plugin.json`（release 的 `manifest` 原文）与产物（原生写 `LibraryName()`，脚本写 `Parsed.Main`）→ `installPlugin(ctx, dir, "listed")`。
  - `installPlugin(ctx, srcDir, trust string) (PluginInfo, error)`：现在的 `InstallPlugin` 主体；公开的 `InstallPlugin(ctx, srcDir)` = `installPlugin(ctx, srcDir, "")`。
  - `trust == "listed"` 时 upsert 写 `trust = 'listed'`；原生插件还写 `granted_hash = excluded.permission_hash`，并且 `enabled` = 「之前没有这一行，或之前启用着，或之前是被系统停用的（`disabled_reason` 非空）」——用户手动停用的保持停用。脚本插件只写 `trust`，`enabled` 规则照旧。
  - 脚本插件安装时也计算 `library_hash` = `Parsed.Main` 文件的 sha256（撤销与升级信任按它查）。
- [ ] **Step 1: 失败的测试**
  - `TestInstallFromRegistryNative`：发布 `dlx` 并刷新 → `InstallPluginFromRegistry("dlx")` → `Native`、`Enabled`、`!NeedsGrant`、`Trust == "listed"`，record 文件出现（子进程已配置）。
  - `TestInstallFromRegistryScript`：发布脚本插件 `hello`（`main:"index.js"`，内容 `export default function () {}`，`permissions:{"net":["api.example.com"]}`）→ 安装 → `!Native`、`NeedsGrant`、`Trust == "listed"`、`!Enabled`；授权并启用后 `Enabled`。
  - `TestInstallFromRegistryMismatch` 表驱动：sha 不对 / 多一个字节 / 404 → 返回错误，`errors.Is(err, core.ErrRegistryMismatch)`（404 除外，只要求是错误）；之后 `ListPlugins` 里没有这个插件，`DataDir/plugins` 下没有它的目录也没有 `.download-*`。
  - `TestPublicInstallIsNeverListed`：同一份文件从文件夹 `InstallPlugin` → `Trust == ""`、`NeedsGrant`。
  - `TestRegistryUpdate`：装 0.1.0 → 发布加了 0.2.0 的名单并刷新 → `ListPluginRegistry` 里 `Update == true` → `InstallPluginFromRegistry` → 版本 0.2.0、`Enabled`、`Trust == "listed"`、配置保留（升级前 `SetPluginConfig` 设过 `endpoint`）。
  - `TestRegistryUpdateKeepsUserDisabled`：装 0.1.0 → 手动停用 → 升级到 0.2.0 → 仍 `!Enabled`。
  - `TestRegistryReplacesUserInstall`：从文件夹装 `dlx`（另一份文件）→ `InstallPluginFromRegistry("dlx")` → `Trust == "listed"`、`Enabled`。
  - 去掉 `TestRegistryRevokes` 的 skip。
- [ ] **Step 2:** `CGO_ENABLED=0 go test ./core -run 'Registry|InstallFromRegistry|PublicInstall'` → FAIL
- [ ] **Step 3:** 实现
- [ ] **Step 4:** `CGO_ENABLED=0 go test ./core/... ./cmd/...`、`go test -race ./core` → PASS
- [ ] **Step 5:** Commit `feat(core): install and update plugins from the registry`

---

### Task 4: 插件仓库工具与模板

**Files:** Create `cmd/godwit-registry/main.go`、`cmd/godwit-registry/main_test.go`、`docs/registry/README.md`、`docs/registry/registry.json`、`docs/registry/publish.yml`

**Interfaces:**
- Consumes: Task 1 的 `ParseRegistry`。
- `godwit-registry check [--fetch] registry.json`：`ParseRegistry`；`--fetch` 时下载每个产物核对 `size` 与 `sha256`；任何错误退出 1 并逐条打印。
- `godwit-registry sign registry.json`：私钥从环境变量 `GODWIT_REGISTRY_KEY`（ed25519 私钥 seed 的 base64，32 字节）读；先跑 `check`（不 fetch）；写 `registry.json.sig`。
- `godwit-registry keygen`：打印两行 `public: <base64>` 与 `private: <base64 seed>`。
- 函数 `run(args []string, stdout, stderr io.Writer) int`，测试直接调。
- `docs/registry/`：
  - `README.md`：仓库结构、提交一个插件的 PR 要包含什么、审核清单（读源码或确认来源、确认产物由可复现的 CI 构建、`check --fetch` 通过、原生插件的 `describe` 与 manifest 一致、DLX 这类依赖非公开接口的风险写在 `summary`）、撤销流程（删 release、合并即发布）、密钥轮换（换公钥需要发 Godwit 新版本）。
  - `registry.json`：`schema:1`、空 `plugins` 的起始文件。
  - `publish.yml`：push 到 `main` 时 `go run github.com/godwit-email/godwit/cmd/godwit-registry@main check --fetch registry.json` → `sign` → 用 `aws s3 cp --endpoint-url` 把 `registry.json` 与 `.sig` 上传到 `godwit-downloads` 桶的 `plugins/`，`--cache-control no-cache`；secrets 名与 `release.yml` 一致（`R2_ACCESS_KEY_ID`、`R2_SECRET_ACCESS_KEY`），新增 `GODWIT_REGISTRY_KEY`；PR 上只跑 `check --fetch`。
- [ ] **Step 1: 失败的测试**（`main_test.go`）
  - `TestKeygenSignVerify`：`keygen` 的私钥放进环境变量 → `sign` 一个合法名单 → `.sig` 能被 `keygen` 的公钥验过。
  - `TestCheckRejects`：不合法名单退出 1，stderr 含原因；合法名单退出 0。
  - `TestCheckFetch`：`httptest.NewTLSServer` 提供产物，名单 sha 不符时 `check --fetch` 退出 1。（工具的 HTTP 客户端可注入：包级变量 `client = http.DefaultClient`，测试换成 `srv.Client()`。）
  - `TestSignWithoutKey`：没设环境变量退出 1。
- [ ] **Step 2:** `CGO_ENABLED=0 go test ./cmd/godwit-registry` → FAIL
- [ ] **Step 3:** 实现
- [ ] **Step 4:** 同上 → PASS；`docs/registry/registry.json` 能过 `go run ./cmd/godwit-registry check docs/registry/registry.json`
- [ ] **Step 5:** Commit `feat: godwit-registry tool and the plugin repository template`

---

### Task 5: 扩展页的插件目录

**Files:** Modify `app/settings_extensions.go`、`app/settings_extensions_test.go`、`app/errors.go`、`internal/i18n/zh.go`、`internal/i18n/en.go`

**Interfaces:**
- Consumes: Task 2、3 的 `RegistryView`、`RefreshPluginRegistry`、`ListPluginRegistry`、`InstallPluginFromRegistry`、`ErrRegistryMismatch`；P1 的 `nativeCard`。
- 行为：
  - 打开扩展页时 `load()` 同时读 `ListPluginRegistry`；每次进入页面先在后台调一次 `RefreshPluginRegistry`，完成后重读（失败不弹 toast，只显示 `Stale`）。
  - 插件标签页顶部，`view.On` 为真时一个 `settingsSection`「插件目录」：每项一行，左边标题（`Title.For(lang)`，空则 `Name`）+ 「原生」/「脚本」pill + `Version`，下面一行 `Summary`；右边按状态：未装且可装 →「安装」；已装、`InstalledTrust == "listed"`、`Update` →「更新到 %s」；已装、`InstalledTrust != "listed"` 且可装 →「替换为目录版本」；已装其他情况 → 灰字「已安装」；不可装 → 灰字原因。区块副标题：`Stale` 非空时「插件目录暂时无法更新，显示的是上次的内容。」；没有条目时正文「插件目录里还没有插件。」。
  - 原生插件的「安装」「替换为目录版本」先弹现有 `confirmView`（新分支 `"registry"`，记住条目名）：标题「安装原生插件 %s %s」，正文为 spec 3.1 的风险文案，替换时多一句「将替换你自己安装的 %s。」，确认按钮「安装」。脚本插件的安装与所有「更新」不弹确认。确认后 `e.write("plugins", InstallPluginFromRegistry)`，成功后展开该插件（`e.plugin = name`）。
  - `nativeCard` 的信任行：`Trust == "listed"` →「已审核：Godwit 插件目录。」。脚本插件的权限卡片在 `Trust == "listed"` 时标题下多一行同样的文案。
  - `DisabledReason == "trust_revoked"` 时行内告警显示「Godwit 插件目录已撤销对这个文件的信任。」（其他原因保持原样显示）。
  - `app/errors.go`：`ErrRegistryMismatch` →「下载的文件与插件目录记录不符，已丢弃。」；`ErrRegistrySignature` →「插件目录的签名不对，没有使用它。」。
- [ ] **Step 1: 失败的测试**（`withAccount` + `coretest.WithRegistry`；假目录发布原生 `dlx` 与脚本 `hello`）
  - `TestRegistrySectionInstallNative`：看到「插件目录」「原生」「脚本」→ 点 `dlx` 行的「安装」→ 看到「安装原生插件 dlx 0.1.0」与风险文案 → 点确认「安装」→ 等到 `Enabled && Trust == "listed"` → 看到「已审核：Godwit 插件目录。」。
  - `TestRegistrySectionInstallScript`：点 `hello` 的「安装」→ 不弹确认，直接装好 → 看到「待授权」「它请求的权限」「已审核：Godwit 插件目录。」。
  - `TestRegistrySectionUpdate`：装好 0.1.0 后发布 0.2.0 → 重新打开页面 → 看到「更新到 0.2.0」→ 点 → 版本变成 0.2.0，没有弹确认。
  - `TestRegistryRevokedNotice`：装好后发布不含它的名单 → 重新打开页面 → 看到「Godwit 插件目录已撤销对这个文件的信任。」。
  - `TestRegistrySectionHiddenWhenOff`：没有来源时看不到「插件目录」。
- [ ] **Step 2:** `CGO_ENABLED=0 go test ./app -run Registry` → FAIL
- [ ] **Step 3:** 实现
- [ ] **Step 4:** `CGO_ENABLED=0 go test ./...`、`go vet ./...`、gofmt、`go test -race ./app ./core/...` → PASS
- [ ] **Step 5:** Commit `feat(app): the plugin directory in Extensions`

---

## 需要用户做的事（不在代码里）

1. 生成签名密钥（`go run ./cmd/godwit-registry keygen`），私钥存为插件仓库的 GitHub Secret `GODWIT_REGISTRY_KEY`，公钥填进 `core/registry.go` 的 `registryPublicKey`。在那之前目录功能关闭。
2. 创建 `godwit-email/plugins` 仓库，放入 `docs/registry/` 的三个文件（`publish.yml` 放到 `.github/workflows/`），配好 R2 secrets。
3. 决定 DLX 是否进目录。

## 不在 P2

- 翻译接入、失败计数（P3）；草稿翻译、空闲退出（P4）。
- Windows `LoadLibraryEx` 依赖 DLL（P1 审阅时推到 P2，本计划不做：DLX 是单个 DLL，目录里还没有多 DLL 插件，等有了再加，记在 spec §9）。
- 名单的多签、作者签名、评分。
