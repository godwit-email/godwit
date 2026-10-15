# 原生插件 P1：核心加载链路 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 从文件夹安装一个原生插件（`plugin.json` + `plugin.dylib|so|dll`）→ 待授权 → 风险提示 → 用户授权该文件 → 启用后由子进程 `godwit plugin-host` 校验哈希、加载、`describe` 核对、`configure` 下发配置；文件变化需要重新授权；停用、卸载结束子进程。

**Architecture:** `core/plugins` 新增三块：manifest 的原生分支与大小限制、子进程端 `ServeNative`（哈希校验 → purego/LoadLibrary 加载 → stdio JSON 行）、主进程端 `NativeProcess`（起子进程、在途上限、超时 kill、退出后懒重启）。`core` 的安装 / 授权 / 启停 / 配置 / 卸载按 manifest 分流，`runningPlugin` 多一个 `native` 字段。测试里子进程是测试二进制本身，用假库（不 dlopen）；真实 dlopen 由一个用 `cc` 现编的 C 库单测覆盖。

**Tech Stack:** Go 1.27.1，`github.com/ebitengine/purego v0.11.1`（改为直接依赖），`CGO_ENABLED=0` 不变。

**Spec:** `docs/superpowers/specs/2026-10-14-native-plugins-design.md`（§2.2、2.3、2.6、3.2、3.3、3.6、4.1、4.3、4.4、4.5、5、6 P1、7）。P0 代码在分支 `experiment/native-p0-e3` 的 `experiments/native-p0/`，`host/pluginhost.go` 和 `host/client.go` 是本计划 Task 2、3 的原型。

## Global Constraints

- `CGO_ENABLED=0` 下 `go build ./...`、`go test ./...` 必须通过（CI 现状）；`go vet`、`gofmt` 干净。
- ABI v1 形状照 spec 4.4：唯一导出 `int godwit_plugin_init(uint32_t host_abi, godwit_plugin_api *api)`；函数表 `{uint32_t abi_version; call; free_buffer}`；请求上限 1 MiB，响应上限 4 MiB。
- 信封：`{"ok":true,"result":…}` / `{"ok":false,"error":{"code","message","retryable"}}`；错误码集合 `invalid_request`、`unsupported_language`、`rate_limited`、`upstream`、`internal`。
- 子进程命令：`godwit plugin-host --library <绝对路径> --sha256 <hex>`；退出码 3 = 哈希不符、未加载；4 = 加载或 init 失败；stdin 关闭后退出 0。环境只有 `PATH`（照 `startMCPProcess`）。
- 主进程端：每插件最多 4 个在途调用；单次调用默认 60 秒，超时 kill 进程组；关闭时先关 stdin、等 2 秒、再 kill。
- 大小限制：脚本插件单文件 5 MiB / 合计 20 MiB（不变）；原生插件 64 MiB / 80 MiB。
- 原生插件 `permission_hash = source_hash`；`trust` 取 `''` / `user`（P2 加 `listed`）。
- 迁移只追加（新的一个 schema 版本）；凭据不进日志 / 错误；子进程 stderr 进错误前经 `s.secrets.Redact`。
- 用户可见文案中英两份（`internal/i18n/zh.go`、`en.go`，`TestKeysMatch` 会查）。

## Review Focus

1. **授权前就加载**：未授权或文件变了时，没有任何路径会启动子进程。→ Task 4 `TestNativeNeverStartsUngranted`。
2. **子进程卡死或崩溃拖住主进程**：`describe` 不返回、子进程启动即退出（退出码 3/4），启用要在时限内报错，行被停用而不是挂住 `pluginsMu`。→ Task 3 `TestNativeStartupExit`、Task 4 `TestNativeDescribeMismatchDisables`。
3. **同名脚本插件被原生版本覆盖（或反过来）**：更新换了类型，旧授权不能沿用。→ Task 4 `TestNativeReplacesScriptNeedsGrant`。
4. **配置里的 secret 出现在错误里**：子进程 stderr 尾部回显了 secret。→ Task 4 `TestNativeErrorRedactsSecret`。
5. **Windows 上卸载删不掉库文件**：卸载必须先结束子进程再删目录。→ Task 4 `TestNativeUninstall` 断言目录消失（Windows CI 上跑同一测试）。

