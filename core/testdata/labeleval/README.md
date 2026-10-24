# Auto-label eval cases

Cases for `TestLabelEvalLive` (`core/labeleval_test.go`): mail, and the answer
each preset label should get for it. The eval delivers each mail to a test IMAP
server, syncs it into the Inbox and lets the real classifier ask the judge, with
a recorder around the judge for the raw probabilities. It scores those against
the answers here, with no model as referee.

These samples only show the eval runs; they are too few to conclude anything.
Keep real cases outside the repository and point `GODWIT_LIVE_LABEL_CASES` at
their directory. Samples here use only RFC 2606 domains (`example.com`,
`*.example`, …) and made-up content.

## Running

Each judge runs only with its key set; with none, the test is skipped and
`go test ./...` calls no model.

```sh
GODWIT_LIVE_OPENAI_KEY=… \
GODWIT_LIVE_TYPESAFE_KEY=… \
GODWIT_LIVE_GEN_MODEL=gpt-… \
GODWIT_LIVE_LABEL_CASES=~/godwit-label-cases \
  go test ./core -run TestLabelEvalLive -v -timeout 1h
```

- `GODWIT_LIVE_OPENAI_KEY`: OpenAI Decisions as the judge service.
- `GODWIT_LIVE_TYPESAFE_KEY`: TypeSafe System One as the judge service.
- `GODWIT_LIVE_GEN_MODEL` (with the OpenAI key): no judge service; this OpenAI
  model answers as the default and small model.
- `GODWIT_LIVE_LABEL_CASES`: the case directory; this one when unset.

Every way runs the cases under the presets seeded in Chinese, English and
Japanese, and under each label set that has that language. The report lists,
per run, TP/FP/FN/TN, precision, recall and F1 for each label and all of them
at every threshold from 0.50 to 0.95 (a generation model answers 0 or 1, so
one point), totals by mail language, and the cases each label got wrong at the
threshold setting, by case ID and probability. Lines with 100 or more scored
cases get 95% bootstrap intervals. It holds no mail content. The test fails only
on an invalid case (before any model is called) or when the labels the
classifier wrote differ from the recorded answers.

## Cases: `*.jsonl`

One case a line:

```json
{"id": "en-bill-invoice", "lang": "en", "note": "optional",
 "mail": {"from": "Billing <billing@cloud.example.net>", "to": ["alice@example.com"],
          "cc": [], "subject": "Invoice for October", "date": "2026-10-01T08:00:00Z",
          "text": "Amount due: $42.00", "attachments": [{"name": "invoice.pdf", "type": "application/pdf", "size": 2048}]},
 "expect": {"otp": "no", "bill": "yes", "order": "no", "travel": "no", "calendar": "no", "notice": "either", "newsletter": "no"}}
```

- `id`: unique across all files. `lang`: the mail's language, `zh`, `en` or `ja`.
- The mail is either `mail` or `raw`, a whole RFC 5322 message (bare `\n`
  line ends are fine). `mail` is built as Compose builds mail, so its text part
  is the HTML's text: give `text` or `html`, not both. `date` is RFC 3339
  (default now); attachments are filler of the given size.
- `expect` gives every preset (`otp`, `bill`, `order`, `travel`, `calendar`,
  `notice`, `newsletter`) `yes`, `no` or `either`. `either` is counted, not
  scored. A refused label, or a mail with no valid answer, scores as no, as the
  classifier treats it, and is counted apart.
- `set`: run this case only under that label set, with answers that follow it.

A missing or unknown label, a bad value, a repeated ID or an unknown field
stops the run before any model is called, listing every problem at once.

## Label sets: `<name>.labelset.json`

Rewrites some presets' names and descriptions, through the same edit a user
makes (the slug stays), by preset language:

```json
{"paraphrase": true,
 "labels": {"en": {"otp": {"name": "Codes", "description": "…"}}, "zh": {…}}}
```

A set runs only under the preset languages it lists. A `paraphrase` set keeps
every answer, so the cases with no `set` run under it too, beside the presets,
to compare wordings. Any other set (like `negation` here, a description with
"but not …") runs only the cases that name it. Sets cannot add labels: a new
label's slug is not stable.
