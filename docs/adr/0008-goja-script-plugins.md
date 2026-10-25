---
status: superseded by [0024](0024-pi-moejs-agent-runtime.md)
---

# 脚本插件用 esbuild + goja 运行

脚本插件的 TS 由 Go 侧的 esbuild 库编译成 ES2022（降级 async generator 和 for-await），再在 goja 里运行：纯 Go、可以 `Interrupt` 抢占、默认不暴露任何全局对象。每个插件一个 goja 实例，由一个专属 goroutine 驱动。goja 没有单实例内存上限，用堆用量看门狗加宿主侧的数据大小上限代替；需要硬上限时再改成子进程运行。

## Considered Options

- **QuickJS-on-wazero**（2026-10-09 原型对比后放弃）：`fastschema/qjs` 已停止维护、和新版 wazero 不兼容，死循环会卡住整个进程，而且默认挂载工作目录。
