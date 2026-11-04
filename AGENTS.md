## Agent skills

### Issue tracker

Issues live in GitHub Issues for `godwit-email/godwit`, managed with the `gh` CLI. See `docs/agents/issue-tracker.md`.

### Triage labels

The five default triage labels: `needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: one `GLOSSARY.md` and `docs/adr/` at the repo root. See `docs/agents/domain.md`.

## Model-facing interfaces

When adding or widening anything a model or extension can call (a Wit tool, a plugin host API or permission, a `godwit mcp` tool, an `mcp`-role method), read ADR 0003 and 0004 first, then hold the change to these rules. Terms follow `GLOSSARY.md`.

- **A name is a promise.** A permission, role or tool name, and the description the model sees, cover everything the call can do, worst case included. A change that does more than its name says gets a new name or a split, plus an update to the allowlist's doc comment (`mcpMethods` in `rpc/roles.go`, `Permissions` in `core/plugins/manifest.go`, `askTools` in `core/wit.go`).
- **Decide egress up front**, and say in the PR whether the change can cause it. Egress is mail or data leaving the machine: a send, or a request to a host. It counts when it happens later without the user, so creating or changing persistent state that egresses unattended (a recipient or URL in a rule, a destination in plugin config, a send setting, a grant that skips future approvals) is egress too.
- **Egress goes through an approval** whose card names the recipients or destination host. A built-in tool declares its hosts in `toolDesc.Egress`, so the approval and audit carry them. 本次对话都允许 (`sessionTools`) grants only tools with no destination.
- **`full` gets every new `core.Service` method.** An exported method is callable by the user's programs unless `hidden` in `rpcbind/bind.go` keeps it off the wire; `rpcbind/testdata/full_methods.txt` pins the result so the choice shows in review. A method that sends or egresses also goes into `audit` there (ADR 0003).
- **Sending stays with the user.** Wit and plugins write drafts; `godwit mcp` sends only through `RequestMCPSend`, under the user's 通过 MCP 发送 setting.

## IMAP server quirks

Fix an IMAP server's non-standard behavior in the go-imap fork ([godwit-email/go-imap](https://github.com/godwit-email/go-imap), branch `godwit`), not with a wrapper or workaround in Godwit: a fix in the library covers every caller and STARTTLS connections, and new code cannot miss it. Push the fork commit to `godwit`, tag it with the next `v2.0.0-beta.8.godwit.N` (cloud agent sessions cannot push tags: ask a maintainer to push it), and point the `replace` in `go.mod` at that tag. Keep a Godwit-side test that reproduces the server's behavior (e.g. `TestToParsedReducedHeaderEcho` in `core/mailsync`), so a later bump that loses the patch fails here.
