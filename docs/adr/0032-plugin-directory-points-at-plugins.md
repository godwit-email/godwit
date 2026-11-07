---
status: proposed
---

# 插件目录只维护指向，发布时合成 registry.json

`godwit-email/plugins` 里的 `registry.json` 把所有插件的版本和文件清单写在一起，插件的发版信息却由各自的仓库产生。改为仓库只维护一份目录：每个条目写展示信息，再用提交 SHA 指向插件自己维护的 `godwit-plugin.json`。CI 按提交 SHA 取回这些文件、校验后合成 `registry.json`，签名发布。应用读取和校验的方式不变（[0016](0016-signed-plugin-registry.md)）。

目录条目说明插件是什么，由审核方掌握：

```json
{
  "name": "zen-free",
  "title": { "default": "Zen Free" },
  "summary": { "default": "…", "zh-Hans": "…" },
  "category": "models",
  "publisher": "Godwit",
  "dataFlow": "third-party",
  "homepage": "https://github.com/godwit-email/plugins/tree/main/packages/zen-free",
  "source": { "repo": "godwit-email/plugins", "path": "packages/zen-free/godwit-plugin.json", "sha": "<完整提交 SHA>" }
}
```

`godwit-plugin.json` 说明插件发了什么，由作者维护，内容是 `name` 和 `releases`，格式和现在 `registry.json` 里的一样，包括每个文件的 URL、大小和 SHA-256。

## Considered Options

- **应用直接读插件 JSON**（`index.json` 加每个插件一个文件）：应用只凭提交 SHA 无法校验内容，只能相信 GitHub 和 TLS，而这份 JSON 决定了哪个二进制能通过校验，违反 0016「不依赖谁控制 CDN」。改用文件 SHA-256 又要多一层镜像和按插件缓存，还要同时发布两种格式。只有几个插件时不值得。
- **作者用自己的密钥签名，发版不走 PR**：发版更省事，但「已审核」不再成立，作者的密钥泄露就能给所有用户推送原生插件。
- **目录不放展示信息，全写进插件 JSON**：作者发新版时可以悄悄淡化风险说明。

## Consequences

- 发版分两步：作者发布包含新 `godwit-plugin.json` 的提交，再提 PR 改目录里的 `sha`。这个 PR 就是审核，PR 检查要显示合成结果相对线上 `registry.json` 的差异，审核的是这份差异。
- `releases` 要累积。安装中的旧版本如果不在列表里，应用会按撤销处理。
- `registry.json` 不再提交进仓库，是 CI 的产物；`updated` 由 CI 在合成时写入，不再手工修改。提交 SHA 要用完整的 40 位。
- 撤销：删掉目录条目，撤销整个插件；条目上的 `revoked`（版本列表）撤销单个版本，不需要作者配合。
- 某个插件的提交取不回时（仓库被删或改写历史），合成失败，所有发布都会卡住。这时删掉或改掉这个条目再发布。
- 不维护旧格式，也不做兼容层。URL 和 `schema` 不变，新字段放在插件条目上，旧版应用读不到它们，但照常收到列表和撤销，这不需要任何额外工作。
- `godwit-registry build` 负责合成，并要求每个条目都有 `category`、`publisher` 和 `dataFlow`。`CheckRegistry` 严格校验，所以插件仓库的 CI 要用包含这些字段的版本。
- 合成后的文件仍是一个，整体下载，上限 `maxRegistry`（4 MB）。接近上限时再考虑拆分。
- `name` 发布后不可变，改显示名只改 `title`。
- 对话框里的 README 直接从 `homepage` 对应的 GitHub 仓库读取，先试 `README.<语言>.md`，再试 `README.md`，不锁定版本，也不在审核范围内，界面要说明这一点。只在用户打开对话框时请求，不加载远程图片，内容不发给模型。风险说明以目录条目为准。
