# 订阅型模型提供商（ChatGPT、Grok）实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用户在「模型服务」里选 ChatGPT 或 Grok 磁贴，浏览器登录一次，之后用订阅额度生成。

**Architecture:** 复用现有 `core/auth.Flow`（PKCE 回环）给 ChatGPT，新增同形的 `auth.DeviceFlow` 给 Grok；两者都通过现有 `core.BeginOAuth/AwaitOAuth` 暴露。模型服务的钥匙串 key 槽位改存 refresh token，解析模型时换成内存缓存的 access token 放进 `ResolvedModel.APIKey`，引擎和 Responses 插件不改协议。

**Tech Stack:** Go 1.2x、`golang.org/x/oauth2`（仅授权码交换）、Genkit + openai-go（现状）、mygo UI、testify；测试用 `core/internal/testoauth`、`core/internal/testllm`。

**Spec:** `docs/superpowers/specs/2026-10-14-subscription-model-providers-design.md`

## Global Constraints

- 错误信息永不包含 token、key、响应正文或 Location（见 `core/auth/oauth.go` 的 `tokenError`、`core/ai/listmodels.go`）。
- ChatGPT 常量：授权 `https://auth.openai.com/api/accounts/authorize`，token `https://auth.openai.com/api/accounts/oauth/token`，首次 `client_id=dynamic_agent_client`，scope `openid profile email offline_access resource.invoke chatgpt.tokens.use.direct`，`resource=https://api.openai.com/v1`，`agent_name_hint=Godwit`，回调 `http://127.0.0.1:{port}/auth/callback`，BaseURL `https://api.openai.com/v1`。
- Grok 常量：`client_id=b1a00492-073a-47ea-816f-4c329264a828`，设备码 `https://auth.x.ai/oauth2/device/code`，token `https://auth.x.ai/oauth2/token`，scope `openid profile email offline_access grok-cli:access api:access`，`referrer=godwit`，BaseURL `https://api.x.ai/v1`，回退模型 `grok-4.7`、`grok-4.6`。
- 两个 provider 只有生成格式 `openai-responses`，judge 为空；地址固定。
- 文案：登录按钮英文原文 **Continue with ChatGPT**（中英文都用）；用量链接 `https://chatgpt.com/settings/usage`。i18n 的 zh/en 键必须成对（`internal/i18n/i18n_test.go` 的 `TestKeysMatch`）。
- 所有用户可见文案走 `i18n.T`；UI 间距/颜色只用 theme tokens。
- 每个任务结束 `go test ./core/... ./app/...` 相关包通过后再提交；提交信息结尾带 `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`。

## Review Focus

1. 用户在 OpenAI 页面拒绝授权（回调 `error=access_denied`）：表单显示「登录被拒绝」，按钮可再点，没有半保存的服务。→ Task 7 测试 `TestOAuthTileLoginDenied`。
2. 用户登录了但没授予 plan 使用权（token 响应 `scope` 缺 `chatgpt.tokens.use.direct`）：登录失败并说明，不写钥匙串。→ Task 1 测试 `TestFlowRequiresScope`。
3. Grok 用户一直不点确认：设备码到期后 `Wait` 返回 `ErrFlowExpired`，UI 显示超时并允许重试。→ Task 3 测试 `TestDeviceFlowExpires`。
4. 登录进行中用户关掉对话框：`Cancel(flowID)` 被调用，`Await` 的取消不弹错误 toast。→ Task 7 测试 `TestOAuthTileCloseCancels`。
5. ChatGPT 的 `/v1/models` 条目可能既有 `id` 又有 `slug`，且含 `visibility != "list"` 的隐藏模型：只用 `id`，隐藏的不展示。→ Task 6 测试 `TestListModelsVisibility`。

---

### Task 1: `auth.OAuthConfig` 扩展与 `Flow` 的动态客户端支持

**Files:**
- Modify: `core/auth/providers.go`（`OAuthConfig`）
- Modify: `core/auth/oauth.go`（`Grant`、`StartFlow`、`callback`、`exchange`、`grantFrom`）
- Modify: `core/internal/testoauth/server.go`
- Test: `core/auth/oauth_test.go`

**Interfaces:**
- Produces:
  ```go
  type OAuthConfig struct {
      ClientID, AuthURL, TokenURL string
      Scopes                      []string
      RedirectHost, RedirectPath  string            // "" = "localhost", "/callback"
      AuthParams, TokenParams     map[string]string // 额外的授权 / 换 token 与刷新参数
      DynamicClient               bool              // 回调带 client_id，之后用它
      NoPrompt                    bool              // 不发 prompt=select_account
      Nonce                       bool              // 授权请求带随机 nonce
      RequireScope                string            // 非空：token 响应的 scope 必须含它
      DeviceURL                   string            // 非空：设备码流程（Task 3）
  }
  type Grant struct {
      Email, RefreshToken, AccessToken string
      Expiry                           time.Time
      ClientID, Scope                  string // ClientID 仅 DynamicClient 时非空
  }
  var ErrScopeMissing = errors.New("oauth: the sign-in did not grant the access this app needs")
  ```
- testoauth 新增：`const DynamicClientID = "godwit-test-dynamic"`、`const IssuedClientID = "godwit-test-issued"`；`func (s *Server) Dynamic(on bool)`（授权接受 DynamicClientID，回调附 `client_id=IssuedClientID`，token 端点接受 IssuedClientID）；`func (s *Server) Require(param, value string)`（授权与 token 请求缺该参数即 400 `invalid_request`）；`func (s *Server) GrantScope(scope string)`（token 响应带 `scope`）；`authorize` 接受 `redirect_uri` 主机 `localhost` 或 `127.0.0.1`，路径任意。

- [x] **Step 1: 写失败测试**（追加到 `core/auth/oauth_test.go`）