---

## File Structure

```
core/plugins/manifest.go          NativeSpec、Capabilities、ParseManifest 原生规则
core/plugins/install.go           Limits、ScriptLimits / NativeLimits、Snapshot/HashDir 带限制、LibraryName
core/plugins/native_host.go       ServeNative / ServeNativeWith：子进程端（哈希、加载、JSON 行）
core/plugins/native_open_unix.go  purego Dlopen / Dlsym（darwin、linux）
core/plugins/native_open_windows.go  syscall.LoadLibrary / GetProcAddress
core/plugins/native.go            NativeProcess：主进程端客户端
core/plugins/testdata/native/echo.c  真实 dlopen 测试用的 C 库
sdk/native/godwit_plugin.h        给插件作者的 ABI 头文件
cmd/godwit/run.go                 plugin-host 子命令
core/store/schema.go              新迁移：trust、library_hash
core/extensions.go                原生分支：安装、行、信息、授权、加载、停止、配置
core/actions.go、core/hooks.go     跳过没有 goja 插件的运行实例
core/coretest/coretest.go         ServeNativeIfChild、NativePluginFixture、SetNativeHostForTest 接线
app/settings_extensions.go        原生 pill、风险卡片、信任行、「仍然启用」
internal/i18n/zh.go、en.go         文案
assets/entitlements.plist、mygo.config.ts  disable-library-validation
README.md                         一行说明
```

---

### Task 1: manifest 原生分支与大小限制

**Files:**
- Modify: `core/plugins/manifest.go`, `core/plugins/install.go`
- Test: `core/plugins/manifest_test.go`, `core/plugins/install_test.go`

**Interfaces:**
- Produces:
  - `const NativeABI uint32 = 1`
  - `type NativeSpec struct { ABI uint32 \`json:"abi"\` }`
  - `Manifest` 加字段 `Native *NativeSpec \`json:"native,omitempty"\``、`Capabilities []string \`json:"capabilities,omitempty"\``（放在 `Main` 之后，JSON 输出顺序无关）。`Main` 改为 `json:"main,omitempty"`。
  - `func (m Manifest) IsNative() bool`
  - `type Limits struct{ File, Total int64 }`；`var ScriptLimits = Limits{5 << 20, 20 << 20}`；`var NativeLimits = Limits{64 << 20, 80 << 20}`；删掉 `MaxFileBytes` / `MaxTotalBytes`。
  - `func Snapshot(src, dst string, lim Limits) (string, error)`、`func HashDir(dir string, lim Limits) (string, error)`；`files`、`copyTree`、`copyFile` 跟着带 `lim`。`BuildWith` 里的 `HashDir` 用 `ScriptLimits`。
  - `func LibraryName() string`：`darwin` → `plugin.dylib`，`windows` → `plugin.dll`，其余 → `plugin.so`。

- [ ] **Step 1: 失败的测试**（`manifest_test.go`）
  - `TestParseManifestNative`：`{"name":"dlx","version":"0.1.0","native":{"abi":1},"capabilities":["translate"],"config":[{"key":"endpoint","type":"text","label":"Endpoint"}]}` 通过；`IsNative()` 为真；`Main == ""`。
  - `TestParseManifestNativeRejects` 表驱动，每项都要报错：同时有 `native` 和 `main`；`native` 带 `permissions`（任意非空字段）；`"abi":2`；`"abi":0`；缺 `capabilities`；`capabilities:["judge"]`；`capabilities` 重复。
  - `TestParseManifestScriptCapabilities`：脚本插件带 `capabilities:["translate"]` 通过，带 `["judge"]` 失败；不带也通过。
  - `install_test.go`：现有测试改用 `ScriptLimits`；新增 `TestSnapshotNativeLimits`：一个 6 MiB 文件，`Snapshot(…, ScriptLimits)` 失败，`Snapshot(…, NativeLimits)` 成功，`HashDir(dst, NativeLimits)` 等于返回的哈希。

