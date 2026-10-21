# macOS 的 disable-library-validation 加在主程序上

macOS 正式包以 Developer ID 签名并开启 hardened runtime，这时库验证只放行同一 Team ID 签名的库，第三方原生插件必然被拒。实测 `com.apple.security.cs.disable-library-validation` 必要且充分。因为插件子进程就是 `godwit plugin-host`，和主程序是同一个可执行文件，v1 把这个 entitlement 加在 `godwit` 主程序上。

## Consequences

主进程也不再校验它加载的库是谁签的：能写入 Godwit 安装目录的本机程序，可以换进一个 ad-hoc 签名的库，继承 Godwit 获得的系统权限（通知等）。钥匙串不受影响，因为 go-keyring 在 macOS 上通过 `/usr/bin/security` 读写，同一用户的任何进程本来就能读到。名单外的插件由 `Snapshot` 逐字节复制成新文件，不带隔离标记，所以不经过 Gatekeeper，替代它的是风险提示和按哈希授权。

## Considered Options

做一个只负责加载的小程序 `godwit-plugin-host`，放进 `resources/<platform>/bin/`，用 mygo 的 `macos.helperEntitlements` 只给它这个 entitlement，主程序保留库验证。代价是多一个 `cmd/`，发布时每个平台多一次 `go build`。要收紧时改成这个方案。
