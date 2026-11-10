# Skill 只是提示词，不执行脚本

Skill 遵循 Agent Skills 开放标准（`SKILL.md`），但 Godwit **不执行 skill 里的 `scripts/`**：要运行代码就写成 Plugin，走 manifest 的权限声明和用户授权。`references/`、`assets/` 照常可以读取。这样「放进一个 `SKILL.md` 就多一个按钮」不会变成一条绕过插件权限的执行通道。

Skill 的 UI 绑定写在规范预留给客户端的 `metadata` 里，统一用 `godwit-` 前缀（`godwit-action-on`、`godwit-action-output`），避免和其他客户端冲突。没有 `godwit-action-*` 的 skill 不出现在邮件或草稿的操作菜单上，只由 Wit 按需调用。内置的翻译、总结、润色本身也是 skill，和第三方 skill 走同一条路。

可选的 `godwit-display-name` 是用户展示名；`name` 始终是目录、`/name` 和工具调用使用的稳定 ID。未指定展示名时，只在展示时把 slug 转为首字母大写的词组（如 `create-skill` → `Create Skill`）。可选的 `godwit-icon` 只选择内置图标：`sparkles`、`file-text`、`mail`、`list`、`list-ordered`、`check`、`time`、`languages`、`search`、`shield`、`tag`、`code`、`pen-line`、`quote`；手写文件中的空值或未知值回退到 `sparkles`，不加载 SVG 或 URL。

`godwit-action-output: wit` 仅支持 `godwit-action-on: message`：用户从阅读器选择后，通过 Wit 的 `/name` 和引用邮件启动，可继续追问；不承诺草稿、选区或会话的交接。`panel` 只在阅读器里显示一次性结果卡片。Wit 的 `list_skills` 同时列出有效且启用的无 UI 绑定和 `wit` Skill，复用 `use_skill` 加载提示词；`RunSkill` 拒绝 `wit` 输出，引导调用者通过 Wit 启动。

Wit 的 `create_skill` 可以提议保存一个新的用户 Skill。审批卡片完整展示稳定名称、展示名、内置图标、描述、提示词和可选 UI 绑定；展示名限非空白单行、最多 80 字符，非空未知图标直接拒绝，省略图标时审批参数明确保留最终的 `sparkles`。用户每次确认后才写入 `DataDir/skills/<name>/SKILL.md`，不授予「本次对话都允许」。它不覆盖内置或已有 Skill，不写脚本、模型配置或其他文件，也不自动运行新 Skill；创建本身没有外发，之后使用仍遵守原有模型设置、脱敏和工具审批。文件不能和审批台账一起提交，所以复用非事务执行路径：写入后若台账未能记录结果，重启标为不确定，不自动重试。
