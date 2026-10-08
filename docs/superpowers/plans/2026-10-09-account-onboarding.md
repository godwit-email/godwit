# Godwit 账户接入 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在核心邮件引擎之上补齐账户接入：服务商识别、逐项连接检查、Outlook OAuth（公开客户端 + PKCE，XOAUTH2）、令牌刷新与撤销、重新登录与更新凭据、钥匙串不可用时的行为。完成后不需要 GUI，就能通过 Go 测试走完「输入邮箱 → 识别 → 检查 → 登录 → 过期 → 重新登录」的完整流程。

**Architecture:** 服务商数据与识别、OAuth 流程放在新包 `core/auth`，不依赖 `core`。`core` 里所有取凭据的地方收拢到一个函数 `(*Service).credentials`，密码账户读钥匙串，OAuth 账户经每账户一把锁的刷新逻辑取访问令牌。`conn` 增加 XOAUTH2、IMAP `ID` 和错误分类。浏览器由调用方（GUI / headless）打开，core 只给出授权地址，并在本机回环地址上等待回调。

**Tech Stack:** 计划 1 的全部依赖；新增 `golang.org/x/oauth2` v0.36.0（PKCE：`oauth2.GenerateVerifier`、`oauth2.S256ChallengeOption`、`oauth2.VerifierOption`）。XOAUTH2 的 SASL 客户端自己写（`go-sasl` 没有）。

**Spec:** `docs/superpowers/specs/2026-10-08-godwit-design.md`（重点：第 4 节 Accounts API、7.5 账户接入、7.6 账户页与「登录过期」、5.5 日志脱敏、第 10 节 Microsoft 应用注册）

**基线:** 从 `feat/core-mail-engine`（计划 1，PR godwit-email/godwit#1）拉出 `feat/account-onboarding`；计划 1 合并后改基于 `main`。

### 前置条件（不阻塞实施）

- Microsoft 应用注册由维护者在 Entra 管理中心完成：「个人 Microsoft 账户和任何组织目录」、平台「移动和桌面应用程序」、重定向 URI `http://localhost`、允许公共客户端流、API 权限 `IMAP.AccessAsUser.All`、`SMTP.Send`、`offline_access`、`openid`、`email`。得到的客户端 ID 填进 `auth.MicrosoftClientID`（公开客户端 ID 不是机密，可以提交）。为空时 Outlook 的 OAuth 返回 `auth.ErrOAuthNotConfigured`，其余功能不受影响；测试不需要它。

## Global Constraints

- 计划 1 的 Global Constraints 全部继续有效（Go 1.27.1、module `github.com/godwit-email/godwit`、不用 cgo、mygo sqlite Go API 单语句、迁移只追加、外键、只暴露 int64 ID、密码只存系统钥匙串）。
- 凭据（密码、授权码、刷新令牌、访问令牌、授权码 `code`、PKCE verifier）不进 SQLite、日志、错误信息和事件。
- **不提供明文回退**：钥匙串不可用时不把凭据写进文件或数据库。
- 钥匙串条目沿用 `service = "godwit"`、`user = <install-id>/account:<id>`。OAuth 账户在该条目里**只存刷新令牌**（Windows 凭据管理器单条上限 2560 字节）；访问令牌只放内存。
- `auth_type` 取值 `password` / `oauth`；`status` 取值 `ok` / `reauth_required` / `error`（spec 第 4 节）。
- OAuth 回调只监听 `127.0.0.1`，重定向地址写作 `http://localhost:<port>/callback`；一次授权流程最长 10 分钟。
- 网络与 DNS 都可注入（`*net.Resolver` 接口、`http.Client`、授权端点地址），测试全部在本机完成，不访问外网。

## Review Focus

