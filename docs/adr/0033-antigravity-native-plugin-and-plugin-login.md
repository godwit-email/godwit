---
status: accepted
---

# Antigravity 做成原生插件，主机提供通用的插件登录入口

Google Antigravity 不做成内置服务（替代 PR 485 的方案），做成一个原生插件。主机只加一个和供应商无关的登录入口：能力 `login` 和一组 `login.*` 方法。插件在本机开一个 loopback HTTP 服务，把宿主 SDK 发来的 OpenAI 请求转成 ACP 调用，宿主的流式、工具审批和脱敏都不用改（[0020](0020-plugin-models-host-sdk.md)）。ACP 客户端留在插件里，不是主机标准。

## 决定

维护者确认了以下几点：

1. 允许个人 Google 账号登录（`oauth-personal`），但插件**不进官方目录**，只能手动安装，登录前必须确认风险说明。
2. 用维护者的账号跑探针，结果见「验证状态」。
3. 只做 macOS。Linux 和 Windows 上 ACP 服务只能把 token 存成明文文件。
4. macOS 上和本机其他 ACP 编辑器共用同一个登录。
5. 代码放在私有仓库 `godwit-email/plugin-antigravity`。

## 账号和数据风险

这部分的结论对用户不利，不应被实现细节盖过去。

