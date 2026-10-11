# Review: docs/superpowers/plans/2026-10-12-model-capabilities.md

Read-only review against the code at feat/ui-align. Plan line numbers are written `plan:N`.

## High

1. **Kind and GenFormat are two sources of truth, and `prepare` still runs on Kind.** The plan has `Kind` "stays as stored, runtime reads GenFormat" (plan:117). But all of the following run on Kind:
   - validation and URL normalization: `ai.ValidKind(in.Kind)` and `NormalizeBaseURL(in.Kind, …)` (core/models.go:123-127);
   - the key-keep guard: `old.Kind != in.Kind` → ErrKeyNeedsConfirm (core/models.go:165);
   - the list endpoint and headers: `ListModels(…, m.Kind, …)` (core/models.go:396, models_check.go:34);
   - the request-URL preview: `ai.Endpoint` (models_check.go:33);
   - the Genkit instance switch: `instance()` (core/ai/runtime.go:161-176);
   - the Local API input: `contract/api.go:40` `CheckModelService(…, core.ModelServiceInput)`.

   Two columns will drift. When a format changes, the guard compares a stale Kind and silently weakens.
   **Fix:** keep one column. `kind` *is* the generate format: `openai`=chat, `openai-responses`=responses, `anthropic`=messages, and `""`=generate off. GenFormat becomes only a UI label mapping. Add only the columns `provider`, `judge_format` and `judge_model`. No data migration of Kind is needed, and Review Focus 1 holds by construction.

2. **One base URL cannot serve messages and a judge format.** For anthropic, `NormalizeBaseURL` strips `/v1` (core/ai/baseurl.go:39-41), and `Endpoint` adds `/v1/messages` (:58). Take the design's add-other state: OpenRouter `https://openrouter.ai/api/v1` with `messages` plus `systemone` (design preset `add-other`). It stores `https://openrouter.ai/api`, so a judge at `base+"/systemone"` hits `/api/systemone`, which is wrong.

   Judge-only services hit a related problem. TypeSafe has no generate kind, and `NormalizeBaseURL` returns "unknown service kind" (:26).

   The suffix lists also lack `/decisions` and `/systemone`. Pasting the full endpoint isn't stripped.

   **Fix:** in Task 2, store the URL as the user gave it, normalized once and kind-independently: scheme check, trailing slash removed, and known suffixes stripped, including `/decisions`, `/systemone` and `/messages`. Derive per-format endpoints from it:
   - `messages` → if the base ends in `/v1` use `base+"/messages"`, else `base+"/v1/messages"`, and give the anthropic SDK the base minus `/v1`;
   - `decisions` → `base+"/decisions"`;
   - `systemone` → `base+"/systemone"`.

   Add `core/ai/baseurl.go` to Task 2's files. Add tests for the OpenRouter+messages+systemone case and for typesafe.

3. **Judge-only services cannot be saved, and the app would panic on the first save.**
   - `AddModelService` and `UpdateModelService` return `errNoModels` when `len(in.Models)==0` (core/models.go:230, 287).
   - `app/settings_models.go:268` does `saved.Models[0]` when DefaultModel is empty. Adding only TypeSafe (a design state, `add-typesafe`) would hit both.

   **Fix:** require models only when generate is on, and require JudgeModel when judge is on. Require at least one capability on. In the app, set DefaultModel only when `len(saved.Models)>0`. Add a test for "TypeSafe only".

4. **`TestCapability` for unsaved input can't run generation through the Engine.** `instance()` calls `e.Current(ctx, m.ServiceID)` and returns errStale when there is no row (core/ai/runtime.go:141-145; `Current` comes from core/models.go:463-469). The existing probe `TestModelService(ctx, id)` needs a saved id (core/models.go:416).

   The plan's signature `TestCapability(ctx, in, capability)` (plan:112) also has no `id`. That makes KeyKeep impossible: `prepare` needs the id to read the stored key (core/models.go:145-155), and persisting needs to know which row.

   **Fix:** use `TestCapability(ctx, id int64, in ModelServiceInput, cap string)` like `CheckModelService`. For unsaved generation, run on a throwaway `ai.NewEngine` with `Current=nil` that is closed afterwards, or relax `Current` for ServiceID 0. Put that change in Task 2's files (`core/ai/runtime.go`). Retire `TestModelService` and update `app/settings_models.go:392` and its tests in the same task.