1. **网易 163 / 126 / yeah.net 不发 IMAP `ID` 就拒绝 SELECT（「Unsafe Login」）**：用户填对了授权码，连接检查却在 IMAP 步骤失败。服务器声明 `ID` 能力时，登录后先发 `ID`。→ Task 2。
2. **刷新令牌轮换**：Microsoft 刷新时可能返回新的刷新令牌，新令牌必须先写进钥匙串再使用；写入失败时不能丢掉仍然有效的旧令牌。→ Task 6。
3. **并发刷新**：worker、手动 `SyncNow`、发件箱、附件下载同时需要令牌时，只发一次刷新请求。→ Task 6。
4. **用另一个账户登录**：重新登录时用户在浏览器里选了别的 Microsoft 账户，应当拒绝并保留原有令牌，而不是悄悄换成别人的邮箱。→ Task 7。
5. **钥匙串暂时不可用**（Linux 会话未解锁、macOS 钥匙串被锁）：账户状态是 `error`，不是 `reauth_required`，不要求用户重新输入密码；钥匙串恢复后自动继续同步。→ Task 4。

---

## File Structure

```
core/auth/providers.go     服务商表、Provider 类型、ListProviders、ByID
core/auth/detect.go        Detect：域名表 → MX → RFC 6186 SRV → 猜测
core/auth/oauth.go         OAuth 配置、Flow（PKCE、state、回环监听、换取令牌、id_token 邮箱）、Refresh
core/auth/errors.go        ErrOAuthNotConfigured、ErrInvalidGrant、ErrFlowExpired
core/conn/xoauth2.go       XOAUTH2 SASL 客户端
core/conn/classify.go      FailureKind 与 Classify(err)
core/conn/conn.go          Endpoint.AccessToken；登录后发 ID；XOAUTH2 分支
core/check.go              CheckAccount、CheckReport、CheckStep
core/credentials.go        credentials()、钥匙串读写包装、ErrKeychainUnavailable、KeychainStatus、令牌缓存与刷新锁
core/oauth.go              BeginOAuth、AwaitOAuth、CancelOAuth（Service 侧流程登记）
core/accounts.go           AddAccount 支持 OAuth；UpdateAccount、UpdatePassword、Reauthorize
core/types.go              Provider 相关别名、AccountInput / Account / AccountPatch 新字段
core/store/schema.go       迁移 5
core/internal/testmail/    XOAUTH2、ID 能力与记录、RequireID 选项
core/internal/testoauth/   假的授权 / 令牌端点 + 模拟浏览器
```

---

### Task 1: 服务商表与识别

**Files:**
- Create: `core/auth/providers.go`, `core/auth/detect.go`
- Modify: `core/accounts.go`（`DetectProvider`、`ListProviders`）
- Test: `core/auth/detect_test.go`

**Interfaces:**
- Produces:
  - `type auth.Server struct { Host string; Port int; Security conn.Security }`
  - `type auth.Provider struct { ID, Name string; Domains []string; IMAP, SMTP auth.Server; Login string; HelpURL string; OAuth *auth.OAuthConfig }`；`Login` 取值 `app_password`（应用专用密码）/ `authorization_code`（授权码）/ `client_password`（客户端专用密码）/ `password` / `oauth`
  - `auth.ListProviders() []Provider`（顺序：gmail、outlook、qq、netease163、netease126、exmail、icloud、fastmail；不含 `other`）；`auth.ByID(id string) (Provider, bool)`
  - `type auth.Resolver interface { LookupMX(ctx, name string) ([]*net.MX, error); LookupSRV(ctx, service, proto, name string) (string, []*net.SRV, error) }`（`*net.Resolver` 满足它）
  - `auth.Detect(ctx, r Resolver, email string) (Provider, auth.Source, error)`；`type Source string`：`domain` / `mx` / `srv` / `guess`
  - `(*core.Service).DetectProvider(ctx, email string) (auth.Provider, auth.Source, error)`（用 `net.DefaultResolver`，DNS 总超时 5 秒）；`(*core.Service).ListProviders() []auth.Provider`

服务商数据（按 spec 7.5 的列表；`HelpURL` 在 Step 3 逐个用 `curl -sI` 确认可访问，失效时换成该服务商帮助中心首页）：