```go
func TestFlowDynamicClient(t *testing.T) {
	srv := testoauth.Start(t)
	srv.Dynamic(true)
	srv.Require("resource", "https://api.example/v1")
	srv.GrantScope("openid email plan.use")
	cfg := srv.Config("openid", "email")
	cfg.ClientID, cfg.DynamicClient, cfg.NoPrompt, cfg.Nonce = testoauth.DynamicClientID, true, true, true
	cfg.RedirectHost, cfg.RedirectPath = "127.0.0.1", "/auth/callback"
	cfg.AuthParams = map[string]string{"resource": "https://api.example/v1", "agent_name_hint": "Godwit"}
	cfg.TokenParams = map[string]string{"resource": "https://api.example/v1"}
	cfg.RequireScope = "plan.use"
	f, err := auth.StartFlow(cfg, "", http.DefaultClient)
	require.NoError(t, err)
	t.Cleanup(f.Close)
	u, _ := url.Parse(f.AuthURL())
	q := u.Query()
	assert.Equal(t, "Godwit", q.Get("agent_name_hint"))
	assert.NotEmpty(t, q.Get("nonce"))
	assert.Empty(t, q.Get("prompt"))
	r := redirectURI(t, f)
	assert.Equal(t, "127.0.0.1", r.Hostname())
	assert.Equal(t, "/auth/callback", r.Path)
	ch := wait(ctx, f)
	srv.Browse(t, f.AuthURL(), "alice@example.com")
	res := recv(t, ch)
	require.NoError(t, res.err)
	assert.Equal(t, testoauth.IssuedClientID, res.g.ClientID)
	assert.Equal(t, "openid email plan.use", res.g.Scope)
	assert.Equal(t, "alice@example.com", res.g.Email)
}

func TestFlowRequiresScope(t *testing.T) {
	srv := testoauth.Start(t)
	srv.GrantScope("openid email")
	cfg := srv.Config("openid", "email")
	cfg.RequireScope = "plan.use"
	f, err := auth.StartFlow(cfg, "", http.DefaultClient)
	require.NoError(t, err)
	t.Cleanup(f.Close)
	ch := wait(ctx, f)
	srv.Browse(t, f.AuthURL(), "alice@example.com")
	assert.ErrorIs(t, recv(t, ch).err, auth.ErrScopeMissing)
}

func TestFlowDynamicClientMissingID(t *testing.T) {
	srv := testoauth.Start(t) // Dynamic off: the callback carries no client_id
	cfg := srv.Config("openid")
	cfg.DynamicClient = true
	f, err := auth.StartFlow(cfg, "", http.DefaultClient)
	require.NoError(t, err)
	t.Cleanup(f.Close)
	ch := wait(ctx, f)
	srv.Browse(t, f.AuthURL(), "alice@example.com")
	require.Error(t, recv(t, ch).err)
}
```

- [x] **Step 2: 跑测试确认失败**

Run: `go test ./core/auth/ -run 'TestFlowDynamicClient|TestFlowRequiresScope' -v`
Expected: 编译错误（`DynamicClient`、`testoauth.Dynamic` 未定义）。

- [x] **Step 3: 实现 testoauth 改动**（`core/internal/testoauth/server.go`）

`Dynamic`、`Require`、`GrantScope` 各存一个字段；`authorize` 校验 `client_id == ClientID || (dynamic && client_id == DynamicClientID)`，检查 `Require` 的参数；回调在 dynamic 时加 `client_id=IssuedClientID`；`token` 校验 `client_id == ClientID || (dynamic && client_id == IssuedClientID)` 与 `Require`；`issue` 在 scope 非空时加 `"scope"` 字段。`authorize` 的 redirect 主机校验改为 `localhost` 或 `127.0.0.1`。

- [x] **Step 4: 实现 auth 改动**

- `StartFlow`：`host := cmp.Or(cfg.RedirectHost, "localhost")`、`path := cmp.Or(cfg.RedirectPath, "/callback")`；mux 路由注册在 `path`；`!cfg.NoPrompt` 才加 `prompt=select_account`；`cfg.Nonce` 时生成 32 字节随机 base64url 加 `nonce`；`AuthParams` 每项 `oauth2.SetAuthURLParam`。
- `callback`：`clientID := q.Get("client_id")` 传给 `exchange`。
- `exchange(q)`：`DynamicClient && clientID == ""` → `errors.New("oauth: the callback named no client ID")`；复制 `f.cfg`，dynamic 时 `ClientID = clientID`；`Exchange` 的 opts 加 `TokenParams` 的 `SetAuthURLParam`；`grantFrom` 后填 `g.ClientID = clientID`（dynamic）和 `g.Scope = tok.Extra("scope")`；`RequireScope != ""` 且 `slices.Contains(strings.Fields(g.Scope), RequireScope)` 为假 → `ErrScopeMissing`。

- [x] **Step 5: 跑全部 auth 测试**

Run: `go test ./core/auth/ ./core/internal/testoauth/ ./core/ -run 'Flow|OAuth|Refresh|Reauth' `
Expected: PASS（现有邮箱 OAuth 用例不变）。

- [x] **Step 6: Commit**

```bash
git add core/auth core/internal/testoauth
git commit -m "feat(auth): dynamic client, fixed redirect path and extra params for OAuth flows"
```

---

### Task 2: `auth.RefreshForm`

**Files:**
- Create: `core/auth/refresh.go`
- Test: `core/auth/oauth_test.go`

**Interfaces:**
- Produces: `func RefreshForm(ctx context.Context, cfg OAuthConfig, hc *http.Client, refreshToken string) (Grant, error)`。表单 POST `cfg.TokenURL`：`grant_type=refresh_token, refresh_token, client_id=cfg.ClientID` + `cfg.TokenParams`。返回的 `Grant.RefreshToken` 在服务端没给或与旧的相同时为 `""`；`Expiry` 缺 `expires_in` 时为 now+30min（复用 `noExpiryTTL`）；`Email` 来自 id_token（复用 `emailFromIDToken`）。错误：响应 JSON `error` 字段按 `tokenError` 同一套映射（`invalid_grant` → `ErrInvalidGrant`，码表外 → `errBadTokenResponse`）；传输失败 → `errors.New("oauth: could not reach the token endpoint")`；ctx 取消/超时用 `%w` 包住。

- [x] **Step 1: 写失败测试**