- [ ] **Step 2: 运行确认失败**：`CGO_ENABLED=0 go test ./core/plugins -run 'Manifest|Snapshot'` → 编译失败或 FAIL。

- [ ] **Step 3: 实现**。`ParseManifest` 规则：`native != nil` 时 `main == ""`、`permissions` 为零值、`abi == NativeABI`、`capabilities` 非空；`native == nil` 时 `main` 校验照旧。两种都要求 `capabilities` 每项属于 `{"translate"}` 且不重复。

- [ ] **Step 4: 运行确认通过**：`CGO_ENABLED=0 go test ./core/plugins` → PASS（其他包此时编译失败是预期的，Task 4 修）。

- [ ] **Step 5: Commit** `feat(plugins): native manifests and per-kind size limits`

---

### Task 2: 子进程端 `plugin-host` 与 ABI 头文件

**Files:**
- Create: `core/plugins/native_host.go`, `core/plugins/native_open_unix.go`, `core/plugins/native_open_windows.go`, `core/plugins/testdata/native/echo.c`, `sdk/native/godwit_plugin.h`
- Modify: `cmd/godwit/run.go`, `go.mod`（purego 直接依赖）
- Test: `core/plugins/native_host_test.go`, `cmd/godwit/run_test.go`

**Interfaces:**
- Produces:
  - `type NativeCall func(method string, req []byte) ([]byte, error)` —— 返回插件的信封原文；error 表示 ABI 层失败。
  - `type NativeOpener func(path string, hostABI uint32) (NativeCall, error)`
  - `func ServeNative(args []string, stdin io.Reader, stdout, stderr io.Writer) int` = `ServeNativeWith(args, …, openNative)`
  - `func ServeNativeWith(args []string, stdin io.Reader, stdout, stderr io.Writer, open NativeOpener) int`
  - 未导出 `openNative(path string, hostABI uint32) (NativeCall, error)`：P0 `host/pluginhost.go` 的 `openLibrary` + `RegisterFunc` + `invoke`，带 1 MiB / 4 MiB 上限和信封校验（首字节 `{`、能解析出 `ok`）。
  - 线协议照 P0：读 `{"id","method","params"}`，每行一个 goroutine，写回把 `id` 拼进信封；ABI 层失败写 `{"id":N,"ok":false,"host_error":"…"}`。
  - flag：`--library`、`--sha256`、`--abi`（默认 `NativeABI`，测试用）。
- `cmd/godwit/run.go`：`args[0] == "plugin-host"` → `return plugins.ServeNative(args[1:], stdin, stdout, stderr)`，放在 `call`、`mcp` 分支旁边。

- [ ] **Step 1: 失败的测试**
  - `echo.c`：实现 ABI v1。`describe` 返回 `{"ok":true,"result":{"name":"echo","version":"1","abi":1,"capabilities":["translate"]}}`；其他方法返回 `{"ok":false,"error":{"code":"invalid_request","message":"unknown method"}}`；一个 `__attribute__((constructor))`，在环境变量 `ECHO_MARKER` 指向的路径写入 `loaded`。
  - `native_host_test.go`，辅助函数 `buildEcho(t) string`：`exec.LookPath("cc")` 找不到就 `t.Skip`；`cc -shared -fPIC -o <tmp>/plugin.<ext> testdata/native/echo.c`（darwin 用 `-dynamiclib`）。
  - `TestServeNativeDeniesBeforeLoad`：设好 `ECHO_MARKER`，`ServeNative([--library lib --sha256 0000], …)` 返回 3，stderr 含 `DENIED_BEFORE_DLOPEN`，marker 文件**不存在**。
  - `TestServeNativeLoadsAndServes`：正确的 sha，stdin 给两行 `{"id":1,"method":"describe","params":{}}`、`{"id":2,"method":"nope","params":{}}` 后 EOF；返回 0；输出两行，`id` 1 的 `result.name == "echo"`，`id` 2 的 `ok == false`；marker 存在。
  - `TestServeNativeABIMismatch`：`--abi 2`，返回 4，stderr 含 `incompatible ABI`。
  - `TestServeNativeHostErrorLine`：用 `ServeNativeWith` 和一个返回 error 的假 `NativeCall`，输出行含 `host_error`。
  - `cmd/godwit/run_test.go` `TestPluginHostSubcommand`：`run(ctx, []string{"plugin-host", "--library", <不存在的路径>, "--sha256", "00"}, …)` 返回 3。