| ID | 域名 | IMAP | SMTP | Login | HelpURL |
|---|---|---|---|---|---|
| gmail | gmail.com, googlemail.com | imap.gmail.com 993 TLS | smtp.gmail.com 465 TLS | app_password | https://support.google.com/accounts/answer/185833 |
| outlook | outlook.com, hotmail.com, live.com, msn.com, hotmail.co.uk, outlook.jp | outlook.office365.com 993 TLS | smtp-mail.outlook.com 587 StartTLS | oauth | — |
| qq | qq.com, foxmail.com, vip.qq.com | imap.qq.com 993 TLS | smtp.qq.com 465 TLS | authorization_code | https://service.mail.qq.com/detail/0/75 |
| netease163 | 163.com | imap.163.com 993 TLS | smtp.163.com 465 TLS | authorization_code | https://help.mail.163.com/faqDetail.do?code=d7a5dc8471cd0c0e8b4b8f4f8e49998b374173cfe9171305fa1ce630d7f67ac2 |
| netease126 | 126.com, yeah.net | imap.126.com 993 TLS | smtp.126.com 465 TLS | authorization_code | 同上 |
| exmail | （只靠 MX） | imap.exmail.qq.com 993 TLS | smtp.exmail.qq.com 465 TLS | client_password | https://open.work.weixin.qq.com/help2/pc/19886 |
| icloud | icloud.com, me.com, mac.com | imap.mail.me.com 993 TLS | smtp.mail.me.com 587 StartTLS | app_password | https://support.apple.com/102654 |
| fastmail | fastmail.com, fastmail.fm | imap.fastmail.com 993 TLS | smtp.fastmail.com 465 TLS | app_password | https://www.fastmail.help/hc/en-us/articles/360058752854 |

`yeah.net` 的服务器是 `imap.yeah.net` / `smtp.yeah.net`，用 netease126 的其余字段，在 `Detect` 里按域名替换主机。MX 后缀映射：`google.com.`、`googlemail.com.` → gmail；`qq.com.` 下以 `mxbiz` 开头的 → exmail；`messagingengine.com.` → fastmail。

识别顺序：域名表 → MX（取优先级最高的一条）→ SRV（`_imaps._tcp` 与 `_submissions._tcp`，没有时 `_submission._tcp`；`.` 目标表示不提供，忽略）→ 猜测 `imap.<domain>:993 TLS`、`smtp.<domain>:465 TLS`，`ID` 为 `other`、`Login` 为 `password`。DNS 出错或超时直接走下一步，不返回错误；只有邮箱地址不合法才返回错误。

- [ ] **Step 1: 写失败的测试**（假 `Resolver`，表驱动）

```go
func TestDetect(t *testing.T) {
	cases := []struct{ email, id string; src auth.Source; imapHost string }{
		{"a@Gmail.com", "gmail", "domain", "imap.gmail.com"},
		{"a@yeah.net", "netease126", "domain", "imap.yeah.net"},
		{"a@corp.example", "gmail", "mx", "imap.gmail.com"},        // MX aspmx.l.google.com.
		{"a@biz.example", "exmail", "mx", "imap.exmail.qq.com"},    // MX mxbiz1.qq.com.
		{"a@srv.example", "other", "srv", "mail.srv.example"},      // _imaps._tcp → mail.srv.example:993
		{"a@none.example", "other", "guess", "imap.none.example"},
		{"a@dnsfail.example", "other", "guess", "imap.dnsfail.example"}, // 假 Resolver 返回超时
	}
	// 断言 ID、Source、IMAP.Host；srv 例子还断言端口取自 SRV 记录
}
func TestDetectRejectsBadAddress(t *testing.T) { /* "not-an-email" → error */ }
func TestProvidersComplete(t *testing.T) {
	// 每个 Provider：IMAP/SMTP Host 非空、端口 > 0、Security 不是 None；Login 非 oauth 时 HelpURL 非空；
	// outlook 的 OAuth 非 nil；ListProviders 顺序与 spec 7.5 一致
}
```

- [ ] **Step 2: 运行，确认失败**：`go test ./core/auth/ -run 'Detect|Providers'` → FAIL（包不存在）
- [ ] **Step 3: 实现**，并逐个 `curl -sI <HelpURL>` 确认可访问（结果贴进报告）
- [ ] **Step 4: 运行，确认通过** → PASS
- [ ] **Step 5: Commit**：`git commit -m "feat(auth): provider table and detection by domain, MX and SRV"`

