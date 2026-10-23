---
status: accepted
---

# 对话存在 SQLite，每条消息一行，不用 JSONL

Wit 的对话（消息、run、审批、引用、摘要）留在 `godwit.db`：消息不再是整段重写的 `conversations.state`，而是每条一行，按对话内只增不减、永不复用的序号排列。草稿改动、审批完成和审计行要在同一个事务里提交（[0004](0004-permission-gateway-taint.md) 的至多一次靠它），把消息或审批搬进文件就是把这个事务拆到两个存储里。

## Considered Options

- **每个对话一个 JSONL 文件**（Claude Code、Codex、pi 的做法）：它们的待审批只在内存里，进程退出就当取消，工具效果也不在另一个需要原子提交的库里，所以文件只追加就够。要跨重启保留审批的系统都放在数据库里；Goose（#4648）、opencode（#10597）、nanobot（#5943）、OpenClaw（#98236）都是从文件迁到 SQLite。Claude Code 0.2.113 从 SQLite 迁到 JSONL，是因为 better-sqlite3 原生模块装不上（anthropics/claude-code#978），Go 和 mygo 自带的 SQLite 没有这个问题。
- **只把消息放进 JSONL、审批留库**：要另做「文件写到第几行」的提交点和启动对账，调研里没有系统这样做。
- **保留整段 blob**：每次保存都重写整个对话，下标靠位置推算，回退要重写整段。

## Consequences

- 回退是删掉某个序号之后的行；序号由对话上的计数器分配，删行后不复用。
- 数据库改为 `auto_vacuum=INCREMENTAL`，空闲时分批 `incremental_vacuum`；已有的库在启动时 VACUUM 一次，空闲磁盘不足就留到下次。删除对话后，空闲空间够多（不少于 64 MiB 且占文件 25%）时交还给文件系统；不承诺不可恢复（不开 `secure_delete`）。
- 迁移是启动时的 Go backfill：逐个对话拆成行，坏的对话跳过并保留旧数据；迁移前复制一份 `godwit.db`，下一个版本删除。`user_version` 提升后，旧版本打不开新库。