```go
func TestRefreshForm(t *testing.T) {
	srv := testoauth.Start(t)
	srv.Require("resource", "r1")
	cfg := srv.Config("openid")
	cfg.TokenParams = map[string]string{"resource": "r1"}
	f := startFlowWith(t, srv, cfg) // startFlow 的变体：接受 cfg；按 startFlow 写
	ch := wait(ctx, f)
	srv.Browse(t, f.AuthURL(), "alice@example.com")
	g := recv(t, ch).g

	g2, err := auth.RefreshForm(ctx, cfg, http.DefaultClient, g.RefreshToken)
	require.NoError(t, err)
	assert.True(t, srv.ValidAccess(g2.AccessToken))
	assert.Empty(t, g2.RefreshToken, "not rotated")

	srv.RotateRefresh(true)
	g3, err := auth.RefreshForm(ctx, cfg, http.DefaultClient, g.RefreshToken)
	require.NoError(t, err)
	assert.NotEmpty(t, g3.RefreshToken)
	assert.NotEqual(t, g.RefreshToken, g3.RefreshToken)

	_, err = auth.RefreshForm(ctx, cfg, http.DefaultClient, "bogus")
	assert.ErrorIs(t, err, auth.ErrInvalidGrant)

	srv.RespondNext(500, `{"error":"boom","secret":"`+g.RefreshToken+`"}`)
	_, err = auth.RefreshForm(ctx, cfg, http.DefaultClient, g3.RefreshToken)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), g.RefreshToken)
}
```

- [x] **Step 2: 跑测试确认失败**

Run: `go test ./core/auth/ -run TestRefreshForm -v` — Expected: `auth.RefreshForm` 未定义。

- [x] **Step 3: 实现 `RefreshForm`**（`core/auth/refresh.go`）

限制读取响应正文 64 KiB；解码 `access_token, refresh_token, expires_in, id_token, scope, error`。

- [x] **Step 4: 跑测试** — `go test ./core/auth/` Expected: PASS

- [x] **Step 5: Commit** — `git commit -m "feat(auth): RefreshForm for token endpoints that take extra params"`

---

### Task 3: `auth.DeviceFlow`（设备码）

**Files:**
- Create: `core/auth/device.go`
- Modify: `core/internal/testoauth/server.go`
- Test: `core/auth/device_test.go`

**Interfaces:**
- Produces:
  ```go
  func StartDevice(ctx context.Context, cfg OAuthConfig, hc *http.Client) (*DeviceFlow, error)
  func (f *DeviceFlow) AuthURL() string   // verification_uri_complete，缺省 verification_uri
  func (f *DeviceFlow) UserCode() string
  func (f *DeviceFlow) Wait(ctx context.Context) (Grant, error)
  func (f *DeviceFlow) Close()
  ```
  `StartDevice` 表单 POST `cfg.DeviceURL`：`client_id, scope=strings.Join(Scopes," ")` + `AuthParams`；解析 `device_code, user_code, verification_uri, verification_uri_complete, expires_in, interval`（`interval` 缺省或 ≤0 → 5 秒）；`verification_uri*` 必须是 https（测试服务器是 http：允许 loopback 主机的 http）。轮询在 `StartDevice` 返回前启动的 goroutine 里进行，内部 ctx 超时取 `min(expires_in, FlowTimeout)`；POST `cfg.TokenURL`：`grant_type=urn:ietf:params:oauth:grant-type:device_code, device_code, client_id`。响应 `error`：`authorization_pending` 继续；`slow_down` 间隔 +5 秒；`access_denied`/`authorization_denied` → `ErrAccessDenied`；`expired_token` 或内部超时 → `ErrFlowExpired`；其他 → `errBadTokenResponse`。成功响应按 `RefreshForm` 同样解析为 `Grant`（RefreshToken 原样保留）。`Wait`/`Close` 语义与 `Flow` 相同（`finishOnce` + `done`）。
- testoauth 新增：`func (s *Server) DeviceConfig(scopes ...string) auth.OAuthConfig`（含 `DeviceURL = URL+"/device/code"`）；`POST /device/code` 返回 `interval: 1`、`expires_in: 5`；`func (s *Server) Approve(email string)` 让后续轮询成功；`func (s *Server) DenyDevice()`；`func (s *Server) SlowDownOnce()`；`func (s *Server) DeviceExpired()`（轮询返回 `expired_token`）。

- [x] **Step 1: 写失败测试**（`core/auth/device_test.go`，package `auth_test`）

```go
func TestDeviceFlowHappyPath(t *testing.T) {
	srv := testoauth.Start(t)
	cfg := srv.DeviceConfig("openid", "email")
	cfg.AuthParams = map[string]string{"referrer": "godwit"}
	f, err := auth.StartDevice(ctx, cfg, http.DefaultClient)
	require.NoError(t, err)
	t.Cleanup(f.Close)
	assert.Contains(t, f.AuthURL(), "user_code="+f.UserCode())
	ch := make(chan result, 1)
	go func() { g, err := f.Wait(ctx); ch <- result{g, err} }()
	srv.Approve("alice@example.com")
	res := recv(t, ch)
	require.NoError(t, res.err)
	assert.Equal(t, "alice@example.com", res.g.Email)
	assert.NotEmpty(t, res.g.RefreshToken)
	assert.True(t, srv.ValidAccess(res.g.AccessToken))
}

func TestDeviceFlowDenied(t *testing.T)  { /* DenyDevice 后 Wait → auth.ErrAccessDenied */ }
func TestDeviceFlowExpires(t *testing.T) { /* DeviceExpired 后 Wait → auth.ErrFlowExpired */ }
func TestDeviceFlowSlowDown(t *testing.T) { /* SlowDownOnce 后 Approve：仍成功，且 srv.TokenRequests() >= 2 */ }
func TestDeviceFlowCancelled(t *testing.T) { /* ctx 取消 → Wait 返回 context.Canceled；Close 幂等 */ }
```

- [x] **Step 2: 跑测试确认失败** — `go test ./core/auth/ -run TestDeviceFlow` Expected: 未定义。

- [x] **Step 3: 实现 testoauth 设备码端点**，再 **Step 4: 实现 `device.go`**。

- [x] **Step 5: 跑测试** — `go test ./core/auth/ ./core/internal/testoauth/` Expected: PASS（总耗时 < 10 s）。

- [x] **Step 6: Commit** — `git commit -m "feat(auth): device-code flow"`

---

