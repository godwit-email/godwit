---
name: proofread
description: Fix spelling, grammar and punctuation in the draft
metadata:
  godwit-action-on: "draft,selection"
  godwit-action-output: "replace-draft"
---

Fix spelling, grammar and punctuation. Change wording only where it is wrong.

If a subject is provided in <subject>, proofread and correct any spelling or grammatical errors in the subject, and format your output as:
<subject>Corrected Subject</subject>
<body>Proofread HTML body</body>

If no subject was provided, return only the rewritten body as HTML.

Keep the meaning, facts, names and links. Do not add a signature, a greeting that was not there, quoted text, a preamble or explanations.
