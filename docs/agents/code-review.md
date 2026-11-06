# Code review

Apply these checks within the review skill's existing report structure. The result should help the author fix real failures and the maintainer judge remaining risk.

## Scope

- Record the base and head commits, and whether staged or unstaged changes are included. Use the request or issue as intent; if none is available, state that limitation and review correctness and project rules without inventing requirements.
- Read the changed code, its callers and callees, and relevant tests. Follow [domain.md](domain.md) for terminology and ADRs. Include changes to contracts, fixtures, configuration and generated consumers when the behavior reaches them.
- Trace affected behavior from its entry point through validation, authorization, persistence and external effects. The first pass should cover the whole change and its affected paths; later rounds should not discover callers that were simply left unread.

## Godwit failure priorities

Use the rows relevant to the change. The linked rules are authoritative, including their exceptions.

| Area | Failure to look for | Rules |
| --- | --- | --- |
| Authority and egress | A tool exceeds its name or permission; an approval no longer covers the final parameters or destination; persistent state causes later unattended egress. Check the actual caller and settings, including `full` and MCP sending. | [AGENTS.md](../../AGENTS.md), [ADR 0003](../adr/0003-no-extension-can-send.md), [0004](../adr/0004-permission-gateway-taint.md), [0023](../adr/0023-mcp-send-setting.md) |
| Sending and approvals | A retry, concurrent claim or restart repeats an effect; an uncertain SMTP result is automatically resent; cancellation races with execution. | [ADR 0006](../adr/0006-send-at-most-once.md), [0004](../adr/0004-permission-gateway-taint.md) |
| Stored data and recovery | A failed save, deletion, migration or partial operation loses mail, drafts or state; recovery cannot distinguish completed work from work safe to retry. Check transaction and failure boundaries. | Relevant storage and operation tests; [ADR 0006](../adr/0006-send-at-most-once.md) for the outbox |
| Privacy | A request or retry bypasses the configured masking policy; restoring values changes tool arguments after approval; a masking failure still sends the request. Respect the documented local-service and policy exceptions. | [ADR 0025](../adr/0025-mask-at-cloud-egress.md), [0004](../adr/0004-permission-gateway-taint.md) |
| Mail and API compatibility | Multiple accounts, copies or folder-role overrides produce inconsistent behavior; a server quirk is patched at the wrong layer; a method or wire error changes without updating its consumers and contract checks. | [AGENTS.md](../../AGENTS.md), [GLOSSARY.md](../../GLOSSARY.md), relevant caller and protocol tests |

## Verify before reporting

- First collect candidate failures, then verify them. Each confirmed defect needs a reachable trigger, user impact, file and line, and evidence connecting the code to the failure.
- Look for counterevidence: caller validation, an enclosing transaction, an approval check, a documented exception or a test covering the case. Recheck security and data-loss findings in a separate verification pass before assigning severity.
- Reproduce with the smallest relevant check when feasible. Otherwise explain the code path and why it is sufficient evidence, and state what was not run. An unverified concern remains a question, not a confirmed defect.
- Compare with the base version: distinguish failures introduced or exposed by this change from unrelated pre-existing defects. Report the latter separately, without attributing them to the author or making them a condition of this change.
- When a pattern fails, inspect its other instances within the affected paths. Group the root cause with all relevant locations and propose the smallest complete fix. Recommend a focused regression test using existing helpers when the failure can be tested deterministically.

## Noise control

- Derive severity from the demonstrated impact and trigger, not the size of the diff or a code-smell label. Project decisions take precedence over general refactoring preferences.
- Limit optional style or maintainability suggestions to three, each with a concrete benefit; zero is fine. This limit does not apply to confirmed defects. Optional suggestions do not block approval.
- Leave mechanically enforced formatting and lint rules to the existing checks; summarize a failing check once instead of restating each diagnostic. A configured check is not evidence that it ran or passed.
- For generated files, inspect the source or generator and relevant output consistency. For lockfiles, inspect the dependency change. Skip cosmetic commentary on these files, while retaining behavioral, compatibility and security checks.

## Report

- State the reviewed scope and counts of confirmed findings by severity, keeping review categories separate where the skill requires it. Count open questions and optional suggestions separately.
- Give each confirmed finding a stable identifier, trigger, user impact, precise location, supporting evidence and smallest fix direction. Distinguish executed checks from conclusions reached by reading code.
- If there are no confirmed findings, say so explicitly. End with unresolved questions and material checks not performed; a clean report describes the reviewed scope, not every possible execution path.

## Re-review

- Keep identifiers stable across rounds. Record each finding as fixed, disproved, risk accepted by the responsible human, or open, with its reason and evidence. Mark a fix verified only after checking the changed code or relevant test result; an author reply alone is not verification.
- Review the fixes, new diff and affected paths. Broaden the scope when new evidence shows a wider impact. Check other instances of a repaired pattern, and reopen settled findings only with new evidence.
- After three rounds without convergence, identify what still prevents resolution and ask the responsible human to choose between splitting the change, supplying missing evidence or revising the approach. Reaching the limit never turns an unresolved defect into approval.
- Finish the round when prior findings are accounted for, new changes and their affected paths have been checked, and remaining findings and validation gaps are explicit.
