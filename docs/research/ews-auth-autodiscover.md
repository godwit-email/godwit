# Exchange 本地部署：EWS 认证与 Autodiscover 实况

调研日期 2026-11-10，回答 [#635](https://github.com/godwit-email/godwit/issues/635)（地图 [#634](https://github.com/godwit-email/godwit/issues/634)）。来源标记：[O] 微软一手文档或协议规范；[推断] 由一手材料推出、没有原文直说；[待验] 文档互相矛盾或查不到，需要实验室或真实用户确认。

## 0. 结论速览

- #581 用户的 **15.2.1544.11 = Exchange 2019 CU14 Apr24HU**（2024-04-23）。CU14 之后 CU14 还出过 SU，最新 15.2.1544.50（Oct26SU）。
- 2016、2019 已于 **2025-10-14** 停止支持（只剩付费 ESU）；SE 自 2025-07-01 起受支持，build 号仍是 15.2（SE RTM = 15.2.2562.17）。2013 于 2023-04-11 停止支持。
- EWS 前端虚拟目录默认开的是 **Anonymous + Windows（Negotiate/NTLM）**；Basic 默认是否开，两份官方文档说法相反 [待验]。所以 **NTLM 是必须实现的**，Basic 只能当可选项。
- **CU14 起 Extended Protection（EP）默认开**；前端 EWS 是 `Accept/Allow`（不强制 CBT），后端 EWS 是 `Required`。客户端必须用 NTLMv2；带上 channel binding 更稳。
- ADFS 现代认证（2019 CU13+）对 EWS 可用，但 ADFS 里只登记了 Outlook 和 Apple Mail 的 client id；第三方客户端要管理员另行注册，**不能指望**。
- Autodiscover 域外顺序：`https://<domain>/autodiscover/autodiscover.{svc|xml}` → `https://autodiscover.<domain>/…` → 未认证 `GET http://autodiscover.<domain>/autodiscover/autodiscover.xml` 跟 302 → SRV `_autodiscover._tcp.<domain>`。返回的 EWS URL 是管理员配的 `ExternalUrl/InternalUrl`，**可以是任意主机**。
- NTLM 认证绑定在 TCP 连接上：三次握手必须走同一条连接，之后每条新连接都要重新握手。多 CAS + 负载均衡时，只要握手期间连接不断、LB 不在一条 TCP 连接内换后端，就没问题；官方说 2016/2019 不需要 session affinity。

## 1. EWS 虚拟目录的认证方式

### 1.1 默认值

- 前端（Default Web Site）`EWS`：**Anonymous authentication + Windows authentication**，SSL required。后端（Exchange Back End）`EWS` 同样是 Anonymous + Windows。[O] https://learn.microsoft.com/en-us/exchange/clients/default-virtual-directory-settings
- 同一页：前端 `Autodiscover` 是 **Anonymous + Basic + Windows**。[O] 同上
- IIS 的 Windows authentication 默认 provider 是 `Negotiate`、`NTLM` 两个，Negotiate 内部先尝试 Kerberos 再回落 NTLM。[O] https://learn.microsoft.com/en-us/iis/configuration/system.webserver/security/authentication/windowsauthentication/
- `Set-WebServicesVirtualDirectory` 参数说明里写着 `BasicAuthentication` "$true … This value is the default"，`WindowsAuthentication`、`OAuthAuthentication`、`WSSecurityAuthentication` 也都写默认 `$true`。[O] https://learn.microsoft.com/en-us/powershell/module/exchangepowershell/set-webservicesvirtualdirectory
- **矛盾 [待验]**：默认值表说 EWS 前端没有 Basic，cmdlet 文档说 Basic 默认开。cmdlet 文档覆盖 2010–SE，可能沿用了旧版本的说法。结论：Basic 不能当成一定可用，NTLM 必须实现。实验室里跑 `Get-WebServicesVirtualDirectory | fl Server,*auth*` 看真实值；ADFS 文档里的示例输出是 `{Ntlm, OAuth, Negotiate}`（见 1.3），没有 Basic，可以佐证默认值表。[O] https://learn.microsoft.com/en-us/exchange/plan-and-deploy/post-installation-tasks/enable-modern-auth-in-exchange-server-on-premises
- Kerberos 对域外客户端不现实：需要能访问 KDC，还要用对 SPN；IIS 文档也说 Kerberos 要能连到 AD，"not feasible in an Internet environment"。[O] IIS windowsAuthentication 同上。所以 Negotiate 在我们这里实际会回落成 NTLM [推断]。

### 1.2 Extended Protection（对 NTLM 实现有硬约束）

[O] https://learn.microsoft.com/en-us/exchange/plan-and-deploy/post-installation-tasks/security-best-practices/exchange-extended-protection

- "Extended Protection is enabled by default when installing Exchange Server 2019 CU14 (or later)"，安装时可用 `/DoNotEnableEP` 或 `/DoNotEnableEP_FEEWS` 跳过。#581 用户在 CU14 上，**很可能已开 EP** [推断，要问用户或看症状]。
- 推荐值：`Default Website/EWS` = `Accept`（UI）/`Allow`（脚本），即"CBT checking is enabled, but not required"；`Exchange Backend/EWS` = `Required`（后端只有前端代理来访，与客户端无关）。`Default Website/AutoDiscover` = `Off`。
- "NTLMv1 can't be used together with Extended Protection"：客户端必须发 NTLMv2 响应。
- 负载均衡：EP 下 **SSL offloading 不支持**；SSL bridging 支持，但 LB 和 Exchange 必须用**同一张证书**，否则 CBT 校验失败。
- NTLM 的 channel binding 是 AUTHENTICATE_MESSAGE 里的 `MsvAvChannelBindings` AV_PAIR（值为 `MD5(ClientChannelBindingsUnhashed)`，没有就填 16 个 0），"The client SHOULD send the channel binding AV_PAIR"。[O] https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-nlmp/c0250a97-2940-40c7-82fb-20d208c71e96
- 对我们的含义 [推断]：前端 EWS 是 Allow 时，不带 CBT 的 NTLMv2 也能过；但管理员若把前端 EWS 改成 `Required`（文档提到这会让 Outlook for Mac、iOS Mail 失败），我们也要带 CBT 才能过。Go 的 NTLM 库是否支持 `tls-server-end-point` channel binding，要在实现票里确认。

### 1.3 ADFS 现代认证（2019 CU13 起）

[O] https://learn.microsoft.com/en-us/exchange/plan-and-deploy/post-installation-tasks/enable-modern-auth-in-exchange-server-on-premises

- "Starting with Exchange Server 2019 CU13, Exchange Server supports OAuth 2.0 … for pure on-premises environments using ADFS"。适用 2019、SE，不适用 2016。混合部署应改用 HMA（Entra ID），不走这条。
- 前提：所有承接客户端连接的服务器 ≥ 2019 CU13；ADFS 2019 及以上（Windows Server 2019+），不能装在 Exchange 上；域外访问可能还要 Web Application Proxy；跑 `/PrepareAD`；`New-AuthServer -Type ADFS …`、`Set-AuthServer -IsDefaultAuthorizationEndpoint $true`、`Set-OrganizationConfig -OAuth2ClientProfileEnabled $true`；按用户分配 `Allow Modern Auth` 认证策略（组织默认策略通常设成 `Block Modern Auth`，含 `BlockModernAuthWebServices`）。
- EWS 在支持列表里："Exchange Web Services (EWS) | Yes"。IMAP、POP、RPC/HTTP 不支持。
- **第三方客户端**：ADFS 里只注册了两个 native client：Outlook `d3590ed6-52b3-4102-aeff-aad2292ab01c`、iOS/macOS Mail `f8d98a96-0999-43f5-8af3-69971c7bb423`，并给它们授 `EWS.AccessAsUser.All` 等 scope。Godwit 要用，管理员得在 ADFS 里另加一个 native client 并授权 [推断]；冒用 Outlook 的 client id 不可取。文档里的客户端表还写了 Thunderbird "Doesn't fall back to Basic auth"、Gmail app "Falls back to Basic auth"，即开了现代认证的用户，不支持它的客户端也可能被拒。
- 客户端发现 ADFS 端点：Exchange "advertises the auth URL of the DefaultAuthorizationEndpoint" 放在"Modern Auth header"里。具体 header 格式（`WWW-Authenticate: Bearer authorization_uri=…` 之类）文档没写 [待验]。
- 已过时的文档：EWS 开发者文档《Authentication and EWS in Exchange》（2015）仍写 "OAuth 2.0 (Exchange Online only)"、"NTLM (Exchange on-premises only)"、"Basic (no longer recommended)"，早于 CU13，OAuth 那一条已被上面的 ADFS 文档推翻。[O] https://learn.microsoft.com/en-us/exchange/client-developer/exchange-web-services/authentication-and-ews-in-exchange

## 2. 用户名格式

| 格式 | Basic | NTLM |
|---|---|---|
| `DOMAIN\user` | 可用 [推断，见下] | 可用：拆成 `DomainName=DOMAIN`、`UserName=user` [O MS-NLMP] |
| UPN `user@corp.example` | 可用 [推断] | Windows 服务器通常接受 `UserName=user@upn`、`DomainName` 为空，但 MS-NLMP 没规定 [待验] |
| 邮件地址（≠ UPN） | 一般不行，除非邮件地址恰好等于 UPN 或管理员加了 UPN 后缀 [推断] | 同左 [推断] |

依据：

- IIS Basic 有 `defaultLogonDomain`、`realm` 两个可选属性，`logonMethod` 默认 `ClearText`，用法与 Win32 `LogonUser` 的 logon type 一一对应。[O] https://learn.microsoft.com/en-us/iis/configuration/system.webserver/security/authentication/basicauthentication
- `LogonUser`："If you use the user principal name (UPN) format, User@DNSDomainName, the lpszDomain parameter must be NULL"。[O] https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-logonusera 。IIS Basic 底层调 LogonUser，因此 `DOMAIN\user` 和 UPN 都能登 [推断：IIS 文档只用 logon type 暗示这一点，没直说]。
- MS-NLMP 只把 `UserName`、`DomainName` 当两个独立字段原样发出（"UserName to User. DomainName to UserDom."），没有定义 UPN 怎么解析。[O] 同 1.2 的 MS-NLMP 链接。UPN 加空 domain 能不能过要看 DC 实现；Windows 客户端实际就是这么发的，但这条没有一手出处 [待验]。
- 邮件地址和 UPN 不同（很常见：UPN 是 `user@corp.local`，邮件是 `user@example.com`）时，只有 Autodiscover 用邮件地址（`EMailAddress`/`Mailbox` 字段），认证一律用 `DOMAIN\user` 或真 UPN [推断]。UI 应把"邮件地址"和"登录用户名"拆成两栏，用户名留空时默认取邮件地址。

## 3. Autodiscover

### 3.1 域外客户端的查找顺序

[O] https://learn.microsoft.com/en-us/exchange/client-developer/exchange-web-services/autodiscover-for-exchange

1. SCP（AD 里的服务连接点）：域内专用，"skip SCP lookup" for non-domain clients。我们跳过。
2. `https://<domain>/autodiscover/autodiscover` + 扩展名
3. `https://autodiscover.<domain>/autodiscover/autodiscover` + 扩展名。扩展名 SOAP 是 `.svc`，POX 是 `.xml`。
4. 以上都失败时：未认证 `GET http://autodiscover.<domain>/autodiscover/autodiscover.xml`（明文 HTTP），收到 302 就把认证请求重发到 `Location`。
5. 最后查 DNS SRV `_autodiscover._tcp.<domain>`，只用指向 SSL 端点、优先级和权重最高的记录。

安全规则（同页 + 错误处理页）：只向 HTTPS 端点发凭据、证书必须有效；单个 401 不代表密码错，要多个 URL 都 401 才提示重输。[O] https://learn.microsoft.com/en-us/exchange/client-developer/exchange-web-services/handling-autodiscover-error-messages

MS-OXDISCO 列的两个 URI 是 `http://<Domain>/Autodiscover/Autodiscover.xml` 和 `https://Autodiscover.<Domain>/…`，第一个写的是 `http`，和 EWS 开发者文档的 `https` 不一致，可能是规范笔误 [待验，按 https 做]。[O] https://learn.microsoft.com/en-us/openspecs/exchange_server_protocols/ms-oxdisco/07838a79-a3be-4d89-baea-affcc41fcb8e

### 3.2 重定向

- HTTP 301/302：重发到 `Location`。[O] 错误处理页；MS-OXDSCLI "the client SHOULD repost the request to the redirection URL"。https://learn.microsoft.com/en-us/openspecs/exchange_server_protocols/ms-oxdscli/d037cc4d-d1d8-42be-8db0-5a49f8c31eea
- POX `Action=redirectAddr` / SOAP `ErrorCode=RedirectAddress`：用新邮件地址**从头**重新生成候选列表；新地址和原地址相同就当该 URL 无效。
- POX `Action=redirectUrl` / SOAP `RedirectUrl`：向新 URL 重发；必须是 HTTPS、这个地址没失败过，"If applicable … get their permission to follow the redirect"，证书有效。[O] 错误处理页；MS-OXDSCLI https://learn.microsoft.com/en-us/openspecs/exchange_server_protocols/ms-oxdscli/e3733d84-2e72-4b9d-8174-a222e72a78c5
- SOAP `ServerBusy`：稍后重试或挪到列表末尾。
- 跳转次数上限：规范没写死；官方示例代码用调用方给的 `maxHops` 参数控制。[O] https://learn.microsoft.com/en-us/exchange/client-developer/exchange-web-services/how-to-get-user-settings-from-exchange-by-using-autodiscover 。按惯例设 10 [推断]。

### 3.3 POX 与 SOAP

[O] Autodiscover for Exchange 页、Get user settings 页

| | SOAP (`autodiscover.svc`) | POX (`autodiscover.xml`) |
|---|---|---|
| 可用版本 | Exchange 2010 起 | Exchange 2007 SP1 起 |
| 请求 | `GetUserSettings`，只要指定的设置（如 `ExternalEwsUrl`、`InternalEwsUrl`、`CasVersion`、`EwsSupportedSchemas`） | POST 邮件地址 + `AcceptableResponseSchema`，返回全部设置 |
| EWS URL 所在 | `UserSetting` `ExternalEwsUrl` / `InternalEwsUrl` | `Protocol Type=EXCH`（内部）/`EXPR`（外部）下的 `EwsUrl`、`ASUrl` |
| 官方建议 | 首选 | SOAP 不可用时的备选 |

我们只支持 2016+，两种都有；SOAP 还能直接拿到 `CasVersion` 和 `EwsSupportedSchemas`，可用于版本判断 [推断]。

### 3.4 EWS URL 可以落到别的主机

- 返回的 EWS URL 就是管理员给 EWS 虚拟目录设的 `ExternalUrl` / `InternalUrl`，任意 URI，例子里 Autodiscover 在 `autodiscover.contoso.com`、EWS 在 `mail.contoso.com`。[O] Set-WebServicesVirtualDirectory 页；Get user settings 页的响应示例
- 因此 Autodiscover 结果会把凭据带去一个用户没输入过的主机。按 AGENTS.md「Decide egress up front」，发现阶段要把最终 EWS 主机给用户看并确认 [推断：这是项目规则，不是微软要求]。
- #581 的情况是"只暴露 EWS"：Autodiscover 很可能根本不可达，手填 EWS URL（`https://<host>/EWS/Exchange.asmx`）必须作为一等入口 [推断]。

## 4. 多 CAS / 负载均衡下的 NTLM

- NTLM over HTTP：服务器回 `401` + `WWW-Authenticate: NTLM`，第二个 401 带 CHALLENGE_MESSAGE。[O] https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-ntht/7daaf621-94d9-4942-a70a-532e81ba293e 。HTTP 用的是 "connection-oriented NTLM"。[O] https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-nlmp/1bf72e97-a970-482d-90fc-776732fea1be
- 认证结果绑在 TCP 连接上：`HttpListener.UnsafeConnectionNtlmAuthentication` 的说明是"subsequent requests over the same TCP connection are processed using the authentication information … of the initial request"。[O] https://learn.microsoft.com/en-us/dotnet/api/system.net.httplistener.unsafeconnectionntlmauthentication 。IIS 侧对应 `authPersistSingleRequest`（默认 false，即认证在连接上保持）。[O] IIS windowsAuthentication 页
- 由此推出的客户端要求 [推断]：
  1. NEGOTIATE → CHALLENGE → AUTHENTICATE 三步必须在**同一条** keep-alive 连接上；中途换连接或连接被关，握手作废。
  2. 每条新 TCP 连接都要重新握手；连接池里的每条连接各自认证。Go 的 `net/http` 不知道这一点，要用专门的 NTLM transport（例如每个账户限 1 条或少量固定连接）。
  3. 握手里的请求体（SOAP）可能被发两三次，大请求要么先用小请求或 HEAD 握手，要么能重放 body。
- 负载均衡：官方说 2016/2019 "session affinity is no longer required"，原因是 Exchange 前端无状态并会代理到托管邮箱的服务器。[O] https://learn.microsoft.com/en-us/exchange/architecture/client-access/load-balancing 。L4 LB 天然按 TCP 连接转发，一条连接内不会换后端；L7 LB 若做连接复用（多路复用到后端），会破坏 NTLM，这属于 LB 配置问题 [推断]。EP 开启时 LB 不能做 SSL offloading（见 1.2）。
- 同页的 HTTP cookie 跨服务器免重认证说的是 Outlook/OWA 这类客户端，不改变 NTLM 每条连接都要握手 [推断]。

## 5. 版本与支持状态

[O] build 表 https://learn.microsoft.com/en-us/exchange/new-features/build-numbers-and-release-dates ；生命周期 https://learn.microsoft.com/en-us/lifecycle/products/exchange-server-2019 、https://learn.microsoft.com/en-us/lifecycle/products/exchange-server-subscription-edition ；ESU https://techcommunity.microsoft.com/blog/exchange/announcing-exchange-2016--2019-extended-security-update-program/4433495

| 产品 | 末个 CU / 当前 | build | 状态 |
|---|---|---|---|
| 2013 | CU23（2019-06-18） | 15.0.1497.2 | 2023-04-11 停止支持 |
| 2016 | CU23（2022-04-20），最新 Oct26SU | 15.1.2507.6 → 15.1.2507.77 | 2025-10-14 停止支持，仅 ESU |
| 2019 | CU13（2023-05-03） | 15.2.1258.12 | 停止支持；ADFS 现代认证起点 |
| 2019 | CU14（2024-02-13） | 15.2.1544.4 | 停止支持，ESU 覆盖；EP 默认开 |
| 2019 | **CU14 Apr24HU（2024-04-23）** | **15.2.1544.11** | #581 用户所在 |
| 2019 | CU15（2025-02-10），最新 Oct26SU | 15.2.1748.10 → 15.2.1748.55 | 停止支持，ESU 覆盖 |
| SE | RTM（2025-07-01），最新 Oct26SU | 15.2.2562.17 → 15.2.2562.56 | 受支持（Modern Lifecycle） |

- 2019 生命周期：mainstream 到 2024-01-09，extended 到 2025-10-14（太平洋时间）。
- ESU：只给已在 2016 CU23 或 2019 CU14/CU15 上的客户，第一期到 2026-04-14，第二期（2026-05 到 10 月）要另买。build 表里 2019 CU14 到 2026-10 仍有 SU，与此一致。
- #581 用户的 15.2.1544.11 落后 CU14 最新 SU 很多（15.2.1544.50），说明这家没跟 SU [推断]；这影响不到 EWS 协议本身。
- 最低支持版本建议 [推断]：2016 CU23 / 2019 CU14 起，正好是 ESU 的准入门槛；更早的版本连 ESU 都没有，不值得测。各版本实际支持的 EWS schema 可用 SOAP Autodiscover 的 `EwsSupportedSchemas` 读出，本文没逐版核对。

## 6. 给后续三张票的输入

- 「认证方式与用户名格式」：NTLMv2 必做（含连接绑定的 transport，最好支持 CBT）；Basic 可选；ADFS OAuth 不做或放后面（要管理员注册 client）；用户名和邮件地址分开填。
- 「发现：Autodiscover 还是手填 EWS URL」：手填为主、Autodiscover 为辅；做 Autodiscover 就按 3.1 顺序，SOAP 优先 POX 兜底，`redirectUrl` 和最终 EWS 主机要给用户确认。
- 「实验室、最低版本与退路」：实验室装 2019 CU14 或 SE（EP 默认开），核对 1.1 的 Basic 默认值、2 的 UPN/邮件地址登录、1.2 的 CBT 在 `Required` 下的表现；最低版本定 2016 CU23 / 2019 CU14。

## 未能核实

- EWS 前端 Basic 默认开还是关（两份官方文档矛盾）。
- NTLM 下 UPN + 空 domain 的服务器端行为（MS-NLMP 未定义；只有二手资料）。
- ADFS 现代认证向客户端公布授权端点的 header 具体格式。
- MS-OXDISCO 里 `http://<Domain>` 是否笔误。
- #581 用户的服务器是否已开 EP、LB 是 L4 还是 L7、是否做 SSL offloading，需要问用户。