- [ ] **Step 2: 运行确认失败**：`CGO_ENABLED=0 go test ./core/plugins -run ServeNative ./cmd/godwit -run PluginHost` → FAIL。

- [ ] **Step 3: 实现**。hash 用流式 `sha256` + `io.Copy`。`sdk/native/godwit_plugin.h` 写 spec 4.4 的头文件原文（含注释里的内存责任、并发、无取消、无 shutdown 说明）；`echo.c` include 它。

- [ ] **Step 4: 运行确认通过**：同上命令 → PASS；`GOOS=windows CGO_ENABLED=0 go build ./...`、`GOOS=linux …` 通过。

- [ ] **Step 5: Commit** `feat(plugins): plugin-host subprocess loads native libraries without cgo`

---

### Task 3: 主进程端 `NativeProcess`

**Files:**
- Create: `core/plugins/native.go`, `core/plugins/native_proc_unix.go`, `core/plugins/native_proc_windows.go`, `core/plugins/native_fake.go`
- Test: `core/plugins/native_test.go`（含本包的 `TestMain` 子进程分支；若包里已有 `TestMain` 就加进去）

**Interfaces:**
- Consumes: Task 2 的 `ServeNativeWith`、`NativeCall`。
- Produces:
  - `type NativeOptions struct { Command []string; Env []string; Library, SHA256 string; Timeout time.Duration; Redact func(string) string }` —— `Command` 是参数前缀（生产是 `[os.Executable(), "plugin-host"]`），后面拼 `--library L --sha256 H`；`Timeout` 0 = 60 秒；`Redact` nil = 原样。
  - `func NewNativeProcess(o NativeOptions) *NativeProcess`（不立即启动）
  - `func (n *NativeProcess) Call(ctx context.Context, method string, params any) (json.RawMessage, error)` —— 成功返回 `result`。
  - `type NativeError struct{ Code, Message string; Retryable bool }`，实现 `error`；插件返回 `ok:false` 时返回它。
  - `var ErrNativeExited = errors.New("plugin: native plugin exited")`；ABI 层失败返回 `fmt.Errorf("plugin: native call failed: %s", host_error)` 包 `ErrNativeABI`（新的哨兵）；超时返回包了现有 `ErrTimeout` 的错误。退出错误带子进程 stderr 尾部（最多 4 KB，经 `Redact`）。
  - `func IsNativeFailure(err error) bool`：`ErrNativeExited`、`ErrNativeABI`、`ErrTimeout` 之一（P3 用它决定是否计入 `pluginFailed`）。
  - `func (n *NativeProcess) Close()`：关 stdin、等 2 秒、kill；之后 `Call` 返回 `ErrClosed`。
  - 在途上限 4（信号量）；子进程死了下一次 `Call` 重新启动。
  - `native_proc_*.go`：`setNativeGroup(*exec.Cmd)`、`killNativeGroup(*exec.Cmd)`（unix 用 `Setpgid` 与 `Kill(-pid)`，windows `Process.Kill`）。
  - `func FakeNativeOpener(stderr io.Writer) NativeOpener`（`native_fake.go`，测试与 `coretest` 共用，不 dlopen）：把 `--library` 文件当作 JSON `{"describe":{…},"initFail":bool,"echoStderr":"…","record":"<路径>"}` 读入。`initFail` 为真时先把 `echoStderr` 写到 stderr，再返回 init 失败（子进程退出码 4）。方法：`describe` → 那段 JSON；`configure` → `{}`，有 `record` 时把请求原文写到那个路径；`translate` → 原样回 `texts`、`source:"en"`；`fail` → `ok:false` 的 `rate_limited`；`probe.sleep {ms}`；`probe.crash` → 先写 `echoStderr` 到 stderr 再 `os.Exit(2)`。