5. **The persisted `CapStatus.Err` is raw error text.** Model services echo keys and bodies in errors. See `core/models_check_test.go:50` ("its body echoes the key"). A TypeSafe 422 body names the offending field and can echo `state`, which is mail. The engine redactor only knows keys of instances it created (`e.keys`, runtime.go:177-181), so a judge-only service's key is never registered.

   **Fix:**
   - Store a code, not text: `CapStatus{State, LatencyMs, HTTPStatus int, Code string /*auth|forbidden|no_list|timeout|unreachable|bad_answer*/, At}`. Render the design's copy from the code via i18n, for example `401 · API Key 无效` and `403 · 这把 Key 没有开通 Decisions API` (design `add-test-failed`).
   - Judge clients never put response bodies or `*url.Error` text into errors. Copy `ListModels` (core/ai/listmodels.go:45-53).
   - Add a test that a key and a body sentinel never reach `CapStatus` or the log.

## Medium

6. **There is no rule for a gen-off service that is still the default.** `resolveRef` only checks `svc.Models` (core/models.go:556-559). "GenFormat '' = off; Models stays" (plan:105-106) means DefaultModel and SmallModel keep resolving to a service the user switched off. `dropRefs(slug, saved.Models)` (app/settings_models.go:272, 287-299) keeps them, because Models didn't change.

   The design's `offWarnText` promises a switch: "改用 <alt> / <first model>" (design renderVals `alt`). The plan only shows the text (plan:214).

   **Fix:**
   - `resolveRef` returns ErrModelNotFound when the generate kind is `""`.
   - On save with generate off, the app re-points DefaultModel to the design's `alt`: the first other service with enabled models, its first model. Otherwise it clears DefaultModel.
   - SmallModel is cleared.
   - Test both.

7. **`Settings.JudgeModel` as `"slug/model"` doesn't fit `resolveRef`, and goes stale on edit.** A judge model is not in `svc.Models`, so `resolveRef`'s found-check (core/models.go:556) would fail. A service has exactly one judge model, so changing jev-latest to jev-preview orphans the ref.

   **Fix:**
   - Store `Settings.JudgeService` (a slug), or keep `slug/model` but resolve it against `judge_format`/`judge_model` with a separate resolver.
   - When the service is missing or has judge off, fall back to the generation judge, as `resolveTask` does for SmallModel (core/models.go:500-505).
   - Extend `dropRefs` (app/settings_models.go:290) and `Settings.validate` (core/settings.go:64).
   - Say whether the first judge-capable service saved becomes the default, as the first generate service does (app/settings_models.go:267-269). The design's normal view has the judge on OpenAI.

8. **The generation fallback with JSON Schema isn't realistic as written.**
   - `ai.Request` has no output-schema field (core/ai/types.go:106-114).
   - The hand-written Responses plugin declares no `Constrained` support (core/ai/responses.go:46), so Genkit would only simulate it in the prompt.
   - `Generator` (plan:82) is undefined.

   **Fix:**
   - Define `type Generator func(ctx context.Context, system, prompt string) (string, error)`. In core it wraps `Engine.Run` with MaxTurns 1 and no tools, like `LLMComplete` (core/pluginhost.go:215-243).
   - Ask for a JSON object in text. Parse it leniently (strip code fences, take the first `{…}`), then validate strictly with the shared validator from Task 1.
   - Document that fallback probabilities are 0/1 (yes/no → 1/0) and that scores are integer levels.
   - Wrap `state` with `ai.Untrusted` and use `UntrustedSystemNote`. In Wit, `item.Block` is already wrapped, so don't wrap it twice.
   - Tests: prose answers, a fenced answer, a missing name, an extra name, a label outside the set, a score above the levels, and a refusal string. Run them for all three formats against the httptest model.