---

### Task 2: conn：XOAUTH2、IMAP ID、错误分类

**Files:**
- Create: `core/conn/xoauth2.go`, `core/conn/classify.go`
- Modify: `core/conn/conn.go`, `core/internal/testmail/imap.go`, `core/internal/testmail/smtp.go`
- Test: `core/conn/conn_test.go`, `core/conn/classify_test.go`

**Interfaces:**
- Produces:
  - `conn.Endpoint` 增加 `AccessToken string`：非空时 IMAP 用 `AUTHENTICATE XOAUTH2`、SMTP 用 `AUTH XOAUTH2`，忽略 `Password`；服务器不支持 XOAUTH2 时返回 `ErrAuthFailed`
  - `conn.NewXOAuth2Client(username, token string) sasl.Client`：初始响应 `"user=" + username + "\x01auth=Bearer " + token + "\x01\x01"`；收到服务器的错误质询时回空响应
  - IMAP 登录成功后，服务器声明 `ID` 能力就发送 `ID ("name" "Godwit" "version" <version>)`；`ID` 失败不算登录失败
  - `type conn.FailureKind string`：`dns` / `connect` / `timeout` / `tls` / `auth` / `protocol`；`conn.Classify(err error) FailureKind`（`*net.DNSError` → dns；`ECONNREFUSED`、`EHOSTUNREACH`、`ENETUNREACH` → connect；`context.DeadlineExceeded` 与超时类 `net.Error` → timeout；`tls.RecordHeaderError`、`x509.*`、`tls.AlertError` → tls；`ErrAuthFailed` → auth；其余 → protocol）
  - testmail：`testmail.WithXOAuth2(validToken string) Option`（IMAP 与 SMTP 都声明并校验 XOAUTH2）；`(*IMAP).IDs() []map[string]string`（收到的 `ID` 参数）；`testmail.RequireID() Option`（未收到 `ID` 前 SELECT 回 `NO Unsafe Login`，模拟网易）；`imapserver` 没有 `ID` 命令，在 testmail 已有的连接包装层拦截 `<tag> ID ...` 行并直接回 `* ID NIL` + `<tag> OK`，同时在能力里加 `ID`；`(*IMAP).Logins()` 同时统计 XOAUTH2 登录

- [ ] **Step 1: 写失败的测试**

```go
func TestXOAuth2IMAPAndSMTP(t *testing.T)   { /* WithXOAuth2("tok")：AccessToken "tok" 能登录两边；"bad" → ErrAuthFailed，错误里不含 "bad" */ }
func TestIMAPSendsIDWhenAdvertised(t *testing.T) { /* RequireID()：DialIMAP 后 SELECT INBOX 成功；IDs()[0]["name"] == "Godwit" */ }
func TestIDFailureIsNotLoginFailure(t *testing.T) { /* 服务器对 ID 回 BAD → DialIMAP 仍成功 */ }
func TestClassify(t *testing.T) {
	// 未注册的 .invalid 主机 → dns；127.0.0.1 上已关闭的端口 → connect；不回应的监听器 + 200ms 期限 → timeout；
	// 自签名证书的 TLS 服务器 → tls；错误密码 → auth；连到 SMTP 端口说 IMAP → protocol
}
```

- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行**：`go test -race ./core/conn/ ./core/internal/testmail/` 与全量 `go test ./core/...` → PASS
- [ ] **Step 5: Commit**：`git commit -m "feat(conn): XOAUTH2, IMAP ID and failure classification"`

---

### Task 3: 逐项连接检查

**Files:**
- Create: `core/check.go`
- Modify: `core/accounts.go`（`TestAccount` 改为基于 `CheckAccount`；`AddAccount` 失败时返回 `*CheckError`）
- Test: `core/check_test.go`