### Task 4: 模型 provider 表、存储与通过 OAuth 添加服务

**Files:**
- Create: `core/models_oauth.go`
- Modify: `core/models.go`（常量、`providerFormats`、`ModelService`、`ModelServiceInput`、`prepare`、`modelCols`/`scanModelService`、`AddModelService`、`UpdateModelService`）
- Modify: `core/oauth.go`（`BeginOAuth` 的 config 查找与设备码分派、`oauthFlow`）
- Modify: `core/store/schema.go`（新迁移）
- Test: `core/models_oauth_test.go`

**Interfaces:**
- Produces:
  ```go
  const ProviderChatGPT, ProviderGrok = "chatgpt", "grok"
  // modelProvider 是一个订阅型 provider 的固定配置；modelOAuth 是包级 var，测试改它指向 testoauth/testllm。
  type modelProvider struct {
      BaseURL string
      OAuth   auth.OAuthConfig
      Models  []string // /models 失败时的回退列表
  }
  var modelOAuth = map[string]modelProvider{ProviderChatGPT: {...}, ProviderGrok: {...}}
  func ModelProviderLogin(p string) string // "oauth" 或 ""
  func ModelProviderURL(p string) string   // 固定地址；非订阅 provider 返回 ""
  var ErrModelLogin = errors.New("sign in to add this service")
  // ModelService 新增 Email string（oauth_email）、未导出 clientID string（oauth_client_id）
  // ModelServiceInput 新增 OAuthFlowID string；未导出 oauthEmail, oauthClientID string（prepare 填）
  ```
  `core/oauth.go`：`oauthFlow` 内嵌类型改为接口 `flow interface{ AuthURL() string; Wait(context.Context) (auth.Grant, error); Close() }`；`BeginOAuth` 先 `oauthConfig(providerID)`，失败再 `s.modelOAuthConfig(ctx, providerID, loginHint)`；config 的 `DeviceURL != ""` 时 `auth.StartDevice`，否则 `auth.StartFlow`。
  `modelOAuthConfig(ctx, p, loginHint)`：复制 `modelOAuth[p].OAuth`；ChatGPT 时 `AuthParams` 加 `ext_agent_host_id = s.hostID()`；若 `loginHint != ""` 且库里有 `provider = 'chatgpt' AND oauth_email = ?` 的行且其 `oauth_client_id` 非空，则 `ClientID = 该值`、删掉 `agent_name_hint`、`DynamicClient = false`。`s.hostID()` = `"urn:uuid:" + installID 的 8-4-4-4-12 形态`。
- 迁移（`core/store/schema.go` 追加一组）：
  ```sql
  ALTER TABLE model_services ADD COLUMN oauth_email TEXT NOT NULL DEFAULT ''
  ALTER TABLE model_services ADD COLUMN oauth_client_id TEXT NOT NULL DEFAULT ''
  ```
- `prepare` 规则（在 provider 校验之后、key action 之前）：当 `ModelProviderLogin(in.Provider) == "oauth"`：`in.BaseURL = ModelProviderURL(in.Provider)`；`in.Kind != "" && in.Kind != ai.KindResponses` → 现有 invalid kind 错误；`in.JudgeFormat != ""` → `fmt.Errorf("%s does not judge", in.Provider)`；`in.APIKey != "" || in.KeyAction == KeyClear` → `errors.New("this service signs in instead of taking a key")`；`in.OAuthFlowID != ""` → `g, err := s.flowGrant(AccountInput{OAuthFlowID: in.OAuthFlowID, Provider: in.Provider})`，`in.oauthEmail, in.oauthClientID = g.Email, g.ClientID`，返回 `(KeyReplace, g.RefreshToken, old, nil)`；否则 `id == 0` → `ErrModelLogin`，`id != 0` → 走 `KeyKeep`。
- `AddModelService`/`UpdateModelService`：INSERT/UPDATE 带 `oauth_email`、`oauth_client_id`（UPDATE 用 `COALESCE(NULLIF(?, ''), oauth_email)`）；成功后 `in.OAuthFlowID != ""` 时 `s.takeGrant(in.OAuthFlowID)`。

- [x] **Step 1: 写失败测试**（`core/models_oauth_test.go`，package `core`）