- 测试子进程：`TestMain` 里 `if len(os.Args) > 1 && os.Args[1] == "plugin-host" && os.Getenv("GODWIT_PLUGINS_FAKE_NATIVE") != "" { os.Exit(ServeNativeWith(os.Args[2:], os.Stdin, os.Stdout, os.Stderr, FakeNativeOpener(os.Stderr))) }`。

- [ ] **Step 1: 失败的测试**（辅助：`fakeLib(t, json) (path, sha)`；`newFake(t, path, sha)` 用 `Command: []string{os.Args[0], "plugin-host"}`、`Env: []string{"PATH=…", "GODWIT_PLUGINS_FAKE_NATIVE=1"}`）
  - `TestNativeCall`：`describe` 返回的 JSON 等于文件里的；`translate` 回显。
  - `TestNativeBusinessError`：`fail` 返回 `*NativeError{Code:"rate_limited"}`，`IsNativeFailure` 为假。
  - `TestNativeTimeoutRestarts`：`Timeout: 500ms`，`probe.sleep {ms:5000}` 在 2 秒内返回、`errors.Is(err, ErrTimeout)`、`IsNativeFailure` 为真；再调 `describe` 成功。
  - `TestNativeCrashRestarts`：`echoStderr: "boom-secret"`、`Redact` 把 `secret` 换成 `***`；`probe.crash` 返回 `ErrNativeExited`，错误文本含 `boom-***`、不含 `boom-secret`；再调 `describe` 成功。
  - `TestNativeStartupExit`：sha 写错，`describe` 在 5 秒内返回 `ErrNativeExited`，文本含 `DENIED_BEFORE_DLOPEN`。
  - `TestNativeInFlightLimit`：并发 8 个 `probe.sleep {ms:300}`，总耗时 ≥ 600 ms（4 个一批）。
  - `TestNativeClose`：`Close()` 后 `Call` 返回 `ErrClosed`。

- [ ] **Step 2: 运行确认失败**：`CGO_ENABLED=0 go test ./core/plugins -run Native` → FAIL。

- [ ] **Step 3: 实现**：照 P0 `host/client.go`，加 `Redact`、`ErrClosed`、错误哨兵。

- [ ] **Step 4: 运行确认通过**：`CGO_ENABLED=0 go test ./core/plugins` 与 `go test -race ./core/plugins` → PASS。

- [ ] **Step 5: Commit** `feat(plugins): NativeProcess runs one plugin-host per native plugin`

---

### Task 4: core 接入

**Files:**
- Modify: `core/store/schema.go`, `core/extensions.go`, `core/actions.go`, `core/hooks.go`, `core/service.go`（字段与默认命令）, `core/coretest/coretest.go`, `core/mcp_test.go`（`TestMain` 加子进程分支）
- Test: `core/native_test.go`（新，`package core_test`）