**Interfaces:**
- Consumes: `conn.Classify`、`conn.Endpoint.AccessToken`（Task 2）
- Produces:
  - `type CheckStep struct { Name string; OK bool; Skipped bool; Kind conn.FailureKind; Detail string }`；`Name` 依次为 `settings` / `imap` / `smtp`（spec 7.5 的三项）；`settings` 失败时 `Kind` 为 `invalid`（在 core 里定义 `KindInvalid conn.FailureKind = "invalid"`），`Detail` 是校验信息
  - `type CheckReport struct { Steps []CheckStep }`；`(CheckReport).OK() bool`
  - `(*Service).CheckAccount(ctx, AccountInput) CheckReport`：前一步失败时后面的步骤 `Skipped`；IMAP 与 SMTP 各自 20 秒超时
  - `type CheckError struct { Report CheckReport }`，实现 `error`；`TestAccount` / `AddAccount` 检查不通过时返回它（`errors.As` 可取）
  - `Detail` 只来自 `conn` 已经脱敏的错误文本，不含密码、令牌和服务器回显

- [ ] **Step 1: 写失败的测试**：`TestCheckAllOK`（三步都 OK）；`TestCheckIMAPAuthFails`（imap `Kind == "auth"`，smtp `Skipped`）；`TestCheckSMTPDown`（imap OK、smtp `Kind == "connect"`）；`TestCheckInvalidSettings`（端口 0 → settings `invalid`，后两步 `Skipped`，没有任何网络连接——testmail `Logins()` 为 0）；`TestAddAccountReturnsCheckError`（`errors.As(err, &ce)`，`ce.Report.Steps[1].Kind == "auth"`，库和钥匙串无残留）；`TestCheckDetailHasNoSecret`（错误密码 `"hunter2-secret"` 不出现在任何 `Detail` 里）。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行，确认通过** → PASS（计划 1 的账户测试继续通过）
- [ ] **Step 5: Commit**：`git commit -m "feat(core): step-by-step connection check"`

---

### Task 4: 钥匙串不可用与账户状态

**Files:**
- Create: `core/credentials.go`
- Modify: `core/accounts.go`、`core/service.go`（`dialAccount`）、`core/outbox.go`（`smtpEndpoint`）、`core/worker.go`、`core/store/schema.go`、`core/types.go`
- Test: `core/credentials_test.go`

**Interfaces:**
- Consumes: 计划 1 的 `keyringUser`、`dialAccount`、`smtpEndpoint`、`awaitCredentials`、`wakeWorker`
- Produces:
  - 迁移 5（每项一条语句）：`ALTER TABLE accounts ADD COLUMN provider TEXT NOT NULL DEFAULT ''`；`ALTER TABLE accounts ADD COLUMN status_detail TEXT NOT NULL DEFAULT ''`
  - `Account` 增加 `Provider, StatusDetail string`；`AccountInput` 增加 `Provider string`
  - `var ErrKeychainUnavailable = errors.New("system keychain is unavailable")`
  - `(*Service).KeychainStatus(ctx) error`：写入、读回、删除一个探测条目（`<install-id>/probe`），任一步失败返回包装了 `ErrKeychainUnavailable` 的错误；首次接入界面在显示表单前调用
  - 包内 `keychainGet / keychainSet / keychainDelete`：`keyring.ErrNotFound` 原样返回，其他错误一律包装成 `ErrKeychainUnavailable`；计划 1 里直接调用 `keyring.*` 的地方全部改用它们
  - `(*Service).credentials(ctx, acc Account, forceRefresh bool) (password, accessToken string, err error)`：`dialAccount` 与 `smtpEndpoint` 用它填 `conn.Endpoint`；本任务只处理密码账户（返回 `password`，忽略 `forceRefresh`）；Task 6 加上 OAuth
  - 状态规则：凭据读取返回 `ErrNotFound` → `status = reauth_required`、`status_detail = "credentials_missing"`；返回 `ErrKeychainUnavailable` → `status = error`、`status_detail = "keychain_unavailable"`，worker 按计划 1 的退避重试（**不停车**），下一次读取成功时清回 `ok` 并清空 `status_detail`；服务器拒绝密码时 `status_detail = "auth_rejected"`
  - `AddAccount` 在连接检查之前先调用 `KeychainStatus`，不可用时直接返回 `ErrKeychainUnavailable`，不连接服务器
  - `AddAccount` 记录 `AccountInput.Provider`（为空时存 `other`）