```go
// oauthLLM points chatgpt at a testoauth server (PKCE) and a testllm Responses server; restores on cleanup.
func oauthLLM(t *testing.T, p string) (*testoauth.Server, *testllm.Server) {
	t.Helper()
	oa, llm := testoauth.Start(t), testllm.Start(t, "openai-responses")
	llm.SetModels("gpt-x")
	saved := modelOAuth[p]
	cfg := oa.Config("openid", "email")
	if p == ProviderGrok {
		cfg = oa.DeviceConfig("openid", "email")
	}
	modelOAuth[p] = modelProvider{BaseURL: llm.URL, OAuth: cfg, Models: saved.Models}
	t.Cleanup(func() { modelOAuth[p] = saved })
	return oa, llm
}

// signIn runs a flow for provider p to its grant and returns the flow ID.
func signIn(t *testing.T, s *Service, oa *testoauth.Server, p, email string) string {
	t.Helper()
	id, authURL, err := s.BeginOAuth(t.Context(), p, "")
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { _, err := s.AwaitOAuth(t.Context(), id); done <- err }()
	if p == ProviderGrok {
		oa.Approve(email)
	} else {
		oa.Browse(t, authURL, email)
	}
	require.NoError(t, <-done)
	return id
}

func TestAddChatGPTService(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	oa, llm := oauthLLM(t, ProviderChatGPT)
	fid := signIn(t, s, oa, ProviderChatGPT, "alice@example.com")
	_, err := s.AddModelService(bg, ModelServiceInput{Name: "ChatGPT", Provider: ProviderChatGPT, Kind: ai.KindResponses, Models: []string{"gpt-x"}})
	assert.ErrorIs(t, err, ErrModelLogin)
	ms, err := s.AddModelService(bg, ModelServiceInput{Name: "ChatGPT", Provider: ProviderChatGPT, Kind: ai.KindResponses, Models: []string{"gpt-x"}, OAuthFlowID: fid, BaseURL: "https://evil.example"})
	require.NoError(t, err)
	assert.Equal(t, llm.URL, ms.BaseURL, "the address is fixed")
	assert.Equal(t, "alice@example.com", ms.Email)
	assert.True(t, ms.HasKey)
	assert.NotEmpty(t, storedKey(t, s, ms.ID), "the refresh token is in the key slot")
	_, ok := s.takeGrant(fid)
	assert.False(t, ok, "the grant was used up")
	_, err = s.AddModelService(bg, ModelServiceInput{Name: "x", Provider: ProviderChatGPT, Kind: ai.KindResponses, Models: []string{"m"}, OAuthFlowID: fid})
	assert.ErrorIs(t, err, ErrUnknownFlow)
}

func TestChatGPTServiceRejectsKeyAndJudge(t *testing.T) { /* APIKey 非空 → error；JudgeFormat 非空 → error；Kind "openai" → error */ }

func TestUpdateChatGPTServiceKeepsLogin(t *testing.T) {
	// 改名、改模型不带 OAuthFlowID：成功，Email 不变，钥匙串条目不变（storedKey 相同）
}

func TestReloginReplacesRefreshToken(t *testing.T) {
	// 第二次 signIn 后 UpdateModelService 带新 OAuthFlowID：storedKey 变了，Email 为新邮箱
}

func TestChatGPTReloginUsesIssuedClient(t *testing.T) {
	// oa.Dynamic(true)；modelOAuth[chatgpt].OAuth.ClientID = testoauth.DynamicClientID, DynamicClient = true
	// 首次 signIn 并添加 → modelServiceBy(...).clientID == testoauth.IssuedClientID
	// BeginOAuth(ctx, "chatgpt", "alice@example.com") 的 authURL 里 client_id == IssuedClientID 且无 agent_name_hint
}

func TestAddGrokServiceByDeviceCode(t *testing.T) { /* 同 TestAddChatGPTService 的成功路径，provider grok */ }

func TestHostID(t *testing.T) {
	s := modelSvc(t)
	assert.Regexp(t, `^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, s.hostID())
}
```

- [x] **Step 2: 跑测试确认失败** — `go test ./core/ -run 'ChatGPT|Grok|HostID' ` Expected: 编译错误。

- [x] **Step 3: 实现迁移、`models_oauth.go`、`prepare` 与 Add/Update 改动、`BeginOAuth` 分派**

`modelOAuth` 里 ChatGPT 的 `OAuth`：`ClientID: "dynamic_agent_client"`，`RedirectHost: "127.0.0.1"`，`RedirectPath: "/auth/callback"`，`DynamicClient, NoPrompt, Nonce: true`，`RequireScope: "chatgpt.tokens.use.direct"`，`AuthParams: {"resource": ..., "agent_name_hint": "Godwit"}`，`TokenParams: {"resource": ...}`；Grok 的 `OAuth`：`DeviceURL`、`AuthParams: {"referrer": "godwit"}`，`Models: ["grok-4.7", "grok-4.6"]`。

- [x] **Step 4: 跑测试** — `go test ./core/` Expected: PASS（含现有 `TestModelServiceCRUD`、`TestModelServiceValidation`）。

- [x] **Step 5: Commit** — `git commit -m "feat(core): chatgpt and grok model providers signed in with OAuth"`

---

### Task 5: 运行时 access token 与引擎的 key 变更

**Files:**
- Create: `core/models_token.go`
- Modify: `core/models.go`（`serviceByRef`、`modelServiceKey`、`CheckModelService`/`TestCapability` 取 key 处、`UpdateModelService`/`RemoveModelService`/`forgetModel` 清缓存、`runText`）
- Modify: `core/service.go`（字段）
- Modify: `core/ai/runtime.go`（`instance`）
- Test: `core/models_oauth_test.go`、`core/ai/runtime_test.go`

**Interfaces:**
- Produces:
  ```go
  var ErrModelReauth = errors.New("the model service sign-in is no longer valid; sign in again")
  // modelCredential 是请求要带的凭据：API key，或订阅型服务的 access token（缓存、临期刷新）。
  // 调用方不得持有 modelMu：刷新会访问网络和钥匙串。
  func (s *Service) modelCredential(ctx context.Context, m ModelService) (string, error)
  func (s *Service) forgetModelToken(id int64)
  ```
  Service 新字段：`modelTokMu sync.Mutex; modelTokens map[int64]modelToken; modelRefreshing map[int64]chan struct{}`，`type modelToken struct{ access string; expiry time.Time; keyVersion int64 }`。
- 规则：缓存命中条件 `keyVersion == m.keyVersion && time.Until(expiry) > tokenMargin`；刷新用 `auth.RefreshForm(rctx, cfg, http.DefaultClient, rt)`，`rctx` 超时 `refreshTimeout`，cfg 来自 `s.modelOAuthConfig` 并把 `ClientID` 设为 `m.clientID`（非空时）；`g.RefreshToken != ""` → `keychainSet(s.modelKeyUser(m.ID, m.keyVersion), g.RefreshToken)`，失败返回 `fmt.Errorf("save rotated refresh token: %w", err)` 且不缓存；`auth.ErrInvalidGrant` → `ErrModelReauth` 并 `putCapStatus(ctx, m.ID, m.Version, CapGen, CapStatus{State: "fail", Code: "auth", HTTPStatus: 401, At: time.Now()})`。
- `serviceByRef` 与 `modelServiceKey`：锁内读行；非订阅 provider 锁内 `modelKey`；订阅 provider 解锁后 `modelCredential`。`CheckModelService`/`TestCapability`：`prepare` 返回 `KeyKeep` 且 provider 是 oauth 时，在 `RUnlock` 后 `key, err = s.modelCredential(ctx, old)`。
- `runText`：`r.Err != nil && r.Err.Code == ai.CodeUnauthorized` → `s.forgetModelToken(req.Model.ServiceID)`。`core/assist.go` 里消费 `Result.Err` 的地方（grep `wait()` / `.Err`）同样加这一行。
- `ai.Engine.instance`：`in.cfg != cfg` 时，若除 `APIKey` 外全同，则只 `delete(e.insts, id)` 不 `cancel`；否则照旧 `cancel(errStale)`。

- [x] **Step 1: 写失败测试**

```go
// core/ai/runtime_test.go
func TestInstanceKeyChangeKeepsOldRuns(t *testing.T) {
	// 用现有 runtime_test 的 fake server 起一个挂起的 run（Turn{Hang: true}），
	// 再用同 ServiceID、同地址、不同 APIKey 的 ResolvedModel 调 e.Run 一次；
	// Release 后第一个 run 以 done 结束而不是 CodeStale；e.inits.Load() == 2。
}

