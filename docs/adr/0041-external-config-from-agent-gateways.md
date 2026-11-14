# Agent 网关通过文件接入 Godwit，同步成托管配置

magpie 这样的 Agent 网关接入一个 agent，是把网关地址、给它的 key 和模型逐键写进这个 agent 自己的配置文件，用户断开时再逐键还原；它的资源库也用同样的方式把 MCP 服务器、技能和指令交给 agent。Godwit 的模型服务和 MCP 服务器存在 SQLite 和钥匙串里，没有网关能写的文件，所以加一个：网关写进 `~/.godwit/external/`，Godwit 把它同步成**托管配置**（见 GLOSSARY）。接入用的是 npm 包 `@godwit/magpie-agent`（`packages/magpie-agent`），跟 Godwit 同版本发布。

| 文件 | 同步成 |
|---|---|
| `models.json` | 模型服务，`{"<名字>": {by, baseURL, apiKey, kind, models: [{id, name, context, …}], model}}` |
| `mcp.json` | MCP 服务器，Claude 的 `{"mcpServers": {…}}` 写法 |
| `skills/` | 技能，每个一个文件夹 |
| `AGENTS.md` | Wit 的系统提示里的用户指令 |

## Consequences

- **一条路径**：网关的插件在 moejs 里运行，分不出操作系统，所以每个系统都是 `~/.godwit/external`，而不是数据目录。放在子目录里，是因为 Windows 的数据目录就是 `~/.godwit`。只有默认数据目录读它，测试和另开的数据目录不读。Godwit 启动时创建这个目录：网关的 `dir` 也只能写一条路径，各系统的数据目录又不同，它就是网关判断 Godwit 已安装的依据。
- **只读**：托管的模型服务和 MCP 服务器带 `managed_by`，导出的修改和删除返回 `ErrModelServiceManaged` / `ErrMCPManaged`，只有同步能改。界面上没有编辑和删除，只有一个「由 magpie 管理」的标记；测试、授权、启用和信任照常可用。
- **用户的优先**：和用户自己的服务、服务器或技能同名时，托管的那一项跳过，不覆盖。文件解析失败时什么都不改，因为网关可能正写到一半。
- **只能指向本机**：模型服务的地址必须是 loopback。网关在本机，这一项能新增的目的地也只有它。MCP 服务器沿用 `mcpURL` 的规则。
- **默认模型只看变化**：文件里的 `model` 变了，`DefaultModel` 就切到它，原来的记进 `prev_default`；用户之后自己改的默认模型不会被改回去。服务移除时，默认模型如果还指向它，就换回 `prev_default`。默认模型切到网关以后，后台功能会在用户不操作时把内容发给网关，这是用户在网关里的选择；按 [0025](0025-mask-at-cloud-egress.md)，localhost 也算云端，照常脱敏。
- **MCP 服务器先不授权**：新同步进来的服务器没有授权，用户授权后才会启动（[0004](0004-permission-gateway-taint.md)）。文件没变时授权保留；服务器运行的内容（命令、地址、环境变量、请求头）变了，授权和工具信任随之清空，和用户自己修改一样。比较用的是各项内容的哈希。
- **断开和资源库各走各的**：magpie 断开模型连接时只还原 `models.json`（留下空的 `"magpie": {}`），服务随之移除；`mcp.json`、`skills/` 和 `AGENTS.md` 跟着它的资源库走：资源库不再给 Godwit 某一项，magpie 就从文件里拿走，Godwit 再跟着移除。所以「由谁管理」记在设置里，空条目不会把它冲掉。
- **stdio MCP 服务器拿到用户目录**：启动时除了 `PATH` 和它自己的环境变量，再给 `HOME`（Windows 上 `USERPROFILE`）。magpie 的服务器（`magpie mcp search` 等）没有它就找不到自己的文件，npx 起的服务器也一样；Godwit 自己的密钥和代理设置照旧不给。
- **技能**：只有这个目录允许顶层是链接的文件夹，因为 magpie 是从自己的资源库链接过来的（实测如此）。技能内部的链接仍然不读，`scripts/` 仍然不执行（[0009](0009-skills-are-prompts-only.md)）。
- **指令**：`AGENTS.md` 最多读 16 KB，转义后作为 `<user_instructions>` 放在 Ask 和 Assist 的系统提示末尾，在安全说明之后，并写明与前面的规则冲突时不遵循。
- **模型照网关的原样写**：`models` 是网关描述的模型对象（magpie 的 `connect` 收到的样子，带 `efforts`、`pricing` 等），Godwit 只读用得上的：`name` 是显示名（magpie 的路由组都有自己的名字），`context` 是上下文窗口（token），都优先于 models.dev，因为 models.dev 不认识路由组。插件因此只做转交，Godwit 用到新字段时不必改插件。不像名字的名字、不在 1000 到一亿之间的窗口丢掉，重复的模型只算一次，都不影响服务本身；读不懂的条目整条跳过，它原有的服务保持不变。
- **逐键写**：插件按键路径逐个写，不把条目作为一个对象返回：magpie 靠存它的 key 的那个键判断是否已接入，模型键也在同一个条目里，整体返回时两者都找不到（实测）。断开后留下的空对象按断开处理。
- **同步时机**：启动时，之后每 3 秒比较一次文件的大小和修改时间。不用 fsnotify。

## Considered Options

- **插件带一个 Bun 模块，调本机 API 写配置**：Godwit 不用读文件，但网关判断「已接入」和断开时还原都靠那个文件，这样就要自己监视文件、自己处理断开，而且要求 Godwit 正在运行。
- **只接模型，资源库以后再做**：两部分用的是同一个文件机制和同一套托管规则，分开做的话这些规则要写两遍。