**Interfaces:**
- Consumes: Task 1 的 `IsNative`、`Limits`、`LibraryName`；Task 3 的 `NativeProcess`、`NativeOptions`。
- Produces:
  - 迁移（`Migrations` 末尾新元素）：`ALTER TABLE plugins ADD COLUMN trust TEXT NOT NULL DEFAULT ''`、`ALTER TABLE plugins ADD COLUMN library_hash TEXT NOT NULL DEFAULT ''`。
  - `PluginInfo` 加 `Native bool`、`Trust string`、`Capabilities []string`、`LibraryHash string`。
  - `pluginRow` 加 `trust, libraryHash string`；`pluginRows` 一并读出。
  - `runningPlugin` 加 `native *plugins.NativeProcess`；脚本插件 `native == nil`，原生插件 `p == nil`、`host == nil`。
  - `Service` 字段 `nativeCmd []string`（`New` 里设为 `[os.Executable(), "plugin-host"]`）、`nativeEnv []string`（默认只有 `PATH`）；`func (s *Service) SetNativeHostForTest(env ...string)` 把 env 追加到 `nativeEnv`（同 `SetPluginNetForTest` 的约定：`New` 之后、任何插件启动之前调用）。
  - `coretest`：`const nativeChildEnv = "GODWIT_CORETEST_NATIVE"`；`func ServeNativeIfChild()`（环境变量 `nativeChildEnv` 存在且 `os.Args[1] == "plugin-host"` 时用 `plugins.FakeNativeOpener` 服务并退出）；`func NativePluginFixture(t *testing.T, name, version string, describe, extra map[string]any) string` 写出 `plugin.json`（原生 manifest，`capabilities:["translate"]`，一个 `text` 配置项 `endpoint` 和一个 `secret` 配置项 `token`）和 `plugin.<ext>`（假库 JSON；`describe` 为 nil 时用与 manifest 一致的值）；`New` 里调 `svc.SetNativeHostForTest(nativeChildEnv + "=1")`。`describe` 之外的假库字段（`record`、`initFail`、`echoStderr`）由测试直接改写 `plugin.<ext>` 的 JSON 设置，`NativePluginFixture` 加一个参数 `extra map[string]any` 合并进去。
- 行为：
  - `InstallPlugin`：`Snapshot(…, NativeLimits)` → 读 manifest → 原生：`library_hash` = `LibraryName()` 文件的 sha256（文件必须存在），`permission_hash` = 快照哈希，跳过 `Build`；脚本：`HashDir(snap, ScriptLimits)` 复核大小，然后照旧 `Build`。upsert 写 `library_hash`，并且 `trust = CASE WHEN permission_hash = excluded.permission_hash THEN trust ELSE '' END`。
  - `checkPluginFiles`：限制按 `r.m.IsNative()` 选。
  - `GrantPermission`：`SET granted_hash = permission_hash, trust = CASE WHEN trust = 'listed' THEN trust ELSE 'user' END`。
  - `loadPlugin`：`loadPluginSecrets` 之后，原生走 `loadNative`：`NewNativeProcess`（`Library` 用 `filepath.Abs(filepath.Join(versionDir, LibraryName()))`，`SHA256: r.libraryHash`，`Redact: s.secrets.Redact`）→ `describe`（30 秒）→ 核对 `name`、`version`、`abi`、`capabilities`（排序后相等），不符返回 `fmt.Errorf("plugin %s: the library describes itself as %s %s", …)` → `configure {"config": pluginConfigLocked 的值}` → 失败都 `Close` 后返回错误；成功后与脚本插件一样登记进 `s.running`（不调 `registerPluginTools`、`startHooks`）。
  - `stopPlugin`：`rp.native != nil` 时 `rp.native.Close()`，否则照旧。
  - `SetPluginConfig` 成功后：`rp.native != nil` 时用新配置再调一次 `configure`，错误返回 `fmt.Errorf("plugin %s did not accept the saved settings: %w", …)`。
  - `actions.go` 的 `runningPlugins`、`hooks.go` 的 beforeSend 循环：跳过 `rp.p == nil`。
  - `ListPlugins` 的 `ponytail:` 注释改成 `shortcut:` 格式并写明原生库最多 64 MiB。