// core/models_oauth_test.go
func TestChatGPTRunUsesAccessToken(t *testing.T) {
	s, bg := modelSvc(t), context.Background()
	oa, llm := oauthLLM(t, ProviderChatGPT)
	fid := signIn(t, s, oa, ProviderChatGPT, "alice@example.com")
	ms, err := s.AddModelService(bg, ModelServiceInput{Name: "ChatGPT", Provider: ProviderChatGPT, Kind: ai.KindResponses, Models: []string{"gpt-x"}, OAuthFlowID: fid})
	require.NoError(t, err)
	m, err := s.resolveModel(bg, ms.Slug+"/gpt-x")
	require.NoError(t, err)
	assert.True(t, oa.ValidAccess(m.APIKey), "APIKey is a live access token, not the refresh token")
	assert.Equal(t, 1, oa.TokenRequests(), "the first token request was the code exchange; the sign-in's access token is reused")
	// 没有缓存时刷新一次，之后命中缓存
	s.forgetModelToken(ms.ID)
	m2, err := s.resolveModel(bg, ms.Slug+"/gpt-x")
	require.NoError(t, err)
	assert.Equal(t, 2, oa.TokenRequests())
	_, err = s.resolveModel(bg, ms.Slug+"/gpt-x")
	require.NoError(t, err)
	assert.Equal(t, 2, oa.TokenRequests(), "cached")
	assert.NotEqual(t, m.APIKey, m2.APIKey)
}

func TestChatGPTRefreshRotatesAndSaves(t *testing.T) {
	// oa.RotateRefresh(true)；添加后 before := storedKey；forgetModelToken；resolveModel；storedKey != before
	// 再 forgetModelToken + resolveModel 成功（新 refresh token 可用）
}

func TestChatGPTRevokedNeedsReauth(t *testing.T) {
	// 添加后 oa.RevokeAll(); forgetModelToken; resolveModel → ErrModelReauth
	// ListModelServices 里该服务 Status[CapGen].Code == "auth"
}

func TestChatGPTCheckListsWithToken(t *testing.T) {
	// 添加后 CheckModelService(id, ms.Input()) 成功，llm.Headers() 最后一个 Authorization == "Bearer "+<access>
	// oa.ValidAccess(去掉 "Bearer " 前缀) 为 true
}

func TestUnauthorizedRunForgetsToken(t *testing.T) {
	// llm.Script(testllm.Turn{Status: 401})；runText 后 TokenRequests 增加一次（下次 resolve 刷新）
}
```

- [x] **Step 2: 跑测试确认失败** — `go test ./core/ ./core/ai/ -run 'AccessToken|Rotates|Reauth|ListsWithToken|Forgets|KeyChange'` Expected: 未定义 / FAIL。

- [x] **Step 3: 实现 `models_token.go`、调用点、`runtime.go`**

登录时拿到的 access token 也要进缓存：`prepare` 消费 grant 时记下 `g.AccessToken, g.Expiry`，Add/Update 成功后 `s.cacheModelToken(id, keyVersion, g)`（未导出小函数）。

- [x] **Step 4: 跑测试** — `go test ./core/... ` Expected: PASS。

- [x] **Step 5: Commit** — `git commit -m "feat(core): access tokens for subscription model services"`

---

### Task 6: `/models` 的 visibility 过滤、用量超限错误码、回退模型列表

**Files:**
- Modify: `core/ai/listmodels.go`、`core/ai/types.go`（`Error.Detail`）、`core/ai/responses.go`、`core/ai/runtime.go`（`classify`）
- Modify: `core/models_check.go`
- Modify: `core/internal/testllm/server.go`、`responses.go`
- Test: `core/ai/listmodels_test.go`、`core/ai/runtime_test.go`、`core/models_oauth_test.go`

**Interfaces:**
- Produces:
  ```go
  // ai.Error 新增 Detail string：机器码；"chatgpt_usage" 表示 ChatGPT 套餐用量上限/不可用
  const DetailChatGPTUsage = "chatgpt_usage"
  ```
  `ListModels` 的条目解析为 `struct{ ID, Visibility string }`，`Visibility != "" && Visibility != "list"` 的跳过。
  `responses.go` 的 `response.failed`：`ev.Response.Error.Code` 为 `subscription_sharing_usage_limit_exceeded` 或 `subscription_sharing_usage_unavailable` → 返回 `errUsageLimit = status.Errorf(status.ErrResourceExhausted, "the ChatGPT plan's usage is exhausted or unavailable")`；`classify` 在 `ResourceExhausted` 分支里 `errors.Is(err, errUsageLimit)`（Genkit 若不保留链，则比较 `status.Of(err)` 的消息等于该文本）→ `&Error{Code: CodeRateLimited, Msg: "the ChatGPT plan's usage limit is reached", Detail: DetailChatGPTUsage}`。
  `CheckModelService`：`ModelProviderLogin(in.Provider) == "oauth"` 且 `ListModels` 返回非 `ErrModelAuth` 的错误或空列表时，`c.ListSupported = false`，`c.Models = slices.Clone(modelOAuth[in.Provider].Models)`，`err = nil`。
- testllm 新增：`func (s *Server) SetHidden(models ...string)`（这些以 `visibility: "hidden"` 列出，其余列为 `"list"`；默认不带字段）；`Turn.FailCode string`（`response.failed` 的 `error.code`，默认 `server_error`）。

- [x] **Step 1: 写失败测试**

```go
// core/ai/listmodels_test.go
func TestListModelsVisibility(t *testing.T) {
	srv := testllm.Start(t, "openai")
	srv.SetModels("gpt-a", "gpt-b")
	srv.SetHidden("gpt-b")
	ids, err := ListModels(ctx, nil, KindOpenAI, srv.URL, "k")
	require.NoError(t, err)
	assert.Equal(t, []string{"gpt-a"}, ids)
}