9. **The `classify_mail` budget would silently starve messages.** `mailContext` shares `Total: 48000` runes across all ids and cuts older bodies to 0 (core/mailcontext.go:31, 101-103). With 50 ids, most messages reach the judge as headers only, and the answers look valid but are wrong.

   **Fix:** call `mailContext(ctx, scope, []int64{id}, mailBudget{PerMessage: N, Total: N})` per message, with N about 8000 runes, which also stays under TypeSafe's 32k state+question limit. Bound the whole tool call:
   - a total deadline;
   - concurrency of about 4;
   - fewer ids (about 10) when the judge is the generation fallback, which costs one generation call per message.

   Note: the plan writes `mailContext(scope, ids, budget)`, but the real signature takes `ctx` first (mailcontext.go:64).

10. **`llm.decide` budgets are unspecified.** `llm.complete` only caps bytes (core/plugins/host.go:430-442), and `h.do` adds no limit (core/pluginhost.go:43-50). With a 128-per-request split (plan:52), one plugin call can fan out into unbounded requests.

    **Fix:**
    - Per call: at most 128 questions, rejected beyond that rather than split.
    - Unique names matching a regex such as `^[a-z][a-z0-9_]{0,40}$`.
    - Choices 2–255 and levels 2–10.
    - State plus all instructions ≤ `MaxLLMBytes`.
    - A per-plugin concurrency limit such as a semaphore of 2.

    Also update the consent copy. `sdk/godwit.d.ts:174` says "runs on the user's chosen model service", but `decide` may send mail to a different vendor (TypeSafe). Change that comment and the `pm.LLM` description (app/settings_extensions.go:337-340) while keeping the hash.

11. **Status persistence races with edits and versioning.**
    - "When the service is saved, it persists the status" (plan:116) has no guard against a test that started before an edit to the URL, key or format.
    - Writing status through `UpdateModelService` bumps `version` and calls `forgetModel` (core/models.go:323-331), which kills in-flight runs (errStale).

    **Fix:** keep status in its own table `model_service_status(service_id REFERENCES … ON DELETE CASCADE, cap, …)`. Write it only `WHERE` the service's `version` equals the version the test started on. Clear rows in the same transaction as an Update that changes the URL, key or the cap's format. Add a race test.

12. **Task boundaries break the build.**
    - Task 2 changes `ModelService` and `TestModelService` and runs only `go test ./core/...` (plan:126). `app/settings_models.go` uses `testService`, `ai.Presets` (through `ModelPresets`, models_check.go:51) and `kindLabel`.
    - "The old preset list goes" (plan:40) breaks `app` and `core/ai/baseurl_test.go` `TestPresets` until Task 5.

    **Fix:** each task ends green on `go build ./... && go vet ./...`. Remove the presets in Task 5, or keep the old list until then. Make the minimal app call-site updates inside Task 2.

13. **Interface inconsistencies between tasks.**
    - The plan's `ErrKeyInvalid` (plan:90) duplicates the existing `ai.ErrModelAuth` (listmodels.go:81). The app keys "Key 失效" off ErrModelAuth (app/settings_models.go:405), and `rpcbind/bind.go` maps errors by sentinel. Reuse ErrModelAuth, and add a distinct `ErrModelForbidden` for 403 because the design shows a separate copy for it.
    - `func (format) Path()` needs a named type: `type JudgeFormat string`.
    - `NewJudge` has no `*http.Client` parameter, so core can't pass `s.modelHTTP` (core/service.go:57), which tests swap. It also has no clock or sleep hook, so a 429-retry test with backoff would sleep for real. Add `NewJudge(c *http.Client, format JudgeFormat, base, key, model string)` and a package-level `sleep` var for tests.
    - `ResolveJudge` is exported, but sibling resolvers are unexported (`resolveModel`, `resolveTask`). Make it `resolveJudge` unless the app needs the label, in which case expose `JudgeLabel`.

