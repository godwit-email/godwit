# Judge APIs research: OpenAI Decisions vs TypeSafe System One (Jev)

Researched 2026-10-11. [O] = official doc, [S] = secondary. Domain warning: the real vendor is typesafe.ai (docs.typesafe.ai, console.typesafe.ai). thejevai.com is an independent, unaffiliated lookalike with a different endpoint (per a Hugging Face guide, [S]); never send keys there.

## 1. OpenAI Decisions API

Sources: [O] https://developers.openai.com/api/docs/guides/decisions ; announcement https://community.openai.com/t/decisions-api-is-now-available-in-public-beta/1403877 ; reference page https://developers.openai.com/api/reference/resources/decisions/methods/create (found by search, not fetchable here; limits below from its search snippet [S]).

- Endpoint: `POST https://api.openai.com/v1/decisions` [O guide]. Public beta since 2026-10-06; GA "in coming weeks".
- Auth: `Authorization: Bearer $OPENAI_API_KEY`, `Content-Type: application/json` [O].
- Model: only `gpt-6-luna` [O].
- Request:
  - `model` string, required.
  - `input` string OR array of user messages with parts `input_text` / `input_image`, required. Images must be inline base64 data URLs per the guide ("HTTP(S) URLs and file_id not supported" [O guide]); the reference snippet claims public URLs and max 128 images [S] - conflict, trust the guide, test.
  - `questions` array, required; each has unique `name`, `instructions` (string, up to 1,048,576 chars [S ref snippet]), `type`, plus:
    - `predicate`: nothing else.
    - `choice`: `choices: [{value, description}]`, 2..255, unique values [S ref snippet].
    - `score`: `levels: [{label, description}]` ordered lowest to highest; count limit not found.
  - Question count limit: NOT verified. The brief says 200; guide states none; sys1grep issue reports 90 accepted (https://github.com/uehaj/sys1grep/issues/213 [S]); LiteLLM documents 1..128 for its route (https://docs.litellm.ai/docs/decisions [S]). Treat 128 as the safe client cap and batch above that.
  - LiteLLM also mentions optional `safety_identifier` [S].
- Response: `{answers: [...], usage}`, answers in request order, echo `name` and `type` [O]:
  - predicate: `{type,name,probability}` (0..1)
  - choice: `{type,name,choice,probabilities:[{value,probability}],confidence}`
  - score: `{type,name,score,probabilities:[{value,label,probability}],confidence}`; `score` = probability-weighted mean of 0-based level indices (fractional) [O]
  - refusal: `{type:"refusal",name}` - must be checked first [O]
  - usage: input_tokens, input_tokens_details{cached_tokens,cache_write_tokens}, output_tokens (0), total_tokens [S: sys1grep issue]
- Errors: not documented on the guide. Assume standard OpenAI `{"error":{message,type,param,code}}` with 400/401/429/5xx (inference, unverified).
- Models listing: nothing documented for decisions; the gpt-6-luna model page (https://developers.openai.com/api/docs/models/gpt-6-luna) lists only chat/responses/batch endpoints and omits decisions. Whether `GET /v1/models` includes it is unverified; hardcode `gpt-6-luna`.
- Latency/limits: "about 10x faster than Responses" [O]; measured 0.2-0.45 s warm, 2.8 s cold [S sys1grep]. Model page rate limits (Build 5,000 RPM / 2M TPM etc.) are for luna generally; decisions-specific limits not documented. $0.10/1M input tokens, no output charge [O]. Context 1.05M tokens.
- Streaming: not mentioned [O]; LiteLLM says unsupported [S]. Assume none.
- Curl [O-derived]:
```
curl https://api.openai.com/v1/decisions -H "Authorization: Bearer $OPENAI_API_KEY" -H "Content-Type: application/json" -d '{"model":"gpt-6-luna","input":"I was charged twice.","questions":[{"type":"predicate","name":"refund","instructions":"Is the customer asking for a refund?"}]}'
```

## 2. TypeSafe System One (Jev)

Sources: [O] https://docs.typesafe.ai/api.md , /introduction/quickstart.md , /models.md , /primitives/{choice,score,noul}.md , /sdk/python/api/exceptions.md ; index https://docs.typesafe.ai/llms.txt.

- Endpoint: `POST https://api.typesafe.ai/v1/systemone` [O].
- Auth: `Authorization: Bearer <API_KEY>` (keys at console.typesafe.ai/keys) [O].
- Models: `jev-latest` (default alias), `jev-preview` (currently also 1.13.0), `jev-1.13.0` (only real build) [O /models.md]. Response `model` reports resolved version.
- List models: `GET https://api.typesafe.ai/v1/models` (same bearer); returns aliases with description and release date; versioned IDs are accepted even if unlisted [O /models.md].
- Request (all top-level required): `state` string|object|array of text; `model` string; `questions` map name -> question (keys chosen by you, not sent to model). Each question: `type` + `instructions` (string|object|array).
  - `noul`: optional `criteria: {true, false}` (string|object|array).
  - `choice`: required `criteria` map option -> description (string|object|array|null); max 255 options.
  - `score`: required `criteria` ordered array of level descriptions; 2..10 levels.
  - Text only. Context 64k tokens total; `state` + longest single question <= 32k [O /models.md]. Max question count: not stated.
- Response: `{model, answers:{name: ...}, usage:{input_tokens,output_tokens}}`:
  - noul: `{type,noul}` 0..1, no confidence
  - choice: `{type,choice,probabilities:{opt:p},confidence}`
  - score: `{type,score,legend:{"0":desc},probabilities:{"0":p},confidence}`; score = sum(level*prob), fractional, 0-based
- Errors: standard HTTP codes + JSON body (schema undefined): 401, 422 (body names offending field), 429 (honor `retry-after`), 529 overloaded; SDK also maps 400/403/404/5xx; request id in `x-typesafe-request-id` header [O /api.md, SDK exceptions].
- Limits: 100K tokens/s and 80 req/s, 429 beyond [O /models.md]. Latency: no official figure (marketing says 70-500 ms [S]). Price $0.042/M input tokens, output free.
- Streaming: not mentioned officially; DigitalOcean says unsupported [S]. Assume none.
- Curl [O]:
```
curl -X POST https://api.typesafe.ai/v1/systemone -H "Authorization: Bearer $TYPESAFE_API_KEY" -H "Content-Type: application/json" -d '{"state":"Please help ASAP","model":"jev-latest","questions":{"urgency":{"type":"noul","instructions":"Does this express urgency?"}}}'
```
- Note: the "yes-no kind" is named `noul` (not "probability"). Official SDKs exist for Python and JS only; no Go SDK found.

## 3. Gateways

| Gateway | OpenAI-shape (`/v1/decisions`) | Jev-shape (`/v1/systemone`) | Source |
|---|---|---|---|
| LiteLLM proxy (v1.104.2+, 1.105.0-rc.3+) | `POST /v1/decisions` (alias `/decisions`), 1..128 questions; non-OpenAI providers get translated bodies | `POST /v1/systemone` (alias `/systemone`); either route can reach either provider, answer returned in route's format; images only for OpenAI; no streaming. `/typesafe/v1/systemone` = raw pass-through | https://docs.litellm.ai/docs/decisions [S-vendor docs] |
| OpenRouter | Added GPT-6 Luna Decisions 2026-10-08 [S remio.ai]; path/shape unconfirmed | Jev models `typesafe/jev-1.13`, `typesafe/jev-router`, `~typesafe/jev-latest` (https://openrouter.ai/typesafe); LiteLLM says upstream path is `/api/alpha/decisions` (state+questions body, Jev-shape) | unofficial; no OpenRouter docs found (docs URL 404) |
| Vercel AI Gateway | Model `openai/gpt-6-luna-decisions`; SDK-only via `experimental_decide` from `ai`; raw HTTP path not documented | not documented | https://vercel.com/ai-gateway/models/gpt-6-luna-decisions |
| LLM Gateway (llmgateway.io) | none | `POST https://api.llmgateway.io/v1/systemone`, model `typesafe/jev-1.13.0`; decision models 400 on chat/completions | https://docs.llmgateway.io/features/system-one |
| Eden AI | none | `POST https://api.edenai.run/v3/alpha/decisions`, `GET /v3/alpha/decisions/models` | https://www.edenai.co/docs/v3/llms/decisions.md |
| DigitalOcean | none | `POST https://inference.do-ai.run/v1/systemone`, model `typesafe-jev-1.13.0`, key = model access key | https://docs.digitalocean.com/products/inference/how-to/use-system-one-api/ |

Conclusion: no gateway proxies the OpenAI shape with certainty except LiteLLM (`/v1/decisions`); the Jev shape is the de facto cross-vendor shape (`/v1/systemone`).

## 4. One Go interface

Common abstraction: `Judge.Ask(ctx, state string, qs []Question) ([]Answer, Usage, error)` with `Question{Name, Instructions, Kind, Labels []Label{Key,Desc}}`, `Answer{Name, Kind, Prob float64 (yes), Choice string, Probs map[string]float64, Score float64, Confidence *float64, Refused bool}`.

| Kind | OpenAI | Jev | Notes |
|---|---|---|---|
| yes/no probability | `predicate` -> `probability` | `noul` -> `noul` | Both 0..1; neither has confidence. Optional noul `criteria{true,false}` can be folded into instructions (LiteLLM does this). |
| choose one of N | `choice` with `choices[{value,description}]` (2..255) | `choice` with `criteria{key:desc}` (<=255) | Map form (Jev, unordered) vs array form (OpenAI). Jev needs >=1 option (LiteLLM rejects 1-option as 400 for OpenAI); require N>=2. Both give probs + confidence. |
| score on levels | `score` with `levels[{label,description}]` | `score` with `criteria[]` (2..10) | Both return 0-based fractional index; Jev probabilities keyed by index string, OpenAI by array with label. Normalize to []float64 by index. |

Wire differences to hide in adapters: OpenAI questions are an array with `name` field, Jev questions are a map keyed by name; OpenAI requires `instructions` (always set), responses are arrays vs maps; OpenAI can return `refusal` (map to Answer.Refused / error); OpenAI input is `input`, Jev is `state`; OpenAI can take images (Jev text only). Cap ~128 questions per request (split larger). Retry 429/529/5xx with backoff.

## Gaps not verified
- OpenAI: exact max questions (200 claimed; 128 per LiteLLM; none per guide), score level count limits, error body, whether `GET /v1/models` lists gpt-6-luna, streaming, decisions-specific rate limits, image URL support conflict. The reference page URL appeared in search but returned 404 to fetch.
- TypeSafe: error body schema, max questions, official latency, official statement on streaming; no Go SDK.
- OpenRouter: actual path and request shape for either model (no official docs reached); Vercel raw HTTP path.
