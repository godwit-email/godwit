---
status: accepted
---

# 一次性补全走 `complete`，回填只经它返回的 `reply`

即时回复、邮件的一行摘要（gist）、压缩、插件的 `llm.complete`、正文翻译、生成模型代答的判断，还有连接测试，都是**一次性补全**：不带工具也不带历史，跑一轮就结束，取回文本。它们统一调用 `Service.complete(ctx, feature, req, max)`。打标签（`mask.Tag`，`feature` 为空就沿用 ctx 上已有的标签）、取 engine、限定只跑一轮、读事件并截断、401 时丢掉 token，都在 `complete` 里完成；测试服务用的临时 engine 走它下面的 `runText`。

`complete` 返回 `reply{Text, Usage}`。`Raw` 请求的 `Text` 是模型的原文，里面还留着占位符，调用方先把 JSON 或 HTML 解析出来，再对解析出的值调用 `reply.Restore` / `reply.RestoreHTML`（[0025](0025-mask-at-cloud-egress.md) 要求先解析再回填）。调用方不再自己拿 `Masker` 来回填。只有两处例外，它们拿 `Masker` 都不是为了回填 `reply`：生成模型代答的判断要把 `Masker` 交给 `ai.NewGenJudge`，那里在校验答案前回填；正文翻译切段之前要先脱敏。

## Considered Options

- **审计也收进 `complete`**：没有采用，审计留给调用方。审计记的是「对什么目标运行了什么」，它和调用并不一一对应：正文翻译一封邮件只记一条，却要分好几段调用，缓存命中时不记；插件翻译和模型翻译共用一条记录；压缩和连接测试本来就不记审计。
- **`runSkill` 和撰写辅助（`AssistSkill`）也走 `complete`**：没有采用。`runSkill` 把文本流式交给界面，翻译还要先扣住哨兵文本；撰写辅助走的是对话的任务和提案（`claimRun`、`drive`、`job.host`）。为了它们，`complete` 得再提供一个事件通道和一个 host 回调，interface 会变宽，而这两处都只有一个调用方。
- **`Raw` 的结果用一个不是 string 的类型，只能 `Unmarshal` 之后回填**：没有采用。即时回复要先自己剥掉代码围栏，判断的答案在 `ai` 包里解析，都套不进去。
- **另建一个 `completion` 结构做参数**：没有采用，直接复用 `ai.Request`，`complete` 强制 `MaxTurns = 1`。

## Consequences

- 新增一次性 AI 功能时，用 `complete`，不要自己调 `e.Run`；需要审计就在调用 `complete` 之前记。
- 判断「插件翻译还是模型翻译」看的是 `Settings.Translator`，不能看运行时用的模型引用：插件模型的引用 `plugin/<名字>/<id>` 也带 `plugin/` 前缀（[0020](0020-plugin-models-host-sdk.md)）。撰写辅助曾因此给插件模型的运行多记一条 `plugin:<名字>/<id>/translate` 审计。
