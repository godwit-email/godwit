# Go 程序纯 Go 构建，不用 cgo

Godwit 的 Go 程序（`core` 和 `godwit` 二进制）都在 `CGO_ENABLED=0` 下构建和测试，这样交叉编译和发布不需要各平台的 C 工具链。需要原生库的地方都走 purego 或 `syscall.LoadLibrary`：SQLite 用 mygo 的 sqlite 插件（purego 加载、开启 FTS5），钥匙串用 go-keyring，原生插件宿主用 purego `Dlopen`（见 [0015](0015-native-plugins-trusted-subprocess.md)）。

这条只约束 Go 代码：网页界面用 React/TypeScript（见 [0012](0012-native-shell-web-content.md)），平台桥接可以用对应平台的语言（如 `native/` 下的 Swift），只要它们不让 Go 构建依赖 cgo。

## Considered Options

- **pimalaya**（Rust 邮件库）：没有 C ABI，接入要么用 cgo，要么单独跑一个进程。
- **DuckDB**：面向分析查询，不适合邮件这种频繁读写单条记录的场景；Go 驱动也需要 cgo。
- **Go `plugin` 包**：不支持 Windows，且要求插件和主程序用同一个 Go 版本编译。
