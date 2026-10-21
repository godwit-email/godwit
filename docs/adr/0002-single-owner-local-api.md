# 一个数据目录只有一个拥有者，其他进程只能走本机 API

GUI 或 `--headless` 在打开数据库前取得数据目录里的排他锁 `godwit.lock`，持有到退出，后来的进程直接失败。其他进程（`godwit mcp`、`godwit call`、用户自己的程序）不能打开数据库，只能通过拥有者在 `godwit.sock` 上提供的 JSON-RPC 2.0 本机 API 访问，方法和 `core.Service` 一一对应。这样 SQLite、同步和操作队列始终只有一个写入者。

## Consequences

- 连接先用 `api-token`（每次启动重新生成，权限 0600）认证，并声明角色：`full` 可调用全部方法，`mcp` 只能用只读方法加草稿相关的白名单。
- 信任边界是同一个系统用户：token 防的是其他用户，不防同一用户的程序；`mcp` 角色是最小权限，不是隔离。
- GUI 里本机 API 默认关闭，要在「隐私与安全」里打开；`--headless` 下总是开启。
- GUI 只能通过 Service 访问核心，不直接读数据库，由 import 边界测试保证。
