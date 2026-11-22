# Reply-date eval cases

Cases for `TestReplyEvalLive` (`core/replyeval_test.go`, #845): unreplied
conversations, and the date by which the newest email wants the user's reply.
The eval delivers each conversation to a test IMAP server, syncs it, runs the
real `summarize` skill with `replyByFormat` (`core/threaddigest.go`) appended
to its prompt, reads the two lines with `parseReplyBy` and checks them with
`replyBy.check`, as the product will (#852). It scores what is left against
the dates here, with no model as referee. Production summaries do not ask for
the lines.

The cases here are synthetic, one per hard case: relative dates, explicit
times, time zones, the sender's own deadline, pay-not-reply, quoted and older
dates, a question with no date. They only show the eval runs; they are far
too few to conclude anything. They use RFC 2606 domains and made-up content.

## The threshold

Written down before the first run, in `replyeval_harness_test.go`:

- **Pass: under 5% of cases get a wrong reply date.** A wrong reply date is a
  date that is wrong, or a date where there should be none.
- A verdict needs **at least 200 cases from at least two mailboxes**; with
  fewer the report says "no verdict".
- A date is right when it is the labelled day, and the labelled time when the
  label has one (a time the label has not is fine). A missed date (none where
  there is one) is reported but is not a wrong reply date.
- Lines that do not read or do not pass the check count as none, as the product
  will treat them, and are also counted as "dropped".
- The summary itself must not get worse: a person reads the samples (below)
  and compares them.

Run each small model you want the verdict for, at least one cloud and one
local.

## Running

Each way runs only with its settings; with none, the test is skipped and
`go test ./...` calls no model.

```sh
GODWIT_LIVE_OPENAI_KEY=… GODWIT_LIVE_REPLY_MODELS=gpt-…-mini,gpt-…-nano \
GODWIT_LIVE_LOCAL_URL=http://localhost:11434/v1 GODWIT_LIVE_LOCAL_MODELS=qwen3:4b,gemma3:4b \
GODWIT_LIVE_REPLY_CASES=~/godwit-reply-cases \
GODWIT_LIVE_REPLY_SAMPLES=~/godwit-reply-samples \
  go test ./core -run TestReplyEvalLive -v -timeout 3h
```

- `GODWIT_LIVE_OPENAI_KEY` and `GODWIT_LIVE_REPLY_MODELS`: OpenAI models,
  comma-separated, one way each, as the default and small model.
- `GODWIT_LIVE_LOCAL_URL` and `GODWIT_LIVE_LOCAL_MODELS`: an OpenAI-compatible
  local server (Ollama, LM Studio) and its models, with no key.
- `GODWIT_LIVE_REPLY_CASES`: the case directory; this one when unset.
- `GODWIT_LIVE_REPLY_SAMPLES`: a directory outside the repository. Every tenth
  case is summarized a second time without the lines, and both summaries go to
  `<dir>/<way>.md` for a person to compare. It holds mail content.
- `GODWIT_LIVE_READING_LANGUAGE`: the summaries' language (default English).

The report, per way: cases and mailboxes, wrong reply dates with their share,
the verdict, and the case IDs that got a wrong date, a date where none, no
date, or lines the check dropped (with why). It holds dates and case IDs,
never mail content. Masking applies as in the product, so cloud models get
the masked text.

## Labelling a real set

Keep real cases outside the repository and point `GODWIT_LIVE_REPLY_CASES` at
them.

1. From each of two or more real mailboxes, take conversations still waiting on
   the user's reply (回复状态 未回复), with and without a date in them: 200 or
   more in all. Include the hard cases above, as they come.
2. Export each conversation's messages as whole RFC 5322 messages, oldest
   first, the user's own replies included, with `Message-ID`, `In-Reply-To`
   and `References` intact so they sync as one conversation.
3. Label the date by which the **newest** email wants the user's **reply**, on
   the user's calendar in their time zone: `YYYY-MM-DD`, with `HH:MM` only when
   the email asks for a time. Several dates: the earliest. Write `none` when
   the date is one by which the sender will do something, when the email asks
   the user to act rather than reply (pay, submit, sign), when the date is only
   in older or quoted emails, or when it gives no date.
4. Decide the label before running any model, and do not change it after
   seeing an answer.

## Cases: `*.jsonl`

One case a line:

```json
{"id": "en-relative-friday", "mailbox": "work", "zone": "Europe/London",
 "note": "optional", "expect": "2026-11-27",
 "messages": ["From: …\nTo: …\nSubject: …\nDate: …\nMessage-ID: <…>\n\nbody", "…"]}
```

- `id`: unique across all files. `mailbox`: which mailbox it came from.
- `zone`: the user's IANA time zone; the label's day and time are on it.
- `expect`: `YYYY-MM-DD`, `YYYY-MM-DD HH:MM` or `none`.
- `messages`: whole messages, oldest first (bare `\n` line ends are fine).
  One message is summarized as a message, more as a thread.

A repeated ID, a missing mailbox, an unknown zone, a bad `expect`, no messages
or an unknown field stops the run before any model is called, listing every
problem at once.