14. **The plan's claim that the Local API doesn't need contract changes is wrong.** `CheckModelService(ctx, id, core.ModelServiceInput) (core.ServiceCheck, error)` is part of the Local API (contract/api.go:40, 246-247), and `contract/scenarios_test.go:235-256` checks its parity. New `ModelServiceInput` fields change the RPC shape, and `Settings.JudgeModel` becomes writable over `SetSettings`. **Fix:** list `contract/scenarios_test.go`. Make sure judge-only input through `CheckModelService` returns a defined result rather than "invalid service kind".

15. **"No-proxy client" doesn't exist.** The engine's client clones `http.DefaultTransport` (runtime.go:84), which honors proxy environment variables, and `NoRedirectClient` does too (listmodels.go:87). **Fix:** say "reuse `s.modelHTTP`/`NoRedirectClient` (no redirects)", or add a proxy rule deliberately for all model traffic, not just the judge.

16. **The 15 s timeout × 3 retries per batch is unbounded across batches and messages.** **Fix:**
    - one context deadline per `Ask` call (15 s total including retries);
    - honor `retry-after`, capped at about 2 s;
    - a separate overall deadline for `classify_mail` (see 9).

## Low

17. **Steering by mail is mis-specified (plan:64, 150).** In Wit, the model writing `questions` has usually read mail already, so mail *can* steer the questions indirectly. What actually holds:
    - the judge's state is only mail, wrapped untrusted;
    - questions come only from tool arguments;
    - answers are typed (a label from the given set, a probability or a score), so no free text flows back.

    **Fix:** rename the test to "answers carry no mail-derived free text; state never appears in questions". Also state that `classify_mail` has no `Egress` func, so judge traffic is treated like model traffic (core/conversations.go:441), and record that as a decision.

18. **`Provider` isn't validated against formats.** **Fix:** `prepare` rejects any format the provider doesn't list (the plan:33-38 table). Provider is fixed after add, because the design shows the provider tiles only on add.

19. **`ListJudgeModels` sources follow the design's `decList`.**
    - Return nothing unless there is a key or the provider is `other`.
    - openai: hardcode `gpt-6-luna` (research §1: the listing is unverified).
    - typesafe: `GET /v1/models`, falling back to the design's three ids.
    - other: the static `FORMATS[fmt].models`. Don't GET `/models` on gateways, which would list hundreds of chat models.
    - Copy: `已从 /models 拉取 N 个判断模型。` or `填好 API Key 后会从 /models 拉取判断模型。`, plus the disabled select with `等待拉取`.

20. **Design `FORMATS.decisions.path` is `''`, but the plan's `Path()` returns `/decisions`.** The plan is correct per the API. Note it as an intended deviation so screenshot comparison doesn't "fix" it.

21. **The anthropic display URL:** the design's anthropic URL is `https://api.anthropic.com/v1` with `/messages`, but the code stores it without `/v1` (baseurl.go:71, 39-41). `canReset` (`!custom && url !== P.url`) would then always show 恢复默认 when editing an Anthropic service. This is solved by 2: store the URL as entered.

## Design `renderVals()` behaviour the plan misses or blurs

22. **Rules to implement exactly:**
    - **`blocked`** = `!name || !url || !anyCap || (add && !custom && !key)`. Edit never needs a key, and `other` never needs one. Neither `blocked` nor the design checks "generate on with zero models", but core requires them (finding 3). Add that to `blocked` and add a `hint` string with an en counterpart.
    - **`hint` order:** 至少开启一种能力 → 填上地址 → 填上 API Key 才能添加 → 填上名称 → 有一项没通过测试，仍然可以添加/保存，之后再改.
    - **Save must not block on a failed test.** Today's save probes first and toasts "settings.models.pick" (app/settings_models.go:215-228). State whether the probe-before-save stays.
    - **`cantTest`** = `!anyCap || (!custom && !hasKey) || !url || testing`.
    - **Status clearing:** a URL change, a key change or a format pick clears **both** `tg` and `td` in the dialog. The plan's "that capability's status" (plan:118) is right only for the persisted format change.
    - **Defaults (`blank`):** judge is on by default for openai and typesafe and off for other. `decModel` defaults to `FORMATS[fmt].models[0]`. A format pick resets `decModel`.
    - **`onKey`:** fetching fills the models with only the first one selected (`on: i === 0`), and fetch=ok shows `已从 /models 拉取 N 个模型，点一下选中。`. 重试 merges new ids as unselected.
    - **`nameAuto`:** only for `other`, and only until the user types a name. A preset sets the name only while `nameAuto` is true. The URL host is looked up in `HOSTS`, else the raw host. The 本地网关 preset URL contains the literal `[PORT]`. Define the real fill: today's `addLocal` uses `http://localhost:` (app/settings_models.go:193-196). Empty-state 连接本地网关 sets the name 本地网关.
    - **Off warnings** show only when editing the service that is the *current default* for that capability and the capability is supported. The generate text depends on whether an `alt` exists, and the save must apply it (finding 6).