- [ ] **Step 1: 写失败的测试**：`TestAddAccountKeychainUnavailable`（`keyring.MockInitWithError` → `errors.Is(err, ErrKeychainUnavailable)`，testmail `Logins()` 为 0，库里没有账户）；`TestKeychainLockedAtRuntime`（添加成功后让钥匙串报错 → `SyncNow` 失败，账户 `Status == "error"`、`StatusDetail == "keychain_unavailable"`；恢复钥匙串后 worker 在退避后自动同步成功，状态回到 `ok`）；`TestCredentialsMissing`（删除钥匙串条目 → `reauth_required` / `credentials_missing`，worker 停车、`Logins()` 不再增加）；`TestKeychainStatusLeavesNoProbe`（探测条目最终不存在）；`TestMigration5`（老库升级后已有账户 `provider == ''`、`status_detail == ''`）。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行**：`go test -race ./core/...` → PASS
- [ ] **Step 5: Commit**：`git commit -m "feat(core): keychain availability, credential errors and account status detail"`

---

### Task 5: OAuth 授权流程

**Files:**
- Create: `core/auth/oauth.go`, `core/auth/errors.go`, `core/oauth.go`, `core/internal/testoauth/server.go`
- Modify: `go.mod`（`golang.org/x/oauth2 v0.36.0`）、`core/auth/providers.go`（outlook 的 `OAuth`）
- Test: `core/auth/oauth_test.go`, `core/oauth_test.go`

**Interfaces:**
- Produces:
  - `type auth.OAuthConfig struct { ClientID, AuthURL, TokenURL string; Scopes []string }`；outlook：`AuthURL` `https://login.microsoftonline.com/common/oauth2/v2.0/authorize`，`TokenURL` `https://login.microsoftonline.com/common/oauth2/v2.0/token`，`Scopes` `openid email offline_access https://outlook.office.com/IMAP.AccessAsUser.All https://outlook.office.com/SMTP.Send`，`ClientID` 取 `auth.MicrosoftClientID`（包级 `var`，默认空串）
  - `auth.ErrOAuthNotConfigured`、`auth.ErrFlowExpired`、`auth.ErrInvalidGrant`（令牌端点返回 `invalid_grant`）
  - `auth.StartFlow(cfg OAuthConfig, loginHint string, hc *http.Client) (*auth.Flow, error)`：监听 `127.0.0.1:0`，生成 PKCE verifier（S256）和 32 字节随机 `state`，授权地址带 `prompt=select_account` 与 `login_hint`
  - `(*auth.Flow).AuthURL() string`；`(*auth.Flow).Wait(ctx) (auth.Grant, error)`：只接受 `GET /callback` 且 `state` 一致的请求（不一致回 400，继续等）；`error=access_denied` 等授权错误直接返回；拿到 `code` 后换取令牌；无论成败都关闭监听；超过 10 分钟返回 `ErrFlowExpired`；`(*auth.Flow).Close()`
  - `type auth.Grant struct { Email, RefreshToken, AccessToken string; Expiry time.Time }`；`Email` 取 `id_token` 载荷里的 `email`，没有时取 `preferred_username`，转小写（`id_token` 直接来自令牌端点的 TLS 响应，不验签）
  - `auth.Refresh(ctx, cfg, hc, refreshToken) (auth.Grant, error)`（`Grant.RefreshToken` 为空表示没有轮换）
  - 回调页面是固定的一段 HTML：「登录完成，可以关闭此页面回到 Godwit。」/ 失败时「登录没有完成，请回到 Godwit 重试。」，不回显任何参数
  - `(*core.Service).BeginOAuth(ctx, providerID, loginHint string) (flowID, authURL string, err error)`；`AwaitOAuth(ctx, flowID) (email string, err error)`（成功后把 `Grant` 留在 Service 内存里，供 Task 6、7 按 `flowID` 取用，10 分钟后丢弃）；`CancelOAuth(flowID)`；`Close` 时关闭全部流程
  - `testoauth.Start(t) *testoauth.Server`：`AuthURL`、`TokenURL`、`Config(scopes...) auth.OAuthConfig`；`(*Server).Browse(t, authURL, email string)` 模拟用户在浏览器里登录（请求授权地址，跟随重定向到回环地址）；`SetEmail`、`RevokeAll()`（之后刷新返回 `invalid_grant`）、`RotateRefresh(bool)`、`TokenRequests() int`、`ValidAccess(token) bool`；签发的 `id_token` 是未签名 JWT

