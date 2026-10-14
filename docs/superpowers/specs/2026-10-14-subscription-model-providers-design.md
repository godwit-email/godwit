# 订阅型模型提供商（ChatGPT、Grok）：设计

日期：2026-10-14 · 状态：已实现

让用户用 ChatGPT 订阅（Sign in with ChatGPT）和 Grok 订阅（SuperGrok / X Premium）当模型服务：浏览器登录一次，Godwit 保存 refresh token，之后像用 API key 一样调用。参考实现：[pi](https://github.com/earendil-works/pi)（`packages/ai/src/auth/oauth/openai-chatgpt.ts`、`xai.ts`）、[Lorca](https://github.com/egoist/Lorca)、[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)。

## 1. 已定的决定

| 问题 | 决定 |
|---|---|
| ChatGPT 走哪套 | OpenAI 官方 [Sign in with ChatGPT](https://developers.openai.com/siwc/token-sharing-open-source)（动态注册客户端），**不是** Codex CLI 借用 client_id 的那套。token 直接打 `https://api.openai.com/v1`，不需要 `chatgpt-account-id` 等私有头 |
| ChatGPT 登录方式 | 浏览器 PKCE，回环回调 `http://127.0.0.1:{任意端口}/auth/callback`（官方允许任意端口），复用现有 `core/auth.Flow` |
| Grok 走哪套 | xAI OIDC（`https://auth.x.ai`），Grok CLI 的公开 client_id `b1a00492-073a-47ea-816f-4c329264a828`，与 pi 一致；token 打 `https://api.x.ai/v1` |
| Grok 登录方式 | 设备码（RFC 8628），与 pi 一致：打开 `verification_uri_complete`，用户点确认，Godwit 轮询。不占端口，绕开 Lorca 提到的回调 CORS 预检 |
| 范围 | 两个一起做 |
| 在设置里怎么出现 | 两个新的 provider 磁贴 `chatgpt`、`grok`，名称默认 ChatGPT / Grok，卡片副标题显示登录邮箱 |
| 能力 | 只有生成（`openai-responses`），没有判断：ChatGPT 的 token 被 Decisions API 拒绝，xAI 没有 Decisions |
| 地址 | 固定，不可改，不显示输入框 |
| 凭据 | refresh token 存钥匙串原 key 槽位；access token 只在内存 |

**不做**：Codex CLI 的 `chatgpt.com/backend-api` 后端；Grok 的 `cli-chat-proxy.grok.com`；ChatGPT 设备码登录（官方流程没有）；给 Grok 磁贴加 API key 备选；WebSocket 传输；用量查询；首次登录的「You're using your ChatGPT plan」欢迎弹窗（见 §8 风险）。

## 2. 协议事实

### 2.1 Sign in with ChatGPT

来源：[注册与登录](https://developers.openai.com/siwc/token-sharing-open-source/sign-in)、[模型与推理](https://developers.openai.com/siwc/token-sharing-open-source/models-and-inference)、[预览期限制](https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations)、[UI 规范](https://developers.openai.com/siwc/ui-ux-guidelines)。

- 授权：`GET https://auth.openai.com/api/accounts/authorize`，参数 `client_id=dynamic_agent_client`（首次）或已签发的 client_id（重登）、`response_type=code`、`redirect_uri`、`scope=openid profile email offline_access resource.invoke chatgpt.tokens.use.direct`、`resource=https://api.openai.com/v1`、`state`、`nonce`、`code_challenge`(+`_method=S256`)、`agent_name_hint=Godwit`（仅首次）、`ext_agent_host_id`（每台安装一个不透明 ID，持久化）、可选 `login_hint`。
- 回调：`code`、`state`，首次还有 **`client_id`**（必须保存，之后换 token、刷新、重登都用它）。
- 换 token：`POST https://auth.openai.com/api/accounts/oauth/token`，表单 `grant_type=authorization_code, code, client_id, code_verifier, redirect_uri, resource`。返回 `access_token, refresh_token, id_token, expires_in(3600), scope`；`scope` 必须含 `chatgpt.tokens.use.direct`，否则视为失败。邮箱取 `id_token` 的 `email`。
- 刷新：同一端点，`grant_type=refresh_token, refresh_token, client_id, resource`。refresh token 每次轮换（单次使用）。
- 推理：只能 `POST /v1/responses` 和 `GET /v1/models`。请求必须 `store:false`、`stream:true`、`input` 为数组、系统提示放 `instructions`（显式 system 消息被拒）；不能发 `max_output_tokens, temperature, top_p, truncation, metadata, user, prompt_cache_retention, previous_response_id` 等。Godwit 的 Responses 插件（`core/ai/responses.go`）现状已全部满足，不需改。
- `/v1/models` 对订阅令牌返回的是 `models[]`（字段 `slug`、`display_name`、`visibility`），不是 API key 的 `data[].id`；只展示 `visibility == "list"` 的，`slug` 作模型 ID 发请求，`display_name` 在模型选择里展示。
- 用量超限：流里 `response.failed`，`error.code` 为 `subscription_sharing_usage_limit_exceeded` 或 `subscription_sharing_usage_unavailable`。UI 规范要求提供「管理用量」链接到 `https://chatgpt.com/settings/usage`，登录按钮文案 **Continue with ChatGPT**。
- 资格：开源且本地运行的应用。Godwit 是 Apache-2.0 桌面应用，符合。

### 2.2 Grok（xAI）

来源：pi `xai.ts`，Lorca `grok.rs`，CLIProxyAPI `internal/auth/xai`。

- 设备码：`POST https://auth.x.ai/oauth2/device/code`，表单 `client_id, scope=openid profile email offline_access grok-cli:access api:access, referrer=godwit`。返回 `device_code, user_code, verification_uri, verification_uri_complete, expires_in, interval`。
- 轮询：`POST https://auth.x.ai/oauth2/token`，表单 `grant_type=urn:ietf:params:oauth:grant-type:device_code, device_code, client_id`。`authorization_pending` 继续；`slow_down` 间隔加 5 秒；`access_denied`/`authorization_denied`/`expired_token` 终止。缺 `interval` 按 5 秒。
- 返回 `access_token, refresh_token, id_token, expires_in`（约 6 小时；缺省按 1 小时）。邮箱取 `id_token` 的 `email`。
- 刷新：同端点 `grant_type=refresh_token, refresh_token, client_id`。可能轮换；没返回新的就沿用旧的。
- 推理：`POST https://api.x.ai/v1/responses`，只需 `Authorization: Bearer`。标准 Responses API，支持 function tools。
- 模型列表：`GET /v1/language-models`（xAI 的 `/v1/models` 混有 grok-imagine 的图和视频模型，这个接口只列文本模型；2026-10-14 用订阅令牌实测可用，返回 `models[].id`，`ai.ListModels` 对 `models[]` 同时认 `slug` 和 `id`）。不设内置回退。
- 403 或错误含 "subscription"/"not eligible"：没有 SuperGrok / X Premium 订阅。

### 2.3 ChatGPT 的 Codex 登录（当前默认，2026-10-14 追加）

真机验证发现：官方 Sign in with ChatGPT 对部分账号（含 Plus）在 `POST /v1/responses` 返回 403 `subscription_sharing_user_not_eligible`，而 `/v1/models` 正常；pi 的 issue #10605 同样，社区办法是改走 Codex 入口。因此 ChatGPT tile 背后有两种登录，**现在默认 Codex**，官方流程保留在代码与测试里，待资格问题解决后在 tile 下放两个入口。

- 区分方式：服务的 `base_url`。官方 `https://api.openai.com/v1`，Codex `https://chatgpt.com/backend-api/codex`；`modelOAuthLogins` 按地址找回登录配置，刷新用对应的客户端，不加新列。
- Codex 客户端（借用 Codex CLI，与 Grok 同类风险）：client `app_EMoamEEZ73f0CkXaXp7hrann`，authorize `https://auth.openai.com/oauth/authorize`，token `https://auth.openai.com/oauth/token`，scope `openid profile email offline_access`，authorize 额外参数 `id_token_add_organizations=true`、`codex_cli_simplified_flow=true`、`originator=godwit`，回调固定 `http://localhost:1455/auth/callback`（`OAuthConfig.RedirectPort`；端口被占报 `auth.ErrRedirectPortBusy`）。只做浏览器登录。邮箱来自 id_token。
- 请求：`POST {base}/responses`，头 `chatgpt-account-id`（从 access token JWT 的 `https://api.openai.com/auth`.`chatgpt_account_id` 解出，不另存）、`originator: godwit`、`OpenAI-Beta: responses=experimental`；`instructions` 为空时填 "You are a helpful assistant."。`store:false`、`stream:true`、`include` 同官方。
- 错误：HTTP 429，或错误码 `usage_limit_reached`、`usage_not_included`、`rate_limit_exceeded` → `CodeRateLimited` + Detail `chatgpt_usage`（仅 Codex 地址下）。官方地址下 HTTP 错误里的 `subscription_sharing_user_not_eligible` → `CodeUnauthorized` + Detail `chatgpt_not_eligible`，测试连接显示 `not_eligible`/403。
- 模型列表：`GET {base}/models?client_version=<Codex CLI 版本>`，`client_version` 必填（缺了 400），后端按每个模型的 `minimal_client_version` 过滤，所以取最新的 Codex CLI 版本：先 GitHub `openai/codex` 的 latest release（`rust-v` 标签），再 npm `@openai/codex`，命中缓存一小时，都失败则沿用上次或兜底 0.159.0 并五分钟内不再查（`ai.codexClientVersion`，照 aio-proxy 的 `codex-version.ts`）。返回 `models[].slug/display_name/visibility`，与 §2.1 同一解析；只展示 `visibility == "list"`。不设内置列表：拉不到就按失败显示。参考 aio-proxy 的 `openai-chatgpt` 插件 `catalog.ts`。

## 3. 数据

`core/store/schema.go` 新迁移：

```sql
ALTER TABLE model_services ADD COLUMN oauth_email TEXT NOT NULL DEFAULT '';
ALTER TABLE model_services ADD COLUMN oauth_client_id TEXT NOT NULL DEFAULT '';
```

- `oauth_email`：登录邮箱，只用于显示和重登时的 `login_hint`。
- `oauth_client_id`：ChatGPT 签发的 client_id；Grok 留空。
- 钥匙串 `installID/model:<id>[:vN]` 槽位对 oauth 服务存 **refresh token**。`key_version` 机制照旧（添加时 v1；重登走 `UpdateModelService` 的换 key 路径换新槽位）。刷新轮换时就地覆盖当前槽位（同 `core/credentials.go` 对邮箱账户的做法）。
- `ext_agent_host_id`：用现有 `meta.install_id`（16 字节随机 hex）格式化成 `urn:uuid:xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx`。pi 要求 UUID 形态，官方只要求不透明；用 UUID 形态两边都满足。

`ModelService` 加 `Email string`（来自 `oauth_email`）；`ModelServiceInput` 加 `OAuthFlowID string`（同 `AccountInput`）。

## 4. 鉴权（`core/auth`）

### 4.1 `OAuthConfig` 扩展

```go
type OAuthConfig struct {
    ClientID, AuthURL, TokenURL string
    Scopes                      []string
    // 新增
    RedirectPath string            // "" = "/callback"（邮箱现状）；ChatGPT 用 "/auth/callback"
    RedirectHost string            // "" = "localhost"（邮箱现状）；ChatGPT 用 "127.0.0.1"
    AuthParams   map[string]string // 额外授权参数：agent_name_hint、ext_agent_host_id、resource、nonce
    TokenParams  map[string]string // 额外 token/刷新参数：resource
    DynamicClient bool             // 回调里的 client_id 取代 cfg.ClientID 换 token
    NoPrompt     bool              // 不发 prompt=select_account（OpenAI 没有这个参数）
    DeviceURL    string            // 非空：设备码流程，StartDevice 用
}
```

`Grant` 加 `ClientID string`（DynamicClient 时填回调带来的）和 `Scope string`（ChatGPT 校验 `chatgpt.tokens.use.direct`）。

### 4.2 `Flow`（PKCE 回环）改动

- `StartFlow` 按 `RedirectHost/RedirectPath` 拼 redirect URL，追加 `AuthParams`，`NoPrompt` 时不加 `prompt`。`nonce` 由 Flow 生成（和 state 一样 32 字节），放进 `AuthParams` 一并发。
- `callback` 读 `client_id`（DynamicClient 且缺失则报错）。
- `exchange` 用回调 client_id 和 `TokenParams` 换 token。x/oauth2 的 `Exchange` 支持 `SetAuthURLParam` 追加参数，够用。

### 4.3 设备码流程（新增 `core/auth/device.go`）

```go
// StartDevice 请求设备码，返回一个与 *Flow 同形的流程。
func StartDevice(ctx context.Context, cfg OAuthConfig, hc *http.Client) (*DeviceFlow, error)
func (f *DeviceFlow) AuthURL() string              // verification_uri_complete，缺省 verification_uri
func (f *DeviceFlow) UserCode() string             // 给 UI 展示（verification_uri_complete 已预填，仅作备查）
func (f *DeviceFlow) Wait(ctx context.Context) (Grant, error) // 轮询到成功、拒绝、过期或 ctx 结束
func (f *DeviceFlow) Close()
```

core 里用一个小接口 `oauthFlow interface{ AuthURL() string; Wait(context.Context) (Grant, error); Close() }` 同时装两种流程，`BeginOAuth/AwaitOAuth/CancelOAuth` 的签名不变。

### 4.4 刷新

新增 `auth.RefreshForm(ctx, cfg, hc, refreshToken) (Grant, error)`：手写表单 POST（`grant_type=refresh_token, refresh_token, client_id` + `TokenParams`），因为 x/oauth2 的 `TokenSource` 不能附加 `resource`。错误映射复用 `tokenError` 的码表（解析响应 `error` 字段，正文永不进错误信息）。邮箱账户继续用现有 `auth.Refresh`。

### 4.5 provider 查找

`core.oauthConfig(providerID)` 先查邮箱 provider 表，再查模型 provider 表（§5）。`chatgpt`、`grok` 与邮箱 provider ID 不冲突。

## 5. 核心（`core`）

### 5.1 provider 表（`core/models.go`）

```go
const (
    ProviderChatGPT = "chatgpt"
    ProviderGrok    = "grok"
)
// providerFormats 加：
ProviderChatGPT: {gen: []string{ai.KindResponses}, judge: nil},
ProviderGrok:    {gen: []string{ai.KindResponses}, judge: nil},
```

新增 `modelOAuth` 表（`core/models_oauth.go`）：provider → 固定 BaseURL、`auth.OAuthConfig`、内置模型回退列表。`ModelProviderLogin(p) string` 给 UI 判断 `"oauth"` 还是 `""`。ChatGPT 的 `AuthParams` 在 `BeginOAuth` 时动态补 `ext_agent_host_id`（依赖 `s.installID`）和重登时的已签发 `client_id`。

### 5.2 添加与更新（`prepare`）

- oauth provider：`in.BaseURL` 强制为固定地址（忽略输入）；`in.Kind` 必须是 `openai-responses`；`JudgeFormat` 必须为空。
- `in.OAuthFlowID != ""`：`flowGrant` 校验 provider 一致，取 grant；key action 视为 `KeyReplace`，key = `g.RefreshToken`，顺带写 `oauth_email = g.Email`、`oauth_client_id = g.ClientID`。保存成功后 `takeGrant`。
- `in.OAuthFlowID == ""` 且是 oauth provider：添加报错「先登录」；更新按 `KeyKeep`（改名、改模型不需要重登）。
- `KeyClear`、`APIKey` 对 oauth provider 无效，报错。

### 5.3 运行时 access token（`core/models_token.go`）

照 `core/credentials.go` 的 `accessToken` 写一份面向服务的：

- `s.modelTokens map[int64]oauthToken`（access、expiry、keyVersion），每服务一个刷新信号量。
- `serviceByRef` / `CheckModelService` / `TestModelService` 取 key 时，对 oauth provider 改调 `s.modelAccessToken(ctx, m, rejected)`：缓存新鲜（距到期 > 2 分钟）直接用；否则读钥匙串 refresh token，`auth.RefreshForm`，轮换时 `keychainSet` 覆盖当前槽位，再缓存。
- `ErrInvalidGrant` → 返回 `ErrModelReauth`（新错误，UI 翻译成「登录已失效，请重新登录」），并写 `model_service_status` 的 gen 为 `fail/auth`。
- 运行返回 `CodeUnauthorized` 时，`core` 在 `Assist` 等调用处对 oauth 服务 `delete(s.modelTokens, id)`，下次强制刷新；不做自动重试。`shortcut: 401 不重试，下一次请求才刷新；用户抱怨再加`。
- `ResolvedModel.APIKey` = access token。引擎的 `instance()` 按 cfg 比较，token 变了会重建 Genkit 实例（每小时一次，可接受）。`e.keys` 的脱敏集合随 token 增长，受运行时长约束，可接受。
- `UpdateModelService`/`RemoveModelService`/`forgetModel` 同时清掉该服务的 token 缓存。

### 5.4 模型列表

- `ai.ListModels` 解析条目时读可选 `visibility`，非空且不是 `"list"` 的跳过（对普通 OpenAI key 返回的条目没有该字段，不受影响）。
- `CheckModelService` 对 oauth provider：`ListModels` 失败（非鉴权错误）时 `ListSupported=false`，并把 `modelOAuth[p].Models` 放进 `Models` 作为推荐列表。

### 5.5 错误码

`core/ai/responses.go` 的 `response.failed`：读 `ev.Response.Error.Code`，`subscription_sharing_usage_limit_exceeded` / `subscription_sharing_usage_unavailable` 映射到 `status.ErrResourceExhausted` → `CodeRateLimited`；其余照旧 `errRespFailed`。`ai.Error` 加 `Detail string`（可选机器码），这两个码填 `"chatgpt_usage"`，UI 据此附「管理用量」链接。

## 6. 界面（`app/settings_models*.go`）

- `providerOrder` 追加 `chatgpt`、`grok`；`providerURL` 填固定地址；`svcProvider` 名称 ChatGPT / Grok；磁贴说明「用 ChatGPT 订阅」「用 SuperGrok / X Premium 订阅」。
- `modelForm` 加 `email, flowID, authURL string; loggingIn bool; loginErr error`。
- oauth provider 的表单：
  - 不渲染地址行和 key 行；渲染一行「账户」：未登录时按钮 **Continue with ChatGPT** / **使用 SuperGrok 登录**；登录中显示「已在浏览器中打开登录页」+「重新打开」+「取消」；已登录显示邮箱 + 「重新登录」。
  - 点登录：`a.oauth.Begin(ctx, provider, email)` → `plat.OpenURL(url)` → `a.oauth.Await`；成功后 `f.email = email, f.flowID = id`，随即触发现有的 `probe`（`CheckModelService` 带 `OAuthFlowID`）拉模型列表。失败显示 `loginErr`。关闭对话框时 `Cancel(flowID)`。
  - 保存条件：`needKey()` 对 oauth provider 改为「新服务且没有 flowID」。
  - judge 区块不渲染（`ProviderFormats` 的 judge 为空，现有逻辑已隐藏）。
- 卡片：oauth 服务副标题显示 `s.Email`，不显示 BaseURL。
- 对话框里的错误：`ErrModelReauth` → 「登录已失效，请重新登录」+ 登录按钮；`Detail == "chatgpt_usage"` → 「ChatGPT 套餐用量已达上限」+ 链接按钮「管理用量」(`https://chatgpt.com/settings/usage`)。阅读页的 AI 错误提示同样处理。
- i18n（`internal/i18n/zh.go`、`en.go`）新键：`settings.models.preset.chatgpt`、`.grok`、`settings.models.login.chatgpt`（"Continue with ChatGPT"，两种语言都用英文原文，按规范）、`settings.models.login.grok`、`settings.models.login.opened`、`.reopen`、`.cancel`、`.relogin`、`.required`、`.expired`、`settings.models.usage_limit`、`settings.models.manage_usage`。

`App.OAuth` 接口（`app/app.go`）已经抽象了 Begin/Await/Cancel，测试里的假实现可以直接复用。

## 7. 测试

- `core/internal/testoauth`：
  - `Server` 加 `Dynamic(bool)`：回调附 `client_id=<issued>`，之后只接受该 client_id；`RequireParams(map)`：授权/换 token 必须带 `resource` 等；`RequireScope`。
  - 新增设备码端点 `POST /device/code`、`/token` 的 `device_code` grant，`Approve()` 让下一次轮询成功，`DenyDevice()`，`SlowDown(n)`。
- `core/auth`：PKCE 流程带 `RedirectPath/RedirectHost/AuthParams/DynamicClient` 的测试；`DeviceFlow` 的成功、pending→成功、slow_down、拒绝、过期、ctx 取消；`RefreshForm` 的轮换/不轮换/`invalid_grant`。
- `core`：`AddModelService` 带 `OAuthFlowID`（钥匙串存 refresh token、列存邮箱与 client_id、grant 被消费）；无 flowID 添加 oauth provider 报错；`resolveRef` 取到 access token；到期刷新且轮换写回；`invalid_grant` → `ErrModelReauth`；`CheckModelService` 的 `visibility` 过滤和回退列表；`response.failed` 用量码 → `CodeRateLimited` + Detail（`testllm` 加一个返回该事件的开关）。
- `app`：oauth 磁贴表单的登录按钮 → Begin/OpenURL/Await → 邮箱显示并拉模型 → 保存传 `OAuthFlowID`；登录失败提示；卡片副标题显示邮箱；用量上限错误显示「管理用量」。
- 真机手测清单（不进 CI）：ChatGPT Plus 账号完整登录 + 一次生成 + 等 1 小时后再生成（刷新路径）；SuperGrok 账号设备码登录 + 生成；两家 `/v1/models` 的实际返回记进 spec 附录。

## 8. 风险与边界

- **Grok 政策**：借用 Grok CLI 的 client_id，没有官方第三方计划，xAI 随时可以限制。ChatGPT 这边是官方计划，Godwit 符合「开源 + 本地」资格。
- **ChatGPT UI 规范**只部分满足：按钮文案和「管理用量」链接做了；首次登录欢迎弹窗和「Using ChatGPT plan」常驻标识这次不做，上架前要补。
- **Grok `/v1/models`** 用订阅 token 能否列模型未验证；回退列表兜底。
- **1 小时重建实例**：ChatGPT access token 每小时换一次，Genkit 实例随之重建一次。现状 `Engine.instance()` 发现同一服务的 cfg 变了会 `cancel(errStale)` 旧实例，正在跑的请求会以 `CodeStale` 失败。**决定**：cfg 只有 `APIKey` 不同（地址、格式都相同）时不取消旧实例，直接替换；旧实例的运行跑完即弃。地址或格式变了仍按现状取消。`runtime_test.go` 加一个用例。
- **钥匙串单槽覆盖**：刷新轮换后 `keychainSet` 失败时，新 refresh token 已被服务端接受而本地没存，下次刷新会 `invalid_grant` → 要求重登。与邮箱账户现状一致。