- **条款明确禁止。** [Antigravity 附加条款](https://antigravity.google/terms)第 6 条把用第三方软件访问服务列为违约，举的例子是「using OpenClaw with Antigravity OAuth」，后果可以是暂停或终止 Antigravity 和 / 或 Gemini CLI 账号。[官方 FAQ](https://antigravity.google/docs/faq) 说，把 `agy` 包进第三方应用在个人方案下不受支持，第三方工具应改用 API key。
- **官方 ACP 不等于放行。** Google 确实发布了 ACP 服务，[ACP 文档](https://antigravity.google/docs/cli/acp)列了 Zed、JetBrains 等编辑器，也提到其他 ACP 客户端，[ACP Registry](https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json) 也收录了它。但这些内容都是针对编辑器的，没有说邮件客户端可以用。走 ACP 比直接拿 OAuth token 调后端好得多，但不能说它合规。
- **确实有封禁。** Google 在 [gemini-cli 讨论](https://github.com/google-gemini/gemini-cli/discussions/20632)里说明：被标记后要填表并重新确认条款，第二次违规永久封禁。用户报告里有付费账号被封，也有人申诉数周没有回音。这些报告都是个案，比例未知。报告里受影响的是 Antigravity 和 Gemini CLI 的访问，没有看到 Gmail 或整个 Google 账号被停用的案例，但也**没有 Google 的保证**。对 Gmail 用户来说，这是最不能接受的一种结果。
- **邮件会被存储，可能被人工查看。** 条款第 5 条说明，个人方案下 Google 会记录交互内容，用于改进产品和机器学习技术，员工和外包人员可能查看，并建议不要提交机密信息。关掉遥测也不能完全停止这类使用。对邮件客户端来说，这比封号更普遍：**每个用户都会遇到**。
- **和 [0019](0019-subscription-oauth-providers.md) 的区别。** 0019 接受的是借用 client_id 的条款风险，当时没有点名的禁令。这里条款写明了，还有实际的封禁记录。如果放进官方目录，等于 Godwit 以审核方的身份推荐用户违约，所以推荐不进目录。

绝不做的事：把 `clientInfo` 伪装成 Zed 等编辑器。ACP 服务会读这个字段并按客户端区别处理，插件如实报告 `godwit`。伪装属于规避检测，风险也更大。

登录前的确认内容（不可跳过）要说清楚三件事：这可能违反 Antigravity 条款，账号可能被暂停；邮件内容会发给 Google，可能被人工查看；推荐改用 Gemini API key 的模型服务。

## 主机：通用登录入口

新能力 `login`，原生插件在 `plugin.json` 的 `capabilities` 里声明，`describe` 必须一致。ABI 版本仍是 1：都是新增的可选方法。旧版应用不认识这个能力，也就装不上这种插件。

| 方法 | 请求 → 响应 | 说明 |
| --- | --- | --- |
| `login.status` | `{}` → `{"signedIn":bool,"account":"a@x","notice":{…}}` | 扩展页显示用；`notice` 是登录前必须确认的内容，由插件提供 |
| `login.start` | `{}` → `{"flow":"…"}` | 立即返回，下载运行时等都在后台做 |
| `login.poll` | `{"flow"}` → `{"state":"preparing\|open\|done\|failed","url":"…","account":"…","message":"…"}` | 主机每秒轮询，`open` 时带 URL |
| `login.cancel` | `{"flow"}` → `{}` | 用户关掉对话框，或超过 `auth.FlowTimeout` |
| `login.logout` | `{}` → `{}` | |

未登录时 `models.list` 返回错误信封 `{"code":"login_required"}`。

选轮询而不是长调用，因为 ABI 没有取消，单次调用有 60 秒上限，并发只有 4 个槽位，长调用会把它们占住。

**凭据归插件所有，主机不存。** 主机不提供「插件写钥匙串」的接口：0003 规定插件不能改自己的配置，为一个用不上它的插件开这个口子不值得。登录流程在主机这边只是一个状态机。

主机的改动：

- `core/plugins/manifest.go` 的 `knownCapabilities` 加 `login`，并更新说明。
- `core.Service` 加 `PluginLoginStatus`、`BeginPluginLogin`、`PollPluginLogin`、`CancelPluginLogin`、`PluginLogout`。`full` 角色都能调用（`rpcbind/testdata/full_methods.txt`）。`BeginPluginLogin` 可能导致外发，进入 `audit`。都不进 `mcp` 角色，也不暴露给 Wit 和脚本插件。
- 只接受 `https` 的 URL，由应用打开，且只打开一次，对话框显示 URL 的主机名。这只是防误用，不是约束：原生插件本来就有全部权限（[0015](0015-native-plugins-trusted-subprocess.md)）。
- 登录或退出成功后清掉 `rp.models` 并调用 `forgetPluginModels`，让模型列表重新获取。
- `configure` 增加 `dataDir`（`DataDir/plugins/<name>/data`，更新时保留，卸载时删除）：原生插件原来没有稳定的目录可用。
- 进程退出时，如果模型列表指向 loopback 地址就清掉，下次调用重新列出，因为新进程的端口不同。
- `pluginModelTransport` 在流式响应结束前算作进行中的调用。现在 loopback 上的流不经过 ABI，空闲定时器可能在生成途中结束进程。这对所有 loopback 模型的插件都是修复。
- 扩展页接上设计稿的「登录」按钮（`canLogin`，见 PR 530 的 `ExtensionsPlugins.dc.html`）。这部分基于 [PR 530](https://github.com/godwit-email/godwit/pull/530)。

**外发判断：** 登录是用户亲手点击触发的，和一键退订同类（0004），记审计（来源 `user`，只记主机名，每个流程一次）。主机不新增无人值守时外发的持久状态。模型调用和其他云端模型一样外发，走 0025 的脱敏（loopback 地址不算 `OnDevice`）。目录条目若存在，`dataFlow` 为 `third-party`。

## 插件：把 PR 485 的问题逐个解决

| PR 485 的问题 | 这里的做法 | 探针结果 |
| --- | --- | --- |
| 明文 `acp_token.json` | 不设 `AGY_ACP_FORCE_FILE_STORAGE`。macOS 上 ACP 服务默认把 token 存进系统钥匙串（服务 `gemini`，账户 `antigravity-acp`），明文文件是 PR 自己强制出来的 | 通过：`$GEMINI_HOME` 下没有 token 文件 |
| harness 内置工具 | `session/new` 传 `_meta.antigravity.enabledTools: []`；客户端能力声明没有 fs 和 terminal | 通过：加过滤时工具为零，标记文件和 URL 都没被碰到；对照组有 15 个工具，包括 `run_command`、`send_message`、`schedule` |
| 提示注入 | 同上，再加兜底：收到任何不属于本次 MCP 的工具调用或权限请求，立即取消回合并让请求失败。这样最坏情况和其他模型一样：只能发起工具调用，还要过 Godwit 的审批 | 工具关闭已验证；兜底只在假 agent 上测过，真实服务没有触发过 |
| 丢失 tools | 每个会话传一个 loopback MCP 服务，并用 `eagerMcpServers` 直接暴露工具。ACP 服务在执行前会发 `session/request_permission`，里面有工具名和参数（`_meta.mcp`、`rawInput.arguments`）；插件把它转成 OpenAI 的 `tool_calls` 并**取消回合**，工具不在插件里执行 | 通过：真实 Wit 回合完成了 `search_mail` 调用、结果回传和作答 |
| 丢失 roles | system 消息放进 `_meta.antigravity.systemPrompt.append`。其余历史编码成 JSON 数组放进提示，让邮件里伪造的「Assistant:」只是字符串内容 | 通过，但只测了一条样本，这只是缓解手段，不是隔离 |
| 每次调用一个进程 | 一个长期运行的 ACP 服务，每个请求一个新会话。**但每个会话会留下一个约 135 MB 的 harness 进程**，服务不声明 `session/close`，调用它也不释放。所以每 5 个会话重启一次服务，占用上限约 0.9 GB；到上限时新回合等正在跑的回合结束。插件 `idle` 设为 120 秒 | 通过：杀掉插件进程后子进程全部退出，下次调用换新端口恢复 |
| （PR 未提）对话落盘 | 服务会把提示原文写进 `$GEMINI_HOME/antigravity-acp/` 的 `brain/` 和 `conversations/`，**里面会有邮件内容**。启动时和每个回合结束后删除 | 通过：回合后只剩 `settings.json`。回合进行中文件还在，进程崩溃后要到下次启动才清 |
| （PR 未提）读取用户配置 | `GEMINI_HOME` 指向 `dataDir`；`HOME` 保持真实，否则钥匙串不可用 | 通过：工具列表里没有 `~/.gemini` 配置的 MCP 和技能 |

**已知局限：** 系统提示只能追加，不能替换，harness 自己的编程助手提示始终在。模型的行为和回答质量要实测。

本机 loopback 服务只监听 `127.0.0.1`，路径带每个进程的随机串（`http://127.0.0.1:<port>/<随机串>/v1`）。其他本机程序猜不到，也就用不了用户的额度。用路径而不是 `models.prepare` 加请求头，是因为 prepare 要把请求体经过 ABI，有 1 MiB 上限，长对话会超。

判断不单独做：宿主已有的 `ai.NewGenJudge` 会用生成模型回答。

运行时在 `login.start` 时下载，从 `dl.google.com` 取固定版本，校验大小和 SHA-256，存到 `dataDir`。登录页由服务打印在 stderr，插件只接受 `accounts.google.com`，并设 `BROWSER=/usr/bin/true` 防止服务自己打开浏览器。不用 registry 的 `assets`，因为它是按模型文件设计的，界面也绑在模型上。版本随插件发布更新。约 100 MB，确认内容里要写明。

### ACP 客户端

用 [`coder/acp-go-sdk`](https://github.com/coder/acp-go-sdk) v0.13.5，不自己写。评估结果：Apache-2.0，核心只依赖标准库，有 `_meta`、`Authenticate`、`Logout`、`Cancel`、`SetSessionConfigOption`，`ProtocolVersionNumber` 是 1，和服务一致。它没有 `mcp/message` 的封装（issue #76），这里不用，MCP 走 HTTP。

共享的只是插件仓库里一个很薄的包 `acpclient`，负责启动进程、握手和 `session/update` 分发。以后有第二个 ACP 插件时再提取。主机不感知 ACP。

## 验证状态

用 ACP 服务 1.4.5 和维护者的账号验证，结果见上表。另外，开发版 Godwit 经本机 API 跑通了：安装、授权、启用；下载运行时并校验；列模型；`TestPluginModel`；一个完整的 Wit 工具回合（模型是 `gemini-3.8-flash`）。

**没有验证的：**

- **真实的浏览器登录。** 本机钥匙串已有登录，`authenticate` 直接完成，没有走到 Google 页面。用隔离的文件存储单独确认过服务会打印正确的 `accounts.google.com` 地址，但没有点完授权。
- **真实的退出登录。** 它会清掉本机其他 ACP 编辑器共用的登录，所以只在假 agent 上测过。
- **扩展页的实际外观。** 只有应用层测试（按钮、确认内容、只打开一次页面），没有人看过界面。
- **长对话和真实邮件下的回答质量。** harness 的系统提示还在。

**升级规则：** 换 ACP 服务版本前必须重跑工具过滤这一项，内置工具关不掉就不能发版（0003、0004）。

## Considered Options

- **内置服务（PR 485）：** 把条款风险和约 1500 行协议代码放进所有用户的主程序，还带明文 token。
- **直接调 Cloud Code 后端：** 拿 OAuth token 调 `cloudcode-pa`，没有 harness，工具和角色都原生支持，也最简单。但这正是 Google 点名的 piggyback 行为，封号风险最高，**不做**。
- **只支持 API key 或 Gemini Enterprise：** 这是 Google 推荐的合规路线（`gemini-api-key`、`oauth-business`），但 API key 用 Godwit 现有的模型服务就能接，不需要插件，只是用不上订阅额度。不过这应该是登录前确认内容里推荐给用户的选择。
- **主机负责存插件凭据：** 接口更完整，但这个插件用不上，而且 0003 不让插件改自己的配置。第二个需要的插件出现时再加。
- **`login.wait` 长调用：** 见上，占槽位，也没有取消。
- **ABI 上做流式：** ABI 没有回调，4 MiB 上限；而 loopback HTTP 不改 ABI 就能复用 0020 的全部能力。

## Consequences

- 用户每次生成都比普通模型慢（新会话约 4 秒起，每 5 次还有一次冷启动约 10 秒）、多耗 token（harness 提示和重发历史）。贴标签这类批量判断会明显变慢。
- 服务不提供账号邮箱，界面只显示「已登录 Google」。为了邮箱去读 token 不值得。
- 卸载插件不会退出登录：钥匙串里的条目是其他编辑器共用的。
- macOS 上 Godwit 和其他 ACP 编辑器共用一个登录：已经在 Zed 登录过的用户，装好插件就是已登录状态；在 Godwit 里退出也会退出其他编辑器。
- 插件依赖 ACP 服务的非公开细节（`_meta.antigravity.*`）。升级时必须重跑探针，工具过滤失效就不能发版。
- Google 收紧策略时，这个插件可能随时不可用，甚至给用户带来账号后果。用户可以卸载，Godwit 不做迁移，也没有数据需要迁移。
