# ChatGPT 默认走 Codex 登录，Grok 借用 CLI 的 client_id

用户可以用 ChatGPT 订阅和 Grok 订阅（SuperGrok / X Premium）作为模型服务：浏览器登录一次，Godwit 把 refresh token 存进钥匙串原来放 API key 的槽位，access token 只在内存里，地址固定不可改。

ChatGPT 本来走 OpenAI 官方的 Sign in with ChatGPT（动态注册客户端，直接打 `api.openai.com/v1`），但真机验证发现它对部分账号（含 Plus）在 `POST /v1/responses` 返回 403 `subscription_sharing_user_not_eligible`。所以**现在默认走 Codex CLI 的登录**（借用它的 client_id，地址 `chatgpt.com/backend-api/codex`），官方流程保留在代码和测试里，两者按服务的 `base_url` 区分。Grok 借用 Grok CLI 的公开 client_id，用设备码登录（不占端口）。

## Consequences

- 借用 Codex CLI 和 Grok CLI 的 client_id 都没有官方的第三方计划，OpenAI 和 xAI 随时可以限制，这是有意接受的服务条款风险。官方的资格问题解决后，ChatGPT 磁贴下改为提供两个入口。
- 刷新轮换后写钥匙串失败时，新 refresh token 已被服务端接受而本地没存，下次会 `invalid_grant`，要求重新登录。和邮箱账户的现状一致。
- ChatGPT 的 access token 每小时换一次。引擎发现 cfg 只有 `APIKey` 不同时直接替换实例，不取消正在跑的请求。