- [ ] **Step 1: 失败的测试**（`core/native_test.go`，用 `coretest.New` 或 `openService` + `SetNativeHostForTest`；两种都要能用）
  - `TestNativeNeverStartsUngranted`：安装 → `Native`、`NeedsGrant`、`Trust == ""`、`!Enabled`；`SetPluginEnabled(true)` 返回 `ErrNeedsGrant`；假库带 `record` 路径，断言 record 文件**不存在**（子进程从没启动）。
  - `TestNativeGrantEnable`：授权 → `Trust == "user"`；启用 → `Enabled`、`DisabledReason == ""`；record 文件内容的 `config.endpoint` 等于 manifest 默认值、`config.token` 不存在。
  - `TestNativeConfigReachesPlugin`：启用后 `SetPluginConfig({"endpoint":"https://x.example","token":"tok-12345678"})` → record 里两项都在。
  - `TestNativeFileChangeNeedsRegrant`：启用后往版本目录里的库文件追加一个字节 → `ListPlugins` 的 `Error` 非空；关掉 Service 用同一数据目录重开 → `DisabledReason == "files_changed"`。另外：源目录库文件改一个字节后重新 `InstallPlugin` → `NeedsGrant`、`Trust == ""`、`!Enabled`。
  - `TestNativeSameFileKeepsGrant`：同一源目录重新安装 → 仍然 `Enabled`、`Trust == "user"`、不需要授权。
  - `TestNativeReplacesScriptNeedsGrant`：先装并启用同名脚本插件（`pluginSrc`），再装同名原生插件 → `NeedsGrant`、`!Enabled`。
  - `TestNativeDescribeMismatchDisables`：假库 `describe.version` 与 manifest 不同；授权后启用返回错误、文本含 `describes itself`；`Enabled` 为假。
  - `TestNativeErrorRedactsSecret`：假库 `initFail: true` 且 `echoStderr` 含配置里的 secret 值；先存配置再启用；错误文本不含该 secret。（`initFail` 让假 opener 先写 stderr 再返回 init 失败，子进程退出码 4。）
  - `TestNativeUninstall`：启用后卸载 → `DataDir/plugins/<name>` 不存在、行消失。
  - `TestNativeTooLarge`：原生插件里放一个 65 MiB 的文件 → 安装失败；脚本插件放 6 MiB → 安装失败（原有限制不变）。
  - 现有 `core` 测试全部照旧通过。

- [ ] **Step 2: 运行确认失败**：`CGO_ENABLED=0 go test ./core -run Native` → FAIL。

- [ ] **Step 3: 实现**（按上面「行为」逐条）。

- [ ] **Step 4: 运行确认通过**：`CGO_ENABLED=0 go test ./core/... ./cmd/...`、`go test -race ./core` → PASS。

- [ ] **Step 5: Commit** `feat(core): install, grant, run and uninstall native plugins`

---

### Task 5: 扩展页、文案、签名与说明

**Files:**
- Modify: `app/settings_extensions.go`, `app/settings_extensions_test.go`（`TestMain` 加 `coretest.ServeNativeIfChild()`）, `internal/i18n/zh.go`, `internal/i18n/en.go`, `mygo.config.ts`, `README.md`
- Create: `assets/entitlements.plist`

**Interfaces:**
- Consumes: Task 4 的 `PluginInfo.Native/Trust/LibraryHash`、`coretest.NativePluginFixture`。
- 界面（复用现有组件，不新增组件）：
  - `pluginRow`：`p.Native` 时名字旁 `statusPill(c, i18n.T("settings.ext.native"), false)`。
  - `pluginBody`：`p.Native` 时用风险卡片替换权限卡片（同样的 `Sidebar` 底色容器），不显示「不包含」行。卡片三行：标题 `settings.ext.native.risk.title`；正文 `settings.ext.native.risk.body`；信任行：`Trust == "user"` → `settings.ext.native.trust.user`，否则 `settings.ext.native.trust.unlisted`；文件行 `Tf("settings.ext.native.file", plugins.LibraryName(), short(p.LibraryHash))`，`short` = 前 8 位 + `…` + 后 4 位，等宽字体。
  - 待授权的原生插件：按钮文案 `settings.ext.native.allow`，`kit.Destructive`，点击仍走 `e.allow`。
