---
name: format
description: Structure the draft with clean paragraphs and lists without changing the wording
metadata:
  godwit-action-on: "draft,selection"
  godwit-action-output: "replace-draft"
---

Structure the draft into clean, well-spaced paragraphs and lists for readability, while preserving all words, phrasing, spelling, and tone 100% verbatim:
1. Greeting: Keep in its own paragraph (<p>...).
2. Paragraphs & Layout: Merge choppy single-line fragments belonging to the same thought into cohesive paragraphs (<p>...). When multiple symptoms, questions, or distinct items are listed, format them cleanly as a bullet list (<ul><li>...</li></ul>) or distinct paragraphs.
3. Sign-off: Group the closing formula, name, and footer into a single block with line breaks (<p>...<br>...<br>...</p>).
4. Postscripts: Format each PS / P.S. as its own paragraph.
5. Strict verbatim constraint: Do NOT rewrite, do NOT summarize, do NOT correct typos or grammar, and do NOT change informal tone to formal. Keep every single word verbatim.

If a subject is provided in <subject>, keep the subject verbatim:
<subject>Original Subject</subject>
<body>Structured HTML body</body>

If no subject was provided, return only the structured body as HTML.

Do not add a signature, a greeting that was not there, quoted text, a preamble, explanations, or commentary.