// core/ai/runtime_test.go
func TestUsageLimitIsRateLimitedWithDetail(t *testing.T) {
	// openai-responses fake，Script(Turn{FailCode: "subscription_sharing_usage_limit_exceeded"})
	// Run → Result.Err.Code == CodeRateLimited && Detail == DetailChatGPTUsage
	// Turn{Fail: "x"}（server_error）仍是 CodeBadResponse（现状）
}

// core/models_oauth_test.go
func TestGrokCheckFallsBackToBuiltinModels(t *testing.T) {
	// grok 服务添加后 llm.SetListStatus(404)；CheckModelService → ListSupported false，Models == ["grok-4.7","grok-4.6"]
	// llm.SetListStatus(401) → ErrModelAuth
}
```

- [x] **Step 2: 跑测试确认失败** — `go test ./core/ai/ ./core/ -run 'Visibility|UsageLimit|FallsBack'`

- [x] **Step 3: 实现**

- [x] **Step 4: 跑测试** — `go test ./core/...` Expected: PASS。

- [x] **Step 5: Commit** — `git commit -m "feat(ai): hidden model filtering and ChatGPT usage-limit errors"`

---

### Task 7: 设置界面：磁贴、登录按钮、邮箱展示、错误文案

**Files:**
- Modify: `app/settings_models.go`、`app/settings_models_view.go`、`app/errors.go`、`app/reader_ai.go:499`
- Modify: `internal/i18n/zh.go`、`internal/i18n/en.go`
- Test: `app/settings_models_test.go`

**Interfaces:**
- Consumes: `core.ProviderChatGPT/ProviderGrok`、`core.ModelProviderLogin`、`core.ModelProviderURL`、`core.ModelService.Email`、`core.ModelServiceInput.OAuthFlowID`、`core.ErrModelReauth`、`ai.DetailChatGPTUsage`、`app.OAuthFlow`（`a.oauth`）。
- Produces（`app/settings_models.go`）：
  ```go
  // modelForm 新字段
  email, flowID, authURL string // 本次登录的邮箱与流程；authURL 供「重新打开」
  loggingIn bool
  loginErr  string
  func (f *modelForm) oauth() bool            // core.ModelProviderLogin(f.provider) == "oauth"
  func (m *modelsView) login(f *modelForm)    // Begin → OpenURL → Await → f.email/f.flowID → m.fetch(f)
  func (m *modelsView) accountRow(c *ui.Context, f *modelForm) // 替代 oauth 表单的地址行与 key 行
  saveService func(ctx context.Context, id int64, in core.ModelServiceInput) (core.ModelService, error) // modelsView 的测试缝，默认 Add/Update
  func svcSubtitle(s core.ModelService) string // 卡片第二行：oauth provider 的 Email，否则 BaseURL
  ```
- 行为：
  - `providerOrder` 追加两项；`providerURL` 两项取 `core.ModelProviderURL`；`svcProvider` 返回 "ChatGPT" / "Grok"；磁贴副标题走 i18n `settings.models.tile.chatgpt` / `.grok`。
  - `newForm(p)`：oauth 时 `name = svcProvider(p)`、`kind = ai.KindResponses`、`genOn = true`。
  - `input()`：oauth 时 `OAuthFlowID = f.flowID`，`APIKey = ""`，`KeyAction = ""`。`anyKey()` 加 `|| f.flowID != ""`；`needKey()` oauth 时为 `f.id == 0 && f.flowID == ""`；`hint()` 对应键 `settings.models.need.login`；`conn()` 追加 `f.flowID`；`unchanged` 比较 `flowID`。`cantTest()` 不改（`anyKey` 已覆盖）。
  - `formView`：`f.oauth()` 时不渲染地址行和 `keyField`，渲染 `accountRow`。`accountRow` 三态：未登录 → 按钮（chatgpt: `settings.models.login.chatgpt`；grok: `settings.models.login.grok`）；`loggingIn` → 文案 `settings.models.login.opened` + 「重新打开」(`OpenURL(f.authURL)`) + 「取消」(`a.oauth.Cancel(f.flowID)`)；已登录（`f.email != ""`）→ 邮箱文本 + 「重新登录」。`loginErr` 非空时下方红字。
  - `login(f)` 失败：`errors.Is(err, context.Canceled)` 不显示；`auth.ErrAccessDenied` → `settings.models.login.denied`；`auth.ErrFlowExpired` → `settings.models.login.timeout`；其他 `humanize(err)`。结果回调要检查 `m.form == f`。
  - `close()`：`f.loggingIn || f.flowID != ""` 时 `a.oauth.Cancel(f.flowID)`。
  - `edit(s)`：`f.email = s.Email`。列表卡片副标题：`ModelProviderLogin(s.Provider) == "oauth"` 时显示 `s.Email`。编辑对话框 desc 用 `cmp.Or(f.email, f.baseURL)`。
  - `app/errors.go`：`errorKeys` 加 `{core.ErrModelReauth, "settings.models.login.expired"}`；`humanize` 对 `aiErr.Detail != ""` 先查键 `"error.ai." + Code + "." + Detail`。
  - `app/reader_ai.go:499` 的 `kit.Alert`：错误是 `*ai.Error` 且 `Detail == ai.DetailChatGPTUsage` 时，action 为「管理用量」按钮，点击 `a.plat.OpenURL("https://chatgpt.com/settings/usage")`。
- i18n 新键（zh / en）：`settings.models.preset.chatgpt`（"ChatGPT"/"ChatGPT"）、`settings.models.preset.grok`（"Grok"）、`settings.models.tile.chatgpt`（"用 ChatGPT 订阅" / "Your ChatGPT plan"）、`settings.models.tile.grok`（"用 SuperGrok / X Premium 订阅" / "Your SuperGrok or X Premium plan"）、`settings.models.account`（"账户" / "Account"）、`settings.models.login.chatgpt`（"Continue with ChatGPT" 两种语言相同）、`settings.models.login.grok`（"使用 SuperGrok 登录" / "Sign in with SuperGrok"）、`settings.models.login.opened`（"已在浏览器中打开登录页，完成后这里会自动继续" / "The sign-in page is open in your browser; this continues when you finish"）、`settings.models.login.reopen`（"重新打开" / "Reopen"）、`settings.models.login.again`（"重新登录" / "Sign in again"）、`settings.models.login.denied`（"登录被拒绝或取消" / "Sign-in was declined"）、`settings.models.login.timeout`（"登录超时，请重试" / "Sign-in timed out; try again"）、`settings.models.login.expired`（"登录已失效，请重新登录" / "The sign-in is no longer valid; sign in again"）、`settings.models.need.login`（"先登录再添加" / "Sign in to add it"）、`error.ai.rate_limited.chatgpt_usage`（"ChatGPT 套餐用量已达上限" / "Your ChatGPT plan's usage limit is reached"）、`settings.models.manage_usage`（"管理用量" / "Manage usage"）。

- [x] **Step 1: 写失败测试**（`app/settings_models_test.go`；`fakeOAuth` 来自 `onboarding_test.go`，装法 `h.app.oauth = fo`；给它加 `func (f *fakeOAuth) fail(flowID string, err error)`，让 `Await` 返回该错误——把 `flows` 的通道元素改成 `struct{ email string; err error }`）

```go
func TestOAuthTileLogin(t *testing.T) {
	h := withLLM(t)
	fo := &fakeOAuth{flows: map[string]chan flowResult{}}
	h.app.oauth = fo
	h.openSettings("models")
	m := h.app.pages[PageSettings].(*settingsPage).models
	var saved core.ModelServiceInput
	m.checkService = func(_ context.Context, _ int64, in core.ModelServiceInput) (core.ServiceCheck, error) {
		return core.ServiceCheck{Models: []string{"gpt-x"}, ListSupported: true}, nil
	}
	m.saveService = func(_ context.Context, _ int64, in core.ModelServiceInput) (core.ModelService, error) {
		saved = in
		return core.ModelService{ID: 9, Name: in.Name, Slug: "chatgpt", Provider: in.Provider, Kind: in.Kind, Models: in.Models, Email: "alice@example.com", HasKey: true}, nil
	}
	h.click("添加服务")
	h.click("ChatGPT")
	h.see("先登录再添加")
	h.click("Continue with ChatGPT")
	h.until("the sign-in page opens", func() bool {
		return slices.Contains(h.plat.Calls(), "OpenURL https://login.example/authorize?n=1")
	})
	h.see("已在浏览器中打开登录页")
	fo.finish("f1", "alice@example.com")
	h.until("signed in", func() bool { return m.form != nil && m.form.email == "alice@example.com" && slices.Equal(m.form.models, []string{"gpt-x"}) })
	h.see("alice@example.com")
	h.click("添加")
	h.until("saved", func() bool { return saved.OAuthFlowID == "f1" })
	assert.Equal(t, core.ProviderChatGPT, saved.Provider)
	assert.Equal(t, ai.KindResponses, saved.Kind)
	assert.Empty(t, saved.APIKey)
}