- 文案（中 / 英）：
  - `settings.ext.native`：「原生」/ "Native"
  - `settings.ext.native.risk.title`：「以 Godwit 的全部权限运行」/ "Runs with all of Godwit's permissions"
  - `settings.ext.native.risk.body`：「原生插件能读取你的邮件、账户凭据和本机文件，访问任何网络地址，运行程序，也可能让 Godwit 崩溃。Godwit 无法限制它。」/ "A native plugin can read your mail, account credentials and files on this computer, reach any network address, run programs, and can crash Godwit. Godwit cannot restrict it."
  - `settings.ext.native.trust.unlisted`：「这个文件不在 Godwit 审核过的插件目录里。只有在你信任它的来源时才启用。」/ "This file is not in Godwit's reviewed plugin directory. Enable it only if you trust where it came from."
  - `settings.ext.native.trust.user`：「你已授权这个文件。文件变化后需要重新授权。」/ "You allowed this file. It needs your permission again if it changes."
  - `settings.ext.native.file`：「文件：%s · SHA-256 %s」/ "File: %s · SHA-256 %s"
  - `settings.ext.native.allow`：「仍然启用」/ "Enable anyway"
  - `settings.ext.sub` 改为：「Skill 写 prompt 就能用；脚本插件运行在沙箱里，只能用你批准的权限，原生插件拥有 Godwit 的全部权限；MCP 给 Wit 接入外部工具。」/ "Skills are just prompts; script plugins run in a sandbox with only the permissions you approve, native plugins have all of Godwit's; MCP gives Wit outside tools."
- `assets/entitlements.plist`：只有 `com.apple.security.cs.disable-library-validation` = true；`mygo.config.ts` 的 `macos` 加 `entitlements: "assets/entitlements.plist"`，旁边一行注释写原因（原生插件由其他开发者签名，见 spec 7.1）。
- `README.md` Extensions 列表加一条：原生插件（动态库）以 Godwit 的全部权限运行，安装时会提示风险，按文件授权。

- [ ] **Step 1: 失败的测试**（`app/settings_extensions_test.go`）
  - `TestNativePluginRiskAndGrant`：`h.plat.dir = coretest.NativePluginFixture(t, "dlx", "0.1.0", nil, nil)` → 打开扩展页 → 点「从文件夹安装」→ 等到装好 → 看到「原生」「待授权」「以 Godwit 的全部权限运行」「这个文件不在 Godwit 审核过的插件目录里」「SHA-256」；看不到「不包含」「允许并启用」→ 点「仍然启用」→ 等到 `Enabled && !NeedsGrant` → 看到「你已授权这个文件」。
  - `TestScriptPluginCardUnchanged`：现有 `config-demo` 夹具的待授权页仍有「它请求的权限」「不包含」「允许并启用」，没有「原生」。
- [ ] **Step 2: 运行确认失败**：`CGO_ENABLED=0 go test ./app -run 'Native|ScriptPluginCard'` → FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行确认通过**：`CGO_ENABLED=0 go test ./app ./internal/i18n`、`CGO_ENABLED=0 go test ./...`、`go vet ./...`、`gofmt -l` 为空 → PASS。
- [ ] **Step 5: Commit** `feat(app): native plugin risk card and per-file consent`

---

## 不在 P1

- registry、`trust = 'listed'`、`InstallPlugin` 的 trust 参数（P2）。
- 翻译调用、`pluginFailed` 的失败计数与三次停用（P3：P1 里只有 `describe` / `configure`，没有运行时调用可计数；`IsNativeFailure` 先备好）。
- `ListPlugins` 的哈希缓存：64 MiB 一次 SHA-256 在几十毫秒级，先用 `shortcut:` 注释记下，慢了再加。spec §5 的这一条推迟。
- 子进程空闲退出（P4）。
