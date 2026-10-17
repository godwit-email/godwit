# Plugin models: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A plugin with the `models` capability publishes models. Godwit lists them in the default, small and judge model pickers, and runs them with the SDKs it already uses: no protocol code in Godwit or in plugins. Plugins only adjust the encoded request (headers, body), as aio-proxy plugins do with their `fetch`.

**Replaces** godwit-email/godwit#108. It keeps the user-facing design agreed there with #90 (model catalog, protocols, plugin judges, the `modelAssets` seam) and drops what only one upstream needed.

## Design

- **Models.** `listModels()` (JS) / `models.list` (native) returns `{id, name, transport: "http", protocol, baseURL}` or `{id, name, transport: "plugin", operation: "judge", assets?}`. Protocols: `openai-completions`, `openai-responses`, `anthropic-message` (generation), `typesafe-systemone`, `openai-decisions` (judge).
- **Requests go through the existing SDKs.** Generation: the Genkit instance of the protocol's kind (`compat_oai` on openai-go, `anthropic` on anthropic-sdk-go, Godwit's Responses plugin on openai-go/v3). Judges: `ai.NewJudge`. Each plugin model gets its own `http.Client`; its `RoundTripper` hands the request the SDK encoded, `{method, url, headers, body}`, to the plugin's `prepareRequest` (JS) / `models.prepare` (native) when it has one, and sends what comes back. The response goes back to the SDK untouched, so streaming, tools, approvals and history work as for any service.
- **Egress.** A JS plugin's prepared request must be https to a host in `permissions.net`, through the plugin network client (`plugins.Client`: own DNS, no private addresses, no proxy); redirects are not followed. Each such request is audited (`plugin:<name>:models`, host and port only), as `net.fetch` is. A native plugin runs with Godwit's rights; its requests use the model client, as a configured service does.
- **References** are `plugin/<name>/<model id>`, as `Settings.Translator` names a plugin: `DefaultModel`, `SmallModel` and `JudgeService` accept them. No slug table, no settings migration. A model service can no longer get the slug `plugin`.
- **Plugin judges** (`transport: "plugin"`): one question per call, one call at a time per plugin (queued callers can cancel), the watchdog counts from dispatch. Assets go through the `modelAssets` seam (#108 design, 8.2); this PR ships its `update_required` default, #90 the real one. Before a judge call a model with assets is verified and `models.load`ed once per process.
- **Errors.** `model_not_ready`, `model_unavailable` and the asset codes are `ai.ErrJudgeUnavailable`; a crash, a timeout or a bad answer is `ai.ErrJudgePlugin` and counts as a plugin failure; `invalid_request` (an input the model cannot take) is per message, like an HTTP 400: the label classifier moves on and keeps that message's labels.
- **Not in this PR** (each needs its own case): body/tool validation of prepared requests, host-made session ids, a persistent catalog, the Decisions judge on openai-go/v3's `Decisions` API.

## Tasks

### Task 1: Capability, SDK types, registration
- [x] `knownCapabilities` gets `models`; `sdk/godwit.d.ts` gets `ctx.models.register({ listModels, prepareRequest?, judge? })` and the model types.
- [x] `core/plugins`: `registerModels` (JS, init only, once, capability required), `Plugin.ListModels / PrepareRequest / Judge`; native `describe` may list `methods`.
- [x] `plugins.ProviderModel` with `ValidateModels`. Tests: registration rules, validation.

### Task 2: Catalog
- [x] `Service.ListPluginModels(ctx)`: every running plugin with `models`, its list cached per running instance and config version, a failing plugin reported with its error and not hiding the others.
- [x] Tests: JS and native plugins list; a plugin that throws; refresh after a settings change.

### Task 3: References and HTTP models
- [x] `ai.ResolvedModel.Client`; the engine uses it and skips its version check for it.
- [x] `resolveRef` resolves `plugin/<name>/<id>`; `pickSlug` never gives `plugin`; settings accept plugin refs.
- [x] `pluginModelTransport`: prepare, egress check, send. `resolveJudge` for HTTP judge protocols.
- [x] Tests (TLS test servers): each generation protocol end to end through the SDK with a header and a body changed by the plugin; a host outside `net` refused; a stopped plugin's ref fails without falling back.

### Task 4: Plugin judges
- [x] `pluginJudge`: admission, one question per call, assets seam, `models.load`, error mapping, plugin failure accounting; `unanswerable` covers `invalid_request`.
- [x] Tests: answers through a native fake; queued cancel; bad answer counts; `model_not_ready` stops the round and keeps labels; `invalid_request` moves on.

### Task 5: Settings UI
- [x] Default, small and judge pickers list plugin models by plugin; a stopped plugin's selection shows as unavailable.

### Task 6: Verification
- [x] `go test ./...`, race on core, vet, gofmt; open the PR.
