<p align="center"><img src="assets/icon.png" width="128" alt=""></p>

<h1 align="center">Godwit</h1>

<p align="center">An open-source, AI-native email client you can extend.</p>

Godwit is a desktop email client for macOS, Windows and Linux. It works with any IMAP/SMTP account and ships with an AI assistant, Wit, that reads, summarizes, translates and drafts mail with the model you choose. Everything AI in it, the built-in features included, runs through the same extension points open to you: drop in a `SKILL.md` and a button appears.

The name comes from the bar-tailed godwit, which holds the record for the longest non-stop bird flight.

> **Status: 0.0.x, early preview.** Expect rough edges, and keep a copy of anything important on your mail server.

## Features

- **Mail**: IMAP/SMTP accounts with sync, offline changes, conversations, full-text search, and HTML compose with reply, forward, CC/BCC and drafts. Setup is detected for Gmail, Outlook (sign in with Microsoft), iCloud, Yahoo, AOL, QQ Mail, 163/126 Mail, Tencent Exmail, Feishu and DingTalk Mail, and any other IMAP server.
- **Wit**: an agent that answers questions about your mail, reads attachments (PDF, Office, CSV) and drafts replies. It never sends without your approval.
- **AI actions**: summarize, translate, adjust tone and length, and quick replies, on a message, a selection or a draft.
- **Your models**: any OpenAI-compatible (Chat Completions or Responses) or Anthropic-compatible service, including local ones like Ollama and LM Studio. API keys stay in the system keychain.
- **Extensions**:
  - **Skills**: a `SKILL.md` ([Agent Skills](https://agentskills.io)) in the data directory's `skills/` folder becomes a Wit skill, or an action button with `godwit-action-*` metadata.
  - **Plugins**: TypeScript plugins with declared permissions add hooks, actions, cards and Wit tools. Types are in [`sdk/godwit.d.ts`](sdk/godwit.d.ts), published as `@godwit/sdk` (`npm i -D @godwit/sdk`). Plugins can read mail and write drafts; none can send.
  - **Native plugins**: a shared library behind a small C ABI ([`sdk/native/godwit_plugin.h`](sdk/native/godwit_plugin.h)), for what a sandbox cannot do. It runs with all of Godwit's rights, so Godwit shows the risk and asks you to allow that exact file; a changed file needs your permission again.
  - **MCP servers**: give Wit external tools. Each server is approved once, and each write call every time.
- **For other agents**: `godwit mcp` is an MCP server that lets Claude, Cursor and other agents read your mail and create drafts, but never send. Turn on **Settings › Local API** and copy the client configuration from there.
- **Updates itself** from dl.godwit.email (the Microsoft Store version is updated by the Store).

## Install

Download the latest build from [godwit.email](https://godwit.email/#download):

- **macOS**: the `.dmg`, or with [Homebrew](https://brew.sh):

  ```sh
  brew install godwit-email/tap/godwit
  ```
- **Windows**: the installer, which installs for the current user without admin rights, or [Godwit on the Microsoft Store](https://apps.microsoft.com/detail/9NS3Q2CGWD12), which the Store updates.
- **Linux**: the `.deb`, or install for the current user (no root) with:

  ```sh
  curl -fsSL https://dl.godwit.email/install.sh | sh
  ```

## Command line

```
godwit [--data-dir DIR]                                the desktop app
godwit --headless [--data-dir DIR]                     serve the Local API without a window
godwit call [--data-dir DIR] METHOD [JSON_ARGS_ARRAY]  call the running Godwit once
godwit mcp [--data-dir DIR]                            an MCP server on stdio for agents
godwit dev-sync-db                                    replace the dev database from production (development builds only)
godwit --version
```

## Development

Godwit is written in Go on [MyGo](https://github.com/egoist/mygo): the main window is native UI, while compose, the Wit page and the message reader are React pages in `web/`. [mise](https://mise.jdx.dev) installs the pinned Go and Bun:

```sh
mise install
mise run generate    # install web dependencies and generate Go/TypeScript code before Go tests
mise run dev         # run the app, reloading on changes
mise run dev:sync-db # replace the dev database from production; stop both apps first
go test ./...        # Go tests
mise run web:check
mise run build       # package for this machine into dist/
```

Native and web UI translations share `internal/i18n/en.json` and `zh-Hans.json`, using ICU MessageFormat. Edit these catalogs, run `mise run generate` to update the Go and TypeScript accessors, and validate with `go tool mygo i18n check`. MyGo's generated Go/TypeScript accessors and client are ignored by Git; do not edit them directly. Development, packaging and `mise run web:check` / `web:test` generate them automatically; run `mise run generate` before using Go commands directly.

Development builds (`mygo dev` and `go run`) use a separate `Godwit-dev` data
directory beside production's `Godwit` directory (on Windows, `~/.godwit-dev`
beside `~/.godwit`). GUI, headless, `call` and `mcp` all use the matching default;
an explicit `--data-dir` still takes precedence. Release builds keep their existing
directory. `MYGO_ENV=production` selects production defaults for an unpackaged binary.

To start dev with production mail and settings, quit both instances and run
`mise run dev:sync-db`, then `mise run dev`. This **overwrites the dev database**.
It copies committed WAL data, validates and migrates only the copy, and leaves
the production database untouched. It refuses to run while either directory is
owned by a running instance. A database already upgraded by an older dev run is
not downgraded by this command.

The sync copies only the database. Dev gets a fresh keychain identity, so enter
account credentials and model/plugin/MCP secrets again in dev. Cached attachments
are downloaded again; draft attachments and outbox files are not copied, and their
file references are cleared to avoid using production or stale dev files. Re-add
draft attachments before sending from dev. Skills and plugin files are not
copied. After you sign in, dev can sync and change the same remote mailbox.

Layout:

| Path | |
|---|---|
| `core/` | the mail engine and service: sync, storage, send, AI, plugins, skills |
| `app/` | the native UI |
| `web/`, `webui/` | the web pages and their Go side |
| `cmd/godwit/` | the executable: GUI, headless, `call`, `mcp` |
| `mcpserver/`, `rpc/` | the MCP server and the Local API |
| `sdk/` | plugin types |
| `docs/` | design specs and plans |
| `site/` | [godwit.email](https://godwit.email), a Cloudflare Worker (`mise run site:dev`) |

The homepage's demo window and main download button share browser-platform detection. macOS shows traffic lights; Windows shows the app caption and right-hand window controls; Linux also uses right-hand controls. The window controls are decorative, not actions that close or resize the website.

Client CI always runs a lightweight changes check, but skips the desktop matrix and embedded-web checks when all changed paths are under `site/`. The separate Site workflow checks the website and deploys it from main. Shared configuration changes, such as `mise.toml`, and mixed site/client changes still run client CI.

Commit messages and PR titles follow [Conventional Commits](https://www.conventionalcommits.org); [release-please](https://github.com/googleapis/release-please) turns them into versions and the changelog. Merging its release PR builds every platform and uploads the installers and updates to the R2 bucket behind dl.godwit.email; each platform's update manifest goes up last, and installed apps update from it as soon as it is there.

Commits whose changed files are all under `site/` are excluded from the application's version calculation and changelog by release-please's `exclude-paths`. Mixed changes, including a site change together with the root `README.md`, still participate in application releases.

## License

[Apache-2.0](LICENSE)
