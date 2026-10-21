# Skill 只是提示词，不执行脚本

Skill 遵循 Agent Skills 开放标准（`SKILL.md`），但 Godwit **不执行 skill 里的 `scripts/`**：要运行代码就写成 Plugin，走 manifest 的权限声明和用户授权。`references/`、`assets/` 照常可以读取。这样「放进一个 `SKILL.md` 就多一个按钮」不会变成一条绕过插件权限的执行通道。

Skill 的 UI 绑定写在规范预留给客户端的 `metadata` 里，统一用 `godwit-` 前缀（`godwit-action-on`、`godwit-action-output`），避免和其他客户端冲突。没有 `godwit-action-*` 的 skill 不出现在界面上，只由 Wit 按需调用。内置的翻译、总结、润色本身也是 skill，和第三方 skill 走同一条路。
