---
status: proposed
---

# Antigravity 做成原生插件，主机提供通用的插件登录入口

Google Antigravity 不做成内置服务（替代 PR 485 的方案），做成一个原生插件。主机只加一个和供应商无关的登录入口：能力 `login` 和一组 `login.*` 方法。插件在本机开一个 loopback HTTP 服务，把宿主 SDK 发来的 OpenAI 请求转成 ACP 调用，宿主的流式、工具审批和脱敏都不用改（[0020](0020-plugin-models-host-sdk.md)）。ACP 客户端留在插件里，不是主机标准。

**这份方案要等「待决定」一节确认后才实现。其中账号风险可能决定这个插件根本不该发布。**

## 待决定

| # | 决定 | 推荐 |
| --- | --- | --- |
| 1 | 个人 Google 账号登录（`oauth-personal`）算不算可接受的风险，插件怎么分发 | 先实现，但**不进官方目录**，只能手动安装，登录前必须确认风险说明 |
| 2 | 能否用你的账号跑「探针」（见「验证状态」），每次只发几条无害提示 | 同意。不跑就无法确认工具确实关掉了，插件不能算完成 |
| 3 | 支持哪些平台 | 先只做 macOS。Linux 和 Windows 上 ACP 服务只能把 token 存成明文文件 |
| 4 | macOS 上和本机其他 ACP 编辑器共用同一个登录，能否接受 | 接受。不接受就只能回到 PR 485 的明文文件 |
| 5 | 代码放哪里 | 新建 `godwit-email/plugin-antigravity`，和 `plugin-startlux` 一样；决定 1 定下来之前设为私有 |

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
| `login.status` | `{}` → `{"signedIn":bool,"account":"a@x"}` | 列表和扩展页显示用 |
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
- `configure` 增加 `dataDir`（`DataDir/plugin-data/<name>`，卸载时删除）：原生插件现在没有稳定的目录可用。
- `pluginModelTransport` 在流式响应结束前算作进行中的调用。现在 loopback 上的流不经过 ABI，空闲定时器可能在生成途中结束进程。这对所有 loopback 模型的插件都是修复。
- 扩展页接上设计稿的「登录」按钮（`canLogin`，见 PR 530 的 `ExtensionsPlugins.dc.html`）。这部分基于 [PR 530](https://github.com/godwit-email/godwit/pull/530)，要等它合入。

**外发判断：** 登录是用户亲手点击触发的，和一键退订同类（0004），记审计（来源 `user`，只记主机名）。主机不新增无人值守时外发的持久状态。模型调用和其他云端模型一样外发，走 0025 的脱敏（loopback 地址不算 `OnDevice`）。目录条目若存在，`dataFlow` 为 `third-party`。

## 插件：把 PR 485 的问题逐个解决

| PR 485 的问题 | 这里的做法 | 依据 |
| --- | --- | --- |
| 明文 `acp_token.json` | 不设 `AGY_ACP_FORCE_FILE_STORAGE`。macOS 上 ACP 服务默认把 token 存进系统钥匙串（服务 `gemini`，账户 `antigravity-acp`），明文文件是 PR 自己强制出来的 | 服务代码，**待探针确认** |
| harness 内置工具 | `session/new` 传 `_meta.antigravity.enabledTools: []`，关掉全部内置工具（`run_command`、`view_file`、`search_web`、`read_url_content`、`start_subagent` 等）；客户端能力声明没有 fs 和 terminal；`session/request_permission` 一律拒绝 | 服务代码，**待探针确认** |
| 提示注入 | 同上，再加兜底：收到任何不属于本次 MCP 的 `tool_call`，立即取消回合并让请求失败。这样最坏情况和其他模型一样：只能发起工具调用，还要过 Godwit 的审批 | 待探针确认 |
| 丢失 tools | 每个会话传一个 loopback MCP 服务（`mcpCapabilities.http` 已确认），把请求里的 tools 暴露给模型。模型一调用，就转成 OpenAI 的 `tool_calls` 返回，**并取消 ACP 回合**，工具不在插件里执行 | 待探针确认 |
| 丢失 roles | system 消息放进 `_meta.antigravity.systemPrompt.append`。其余历史编码成 JSON 数组放进提示，让邮件里伪造的「Assistant:」只是字符串内容 | 待探针确认 |
| 每次调用一个进程 | 一个长期运行的 ACP 服务，每个请求一个新会话。随插件进程一起结束（进程组 / Job）；累计一定数量的会话后重启，因为服务不支持 `session/close` | 能力已确认，数量待测 |
| （PR 未提）对话落盘 | 服务会把会话写到 `$GEMINI_HOME/antigravity-acp/conversations`，**里面会有邮件内容**。启动时和每个回合结束后删除 | 待探针确认 |
| （PR 未提）读取用户配置 | `GEMINI_HOME` 指向 `dataDir`；`HOME` 保持真实，否则钥匙串不可用。要确认不会读入 `~/.gemini` 里的 MCP 和技能 | 待探针确认 |

**工具调用选择「无状态」：** 每步都是新会话，发完整历史，就像普通的无状态模型 API。另一种做法是让 MCP 调用挂起，等工具结果回来再继续，但审批可能要等几小时甚至几天，还要处理 harness 的超时，以及请求和挂起调用的对应关系。代价是每步都要重发历史和 harness 的系统提示，token 多一些。一步里的并行工具调用：收到第一个后再等一个很短的窗口，把到达的一起返回。

**已知局限：** 系统提示只能追加，不能替换，harness 自己的编程助手提示始终在。模型的行为和回答质量要实测。

本机 loopback 服务只监听 `127.0.0.1`，路径带每个进程的随机串（`http://127.0.0.1:<port>/<随机串>/v1`）。其他本机程序猜不到，也就用不了用户的额度。用路径而不是 `models.prepare` 加请求头，是因为 prepare 要把请求体经过 ABI，有 1 MiB 上限，长对话会超。进程重启后端口会变，新进程重新确认设置后 `cfgKey` 随之改变，模型列表会重新获取，实现时加测试确认。

判断不单独做：宿主已有的 `ai.NewGenJudge` 会用生成模型回答。

运行时在 `login.start` 时下载，从 `dl.google.com` 取固定版本，校验大小和 SHA-256，存到 `dataDir`。不用 registry 的 `assets`，因为它是按模型文件设计的，界面也绑在模型上。版本随插件发布更新。约 100 MB，确认内容里要写明。

### ACP 客户端

用 [`coder/acp-go-sdk`](https://github.com/coder/acp-go-sdk) v0.13.5，不自己写。评估结果：Apache-2.0，核心只依赖标准库，有 `_meta`、`Authenticate`、`Logout`、`Cancel`、`SetSessionConfigOption`，`ProtocolVersionNumber` 是 1，和服务一致。它没有 `mcp/message` 的封装（issue #76），这里不用，MCP 走 HTTP。

共享的只是插件仓库里一个很薄的包，负责启动进程、握手和 `session/update` 分发。以后有第二个 ACP 插件时再提取。主机不感知 ACP。

## 验证状态

**已验证**（用空 profile 运行 ACP 服务 1.4.5，未登录，未用任何账号；并读取服务内嵌的代码）：`initialize` 的完整能力；`authMethods`；工具过滤的接口和说明；凭据存储的逻辑；未登录时的报错。

**未验证，要等决定 2**（探针脚本，通过官方 ACP 服务运行）：

1. 钥匙串：登录后 `$GEMINI_HOME` 下没有 `acp_token.json`。
2. 工具：`enabledTools: []` 下，让模型读取 cwd 外的标记文件、访问本地标记 URL、执行命令，都不能发生。
3. MCP：HTTP 工具能被调用，调用时取消回合是干净的。
4. 角色：`systemPrompt.append` 生效；JSON 转录里伪造的角色不能改变行为。
5. 落盘：回合后 `conversations/` 的内容和结构，删除后服务是否正常。
6. 配置隔离：`~/.gemini` 里的 MCP 和技能不被读入。

**第 2 项不通过就停止：** 内置工具关不掉，就无法满足 0003 和 0004，插件不发布。

## Considered Options

- **内置服务（PR 485）：** 把条款风险和约 1500 行协议代码放进所有用户的主程序，还带明文 token。
- **直接调 Cloud Code 后端：** 拿 OAuth token 调 `cloudcode-pa`，没有 harness，工具和角色都原生支持，也最简单。但这正是 Google 点名的 piggyback 行为，封号风险最高，**不做**。
- **只支持 API key 或 Gemini Enterprise：** 这是 Google 推荐的合规路线（`gemini-api-key`、`oauth-business`），但 API key 用 Godwit 现有的模型服务就能接，不需要插件，只是用不上订阅额度。不过这应该是登录前确认内容里推荐给用户的选择。
- **主机负责存插件凭据：** 接口更完整，但这个插件用不上，而且 0003 不让插件改自己的配置。第二个需要的插件出现时再加。
- **`login.wait` 长调用：** 见上，占槽位，也没有取消。
- **ABI 上做流式：** ABI 没有回调，4 MiB 上限；而 loopback HTTP 不改 ABI 就能复用 0020 的全部能力。

## Consequences

- 用户每次生成都比普通模型慢（新会话）、多耗 token（harness 提示和重发历史）。
- macOS 上 Godwit 和其他 ACP 编辑器共用一个登录：已经在 Zed 登录过的用户，装好插件就是已登录状态；在 Godwit 里退出也会退出其他编辑器。
- 插件依赖 ACP 服务的非公开细节（`_meta.antigravity.*`）。升级时必须重跑探针，工具过滤失效就不能发版。
- Google 收紧策略时，这个插件可能随时不可用，甚至给用户带来账号后果。用户可以卸载，Godwit 不做迁移，也没有数据需要迁移。