- [ ] **Step 1: 写失败的测试**：`TestFlowHappyPath`（`Browse` 后 `Wait` 返回邮箱和令牌；令牌请求里带了 `code_verifier`，授权请求里 `code_challenge_method=S256`）；`TestFlowWrongStateIgnored`（先用错误 `state` 请求回环地址 → 400，随后正确回调仍然成功）；`TestFlowAccessDenied`（授权服务器重定向 `error=access_denied` → 返回错误，监听已关闭——再连回环端口被拒）；`TestFlowExpires`（把超时调到 100ms → `ErrFlowExpired`，端口已关闭）；`TestFlowCancelled`（取消 ctx → 返回，端口已关闭）；`TestOutlookNotConfigured`（`MicrosoftClientID == ""` → `BeginOAuth` 返回 `ErrOAuthNotConfigured`）；`TestCallbackPageEchoesNothing`（回调响应正文不含 `code` 和 `state` 的值）。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行**：`go test -race ./core/auth/ ./core/` → PASS
- [ ] **Step 5: Commit**：`git commit -m "feat(auth): OAuth authorization code flow with PKCE and loopback redirect"`

---

### Task 6: OAuth 账户与令牌刷新

**Files:**
- Modify: `core/credentials.go`、`core/accounts.go`、`core/service.go`、`core/outbox.go`、`core/attachments.go`（凭据都经 `credentials`）
- Test: `core/oauth_account_test.go`

**Interfaces:**
- Consumes: Task 4 的 `credentials` 与状态规则；Task 5 的 `Grant`、`Refresh`、Service 内存里的流程结果
- Produces:
  - `AccountInput` 增加 `OAuthFlowID string`：非空时 `AddAccount` 走 OAuth——`Email` 必须与授权得到的邮箱相同（不区分大小写），否则返回 `ErrWrongAccount`；`auth_type = 'oauth'`；钥匙串只存刷新令牌；`Username` 取邮箱
  - `var ErrWrongAccount = errors.New("signed in with a different account")`
  - `credentials` 对 OAuth 账户返回 `AccessToken`：内存里的访问令牌剩余有效期大于 2 分钟就直接用；否则取该账户的刷新锁（独立于同步锁，可被 ctx 取消），再检查一次，然后 `auth.Refresh`；返回了新刷新令牌时**先写钥匙串**，写入失败就返回错误、内存里不缓存新令牌（旧刷新令牌仍在钥匙串里）
  - `auth.ErrInvalidGrant` → `status = reauth_required`、`status_detail = "token_revoked"`，清空内存令牌，worker 停车；网络错误按普通同步失败退避，不改状态
  - 服务器拒绝访问令牌（`conn.ErrAuthFailed`）时：清掉内存令牌，强制刷新一次再连一次；仍被拒才按计划 1 的规则进入 `reauth_required`（IMAP）或 `smtp_reauth`（SMTP）
  - `RemoveAccount` 同时清掉内存令牌（钥匙串条目已由计划 1 删除）

- [ ] **Step 1: 写失败的测试**：`TestAddOAuthAccount`（`BeginOAuth` → `Browse` → `AwaitOAuth` → `AddAccount{OAuthFlowID}`；`SyncNow` 用 XOAUTH2 收到邮件；钥匙串里的值等于刷新令牌，长度小于 2560）；`TestAddOAuthAccountEmailMismatch`（`ErrWrongAccount`，库与钥匙串无残留）；`TestRefreshIsSingleFlight`（让访问令牌过期，10 个 goroutine 同时 `SyncNow` / `GetAttachment` → `TokenRequests()` 只增加 1）；`TestRefreshRotationPersisted`（`RotateRefresh(true)` → 刷新后钥匙串里是新令牌；让钥匙串写入失败 → 返回错误，钥匙串仍是旧令牌，下一次恢复后能用旧令牌刷新）；`TestRevokedTokenNeedsReauth`（`RevokeAll` → 状态 `reauth_required` / `token_revoked`，worker 停车，之后 `TokenRequests()` 不再增加）；`TestRejectedAccessTokenRefreshesOnce`（服务器提前作废访问令牌 → 自动刷新一次后同步成功）；`TestSendWithOAuth`（OAuth 账户 `Send` → worker 经 XOAUTH2 发出）。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行**：`go test -race -count=5 ./core/ -run 'OAuth|Refresh|Revoked|Rejected'` 与全量 → PASS
- [ ] **Step 5: Commit**：`git commit -m "feat(core): OAuth accounts with single-flight refresh, rotation and revocation"`