23. **用途 card details:**
    - Options are formatted `<service name> / <model>`.
    - When no generate model exists, both selects are disabled and show `先添加一个服务`. The judge select is disabled too, even if a judge service exists.
    - Status texts:
      - generate: `未配置，这些功能暂时不可用`;
      - judge: `未配置`;
      - judge fallback: `用 <svc> / <model> 回答判断题，约慢 10 倍`, amber ("slow");
      - OK: `连接正常 · <ms> ms` from the persisted latency.
    - The alert text appends `，判断也一样。` when the judge default is also broken.
    - `suggest` = there is a generate default and no judge default.
    - Undefined: what the line says when the default is untested. Pick `<svc> · 未测试` or hide the latency.

24. **Service list chip:**
    - `untested` uses an amber dot (#FD9A00), but today's `statusLook` is grey (app/settings_models.go:446-455), so it needs a token.
    - Define how the chip derives from two capability statuses: fail if either is an auth failure, untested if any enabled one is untested, else OK.
    - Capability chips appear only when the service has enabled models or a judge model. ✓ means the service is the *current default* for that capability.

## Missing tests per Review Focus

25. **Review Focus 1 (migration):** use old rows for every old preset kind, including Anthropic stored without `/v1`, Ollama on loopback, and a slug-backfilled name. Assert `resolveModel`, `resolveTask(small)` and `TaskModel` give the same `ResolvedModel` (Kind, BaseURL, Model) before and after. Assert the inferred provider and that judge starts off.

26. **Review Focus 2 (answer validation):** one table shared by all three adapters, covering:
    - an out-of-set label;
    - a probability outside 0..1;
    - a score below 0 or above levels−1;
    - a missing or duplicate answer name;
    - an answer type different from the question's;
    - a refusal (OpenAI `type:"refusal"`; for the generation fallback, a prose refusal);
    - a truncated body or JSON over the cap (use LimitReader).

    NaN can't arrive in JSON. Test it only in the generation fallback's parsed numbers.

27. **Review Focus 3 (`classify_mail`):**
    - `scope.ReadMail()` is set after the call;
    - refs equal the `search_mail` refs for the same message (same citation number);
    - more than 50 ids is rejected;
    - every message gets its body (finding 9);
    - the judge state contains the `untrusted_email` wrapper;
    - a gateway row exists;
    - the fallback path works with `JudgeModel==""`.

28. **Review Focus 4 (test status):**
    - persisted per capability;
    - a URL change clears both statuses, while a format change clears only its own;
    - a stale test result isn't written (finding 11);
    - `CapStatus` holds no key or body text (finding 5);
    - a status write doesn't bump `version`.

29. **Review Focus 5 (dialog states):** screenshots alone won't catch logic. Add unit tests on the form's derived state for `blocked`, `hint`, `cantTest`, `nameAuto` with presets, the `offWarn`/`alt` choice, and `decList` gating. Only SettingsModels, SettingsModelsEmpty and SettingsModelsSheet have renders in `$D/dshots`. The other 12 must be rendered first with `$D/pp/shot.mjs`, so make that an explicit step at the start of Tasks 4 and 5.
