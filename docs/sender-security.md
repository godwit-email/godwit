# Sender security

There is no provider table, sender allowlist or manual server configuration.
Godwit accepts provider Authentication-Results only from the group prepended
above the first Received trace. This relies on normal SMTP trace insertion; it
is not a cryptographic claim about arbitrary imported messages. Results below
that boundary are not authenticated by matching a server name, IMAP hostname,
MX record, EHLO name or an extra Received line. Those comparisons were removed.
Split receiver results above the boundary are combined only within one service
group. Older results never override the newest group.

Authentication domain properties share provider-independent normalization: balanced
quote wrappers (including escaped/nested provider quoting), surrounding spaces,
case, a final DNS root dot, and IDNA domains. Header folding, nested comments and
quoted semicolons are tokenized before result parsing; comments/reasons cannot
supply authentication properties. Interior domain whitespace, malformed quoting,
duplicate properties and invalid domain labels are rejected rather than repaired.
Nameless result clauses such as `=none smtp.remote-ip=...` are ignored in
isolation; they never grant authentication evidence or suppress valid named
checks. Named malformed authentication checks still fail closed. This tolerance
is provider-independent and does not assume that the missing method was iprev.
Mailbox properties use the domain of the parsed mailbox. These corrections do
not change the receiver trust boundary or turn an SPF-only pass into a badge.
Previously stored AI verdicts require an explicit rescan to be reassessed; this
code change does not rewrite the user's existing mailbox database. If a rejected
header was already cached as empty, its original reception headers must be
fetched again before a rescan can use the corrected parser; discarded data
cannot be reconstructed from an empty cache value.

For an unverified, signed message, Godwit downloads the original RFC 5322 bytes
through the account's authenticated IMAP connection and verifies DKIM locally.
Only public DNS queries for DKIM keys and DMARC policy are sent; message contents
are not submitted to a third-party checking service. The existing 25 MiB message
limit applies, up to five signatures are checked, and DNS/raw downloads have
deadlines. Errors remain queued for retry. Oversized messages stay neutral.
The cached snippet, rendered HTML and an Authentication-Results assertion are
never substitutes for a valid signature. Duplicate/multiple From addresses,
From mismatches, changed bodies and unrelated signing domains grant no badge.
An explicit rescan repeats the original-byte verification.

Green requires an aligned trusted receiver result or locally verified DKIM.
A local DMARC pass is reported only when a published DMARC policy exists and
the verified signing domain meets its strict/relaxed alignment rule. Unknown
SPF-only results below Received cannot be authenticated locally and stay neutral.
Blue additionally requires successful BIMI certificate validation of domain,
lifetime, complete certificate chain and BIMI-specific key usage; bimi=pass alone
cannot grant blue. The certificate cache expires within an hour and never beyond
certificate validity. CRL/OCSP and Certificate Transparency are not implemented.
Authentication is not a guarantee that the sender or content is harmless.

Display-name spoofing detection operates independently. AI content scanning is
off by default and uses the configured judge, which may be remote. Errors and
refusals remain unscanned; AI cannot clear native warnings. Sent/draft/trash are
excluded. Queued/stopped rescans preserve existing verdicts, and stopped scans
cannot publish stale results.

Upgrade migrations invalidate preview verdicts derived from ambiguous headers.
Old cached messages are refreshed in bounded header-only batches via the existing
IMAP connection; messages requiring DKIM verification then fetch original bytes.

## Scan admission and progress

The GUI and headless app enable one background security worker. Concurrent batch
execution and manual rescan admission are guarded in the service, as well as by
disabled controls in Settings. Pending background work or an active synchronization
blocks another manual run. After Stop, the current model call may finish; its
stale write is rejected and a new run waits until that call has left the worker.
Other model features (labels, Wit, etc.) are not serialized by this security guard.

A manual run captures one cutoff timestamp, highest message ID and total. Its
pending count uses that same scope and excludes sent/draft/trash, so older queues
and later arrivals cannot freeze or reset the progress. Completed results are
stored and published per message, rather than once per batch. The UI polls once
per second, so fast consecutive completions can still appear together. Global
queue status is separate and keeps Start disabled until all current work is idle.

The security judge uses conservative English instructions with a German reminder.
Ordinary newsletters, invoices, onboarding and requested login links alone are
not evidence of fraud. Consistent sender/service context is considered, without
allowlisting domains; polite attacks remain in scope. These instructions do not
establish model accuracy. False-positive and phishing recall must be measured on
real labeled messages with the configured model; synthetic judge tests validate
control flow only.