---

### Task 7: 重新登录与更新账户

**Files:**
- Modify: `core/accounts.go`、`core/types.go`
- Test: `core/reauth_test.go`

**Interfaces:**
- Consumes: Task 3 的 `CheckAccount`；Task 4 的状态规则；Task 6 的 OAuth 凭据；计划 1 的 `wakeWorker`（计划 1 的交接要求：任何更新凭据的接口都要唤醒 worker）
- Produces:
  - `(*Service).UpdatePassword(ctx, id int64, password string) error`：只用于 `password` 账户；先用新密码对账户现有服务器做 `CheckAccount`（不通过返回 `*CheckError`，钥匙串不变）→ 写钥匙串 → 清 `status`、`status_detail`、`smtp_reauth` → `wakeWorker`
  - `(*Service).Reauthorize(ctx, id int64, flowID string) error`：只用于 `oauth` 账户；授权邮箱与账户邮箱不同返回 `ErrWrongAccount`，原有令牌不变；成功则写钥匙串、替换内存令牌、清状态、`wakeWorker`
  - `type ServerSettings struct { Username string; IMAPHost string; IMAPPort int; IMAPSecurity Security; SMTPHost string; SMTPPort int; SMTPSecurity Security }`
  - `type AccountPatch struct { Name, Alias, Signature *string; IsDefault *bool; SyncLimit *int; Servers *ServerSettings }`
  - `(*Service).UpdateAccount(ctx, id int64, p AccountPatch) (Account, error)`：`Servers` 非空时先用现有凭据做 `CheckAccount`，通过才保存并 `wakeWorker`；`IsDefault` 为 true 时其他账户取消默认，为 false 且它是唯一默认时返回错误；`SyncLimit` 必须在 1–100000；所有字段在一个 `Transaction` 里写入
  - spec 7.6「重新登录后自动发出」：重新登录或更新密码后，发件箱里因 `smtp_reauth` 暂停的邮件在下一轮发出

- [ ] **Step 1: 写失败的测试**：`TestUpdatePasswordResumes`（IMAP 改密码 → `reauth_required` 且 worker 停车 → `UpdatePassword` 新密码 → 5 秒内收到新邮件，状态 `ok`）；`TestUpdatePasswordWrong`（返回 `*CheckError`，钥匙串仍是旧值）；`TestSMTPReauthOutboxResumes`（SMTP 拒绝 → 发件箱暂停、`Send` 的邮件留在 `queued` → `UpdatePassword` → 邮件发出，`Inbox.Messages()` 恰好 1 封）；`TestReauthorizeOAuth`（`RevokeAll` → `reauth_required` → 新流程 → `Reauthorize` → 同步恢复）；`TestReauthorizeWrongAccount`（用别的邮箱完成授权 → `ErrWrongAccount`，钥匙串仍是原令牌，状态不变）；`TestUpdateAccountServersChecked`（把 IMAP 端口改成关闭的端口 → `*CheckError`，库里设置不变）；`TestUpdateAccountDefault`（设为默认后只有它是默认；取消唯一默认 → 错误）。
- [ ] **Step 2: 运行，确认失败** → FAIL
- [ ] **Step 3: 实现**
- [ ] **Step 4: 运行**：`go test -race ./...`、`CGO_ENABLED=0 go test ./...`、`CGO_ENABLED=0 go build ./...` → 全部通过
- [ ] **Step 5: Commit**：`git commit -m "feat(core): re-login, password updates and account settings"`