func TestOAuthTileLoginDenied(t *testing.T) {
	// 同上到点击 "Continue with ChatGPT"，然后 fo.fail("f1", auth.ErrAccessDenied)
	// h.see("登录被拒绝或取消")；h.see("Continue with ChatGPT")；m.form.flowID == ""；saveService 未被调用
}

func TestOAuthTileCloseCancels(t *testing.T) {
	// 点登录后 h.click("关闭")：h.until(fo.Canceled() == ["f1"])；m.form == nil
	// h.plat.Calls() 里没有以 "Toast" 开头的记录（按 fakePlatform 的 record 名；若 toast 不经 platform，断言 h.app 的 toast 状态为空）
}

// svcSubtitle 是卡片第二行：订阅型服务显示邮箱，其他显示地址。
func TestServiceCardSubtitle(t *testing.T) {
	assert.Equal(t, "alice@example.com", svcSubtitle(core.ModelService{Provider: core.ProviderChatGPT, Email: "alice@example.com", BaseURL: "https://api.openai.com/v1"}))
	assert.Equal(t, "https://api.openai.com/v1", svcSubtitle(core.ModelService{Provider: core.ProviderOpenAI, BaseURL: "https://api.openai.com/v1"}))
}

func TestHumanizeChatGPTUsage(t *testing.T) {
	err := &ai.Error{Code: ai.CodeRateLimited, Detail: ai.DetailChatGPTUsage}
	assert.Equal(t, "ChatGPT 套餐用量已达上限", humanize(err))
	assert.Equal(t, "登录已失效，请重新登录", humanize(core.ErrModelReauth))
}
```

- [x] **Step 2: 跑测试确认失败** — `go test ./app/ -run 'OAuthTile|OAuthService|HumanizeChatGPT'` Expected: 编译错误。

- [x] **Step 3: 实现 i18n 键、表单状态与 `accountRow`、`login`、卡片副标题、错误映射、reader 的「管理用量」按钮**

`login` 照 `app/onboarding.go` 的 `startOAuth` 结构写（`a.do` 两段：Begin+OpenURL，再 Await）。

- [x] **Step 4: 跑测试** — `go test ./app/ ./internal/i18n/` Expected: PASS（含 `TestKeysMatch` 与现有 `TestModelServiceCRUD`）。

- [ ] **Step 5: 手动验证**（真机，不进 CI）：`mise run dev`（或项目的启动方式）→ 设置 → 模型服务 → 添加服务 → ChatGPT → Continue with ChatGPT → 浏览器完成 → 回到 Godwit 看到邮箱和模型列表 → 添加 → 在 Wit 里发一句话收到回复。Grok 同样走一遍。把两家 `/v1/models` 的真实返回摘要追加到 spec 的附录。

- [x] **Step 6: Commit** — `git commit -m "feat(settings): sign in with ChatGPT and SuperGrok as model services"`

---

### Task 8: 收尾

- [x] `go vet ./... && go test ./...` 全绿。
- [x] `docs/superpowers/specs/2026-10-14-subscription-model-providers-design.md` 状态改为「已实现」，§8 里真机验证结果（Grok `/v1/models` 是否可用）更新。
- [x] Commit：`docs(specs): mark subscription providers as implemented`。
